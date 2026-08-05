package actionbroker

import (
	"encoding/base64"
	"encoding/binary"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestVerifierRejectsMutationExpiryAndReplay(t *testing.T) {
	key := make([]byte, 32)
	for index := range key {
		key[index] = byte(index + 1)
	}
	now := time.Unix(1_700_000_000, 0)
	verifier := NewVerifierWithClock(key, func() time.Time { return now })
	body := []byte(`{"action_id":"act_test","reason":"planned recovery"}`)
	nonce := base64.RawURLEncoding.EncodeToString(make([]byte, 16))
	timestamp := strconv.FormatInt(now.Unix(), 10)
	header := http.Header{}
	header.Set(TimestampHeader, timestamp)
	header.Set(NonceHeader, nonce)
	header.Set(SignatureHeader, Sign(key, timestamp, nonce, body))
	if err := verifier.Verify(header, body); err != nil {
		t.Fatalf("valid request: %v", err)
	}
	if err := verifier.Verify(header, body); err == nil {
		t.Fatal("replayed request accepted")
	}

	otherNonce := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef"))
	header.Set(NonceHeader, otherNonce)
	header.Set(SignatureHeader, Sign(key, timestamp, otherNonce, body))
	if err := verifier.Verify(header, append(body, 'x')); err == nil {
		t.Fatal("mutated body accepted")
	}

	expired := strconv.FormatInt(now.Add(-31*time.Second).Unix(), 10)
	header.Set(TimestampHeader, expired)
	header.Set(SignatureHeader, Sign(key, expired, otherNonce, body))
	if err := verifier.Verify(header, body); err == nil {
		t.Fatal("expired request accepted")
	}

	future := strconv.FormatInt(now.Add(31*time.Second).Unix(), 10)
	header.Set(TimestampHeader, future)
	header.Set(SignatureHeader, Sign(key, future, otherNonce, body))
	if err := verifier.Verify(header, body); err == nil {
		t.Fatal("future request accepted")
	}

	header.Del(SignatureHeader)
	if err := verifier.Verify(header, body); err == nil {
		t.Fatal("unsigned request accepted")
	}
}

func TestVerifierNonceCacheIsBounded(t *testing.T) {
	key := make([]byte, 32)
	now := time.Unix(1_700_000_000, 0)
	verifier := NewVerifierWithClock(key, func() time.Time { return now })
	body := []byte(`{"action_id":"act_bounded_nonce_cache","reason":"planned recovery"}`)
	for index := 0; index < 1100; index++ {
		raw := make([]byte, 16)
		binary.BigEndian.PutUint64(raw[8:], uint64(index))
		nonce := base64.RawURLEncoding.EncodeToString(raw)
		timestamp := strconv.FormatInt(now.Unix(), 10)
		header := http.Header{}
		header.Set(TimestampHeader, timestamp)
		header.Set(NonceHeader, nonce)
		header.Set(SignatureHeader, Sign(key, timestamp, nonce, body))
		if err := verifier.Verify(header, body); err != nil {
			t.Fatalf("nonce %d: %v", index, err)
		}
	}
	if len(verifier.nonces) != 1024 || len(verifier.order) != 1024 {
		t.Fatalf("nonce cache size=(%d,%d)", len(verifier.nonces), len(verifier.order))
	}
}

func TestDecodeKey(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(make([]byte, 32))
	if _, err := DecodeKey(encoded); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeKey("short"); err == nil {
		t.Fatal("invalid key accepted")
	}
}
