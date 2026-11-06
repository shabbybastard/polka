package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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
	if got := run(http.MethodPost, "http://example.com/api", "http://evil.example", "", false); got != http.StatusNoContent {
		t.Errorf("anonymous POST = %d", got)
	}
	if got := run(http.MethodGet, "http://example.com/api", "http://evil.example", "", true); got != http.StatusNoContent {
		t.Errorf("cross-origin GET = %d", got)
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
