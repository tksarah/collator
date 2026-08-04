package agentclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/shiden-guardian/shiden-guardian/internal/model"
)

type Client struct {
	observe *http.Client
	reward  *http.Client
	control *http.Client
}

func New(observeSocket, controlSocket string) *Client {
	return &Client{observe: unixClient(observeSocket, 12*time.Second), reward: unixClient(observeSocket, 30*time.Second), control: unixClient(controlSocket, 14*time.Minute)}
}
func unixClient(socket string, timeout time.Duration) *http.Client {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socket)
	}}
	return &http.Client{Transport: transport, Timeout: timeout}
}

func decode[T any](client *http.Client, request *http.Request) (T, error) {
	var out T
	response, err := client.Do(request)
	if err != nil {
		return out, err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return out, fmt.Errorf("agent %s: %s", response.Status, string(body))
	}
	err = json.NewDecoder(response.Body).Decode(&out)
	return out, err
}
func (c *Client) Snapshot(ctx context.Context) (model.AgentSnapshot, error) {
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/v1/snapshot", nil)
	return decode[model.AgentSnapshot](c.observe, request)
}
func (c *Client) Metrics(ctx context.Context) (string, error) {
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/v1/metrics", nil)
	response, err := c.observe.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return "", fmt.Errorf("agent metrics: %s", response.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	return string(raw), err
}
func (c *Client) Logs(ctx context.Context, limit int, since, priority, query string) ([]model.LogLine, error) {
	values := url.Values{}
	values.Set("limit", strconv.Itoa(limit))
	if since != "" {
		values.Set("since", since)
	}
	if priority != "" {
		values.Set("priority", priority)
	}
	if query != "" {
		values.Set("query", query)
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/v1/logs?"+values.Encode(), nil)
	result, err := decode[struct {
		Lines []model.LogLine `json:"lines"`
	}](c.observe, request)
	return result.Lines, err
}
func (c *Client) RewardSnapshot(ctx context.Context) (model.RewardSnapshot, error) {
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/v1/rewards/snapshot", nil)
	return decode[model.RewardSnapshot](c.reward, request)
}
func (c *Client) RewardScan(ctx context.Context, from, to int64) (model.RewardScan, error) {
	values := url.Values{"from": {strconv.FormatInt(from, 10)}, "to": {strconv.FormatInt(to, 10)}}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/v1/rewards/scan?"+values.Encode(), nil)
	return decode[model.RewardScan](c.reward, request)
}
func (c *Client) Restart(ctx context.Context, actionID, reason string) (map[string]any, error) {
	body, _ := json.Marshal(map[string]string{"action_id": actionID, "reason": reason})
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/v1/restart", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return decode[map[string]any](c.control, request)
}
