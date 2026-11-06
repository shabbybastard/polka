// Package enrich enriches books with external ratings and covers:
// LiveLib, Google Books, Open Library. Results are cached on disk.
package enrich

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"unicode"

	"golang.org/x/net/html"
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

	// Bump this when the parser or the cached result shape changes.
	cacheVersion = 5
)

// Sources lists all sources in priority order.
var Sources = []string{SourceLiveLib, SourceFantLab, SourceGoogleBooks, SourceOpenLibrary}

type Result struct {
	Source       string   `json:"source"`
	Rating       float64  `json:"rating"`
	RatingMax    int      `json:"ratingMax,omitempty"`
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
	// SourceKey lists the rating sources enabled for this entry, so
	// toggling a source does not keep serving stale source lists.
	SourceKey string `json:"sourceKey,omitempty"`
	// GoogleKey changes when the Google Books API key is replaced, making
	// old empty Google results re-fetch automatically.
	GoogleKey string `json:"googleKey,omitempty"`
	Version   int    `json:"version,omitempty"`
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

	googleAPIKey string // guarded by mu

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

func enabledSourcesKey(enabled map[string]bool) string {
	var keys []string
	for _, source := range Sources {
		if enabled[source] {
			keys = append(keys, source)
		}
	}
	return strings.Join(keys, ",")
}

func googleKeyTag(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}

// SetGoogleAPIKey updates the key used for Google Books queries. Changing it
// also clears a previous Google backoff so the new key is tried immediately.
func (p *Provider) SetGoogleAPIKey(key string) {
	key = strings.TrimSpace(key)
	p.mu.Lock()
	if p.googleAPIKey != key {
		p.googleAPIKey = key
		delete(p.backoff, SourceGoogleBooks)
	}
	p.mu.Unlock()
}

func (p *Provider) googleAPIKeyValue() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.googleAPIKey
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

// RefreshThrottled ignores a cached rating result for one book and fetches
// it again. Used by the "empty only" manual rating job.
func (p *Provider) RefreshThrottled(ctx context.Context, key, title, author string, enabled map[string]bool, tagSources map[string]bool) *Enrichment {
	p.mu.Lock()
	delete(p.cache, key)
	p.mu.Unlock()
	return p.GetThrottled(ctx, key, title, author, enabled, tagSources)
}

// HasCachedRating reports whether the cache already has a rating from one of
// the currently enabled sources for this book.
func (p *Provider) HasCachedRating(key string, enabled map[string]bool) bool {
	sourceKey := enabledSourcesKey(enabled)
	googleKey := googleKeyTag(p.googleAPIKeyValue())
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.cache[key]
	if !ok || e.Result == nil || e.Version != cacheVersion || e.SourceKey != sourceKey || e.GoogleKey != googleKey {
		return false
	}
	for _, src := range e.Result.Sources {
		if enabled[src.Source] && src.Rating > 0 {
			return true
		}
	}
	return false
}

// Cached returns a read-only view of the enrichment already stored for a
// book. It never performs network requests; disabled sources and expired
// entries are omitted. The Telegram bot uses it for instant book cards.
func (p *Provider) Cached(key string, enabled map[string]bool) *Enrichment {
	sourceKey := enabledSourcesKey(enabled)
	googleKey := googleKeyTag(p.googleAPIKeyValue())
	p.mu.Lock()
	e, ok := p.cache[key]
	p.mu.Unlock()
	if !ok || e.Result == nil || e.Version != cacheVersion || e.SourceKey != sourceKey || e.GoogleKey != googleKey {
		return nil
	}
	ttl := successTTL
	if e.Result.Negative {
		ttl = negativeTTL
	}
	if time.Since(e.FetchedAt) >= ttl {
		return nil
	}
	view := &Enrichment{}
	for _, src := range e.Result.Sources {
		if enabled[src.Source] {
			view.Sources = append(view.Sources, src)
		}
	}
	if len(view.Sources) == 0 {
		return nil
	}
	for i := range view.Sources {
		src := view.Sources[i]
		if view.Primary == nil || (view.Primary.Rating <= 0 && src.Rating > 0) {
			copy := src
			view.Primary = &copy
		}
	}
	view.ExtraSourceCount = len(view.Sources) - 1
	if view.ExtraSourceCount < 0 {
		view.ExtraSourceCount = 0
	}
	return view
}

// ForgetBook drops cached enrichment and similar-books data for one book.
// The next page load queries the sources again.
func (p *Provider) ForgetBook(bookID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.cache[bookID]; ok {
		delete(p.cache, bookID)
		p.persistCache()
	}
	if p.similar == nil {
		p.loadSimilarCache()
	}
	if _, ok := p.similar["sim:"+bookID]; ok {
		delete(p.similar, "sim:"+bookID)
		p.persistSimilarCache()
	}
}

func (p *Provider) getEnrichment(ctx context.Context, key, title, author string, enabled map[string]bool, tagSources map[string]bool, throttle bool) *Enrichment {
	tagKey := tagSourcesKey(tagSources)
	sourceKey := enabledSourcesKey(enabled)
	googleKey := googleKeyTag(p.googleAPIKeyValue())
	p.mu.Lock()
	if e, ok := p.cache[key]; ok {
		ttl := successTTL
		if e.Result.Negative {
			ttl = negativeTTL
		}
		if e.Version == cacheVersion && e.SourceKey == sourceKey && e.GoogleKey == googleKey &&
			time.Since(e.FetchedAt) < ttl && (tagKey == "" || e.TagKey == tagKey) {
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
		if source == SourceGoogleBooks && googleKey == "" {
			// Google Books rejects unauthenticated traffic with 429, so
			// do not waste a request (and a backoff) without a key.
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
			// Prefer a source that actually has a rating as the primary
			// result; a tag-only source must not hide another rating.
			if res.Primary == nil || (res.Primary.Rating <= 0 && r.Rating > 0) {
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
	p.cache[key] = cacheEntry{
		Result: res, FetchedAt: time.Now(),
		TagKey: tagKey, SourceKey: sourceKey, GoogleKey: googleKey, Version: cacheVersion,
	}
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
	case SourceFantLab:
		return p.fetchFantLab(ctx, title, author, throttle)
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

// livelibHit is one book card from a LiveLib search result. The result page
// already contains the author, rating and genres, so there is no reason to
// open the book page with a second request.
type livelibHit struct {
	InfoURL      string
	Title        string
	Authors      []string
	Rating       float64
	RatingsCount int
	Tags         []string
}

var livelibCountRe = regexp.MustCompile(`(?i)\(([\d\s\x{00A0}]+)\s*читател`)

func livelibRatingCount(title string) int {
	m := livelibCountRe.FindStringSubmatch(title)
	if m == nil {
		return 0
	}
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, m[1])
	count, _ := strconv.Atoi(digits)
	return count
}

func (p *Provider) fetchLiveLib(ctx context.Context, title, author string, includeTags, throttle bool) (*Result, error) {
	q := strings.TrimSpace(title)
	if first := firstAuthor(author); first != "" {
		q += " " + first
	}
	if throttle {
		p.throttle(ctx, SourceLiveLib)
	}
	body, err := p.get(ctx, p.LiveLibBase+"/find/books/"+url.PathEscape(q), true)
	if err != nil {
		return nil, err
	}
	hit := bestLiveLibHit(parseLiveLibSearch(body), title, author)
	if hit == nil {
		return nil, nil // page fetched, but no confident match
	}
	res := &Result{
		Source:       SourceLiveLib,
		Rating:       hit.Rating,
		RatingMax:    5,
		RatingsCount: hit.RatingsCount,
		InfoURL:      p.LiveLibBase + hit.InfoURL,
	}
	if includeTags {
		res.Tags = hit.Tags
	}
	if res.Rating <= 0 && len(res.Tags) == 0 {
		return nil, nil
	}
	return res, nil
}

// firstAuthor keeps the LiveLib query focused on the first author. The store
// joins multiple authors with ", "; searching for all of them at once usually
// returns nothing.
func firstAuthor(authors string) string {
	if i := strings.IndexRune(authors, ','); i >= 0 {
		authors = authors[:i]
	}
	return strings.TrimSpace(authors)
}

func parseLiveLibSearch(body []byte) []livelibHit {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil
	}
	var hits []livelibHit
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "div" && hasClass(n, "object-wrapper") {
			if hit, ok := parseLiveLibHit(n); ok {
				hits = append(hits, hit)
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return hits
}

func parseLiveLibHit(block *html.Node) (livelibHit, bool) {
	var hit livelibHit
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if hit.RatingsCount == 0 {
				if title := attr(n, "title"); title != "" {
					hit.RatingsCount = livelibRatingCount(title)
				}
			}
			switch {
			case n.Data == "a" && hasClass(n, "title") && hit.Title == "":
				hit.Title = nodeText(n)
				if href := attr(n, "href"); strings.HasPrefix(href, "/book/") {
					hit.InfoURL = href
				}
			case n.Data == "a" && hasClass(n, "description"):
				name := strings.TrimSpace(attr(n, "title"))
				if name == "" {
					name = nodeText(n)
				}
				if name != "" {
					hit.Authors = append(hit.Authors, name)
				}
			case n.Data == "a" && hasClass(n, "label-genre"):
				if tag := nodeText(n); tag != "" {
					hit.Tags = append(hit.Tags, tag)
				}
			case n.Data == "span" && attr(n, "itemprop") == "ratingValue" && hit.Rating == 0:
				raw := strings.ReplaceAll(nodeText(n), ",", ".")
				if v, err := strconv.ParseFloat(raw, 64); err == nil {
					hit.Rating = v
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(block)
	if hit.Title == "" || hit.InfoURL == "" {
		return hit, false
	}
	return hit, true
}

// bestLiveLibHit prefers an exact title and matching author. It never falls
// back to "the first result": a wrong rating and someone else's genres are
// worse than no enrichment at all.
func bestLiveLibHit(hits []livelibHit, title, author string) *livelibHit {
	bestScore := 0
	var best *livelibHit
	for i := range hits {
		h := &hits[i]
		score := 0
		if normalizeName(h.Title) == normalizeName(title) {
			score += 4
		} else if titlesMatch(title, h.Title) {
			score += 2
		} else {
			continue
		}
		if author != "" {
			switch {
			case len(h.Authors) == 0:
				// A card without an author is only trustworthy when its
				// title is exactly the requested one.
				if score < 4 {
					continue
				}
			case authorsMatch(author, h.Authors):
				score += 3
			default:
				continue
			}
		}
		if score > bestScore {
			bestScore = score
			best = h
		}
	}
	return best
}

func titlesMatch(query, candidate string) bool {
	q := strings.Fields(normalizeName(query))
	c := strings.Fields(normalizeName(candidate))
	if len(q) == 0 || len(c) < len(q) {
		return false
	}
	// Every query word must appear in the candidate and keep its order.
	// Edition suffixes ("том 1", "иллюстрации") are tolerated; a different
	// book that only shares the first word is not.
	pos, extra := 0, 0
	for _, token := range c {
		if pos < len(q) && token == q[pos] {
			pos++
		} else {
			extra++
		}
	}
	if pos != len(q) {
		return false
	}
	if len(q) == 1 {
		return extra == 0
	}
	return extra <= 2
}

func authorsMatch(query string, candidates []string) bool {
	for _, candidate := range candidates {
		if authorNameMatches(query, candidate) {
			return true
		}
	}
	return false
}

func authorNameMatches(a, b string) bool {
	at := significantTokens(a)
	bt := significantTokens(b)
	if len(at) == 0 || len(bt) == 0 {
		return false
	}
	set := make(map[string]bool, len(bt))
	for _, token := range bt {
		set[token] = true
	}
	common := 0
	for _, token := range at {
		if set[token] {
			common++
		}
	}
	// Require at least two matching name parts, or the only part when the
	// query has just a surname. Initials are ignored by significantTokens.
	need := len(at)
	if len(bt) < need {
		need = len(bt)
	}
	if need > 2 {
		need = 2
	}
	return common >= need
}

func significantTokens(s string) []string {
	var out []string
	for _, token := range strings.Fields(normalizeName(s)) {
		if len([]rune(token)) >= 2 {
			out = append(out, token)
		}
	}
	return out
}

// normalizeName lowercases a name, unifies ё and drops punctuation so that
// "Мастер и Маргарита" and "Мастер и Маргарита (иллюстрации)" compare well.
func normalizeName(s string) string {
	s = strings.ToLower(strings.ReplaceAll(s, "ё", "е"))
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			b.WriteString(node.Data)
			b.WriteByte(' ')
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

func hasClass(n *html.Node, class string) bool {
	for _, token := range strings.Fields(attr(n, "class")) {
		if token == class {
			return true
		}
	}
	return false
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// --- FantLab ---

type fantlabID string

func (id *fantlabID) UnmarshalJSON(data []byte) error {
	*id = fantlabID(strings.Trim(strings.TrimSpace(string(data)), `"`))
	return nil
}

type fantlabFloat float64

func (n *fantlabFloat) UnmarshalJSON(data []byte) error {
	s := strings.TrimSpace(string(data))
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	s = strings.Trim(s, `"`)
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}
	*n = fantlabFloat(v)
	return nil
}

// fantlabBookType filters out articles, reviews and other non-book entries.
// FantLab exposes the type in name_eng in search results and in
// work_type_name on the work page.
func fantlabBookType(nameEng, workTypeName string, workTypeID int) bool {
	if workTypeID == 7 || workTypeID == 52 { // other, review
		return false
	}
	for _, raw := range []string{nameEng, workTypeName} {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "article", "review", "other", "documental", "documentary":
			return false
		}
	}
	return true
}

func (p *Provider) fetchFantLab(ctx context.Context, title, author string, throttle bool) (*Result, error) {
	if throttle {
		p.throttle(ctx, SourceFantLab)
	}
	q := title
	if first := firstAuthor(author); first != "" {
		q += " " + first
	}
	body, err := p.get(ctx, p.FantLabBase+"/search-works?q="+url.QueryEscape(q), true)
	if err != nil {
		return nil, err
	}
	var search struct {
		Matches []struct {
			WorkID        fantlabID      `json:"work_id"`
			RusName       string         `json:"rusname"`
			Name          string         `json:"name"`
			NameEng       string         `json:"name_eng"`
			WorkTypeID    int            `json:"work_type_id"`
			WorkTypeName  string         `json:"work_type_name"`
			AutorRus      string         `json:"autor_rusname"`
			AllRus        string         `json:"all_autor_rusname"`
			AllName       string         `json:"all_autor_name"`
			Rating        []fantlabFloat `json:"rating"`
			Midmark       []fantlabFloat `json:"midmark"`
			MidmarkWeight []fantlabFloat `json:"midmark_by_weight"`
		} `json:"matches"`
	}
	if err := json.Unmarshal(body, &search); err != nil {
		return nil, err
	}
	var pickedID string
	bestScore := 0
	for i := range search.Matches {
		m := &search.Matches[i]
		if !fantlabBookType(m.NameEng, m.WorkTypeName, m.WorkTypeID) {
			continue
		}
		score := 0
		switch {
		case normalizeName(m.RusName) == normalizeName(title) || normalizeName(m.Name) == normalizeName(title):
			score += 4
		case titlesMatch(title, m.RusName) || titlesMatch(title, m.Name):
			score += 2
		default:
			continue
		}
		if author != "" {
			names := []string{m.AutorRus, m.AllRus, m.AllName}
			if !authorsMatch(author, names) {
				continue
			}
			score += 3
		}
		if id := string(m.WorkID); id != "" && id != "0" && score > bestScore {
			bestScore = score
			pickedID = id
		}
	}
	if pickedID == "" {
		return nil, nil
	}

	if throttle {
		p.throttle(ctx, SourceFantLab)
	}
	body, err = p.get(ctx, p.FantLabBase+"/work/"+pickedID, true)
	if err != nil {
		return nil, err
	}
	var work struct {
		Rating struct {
			Rating     fantlabFloat `json:"rating"`
			TrueRating fantlabFloat `json:"true_rating"`
			Voters     int          `json:"voters"`
		} `json:"rating"`
		ValMidmarkByWeight fantlabFloat `json:"val_midmark_by_weight"`
		ValVoters          int          `json:"val_voters"`
		WorkTypeID         int          `json:"work_type_id"`
		WorkTypeName       string       `json:"work_type_name"`
	}
	if err := json.Unmarshal(body, &work); err != nil {
		return nil, err
	}
	if !fantlabBookType("", work.WorkTypeName, work.WorkTypeID) {
		return nil, nil
	}

	// FantLab rounds rating.rating to two decimals; true_rating keeps the
	// unrounded value (8.52 for Dune, while rating.rating is 8.61).
	raw := float64(work.Rating.TrueRating)
	if raw <= 0 {
		raw = float64(work.Rating.Rating)
	}
	if raw <= 0 {
		raw = float64(work.ValMidmarkByWeight)
	}
	if raw <= 0 {
		return nil, nil
	}
	rating := raw / 2
	if rating > 5 {
		rating = 5
	}
	if rating < 0 {
		rating = 0
	}
	if rating <= 0 {
		return nil, nil
	}
	count := work.Rating.Voters
	if count <= 0 {
		count = work.ValVoters
	}
	return &Result{
		Source:       SourceFantLab,
		Rating:       rating,
		RatingMax:    5,
		RatingsCount: count,
		InfoURL:      "https://fantlab.ru/work" + pickedID,
	}, nil
}

// --- Google Books ---

func (p *Provider) fetchGoogleBooks(ctx context.Context, title, author string, throttle bool) (*Result, error) {
	key := p.googleAPIKeyValue()
	if key == "" {
		return nil, nil
	}
	if throttle {
		p.throttle(ctx, SourceGoogleBooks)
	}
	q := fmt.Sprintf(`intitle:%q`, title)
	if author != "" {
		q += fmt.Sprintf(` inauthor:%q`, author)
	}
	u := p.GoogleBase + "/books/v1/volumes?maxResults=10&q=" + url.QueryEscape(q)
	u += "&key=" + url.QueryEscape(key)
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
			RatingMax:    5,
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
			RatingMax:    5,
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
