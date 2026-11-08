package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/textproto"
	"strconv"
	"strings"

	"github.com/shabbybastard/polka/internal/mailer"
)

// Sending a book to an e-reader by e-mail (send-to-Kindle and friends):
// the admin configures SMTP, each user has their own reader address.

const (
	// 14 MiB is the safe whole-message limit used by Kindle and most
	// e-reader gateways. The setting can lower or raise it for a specific
	// provider; the attachment budget is derived from it below.
	defaultSMTPMaxMessageBytes = 14 << 20
	smtpHeaderAllowance        = 64 << 10 // headers, MIME boundaries, body
	smtpBase64Expansion        = 1.37     // base64 + CRLF line breaks
)

// smtpConfig reads the SMTP settings (the password is decrypted).
func (s *Server) smtpConfig(r *http.Request) mailer.Config {
	ctx := r.Context()
	get := func(k string) string { return s.users.GetSetting(ctx, "smtp."+k, "") }
	port, _ := strconv.Atoi(get("port"))
	sec := get("security")
	if sec == "" {
		sec = "starttls"
	}
	return mailer.Config{
		Host:     get("host"),
		Port:     port,
		User:     get("user"),
		Password: s.decryptSecret(get("password")),
		From:     get("from"),
		Security: sec,
	}
}

func (s *Server) smtpEnabled(r *http.Request) bool {
	return s.users.GetSetting(r.Context(), "smtp.enabled", "0") == "1" && s.smtpConfig(r).Valid()
}

// smtpMaxMessageBytes returns the configured whole-message limit. An empty
// or invalid setting falls back to 14 MiB.
func (s *Server) smtpMaxMessageBytes(r *http.Request) int64 {
	raw := s.users.GetSetting(r.Context(), "smtp.max_message_bytes", "")
	if v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); err == nil && v > 0 {
		return v
	}
	return defaultSMTPMaxMessageBytes
}

// smtpMaxAttachmentBytes reserves 64 KiB for the MIME envelope and accounts
// for base64 expansion, so the final message stays under the SMTP limit.
func smtpMaxAttachmentBytes(maxMessage int64) int64 {
	if maxMessage <= smtpHeaderAllowance {
		return 0
	}
	return int64(float64(maxMessage-smtpHeaderAllowance) / smtpBase64Expansion)
}

func (s *Server) smtpMaxAttachmentBytes(r *http.Request) int64 {
	return smtpMaxAttachmentBytes(s.smtpMaxMessageBytes(r))
}

// normalizeReaderEmail validates an e-reader address with net/mail. An empty
// value is allowed and means "clear the address".
func normalizeReaderEmail(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", true
	}
	addr, err := mail.ParseAddress(raw)
	if err != nil {
		return "", false
	}
	return addr.Address, true
}

// readerEmail — the user's personal e-reader address.
func (s *Server) readerEmail(r *http.Request, userID int64) string {
	return s.users.GetSetting(r.Context(), fmt.Sprintf("reader_email:%d", userID), "")
}

func (s *Server) setReaderEmail(ctx context.Context, userID int64, email string) error {
	return s.users.SetSetting(ctx, fmt.Sprintf("reader_email:%d", userID), email)
}

// GET/POST /api/v1/me/reader-email — the current user's reader address.
func (s *Server) handleReaderEmail(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	if u == nil {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodPost {
		var req struct {
			Email string `json:"email"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		email, ok := normalizeReaderEmail(req.Email)
		if !ok {
			http.Error(w, "invalid email", http.StatusBadRequest)
			return
		}
		if err := s.setReaderEmail(r.Context(), u.ID, email); err != nil {
			s.apiError(w, err)
			return
		}
	}
	writeJSON(w, map[string]any{
		"email":              s.readerEmail(r, u.ID),
		"smtpReady":          s.smtpEnabled(r),
		"maxMessageBytes":    s.smtpMaxMessageBytes(r),
		"maxAttachmentBytes": s.smtpMaxAttachmentBytes(r),
	})
}

// POST /api/v1/books/{id}/send — e-mail the book to the user's reader.
func (s *Server) handleSendBook(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	if u == nil {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if !s.smtpEnabled(r) {
		http.Error(w, "email delivery is not configured", http.StatusServiceUnavailable)
		return
	}
	to := s.readerEmail(r, u.ID)
	if to == "" {
		http.Error(w, "set your reader e-mail first", http.StatusBadRequest)
		return
	}
	if !s.sendLimiter.allow(fmt.Sprintf("user:%d", u.ID)) {
		http.Error(w, "too many e-mails, try again later", http.StatusTooManyRequests)
		return
	}
	bookID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if s.lib == nil {
		http.Error(w, "library dir is not configured", http.StatusNotFound)
		return
	}
	f, err := s.st.BookFile(r.Context(), bookID)
	if err != nil {
		s.apiError(w, err)
		return
	}
	maxAttachment := s.smtpMaxAttachmentBytes(r)
	if maxAttachment <= 0 || (f.Size > 0 && f.Size > maxAttachment) {
		http.Error(w, "book is too large to e-mail", http.StatusRequestEntityTooLarge)
		return
	}
	if probe, err := s.lib.EntrySize(f.Folder, f.File, f.Ext); err == nil && probe > maxAttachment {
		http.Error(w, "book is too large to e-mail", http.StatusRequestEntityTooLarge)
		return
	}
	rc, size, err := s.lib.Open(f.Folder, f.File, f.Ext)
	if err != nil {
		s.apiError(w, err)
		return
	}
	defer rc.Close()
	if size > maxAttachment {
		http.Error(w, "book is too large to e-mail", http.StatusRequestEntityTooLarge)
		return
	}
	data, err := io.ReadAll(io.LimitReader(rc, maxAttachment+1))
	if err != nil {
		s.apiError(w, err)
		return
	}
	if strings.EqualFold(f.Ext, "txt") {
		data = mailer.NormalizeText(data)
	}
	if int64(len(data)) > maxAttachment {
		http.Error(w, "book is too large to e-mail", http.StatusRequestEntityTooLarge)
		return
	}

	d, err := s.st.BookDetails(r.Context(), bookID)
	if err != nil {
		s.log.Warn("send book details", "book", bookID, "error", err)
	}
	title := f.File
	if d != nil && d.Title != "" {
		title = d.Title
	}
	name := sanitizeFileName(title) + "." + f.Ext
	cfg := s.smtpConfig(r)
	if err := cfg.SendContext(r.Context(), to, title, "Polka — "+title, name, data); err != nil {
		s.log.Warn("send book", "book", bookID, "error", err)
		if isSMTPTooLarge(err) {
			http.Error(w, "book is too large to e-mail", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "could not send the book", http.StatusBadGateway)
		return
	}
	s.log.Info("book sent", "book", bookID, "to", to)
	writeJSON(w, map[string]any{"sent": true})
}

// isSMTPTooLarge maps a server-side 552 ("message size exceeds fixed
// maximum") to the HTTP 413 we return for oversized books.
func isSMTPTooLarge(err error) bool {
	var protoErr *textproto.Error
	return errors.As(err, &protoErr) && protoErr.Code == 552
}

// POST /admin/smtp/test — verify the SMTP connection (admin only).
func (s *Server) handleSmtpTest(w http.ResponseWriter, r *http.Request) {
	if err := s.smtpConfig(r).VerifyContext(r.Context()); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

type smtpInput struct {
	Enabled         *bool   `json:"enabled"`
	Host            *string `json:"host"`
	Port            *int    `json:"port"`
	User            *string `json:"user"`
	Password        *string `json:"password"` // empty string = leave unchanged
	From            *string `json:"from"`
	Security        *string `json:"security"`
	MaxMessageBytes *int64  `json:"maxMessageBytes"`
}

// smtpSettings — config for the admin panel (no password, just a "set" flag).
func (s *Server) smtpSettings(r *http.Request) map[string]any {
	ctx := r.Context()
	get := func(k string) string { return s.users.GetSetting(ctx, "smtp."+k, "") }
	maxMessage := s.smtpMaxMessageBytes(r)
	return map[string]any{
		"enabled":            get("enabled") == "1",
		"host":               get("host"),
		"port":               get("port"),
		"user":               get("user"),
		"from":               get("from"),
		"security":           get("security"),
		"hasPassword":        get("password") != "",
		"maxMessageBytes":    maxMessage,
		"maxAttachmentBytes": smtpMaxAttachmentBytes(maxMessage),
	}
}

func (s *Server) saveSmtpSettings(r *http.Request, in smtpInput) error {
	ctx := r.Context()
	set := func(k, v string) error { return s.users.SetSetting(ctx, "smtp."+k, v) }
	if in.Enabled != nil {
		v := "0"
		if *in.Enabled {
			v = "1"
		}
		if err := set("enabled", v); err != nil {
			return err
		}
	}
	if in.Host != nil {
		if err := set("host", strings.TrimSpace(*in.Host)); err != nil {
			return err
		}
	}
	if in.Port != nil {
		if err := set("port", strconv.Itoa(*in.Port)); err != nil {
			return err
		}
	}
	if in.User != nil {
		if err := set("user", strings.TrimSpace(*in.User)); err != nil {
			return err
		}
	}
	if in.From != nil {
		if err := set("from", strings.TrimSpace(*in.From)); err != nil {
			return err
		}
	}
	if in.Security != nil {
		if err := set("security", *in.Security); err != nil {
			return err
		}
	}
	if in.MaxMessageBytes != nil {
		v := *in.MaxMessageBytes
		raw := ""
		if v > 0 {
			raw = strconv.FormatInt(v, 10)
		}
		// An empty value means "use the 14 MiB default"; storing "0" made
		// the admin panel show 14 MiB while the saved value was not applied.
		if err := set("max_message_bytes", raw); err != nil {
			return err
		}
	}
	if in.Password != nil && *in.Password != "" {
		if err := set("password", s.encryptSecret(*in.Password)); err != nil {
			return err
		}
	}
	return nil
}
