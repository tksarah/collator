package notify

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSendThroughLocalUnixSocketWithoutTLSOrAuth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "postfix.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	var message strings.Builder
	var messageMu sync.Mutex
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		_, _ = fmt.Fprint(conn, "220 local-postfix ESMTP\r\n")
		reader := bufio.NewReader(conn)
		dataMode := false
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				done <- err
				return
			}
			trimmed := strings.TrimSpace(line)
			if dataMode {
				if trimmed == "." {
					dataMode = false
					_, _ = fmt.Fprint(conn, "250 queued\r\n")
					continue
				}
				messageMu.Lock()
				message.WriteString(line)
				messageMu.Unlock()
				continue
			}
			switch {
			case strings.HasPrefix(trimmed, "EHLO"):
				_, _ = fmt.Fprint(conn, "250-local-postfix\r\n250 PIPELINING\r\n")
			case strings.HasPrefix(trimmed, "MAIL FROM:"), strings.HasPrefix(trimmed, "RCPT TO:"):
				_, _ = fmt.Fprint(conn, "250 ok\r\n")
			case trimmed == "DATA":
				dataMode = true
				_, _ = fmt.Fprint(conn, "354 end with dot\r\n")
			case trimmed == "QUIT":
				_, _ = fmt.Fprint(conn, "221 bye\r\n")
				done <- nil
				return
			default:
				_, _ = fmt.Fprint(conn, "250 ok\r\n")
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client := SMTP{
		Host:       "localhost",
		Port:       25,
		From:       "guardian@example.test",
		To:         []string{"operator@example.test"},
		UnixSocket: path,
		TLSMode:    "none",
	}
	if err := client.Send(ctx, "Guardian test", "local Postfix path works"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	messageMu.Lock()
	raw := message.String()
	messageMu.Unlock()
	if !strings.Contains(raw, "Subject: Guardian test") || !strings.Contains(raw, "local Postfix path works") {
		t.Fatalf("unexpected message: %q", raw)
	}
}
