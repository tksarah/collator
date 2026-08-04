package notify

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

type SMTP struct {
	Host                 string
	Port                 int
	User, Password, From string
	To                   []string
	UnixSocket           string
	TLSMode              string
}

func (s SMTP) Configured() bool { return s.Host != "" && s.From != "" && len(s.To) > 0 }
func (s SMTP) Send(ctx context.Context, subject, body string) error {
	if !s.Configured() {
		return fmt.Errorf("smtp is not configured")
	}
	network := "tcp"
	address := fmt.Sprintf("%s:%d", s.Host, s.Port)
	if strings.TrimSpace(s.UnixSocket) != "" {
		network = "unix"
		address = s.UnixSocket
	}
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, network, address)
	if err != nil {
		return err
	}
	client, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer client.Close()
	tlsMode := strings.ToLower(strings.TrimSpace(s.TLSMode))
	if tlsMode == "" {
		tlsMode = "required"
	}
	if tlsMode != "none" && tlsMode != "required" && tlsMode != "opportunistic" {
		return fmt.Errorf("invalid SMTP TLS mode %q", tlsMode)
	}
	if tlsMode != "none" {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(&tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}); err != nil {
				return err
			}
		} else if tlsMode == "required" {
			return fmt.Errorf("smtp server does not offer STARTTLS")
		}
	}
	if s.User != "" {
		if tlsMode == "none" {
			return fmt.Errorf("SMTP authentication requires TLS")
		}
		if err := client.Auth(smtp.PlainAuth("", s.User, s.Password, s.Host)); err != nil {
			return err
		}
	}
	if err := client.Mail(s.From); err != nil {
		return err
	}
	for _, to := range s.To {
		if err := client.Rcpt(to); err != nil {
			return err
		}
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	message := "From: " + s.From + "\r\nTo: " + strings.Join(s.To, ",") + "\r\nSubject: " + subject + "\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" + body
	_, err = writer.Write([]byte(message))
	closeErr := writer.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return client.Quit()
}
