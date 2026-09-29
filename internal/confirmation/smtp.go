package confirmation

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

type SMTP struct {
	Address, Username, Password, From string
	AllowLoopbackPlaintext            bool
}

func ValidEmail(value string) bool {
	a, err := mail.ParseAddress(value)
	return err == nil && a.Address == value && !strings.ContainsAny(value, "\r\n") && len(value) <= 254
}
func (s SMTP) Send(ctx context.Context, to, subject, body string) error {
	host, _, err := net.SplitHostPort(s.Address)
	if err != nil || !ValidEmail(s.From) || !ValidEmail(to) || strings.ContainsAny(subject, "\r\n") {
		return errors.New("invalid SMTP configuration")
	}
	conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", s.Address)
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline := time.Now().Add(20 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	conn.SetDeadline(deadline)
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer client.Close()
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err = client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	} else if ip := net.ParseIP(host); !s.AllowLoopbackPlaintext || ip == nil || !ip.IsLoopback() {
		return errors.New("SMTP STARTTLS required")
	}
	if s.Username != "" {
		if err = client.Auth(smtp.PlainAuth("", s.Username, s.Password, host)); err != nil {
			return err
		}
	}
	if err = client.Mail(s.From); err != nil {
		return err
	}
	if err = client.Rcpt(to); err != nil {
		return err
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n", s.From, to, mime.QEncoding.Encode("UTF-8", subject), strings.ReplaceAll(body, "\n", "\r\n"))
	if err != nil {
		return err
	}
	if err = w.Close(); err != nil {
		return err
	}
	return client.Quit()
}
