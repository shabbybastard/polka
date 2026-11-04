// Package enrich enriches books with external ratings and covers:
// LiveLib, Google Books, Open Library. Results are cached on disk.
package enrich

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	SourceLiveLib     = "livelib"
	SourceGoogleBooks = "google_books"
	SourceOpenLibrary = "open_library"

	successTTL   = 30 * 24 * time.Hour
	negativeTTL  = 7 * 24 * time.Hour
	transientTTL = 5 * time.Minute
	httpTimeout  = 6 * time.Second

	// Background genre loading is deliberately slow: LiveLib treats a
	// bursting client as a bot and starts returning 403.
	throttleDelay  = 2 * time.Second
	throttleJitter = 750 * time.Millisecond
	blockedBackoff = time.Hour
)

// Sources lists all sources in priority order.
var Sources = []string{SourceLiveLib, SourceGoogleBooks, SourceOpenLibrary}

type Result struct {
	Source       string   `json:"source"`
	Rating       float64  `json:"rating"`
	RatingsCount int      `json:"ratingsCount,omitempty"`
	InfoURL      string   `json:"infoUrl,omitempty"`
	CoverURL     string   `json:"coverUrl,omitempty"`
	Tags         []string `json:"tags,omitempty"`
}

type Enrichment struct {
	Primary          *Result  `json:"primary"`
	Sources          []Result `json:"sources"`
	Negative         bool     `json:"negative"`
	ExtraSourceCount int      `json:"extraSourceCount"`
	CoverURL         string   `json:"coverUrl,omitempty"`
	Tags             []string `json:"tags,omitempty"`
}

type cacheEntry struct {
	Result    *Enrichment `json:"result"`
	FetchedAt time.Time   `json:"fetchedAt"`
	// TagKey lists the tag sources used for this entry. Changing the
	// administrator's per-source toggles invalidates the cached tags.
	TagKey string `json:"tagKey,omitempty"`
}

type Provider struct {
	client    *http.Client
	cachePath string

	mu      sync.Mutex
	cache   map[string]cacheEntry
	similar map[string]similarEntry
	backoff map[string]time.Time // source -> do not query until

	throttleMu  sync.Mutex
	lastRequest map[string]time.Time

	// Base URLs are overridden in tests.
	LiveLibBase     string
	GoogleBase      string
	OpenLibraryBase string
	FantLabBase     string
	TasteDiveBase   string
}

func New(cachePath string) *Provider {
	p := &Provider{
		client:          &http.Client{Timeout: httpTimeout},
		cachePath:       cachePath,
		cache:           map[string]cacheEntry{},
		backoff:         map[string]time.Time{},
		lastRequest:     map[string]time.Time{},
		LiveLibBase:     "https://www.livelib.ru",
		GoogleBase:      "https://www.googleapis.com",
		OpenLibraryBase: "https://openlibrary.org",
		FantLabBase:     "https://api.fantlab.ru",
		TasteDiveBase:   "https://tastedive.com",
	}
	p.loadCache()
	return p
}

func (p *Provider) loadCache() {
	data, err := os.ReadFile(p.cachePath)
	if err != nil {
		return
	}
	json.Unmarshal(data, &p.cache)
}

func (p *Provider) persistCache() {
	data, err := json.Marshal(p.cache)
	if err != nil {
		return
	}
	tmp := p.cachePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	os.Rename(tmp, p.cachePath)
}

func tagSourcesKey(tagSources map[string]bool) string {
	var keys []string
	for _, source := range Sources {
		if tagSources[source] {
			keys = append(keys, source)
		}
	}
	return strings.Join(keys, ",")
}

// Get returns book enrichment for interactive use without throttling.
func (p *Provider) Get(ctx context.Context, key, title, author string, enabled map[string]bool, tagSources map[string]bool) *Enrichment {
	return p.getEnrichment(ctx, key, title, author, enabled, tagSources, false)
}

// GetThrottled is used by the background genre loader: it waits between
// actual HTTP requests so a bulk run does not trip anti-bot protection.
func (p *Provider) GetThrottled(ctx context.Context, key, title, author string, enabled map[string]bool, tagSources map[string]bool) *Enrichment {
	return p.getEnrichment(ctx, key, title, author, enabled, tagSources, true)
}

func (p *Provider) getEnrichment(ctx context.Context, key, title, author string, enabled map[string]bool, tagSources map[string]bool, throttle bool) *Enrichment {
	tagKey := tagSourcesKey(tagSources)
	p.mu.Lock()
	if e, ok := p.cache[key]; ok {
		ttl := successTTL
		if e.Result.Negative {
			ttl = negativeTTL
		}
		if time.Since(e.FetchedAt) < ttl && (tagKey == "" || e.TagKey == tagKey) {
			p.mu.Unlock()
			if tagKey == "" && len(e.Result.Tags) > 0 {
				// Do not leak tags through a cache entry when the
				// administrator has every tag source switched off.
				clone := *e.Result
				clone.Tags = nil
				return &clone
			}
			return e.Result
		}
	}
	p.mu.Unlock()

	res := &Enrichment{}
	transientOnly := true
	for _, source := range Sources {
		// A source may be enabled only for genre tags while its rating
		// switch is off.
		if !enabled[source] && !tagSources[source] {
			continue
		}
		p.mu.Lock()
		wait := p.backoff[source]
		p.mu.Unlock()
		if time.Now().Before(wait) {
			continue
		}

		r, err := p.fetch(ctx, source, title, author, tagSources[source], throttle)
		if err != nil {
			// Rate limits and bot blocks deserve a much longer pause than
			// a temporary network failure.
			delay := transientTTL
			var statusErr *httpStatusError
			if errors.As(err, &statusErr) &&
				(statusErr.Status == http.StatusForbidden || statusErr.Status == http.StatusTooManyRequests) {
				delay = blockedBackoff
			}
			p.mu.Lock()
			p.backoff[source] = time.Now().Add(delay)
			p.mu.Unlock()
			continue
		}
		transientOnly = false
		if r != nil {
			res.Sources = append(res.Sources, *r)
			if res.Primary == nil {
				res.Primary = r
			}
			if res.CoverURL == "" {
				res.CoverURL = r.CoverURL
			}
		}
	}

	if len(res.Sources) > 0 {
		res.ExtraSourceCount = len(res.Sources) - 1
		if tagKey != "" {
			res.Tags = collectTags(res.Sources)
		}
	} else {
		res.Negative = true
		if transientOnly {
			// No source responded — do not remember it as "not found".
			return res
		}
	}

	p.mu.Lock()
	p.cache[key] = cacheEntry{Result: res, FetchedAt: time.Now(), TagKey: tagKey}
	p.persistCache()
	p.mu.Unlock()
	return res
}

// collectTags merges tag lists from all sources, dropping duplicates and
// overlong subject phrases from library catalogs.
func collectTags(sources []Result) []string {
	const maxTags = 12
	seen := map[string]bool{}
	out := make([]string, 0, maxTags)
	for _, src := range sources {
		for _, tag := range src.Tags {
			tag = strings.TrimSpace(strings.Trim(tag, `.,;:`))
			if tag == "" || len([]rune(tag)) > 80 {
				continue
			}
			key := strings.ToLower(tag)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, tag)
			if len(out) >= maxTags {
				return out
			}
		}
	}
	return out
}

func (p *Provider) fetch(ctx context.Context, source, title, author string, withTags, throttle bool) (*Result, error) {
	switch source {
	case SourceLiveLib:
		return p.fetchLiveLib(ctx, title, author, withTags, throttle)
	case SourceGoogleBooks:
		return p.fetchGoogleBooks(ctx, title, author, throttle)
	case SourceOpenLibrary:
		return p.fetchOpenLibrary(ctx, title, author, withTags, throttle)
	}
	return nil, nil
}

// throttle spaces real HTTP requests to one source. It is a no-op for
// interactive requests; the background job enables it.
func (p *Provider) throttle(ctx context.Context, source string) {
	p.throttleMu.Lock()
	defer p.throttleMu.Unlock()
	delay := throttleDelay + time.Duration(rand.Int63n(int64(throttleJitter)))
	wait := time.Until(p.lastRequest[source].Add(delay))
	if wait > 0 {
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
	p.lastRequest[source] = time.Now()
}

type httpStatusError struct {
	URL    string
	Status int
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("%s: HTTP %d", e.URL, e.Status)
}

func (p *Provider) get(ctx context.Context, rawURL string, browser bool) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if browser {
		req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) Gecko/20100101 Firefox/125.0")
		req.Header.Set("Accept-Language", "ru,en;q=0.7")
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &httpStatusError{URL: rawURL, Status: resp.StatusCode}
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

// --- LiveLib: search page parsing ---

var (
	livelibRe       = regexp.MustCompile(`(?s)href="(/book/\d+[^"]*)".*?itemprop="ratingValue">([\d.,]+)`)
	livelibGenreRe  = regexp.MustCompile(`"genre"\s*:\s*\[((?:"[^"]*"\s*,?\s*)*)\]`)
	livelibGenreVal = regexp.MustCompile(`"([^"]+)"`)
)

func (p *Provider) fetchLiveLib(ctx context.Context, title, author string, includeTags, throttle bool) (*Result, error) {
	q := strings.TrimSpace(title)
	if throttle {
		p.throttle(ctx, SourceLiveLib)
	}
	body, err := p.get(ctx, p.LiveLibBase+"/find/books/"+url.PathEscape(q), true)
	if err != nil {
		return nil, err
	}
	m := livelibRe.FindSubmatch(body)
	if m == nil {
		return nil, nil // page fetched, no book found
	}
	rating, err := strconv.ParseFloat(strings.ReplaceAll(string(m[2]), ",", "."), 64)
	if err != nil || rating <= 0 {
		return nil, nil
	}
	res := &Result{
		Source:  SourceLiveLib,
		Rating:  rating,
		InfoURL: p.LiveLibBase + string(m[1]),
	}
	if includeTags {
		// The search result only has the rating; genres live on the book
		// page in its JSON-LD. A failure here must not discard the rating.
		if throttle {
			p.throttle(ctx, SourceLiveLib)
		}
		if page, err := p.get(ctx, res.InfoURL, true); err == nil {
			res.Tags = livelibTags(page)
		}
	}
	return res, nil
}

func livelibTags(page []byte) []string {
	m := livelibGenreRe.FindSubmatch(page)
	if m == nil {
		return nil
	}
	var out []string
	for _, q := range livelibGenreVal.FindAllSubmatch(m[1], -1) {
		if tag := strings.TrimSpace(string(q[1])); tag != "" {
			out = append(out, tag)
		}
	}
	return out
}

// --- Google Books ---

func (p *Provider) fetchGoogleBooks(ctx context.Context, title, author string, throttle bool) (*Result, error) {
	if throttle {
		p.throttle(ctx, SourceGoogleBooks)
	}
	q := fmt.Sprintf(`intitle:%q`, title)
	if author != "" {
		q += fmt.Sprintf(` inauthor:%q`, author)
	}
	u := p.GoogleBase + "/books/v1/volumes?maxResults=10&q=" + url.QueryEscape(q)
	body, err := p.get(ctx, u, false)
	if err != nil {
		return nil, err
	}
	var data struct {
		Items []struct {
			VolumeInfo struct {
				AverageRating float64 `json:"averageRating"`
				RatingsCount  int     `json:"ratingsCount"`
				InfoLink      string  `json:"infoLink"`
				ImageLinks    struct {
					Thumbnail string `json:"thumbnail"`
				} `json:"imageLinks"`
			} `json:"volumeInfo"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	for _, item := range data.Items {
		v := item.VolumeInfo
		if v.AverageRating <= 0 {
			continue
		}
		return &Result{
			Source:       SourceGoogleBooks,
			Rating:       v.AverageRating,
			RatingsCount: v.RatingsCount,
			InfoURL:      v.InfoLink,
			CoverURL:     strings.Replace(v.ImageLinks.Thumbnail, "http://", "https://", 1),
		}, nil
	}
	return nil, nil
}

// --- Open Library ---

func (p *Provider) fetchOpenLibrary(ctx context.Context, title, author string, withTags, throttle bool) (*Result, error) {
	if throttle {
		p.throttle(ctx, SourceOpenLibrary)
	}
	params := url.Values{
		"title":  {title},
		"limit":  {"5"},
		"fields": {"key,title,ratings_average,ratings_count,cover_i,subject"},
	}
	if author != "" {
		params.Set("author", author)
	}
	body, err := p.get(ctx, p.OpenLibraryBase+"/search.json?"+params.Encode(), false)
	if err != nil {
		return nil, err
	}
	var data struct {
		Docs []struct {
			Key            string   `json:"key"`
			RatingsAverage float64  `json:"ratings_average"`
			RatingsCount   int      `json:"ratings_count"`
			CoverI         int      `json:"cover_i"`
			Subject        []string `json:"subject"`
		} `json:"docs"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	var fallback *Result
	for _, doc := range data.Docs {
		var tags []string
		if withTags {
			tags = doc.Subject
		}
		r := &Result{
			Source:       SourceOpenLibrary,
			Rating:       doc.RatingsAverage,
			RatingsCount: doc.RatingsCount,
			InfoURL:      p.OpenLibraryBase + doc.Key,
			Tags:         tags,
		}
		if doc.CoverI > 0 {
			r.CoverURL = fmt.Sprintf("https://covers.openlibrary.org/b/id/%d-M.jpg", doc.CoverI)
		}
		if doc.RatingsAverage > 0 {
			return r, nil
		}
		// Keep a subject-only match as a fallback so books without an
		// Open Library rating can still contribute tags.
		if fallback == nil && len(doc.Subject) > 0 {
			fallback = r
		}
	}
	return fallback, nil
}
