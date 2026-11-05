// Package mailer sends e-mails with a single file attachment over SMTP —
// used to deliver books to e-readers (send-to-Kindle and friends).
package mailer

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"mime"
	"net/smtp"
	"path/filepath"
	"strings"
	"time"
)

// Config describes the sending SMTP account.
type Config struct {
	Host     string
	Port     int
	User     string
	Password string
	From     string // envelope/From address; falls back to User
	Security string // "tls" (implicit), "starttls", or "none"
}

func (c Config) from() string {
	if c.From != "" {
		return c.From
	}
	return c.User
}

// Valid reports whether the config has the minimum to attempt sending.
func (c Config) Valid() bool {
	return c.Host != "" && c.Port != 0 && c.from() != ""
}

// Send delivers a message with one attachment to addr.
func (c Config) Send(to, subject, body, attachName string, attach []byte) error {
	if !c.Valid() {
		return fmt.Errorf("smtp is not configured")
	}
	msg := c.build(to, subject, body, attachName, attach)
	return c.deliver(to, msg)
}

// Verify opens an authenticated SMTP session without sending — used by
// the "test connection" button in the admin panel.
func (c Config) Verify() error {
	if !c.Valid() {
		return fmt.Errorf("smtp is not configured")
	}
	client, err := c.dial()
	if err != nil {
		return err
	}
	defer client.Close()
	if err := c.auth(client); err != nil {
		return err
	}
	return client.Quit()
}

func (c Config) deliver(to string, msg []byte) error {
	client, err := c.dial()
	if err != nil {
		return err
	}
	defer client.Close()
	if err := c.auth(client); err != nil {
		return err
	}
	if err := client.Mail(c.from()); err != nil {
		return fmt.Errorf("MAIL FROM: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("RCPT TO: %w", err)
	}
	wc, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := wc.Write(msg); err != nil {
		wc.Close()
		return err
	}
	if err := wc.Close(); err != nil {
		return err
	}
	return client.Quit()
}

func (c Config) dial() (*smtp.Client, error) {
	addr := fmt.Sprintf("%s:%d", c.Host, c.Port)
	if c.Security == "tls" {
		conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: c.Host})
		if err != nil {
			return nil, fmt.Errorf("tls dial: %w", err)
		}
		return smtp.NewClient(conn, c.Host)
	}
	client, err := smtp.Dial(addr)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	if c.Security == "starttls" {
		if err := client.StartTLS(&tls.Config{ServerName: c.Host}); err != nil {
			client.Close()
			return nil, fmt.Errorf("starttls: %w", err)
		}
	}
	return client, nil
}

func (c Config) auth(client *smtp.Client) error {
	if c.User == "" {
		return nil
	}
	if err := client.Auth(smtp.PlainAuth("", c.User, c.Password, c.Host)); err != nil {
		return fmt.Errorf("auth: %w", err)
	}
	return nil
}

func (c Config) build(to, subject, body, attachName string, attach []byte) []byte {
	boundary := "polka-" + fmt.Sprint(time.Now().UnixNano())
	var b strings.Builder
	enc := mime.QEncoding.Encode
	fmt.Fprintf(&b, "From: %s\r\n", c.from())
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", enc("utf-8", subject))
	b.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", boundary)

	fmt.Fprintf(&b, "--%s\r\n", boundary)
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	b.WriteString(body + "\r\n\r\n")

	name := filepath.Base(attachName)
	fallback := asciiFallback(name)
	encoded := percentEncodeParam(name)
	fmt.Fprintf(&b, "--%s\r\n", boundary)
	fmt.Fprintf(&b, "Content-Type: %s; name=%q; name*=utf-8''%s\r\n", attachmentContentType(name), fallback, encoded)
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	fmt.Fprintf(&b, "Content-Disposition: attachment; filename=%q; filename*=utf-8''%s\r\n\r\n", fallback, encoded)
	enc64 := base64.StdEncoding.EncodeToString(attach)
	for i := 0; i < len(enc64); i += 76 {
		end := i + 76
		if end > len(enc64) {
			end = len(enc64)
		}
		b.WriteString(enc64[i:end] + "\r\n")
	}
	fmt.Fprintf(&b, "\r\n--%s--\r\n", boundary)
	return []byte(b.String())
}

// attachmentContentType returns a real MIME type for the attachment instead
// of the generic application/octet-stream. Explicit mappings matter for
// e-readers: Kindle accepts EPUB only when it is labelled as such.
func attachmentContentType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".epub":
		return "application/epub+zip"
	case ".fb2":
		return "application/x-fictionbook+xml"
	case ".pdf":
		return "application/pdf"
	case ".djvu":
		return "image/vnd.djvu"
	case ".txt":
		return "text/plain; charset=utf-8"
	case ".mobi":
		return "application/x-mobipocket-ebook"
	case ".azw3":
		return "application/vnd.amazon.ebook"
	case ".zip":
		return "application/zip"
	}
	if typ := mime.TypeByExtension(filepath.Ext(name)); typ != "" {
		return typ
	}
	return "application/octet-stream"
}

// asciiFallback keeps old mail clients happy: non-ASCII names are replaced
// with a safe generic name, while modern clients use the RFC 2231 parameter.
func asciiFallback(name string) string {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if stem == "" {
		return "book" + ext
	}
	for _, r := range stem {
		safe := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' ||
			r >= '0' && r <= '9' || r == ' ' || r == '_' || r == '-' ||
			r == '.' || r == '(' || r == ')'
		if !safe {
			return "book" + ext
		}
	}
	return name
}

// percentEncodeParam implements RFC 2231/5987 attribute encoding. Spaces are
// %20 (not '+'), and only attr-char characters stay unescaped.
func percentEncodeParam(value string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' ||
			c >= '0' && c <= '9' || strings.ContainsRune("!#$&+-.^_`|~", rune(c)) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0f])
	}
	return b.String()
}
