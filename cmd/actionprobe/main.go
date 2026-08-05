package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

func main() {
	socket := os.Getenv("ACTION_BROKER_SOCKET")
	if socket == "" {
		socket = "/run/shiden-guardian/action-broker/controller.sock"
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socket)
	}}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	body := []byte(`{"action_id":"act_unsigned_probe_2026","idempotency_key":"unsigned-probe","requested_by":"e2e","reason":"unsigned requests must always be rejected"}`)
	request, err := http.NewRequest(http.MethodPost, "http://unix/v1/manual-actions", bytes.NewReader(body))
	if err != nil {
		panic(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		panic(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		panic(fmt.Sprintf("unsigned action status=%d, want 401", response.StatusCode))
	}
	fmt.Println("unsigned action rejected")
}
