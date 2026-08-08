package actionbroker

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	TimestampHeader                  = "X-Guardian-Timestamp"
	NonceHeader                      = "X-Guardian-Nonce"
	SignatureHeader                  = "X-Guardian-Signature"
	MaxBodyBytes                     = 16 << 10
	ActionRestartService             = "restart_service"
	ActionAcknowledgePrunedRewardGap = "acknowledge_pruned_reward_gap"
)

type ManualAction struct {
	ActionID       string          `json:"action_id"`
	IdempotencyKey string          `json:"idempotency_key"`
	IncidentID     string          `json:"incident_id,omitempty"`
	RequestedBy    string          `json:"requested_by"`
	Reason         string          `json:"reason"`
	ActionKind     string          `json:"action_kind"`
	Parameters     json.RawMessage `json:"parameters,omitempty"`
}

type RewardGapParameters struct {
	ExpectedCursor int64 `json:"expected_cursor"`
}

type Result struct {
	ID     string `json:"id,omitempty"`
	Status string `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
}

func DecodeKey(encoded string) ([]byte, error) {
	encoded = strings.TrimSpace(encoded)
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		key, err = base64.RawStdEncoding.DecodeString(encoded)
	}
	if err != nil || len(key) != 32 {
		return nil, errors.New("action broker key must be a base64-encoded 32-byte value")
	}
	return key, nil
}

func message(timestamp, nonce string, body []byte) []byte {
	digest := sha256.Sum256(body)
	return []byte(timestamp + "\n" + nonce + "\n" + base64.RawURLEncoding.EncodeToString(digest[:]))
}

func Sign(key []byte, timestamp, nonce string, body []byte) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(message(timestamp, nonce, body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

type nonceEntry struct {
	value string
	at    time.Time
}

type Verifier struct {
	mu       sync.Mutex
	key      []byte
	now      func() time.Time
	maxSkew  time.Duration
	ttl      time.Duration
	maxNonce int
	nonces   map[string]time.Time
	order    []nonceEntry
}

func NewVerifier(key []byte) *Verifier {
	return NewVerifierWithClock(key, time.Now)
}

func NewVerifierWithClock(key []byte, now func() time.Time) *Verifier {
	return &Verifier{key: append([]byte(nil), key...), now: now, maxSkew: 30 * time.Second, ttl: 2 * time.Minute, maxNonce: 1024, nonces: map[string]time.Time{}}
}

func (v *Verifier) Verify(header http.Header, body []byte) error {
	timestamp := strings.TrimSpace(header.Get(TimestampHeader))
	nonce := strings.TrimSpace(header.Get(NonceHeader))
	signature := strings.TrimSpace(header.Get(SignatureHeader))
	unix, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || nonce == "" || signature == "" {
		return errors.New("missing or invalid broker authentication")
	}
	nonceRaw, err := base64.RawURLEncoding.DecodeString(nonce)
	if err != nil || len(nonceRaw) != 16 {
		return errors.New("invalid broker nonce")
	}
	now := v.now()
	issued := time.Unix(unix, 0)
	if issued.Before(now.Add(-v.maxSkew)) || issued.After(now.Add(v.maxSkew)) {
		return errors.New("expired broker request")
	}
	expected := Sign(v.key, timestamp, nonce, body)
	provided, err := base64.RawURLEncoding.DecodeString(signature)
	expectedRaw, _ := base64.RawURLEncoding.DecodeString(expected)
	if err != nil || !hmac.Equal(provided, expectedRaw) {
		return errors.New("invalid broker signature")
	}
	if !v.remember(nonce, now) {
		return errors.New("replayed broker request")
	}
	return nil
}

func (v *Verifier) remember(nonce string, now time.Time) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	cutoff := now.Add(-v.ttl)
	keep := v.order[:0]
	for _, entry := range v.order {
		if entry.at.Before(cutoff) {
			delete(v.nonces, entry.value)
			continue
		}
		keep = append(keep, entry)
	}
	v.order = keep
	if _, exists := v.nonces[nonce]; exists {
		return false
	}
	for len(v.order) >= v.maxNonce {
		oldest := v.order[0]
		v.order = v.order[1:]
		delete(v.nonces, oldest.value)
	}
	v.nonces[nonce] = now
	v.order = append(v.order, nonceEntry{value: nonce, at: now})
	return true
}

type Client struct {
	http *http.Client
	key  []byte
	now  func() time.Time
}

func NewClient(socket string, key []byte) *Client {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socket)
	}}
	return &Client{http: &http.Client{Transport: transport, Timeout: 10 * time.Second}, key: append([]byte(nil), key...), now: time.Now}
}

func (c *Client) Submit(ctx context.Context, action ManualAction) (int, Result, error) {
	var result Result
	body, err := json.Marshal(action)
	if err != nil {
		return 0, result, err
	}
	nonceRaw := make([]byte, 16)
	if _, err = rand.Read(nonceRaw); err != nil {
		return 0, result, err
	}
	timestamp := strconv.FormatInt(c.now().Unix(), 10)
	nonce := base64.RawURLEncoding.EncodeToString(nonceRaw)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/v1/manual-actions", bytes.NewReader(body))
	if err != nil {
		return 0, result, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(TimestampHeader, timestamp)
	request.Header.Set(NonceHeader, nonce)
	request.Header.Set(SignatureHeader, Sign(c.key, timestamp, nonce, body))
	response, err := c.http.Do(request)
	if err != nil {
		return 0, result, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, MaxBodyBytes))
	if err != nil {
		return response.StatusCode, result, err
	}
	if len(raw) > 0 && json.Unmarshal(raw, &result) != nil {
		return response.StatusCode, result, fmt.Errorf("invalid controller response")
	}
	return response.StatusCode, result, nil
}

func (c *Client) Health(ctx context.Context) error {
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/healthz", nil)
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("controller broker health: %s", response.Status)
	}
	return nil
}
