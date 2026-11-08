package server

import (
	"context"
	"net/http"
	"path"
	"strings"
	"time"
)

// Demo mode (--auth demo): a public showcase server. Every visitor
// gets an ephemeral guest account, so ratings, lists and reading
// progress work — but live only as long as the guest session. Library
// and user management routes are not registered at all.

func (s *Server) demoMode() bool { return s.cfg.Auth == "demo" }

// maybeDemo wraps the root handler with guest provisioning and starts
// the cleanup job when demo mode is on; otherwise it is a no-op.
func (s *Server) maybeDemo(h http.Handler) http.Handler {
	if !s.demoMode() {
		return h
	}
	go s.cleanupGuests()
	return s.withGuest(h)
}

// withGuest gives each new browser session a guest user: if the
// request carries no valid session, one is created on the fly and the
// cookie is attached both to the response and to the current request.
func (s *Server) withGuest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Static assets do not need a guest account. Creating one per JS/CSS
		// request wastes rows and lets a simple page load churn the database.
		if isStaticAsset(r) {
			next.ServeHTTP(w, r)
			return
		}
		if s.currentUser(r) == nil {
			if !s.guestLimiter.allow(clientIP(r)) {
				http.Error(w, "too many requests", http.StatusTooManyRequests)
				return
			}
			if token, _, err := s.users.CreateGuest(r.Context()); err == nil {
				cookie := &http.Cookie{
					Name:     sessionCookie,
					Value:    token,
					Path:     "/",
					HttpOnly: true,
					Secure:   requestIsSecure(r),
					SameSite: http.SameSiteLaxMode,
					MaxAge:   24 * 60 * 60,
				}
				http.SetCookie(w, cookie)
				r.AddCookie(cookie)
			} else {
				s.log.Error("demo guest", "error", err)
			}
		}
		next.ServeHTTP(w, r)
	})
}

const guestMaxAge = 24 * time.Hour

// isStaticAsset reports requests that are served by the embedded SPA or
// static file server and therefore never need a demo guest account.
func isStaticAsset(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	p := r.URL.Path
	if p == "/" || p == "/index.html" {
		return false
	}
	if strings.HasPrefix(p, "/assets/") {
		return true
	}
	switch strings.ToLower(path.Ext(p)) {
	case ".js", ".mjs", ".css", ".map", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".ico",
		".woff", ".woff2", ".ttf", ".otf", ".webmanifest":
		return true
	}
	return false
}

// cleanupGuests periodically wipes stale guest accounts together with
// everything they did (sessions, progress, ratings, lists).
func (s *Server) cleanupGuests() {
	for {
		time.Sleep(time.Hour)
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		if n, err := s.users.CleanupGuests(ctx, guestMaxAge); err != nil {
			s.log.Warn("guest cleanup", "error", err)
		} else if n > 0 {
			s.invalidateBasicCache()
			s.log.Info("guest cleanup", "removed", n)
		}
		cancel()
	}
}
