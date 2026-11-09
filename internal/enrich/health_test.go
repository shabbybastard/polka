package enrich

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func healthServer(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCheckSourcesOK(t *testing.T) {
	ll := healthServer(t, livelibHTML, http.StatusOK)
	fl := healthServer(t, fantlabRatingSearch, http.StatusOK)
	gb := healthServer(t, googleJSON, http.StatusOK)
	ol := healthServer(t, openLibJSON, http.StatusOK)
	td := healthServer(t, `{"similar":{"results":[{"name":"Dune Messiah","type":"book"}]}}`, http.StatusOK)

	p := New(filepath.Join(t.TempDir(), "cache.json"))
	p.LiveLibBase = ll.URL
	p.FantLabBase = fl.URL
	p.GoogleBase = gb.URL
	p.OpenLibraryBase = ol.URL
	p.TasteDiveBase = td.URL
	p.SetGoogleAPIKey("google-key")

	checks := p.CheckSources(context.Background(),
		map[string]bool{SourceLiveLib: true, SourceFantLab: true, SourceGoogleBooks: true, SourceOpenLibrary: true},
		map[string]bool{},
		SimilarConfig{FantLab: true, TasteDive: true, TasteDiveKey: "tastedive-key"},
	)
	if len(checks) != 5 {
		t.Fatalf("checks = %d, want 5", len(checks))
	}
	for _, check := range checks {
		if check.State != SourceStateOK {
			t.Errorf("%s = %+v, want ok", check.Source, check)
		}
		if check.Status != http.StatusOK {
			t.Errorf("%s status = %d, want 200", check.Source, check.Status)
		}
	}
}

func TestCheckSourcesErrorDoesNotLeakKey(t *testing.T) {
	gb := healthServer(t, "quota exceeded", http.StatusTooManyRequests)
	p := New(filepath.Join(t.TempDir(), "cache.json"))
	p.GoogleBase = gb.URL
	p.SetGoogleAPIKey("super-secret-google-key")

	checks := p.CheckSources(context.Background(),
		map[string]bool{SourceGoogleBooks: true}, nil, SimilarConfig{})
	if len(checks) != 5 {
		t.Fatalf("checks = %d, want 5", len(checks))
	}
	check := checks[2]
	if check.Source != SourceGoogleBooks || check.State != SourceStateError || check.Status != http.StatusTooManyRequests {
		t.Fatalf("google check = %+v", check)
	}
	if strings.Contains(check.Message, "super-secret-google-key") || strings.Contains(check.Message, gb.URL) {
		t.Errorf("message leaks the key or URL: %q", check.Message)
	}
	if check.Message != "HTTP 429" {
		t.Errorf("message = %q, want HTTP 429", check.Message)
	}
}

func TestCheckSourcesMissingKeyAndDisabled(t *testing.T) {
	p := New(filepath.Join(t.TempDir(), "cache.json"))

	checks := p.CheckSources(context.Background(),
		map[string]bool{SourceGoogleBooks: true},
		nil,
		SimilarConfig{TasteDive: true},
	)
	states := map[string]string{}
	for _, check := range checks {
		states[check.Source] = check.State
	}
	if states[SourceGoogleBooks] != SourceStateMissingKey {
		t.Errorf("google = %q, want missing_key", states[SourceGoogleBooks])
	}
	if states[SourceTasteDive] != SourceStateMissingKey {
		t.Errorf("tastedive = %q, want missing_key", states[SourceTasteDive])
	}
	if states[SourceLiveLib] != SourceStateDisabled {
		t.Errorf("livelib = %q, want disabled", states[SourceLiveLib])
	}
}

func TestCheckSourcesDegraded(t *testing.T) {
	fl := healthServer(t, `{"matches":[]}`, http.StatusOK)
	p := New(filepath.Join(t.TempDir(), "cache.json"))
	p.FantLabBase = fl.URL

	checks := p.CheckSources(context.Background(),
		map[string]bool{SourceFantLab: true}, nil, SimilarConfig{})
	if checks[1].State != SourceStateDegraded {
		t.Fatalf("fantlab = %+v, want degraded", checks[1])
	}
}

func TestCheckSourcesMalformedResponse(t *testing.T) {
	fl := healthServer(t, "not json", http.StatusOK)
	p := New(filepath.Join(t.TempDir(), "cache.json"))
	p.FantLabBase = fl.URL

	checks := p.CheckSources(context.Background(),
		map[string]bool{SourceFantLab: true}, nil, SimilarConfig{})
	check := checks[1]
	if check.State != SourceStateError || check.Status != http.StatusOK || check.Message != "unexpected response" {
		t.Fatalf("fantlab = %+v, want error/200/unexpected response", check)
	}
}

func TestCheckSourcesNetworkError(t *testing.T) {
	p := New(filepath.Join(t.TempDir(), "cache.json"))
	p.FantLabBase = "http://127.0.0.1:1"

	checks := p.CheckSources(context.Background(),
		map[string]bool{SourceFantLab: true}, nil, SimilarConfig{})
	check := checks[1]
	if check.State != SourceStateError || check.Status != 0 || check.Message == "" {
		t.Fatalf("fantlab = %+v, want error/0/non-empty message", check)
	}
}

func TestCheckSourcesClearsBackoff(t *testing.T) {
	fl := healthServer(t, fantlabRatingSearch, http.StatusOK)
	p := New(filepath.Join(t.TempDir(), "cache.json"))
	p.FantLabBase = fl.URL
	p.backoff[SourceFantLab] = time.Now().Add(time.Hour)

	checks := p.CheckSources(context.Background(),
		map[string]bool{SourceFantLab: true}, nil, SimilarConfig{})
	if checks[1].State != SourceStateOK {
		t.Fatalf("fantlab = %+v, want ok", checks[1])
	}
	if _, ok := p.backoff[SourceFantLab]; ok {
		t.Error("backoff was not cleared after a successful check")
	}
}
