package enrich

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// Source health checks used by the administrator panel. A check is an
// explicit diagnostic request: it bypasses the enrichment cache and the
// per-source backoff, and it never changes the cached book data.

const (
	SourceStateOK         = "ok"
	SourceStateDegraded   = "degraded" // HTTP 200, but the expected data was not found
	SourceStateError      = "error"
	SourceStateMissingKey = "missing_key"
	SourceStateDisabled   = "disabled"
)

type SourceCheck struct {
	Source    string `json:"source"`
	State     string `json:"state"`
	Status    int    `json:"status,omitempty"`
	LatencyMs int64  `json:"latencyMs,omitempty"`
	Message   string `json:"message,omitempty"`
}

var errUnexpectedResponse = errors.New("unexpected response")

// CheckSources probes every source that is enabled for ratings, tags or
// similar books. Checks run in parallel; disabled sources are reported
// without a network request. The Google Books key is taken from the provider
// (SetGoogleAPIKey), the TasteDive key from cfg.
func (p *Provider) CheckSources(ctx context.Context, enabled, tagSources map[string]bool, cfg SimilarConfig) []SourceCheck {
	sources := []string{SourceLiveLib, SourceFantLab, SourceGoogleBooks, SourceOpenLibrary, SourceTasteDive}
	out := make([]SourceCheck, len(sources))
	var wg sync.WaitGroup
	for i, source := range sources {
		i, source := i, source
		if !sourceActive(source, enabled, tagSources, cfg) {
			out[i] = SourceCheck{Source: source, State: SourceStateDisabled}
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = p.checkSource(ctx, source, cfg.TasteDiveKey)
		}()
	}
	wg.Wait()
	return out
}

func sourceActive(source string, enabled, tagSources map[string]bool, cfg SimilarConfig) bool {
	switch source {
	case SourceLiveLib:
		return enabled[SourceLiveLib] || tagSources[SourceLiveLib]
	case SourceFantLab:
		return enabled[SourceFantLab] || cfg.FantLab
	case SourceGoogleBooks:
		return enabled[SourceGoogleBooks]
	case SourceOpenLibrary:
		return enabled[SourceOpenLibrary] || tagSources[SourceOpenLibrary]
	case SourceTasteDive:
		return cfg.TasteDive
	}
	return false
}

func (p *Provider) checkSource(ctx context.Context, source, tastediveKey string) SourceCheck {
	start := time.Now()
	check := SourceCheck{Source: source}
	var (
		degraded bool
		err      error
	)
	switch source {
	case SourceLiveLib:
		degraded, err = p.probeLiveLib(ctx)
	case SourceFantLab:
		degraded, err = p.probeFantLab(ctx)
	case SourceGoogleBooks:
		key := p.googleAPIKeyValue()
		if key == "" {
			return SourceCheck{Source: source, State: SourceStateMissingKey}
		}
		degraded, err = p.probeGoogleBooks(ctx, key)
	case SourceOpenLibrary:
		degraded, err = p.probeOpenLibrary(ctx)
	case SourceTasteDive:
		if tastediveKey == "" {
			return SourceCheck{Source: source, State: SourceStateMissingKey}
		}
		degraded, err = p.probeTasteDive(ctx, tastediveKey)
	default:
		return SourceCheck{Source: source, State: SourceStateDisabled}
	}

	check.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		check.State = SourceStateError
		check.Message = sourceCheckMessage(err)
		var statusErr *httpStatusError
		if errors.As(err, &statusErr) {
			check.Status = statusErr.Status
		} else if errors.Is(err, errUnexpectedResponse) {
			// The request itself succeeded; only the response body was not
			// what the parser expected.
			check.Status = http.StatusOK
		}
		return check
	}
	check.Status = http.StatusOK
	if degraded {
		check.State = SourceStateDegraded
	} else {
		check.State = SourceStateOK
	}
	// A successful probe proves the source is reachable: drop a stale
	// backoff so the next book page can use the source again immediately.
	p.mu.Lock()
	delete(p.backoff, source)
	p.mu.Unlock()
	return check
}

func (p *Provider) probeLiveLib(ctx context.Context) (bool, error) {
	body, err := p.get(ctx, p.LiveLibBase+"/find/books/"+url.PathEscape("Дюна"), true)
	if err != nil {
		return false, err
	}
	return len(parseLiveLibSearch(body)) == 0, nil
}

func (p *Provider) probeFantLab(ctx context.Context) (bool, error) {
	body, err := p.get(ctx, p.FantLabBase+"/search-works?q="+url.QueryEscape("Дюна"), true)
	if err != nil {
		return false, err
	}
	var data struct {
		Matches []json.RawMessage `json:"matches"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return false, errUnexpectedResponse
	}
	return len(data.Matches) == 0, nil
}

func (p *Provider) probeGoogleBooks(ctx context.Context, key string) (bool, error) {
	u := p.GoogleBase + "/books/v1/volumes?maxResults=1&q=" +
		url.QueryEscape("intitle:Dune") + "&key=" + url.QueryEscape(key)
	body, err := p.get(ctx, u, false)
	if err != nil {
		return false, err
	}
	var data struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return false, errUnexpectedResponse
	}
	return len(data.Items) == 0, nil
}

func (p *Provider) probeOpenLibrary(ctx context.Context) (bool, error) {
	u := p.OpenLibraryBase + "/search.json?title=" + url.QueryEscape("Dune") +
		"&limit=1&fields=key"
	body, err := p.get(ctx, u, false)
	if err != nil {
		return false, err
	}
	var data struct {
		Docs []json.RawMessage `json:"docs"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return false, errUnexpectedResponse
	}
	return len(data.Docs) == 0, nil
}

func (p *Provider) probeTasteDive(ctx context.Context, key string) (bool, error) {
	u := fmt.Sprintf("%s/api/similar?type=book&limit=1&k=%s&q=%s",
		p.TasteDiveBase, url.QueryEscape(key), url.QueryEscape("book:Dune"))
	body, err := p.get(ctx, u, false)
	if err != nil {
		return false, err
	}
	var data struct {
		Similar struct {
			Results []json.RawMessage `json:"results"`
		} `json:"similar"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return false, errUnexpectedResponse
	}
	return len(data.Similar.Results) == 0, nil
}

// sourceCheckMessage returns a safe, human-readable reason. It deliberately
// never includes the request URL: Google Books and TasteDive carry API keys
// in the query string.
func sourceCheckMessage(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, errUnexpectedResponse) {
		return "unexpected response"
	}
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) {
		return fmt.Sprintf("HTTP %d", statusErr.Status)
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Err != nil {
			return urlErr.Err.Error()
		}
		return "network error"
	}
	return "network error"
}
