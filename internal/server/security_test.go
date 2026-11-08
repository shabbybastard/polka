package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestSecurityHeaders(t *testing.T) {
	h := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest("GET", "https://x/", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	for _, hdr := range []string{"Content-Security-Policy", "X-Content-Type-Options", "X-Frame-Options", "Strict-Transport-Security"} {
		if rec.Header().Get(hdr) == "" {
			t.Errorf("missing %s", hdr)
		}
	}
	// HSTS must not be sent over plain http
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest("GET", "http://x/", nil))
	if rec2.Header().Get("Strict-Transport-Security") != "" {
		t.Error("HSTS sent over http")
	}
}

func TestOriginCheck(t *testing.T) {
	s := &Server{}
	h := s.originCheck(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	run := func(method, target, origin, referer string, cookie bool) int {
		req := httptest.NewRequest(method, target, nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if referer != "" {
			req.Header.Set("Referer", referer)
		}
		if cookie {
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "token"})
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	if got := run(http.MethodPost, "http://example.com/api", "http://example.com", "", true); got != http.StatusNoContent {
		t.Errorf("same-origin POST = %d", got)
	}
	if got := run(http.MethodPost, "http://example.com/api", "http://evil.example", "", true); got != http.StatusForbidden {
		t.Errorf("cross-origin POST = %d", got)
	}
	if got := run(http.MethodPost, "http://example.com/api", "", "http://example.com/page", true); got != http.StatusNoContent {
		t.Errorf("same-origin Referer = %d", got)
	}
	if got := run(http.MethodPost, "http://example.com/api", "", "http://evil.example/page", true); got != http.StatusForbidden {
		t.Errorf("cross-origin Referer = %d", got)
	}
	// Origin is checked even without a session cookie: desktop mode has no
	// cookie and every request acts as the owner.
	if got := run(http.MethodPost, "http://example.com/api", "http://evil.example", "", false); got != http.StatusForbidden {
		t.Errorf("anonymous cross-origin POST = %d", got)
	}
	if got := run(http.MethodGet, "http://example.com/api", "http://evil.example", "", true); got != http.StatusNoContent {
		t.Errorf("cross-origin GET = %d", got)
	}
	// Non-browser clients (no Origin/Referer) still work in server mode.
	if got := run(http.MethodPost, "http://example.com/api", "", "", false); got != http.StatusNoContent {
		t.Errorf("non-browser POST = %d", got)
	}
}

func TestOriginCheckTrustedProxyHost(t *testing.T) {
	t.Setenv("POLKA_TRUST_PROXY", "1")
	s := &Server{}
	h := s.originCheck(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "http://internal:12791/api", nil)
	req.Host = "internal:12791"
	req.Header.Set("X-Forwarded-Host", "books.example")
	req.Header.Set("Origin", "https://books.example")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "token"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("trusted forwarded host = %d", rec.Code)
	}
}

func TestSanitizeFileName(t *testing.T) {
	long := ""
	for i := 0; i < 200; i++ {
		long += "Ж"
	}
	got := sanitizeFileName(long)
	if !utf8.ValidString(got) {
		t.Errorf("sanitizeFileName produced invalid UTF-8: %q", got)
	}
	if runes := []rune(got); len(runes) > 120 {
		t.Errorf("sanitizeFileName left %d runes, want <= 120", len(runes))
	}
	if got := sanitizeFileName("a\r\nb.txt"); got != "a__b.txt" {
		t.Errorf("control chars: %q", got)
	}
	if got := sanitizeFileName("CON.txt"); got != "_CON.txt" {
		t.Errorf("reserved name: %q", got)
	}
	if got := sanitizeFileName("name. "); got != "name" {
		t.Errorf("trailing dot/space: %q", got)
	}
}

func TestContentDispositionRFC5987(t *testing.T) {
	got := contentDisposition("attachment", "Книга.fb2")
	if !strings.Contains(got, "filename*=utf-8''") {
		t.Errorf("missing RFC 5987 filename*: %q", got)
	}
	if !strings.Contains(got, `filename="`) {
		t.Errorf("missing ASCII fallback: %q", got)
	}
	if strings.Contains(got, "Книга") {
		t.Errorf("raw non-ASCII in header: %q", got)
	}
}

func TestSafeImageMime(t *testing.T) {
	cases := map[string]string{
		"":              "image/jpeg",
		"image/jpg":     "image/jpeg",
		"image/png":     "image/png",
		"text/html":     "application/octet-stream",
		"image/svg+xml": "application/octet-stream",
	}
	for in, want := range cases {
		if got := safeImageMime(in); got != want {
			t.Errorf("safeImageMime(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRateLimiter(t *testing.T) {
	rl := newRateLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		if !rl.allow("ip1") {
			t.Fatalf("request %d should pass", i)
		}
	}
	if rl.allow("ip1") {
		t.Error("4th request should be blocked")
	}
	if !rl.allow("ip2") {
		t.Error("different key should be independent")
	}
}
