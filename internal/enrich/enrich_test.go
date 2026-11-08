package enrich

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const livelibHTML = `<html><body>
<div class="object-wrapper-outer">
  <div class="object-wrapper object-edition ll-redirect" data-link="/book/1017090091-master-i-margarita">
    <a href="/book/1017090091-master-i-margarita" title="Михаил Булгаков - Мастер и Маргарита"><img/></a>
    <div class="object-info">
      <div class="brow-title"><a href="/book/1017090091-master-i-margarita" class="title">Мастер и Маргарита</a></div>
      <a class="description" href="/author/1" title="Михаил Булгаков">Михаил Булгаков</a>
      <div class="brow-genres">
        <a class="label-genre" href="/genre/1">Классическая проза</a>
        <a class="label-genre" href="/genre/2">Роман</a>
        <a class="label-genre" href="/genre/3">Русская литература</a>
      </div>
      <div class="brow-stats-outer"><span class="rating-book marg-right"><span class="rating-value stars-color-orange" itemprop="ratingValue">4,5</span><span title="Рейтинг 4.500 (1 234 читателей, рейтинг ожидания 4.000)"></span></span></div>
    </div>
  </div>
</div>
</body></html>`

// livelibWrongFirstHTML reproduces the bug where the first search result is
// a different book that merely starts with the same word.
const livelibWrongFirstHTML = `<html><body>
<div class="object-wrapper-outer">
  <div class="object-wrapper object-edition" data-link="/book/1000000001-vot-kakoy-rasseyannyy">
    <div class="object-info">
      <div class="brow-title"><a href="/book/1000000001-vot-kakoy-rasseyannyy" class="title">Вот какой рассеянный</a></div>
      <a class="description" href="/author/2" title="Самуил Маршак">Самуил Маршак</a>
      <div class="brow-genres"><a class="label-genre" href="/genre/4">Детская литература</a></div>
      <span class="rating-value" itemprop="ratingValue">4.7</span>
    </div>
  </div>
</div>
<div class="object-wrapper-outer">
  <div class="object-wrapper object-edition" data-link="/book/1007412814-vot-ya-dzhonatan-safran-foer">
    <div class="object-info">
      <div class="brow-title"><a href="/book/1007412814-vot-ya-dzhonatan-safran-foer" class="title">Вот я</a></div>
      <a class="description" href="/author/3" title="Джонатан Сафран Фоер">Джонатан Сафран Фоер</a>
      <div class="brow-genres">
        <a class="label-genre" href="/genre/5">Современная проза</a>
        <a class="label-genre" href="/genre/6">Современная зарубежная литература</a>
      </div>
      <span class="rating-value" itemprop="ratingValue">3.9</span>
      <span title="Рейтинг 3.902 (996 читателей, рейтинг ожидания 4.000)"></span>
    </div>
  </div>
</div>
</body></html>`

const googleJSON = `{"items":[
  {"volumeInfo":{"title":"Без рейтинга"}},
  {"volumeInfo":{"title":"Мастер и Маргарита","averageRating":4.2,"ratingsCount":128,
    "infoLink":"http://books.google.com/books?id=x",
    "imageLinks":{"thumbnail":"http://books.google.com/cover.jpg"}}}
]}`

const openLibJSON = `{"docs":[
  {"key":"/works/OL1W","title":"Master","ratings_average":3.9,"ratings_count":55,"cover_i":777,
   "subject":["Russian fiction","Classics","Russian fiction"]}
]}`

func newTestProvider(t *testing.T, livelibOK, googleOK, openlibOK bool) *Provider {
	t.Helper()
	handler := func(ok bool, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			if !ok {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.Write([]byte(body))
		}
	}
	ll := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !livelibOK {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(livelibHTML))
	}))
	gb := httptest.NewServer(handler(googleOK, googleJSON))
	ol := httptest.NewServer(handler(openlibOK, openLibJSON))
	t.Cleanup(ll.Close)
	t.Cleanup(gb.Close)
	t.Cleanup(ol.Close)

	p := New(filepath.Join(t.TempDir(), "cache.json"))
	p.LiveLibBase = ll.URL
	p.GoogleBase = gb.URL
	p.OpenLibraryBase = ol.URL
	p.SetGoogleAPIKey("test-key")
	return p
}

func allEnabled() map[string]bool {
	return map[string]bool{SourceLiveLib: true, SourceGoogleBooks: true, SourceOpenLibrary: true}
}

func allTagSources() map[string]bool {
	return map[string]bool{SourceLiveLib: true, SourceOpenLibrary: true}
}

func TestEnrichAllSources(t *testing.T) {
	p := newTestProvider(t, true, true, true)
	res := p.Get(context.Background(), "1", "Мастер и Маргарита", "Булгаков", allEnabled(), allTagSources())

	if len(res.Sources) != 3 || res.Negative {
		t.Fatalf("sources = %d, negative = %v", len(res.Sources), res.Negative)
	}
	if res.Primary.Source != SourceLiveLib || res.Primary.Rating != 4.5 || res.Primary.RatingsCount != 1234 {
		t.Errorf("primary = %+v", res.Primary)
	}
	if res.ExtraSourceCount != 2 {
		t.Errorf("extraSourceCount = %d", res.ExtraSourceCount)
	}
	// Cover from the first source that has one (google)
	if res.CoverURL != "https://books.google.com/cover.jpg" {
		t.Errorf("coverUrl = %q", res.CoverURL)
	}
	gb := res.Sources[1]
	if gb.Source != SourceGoogleBooks || gb.RatingsCount != 128 {
		t.Errorf("google = %+v", gb)
	}
	ol := res.Sources[2]
	if ol.Source != SourceOpenLibrary || ol.CoverURL != "https://covers.openlibrary.org/b/id/777-M.jpg" {
		t.Errorf("openlib = %+v", ol)
	}
	if len(res.Tags) == 0 {
		t.Fatal("external tags were not collected")
	}
	// LiveLib genres come first, then Open Library subjects; duplicates
	// (case-insensitively) are dropped.
	joined := strings.Join(res.Tags, "|")
	for _, want := range []string{"Классическая проза", "Роман", "Russian fiction", "Classics"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing tag %q in %v", want, res.Tags)
		}
	}
	if strings.Count(strings.ToLower(joined), "russian fiction") != 1 {
		t.Errorf("duplicate tags were not removed: %v", res.Tags)
	}
}

func TestEnrichDisabledSources(t *testing.T) {
	p := newTestProvider(t, true, true, true)
	enabled := allEnabled()
	enabled[SourceLiveLib] = false
	res := p.Get(context.Background(), "2", "Мастер и Маргарита", "", enabled, nil)
	if res.Primary == nil || res.Primary.Source != SourceGoogleBooks {
		t.Errorf("primary with livelib off = %+v", res.Primary)
	}
	for _, src := range res.Sources {
		if src.Source == SourceLiveLib {
			t.Error("disabled source must not be queried")
		}
	}
}

func TestTagsWorkWhenRatingSourceDisabled(t *testing.T) {
	p := newTestProvider(t, true, false, false)
	enabled := map[string]bool{SourceLiveLib: false, SourceOpenLibrary: false}
	res := p.Get(context.Background(), "tags-only", "Мастер и Маргарита", "", enabled, map[string]bool{SourceLiveLib: true})
	if len(res.Tags) == 0 {
		t.Fatalf("tags were not fetched: %+v", res)
	}
}

func TestLiveLibTagsComeFromSearchPage(t *testing.T) {
	var searchHits, bookHits int
	ll := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/book/") {
			bookHits++
			w.Write([]byte("<html></html>"))
			return
		}
		searchHits++
		w.Write([]byte(livelibHTML))
	}))
	defer ll.Close()

	p := New(filepath.Join(t.TempDir(), "cache.json"))
	p.LiveLibBase = ll.URL
	enabled := map[string]bool{SourceLiveLib: true}

	res := p.Get(context.Background(), "tags-off", "Мастер и Маргарита", "", enabled, nil)
	if searchHits != 1 || bookHits != 0 {
		t.Errorf("tags off hits: search %d, book %d", searchHits, bookHits)
	}
	if len(res.Tags) != 0 {
		t.Errorf("tags off result: %v", res.Tags)
	}

	// Enabling tags ignores the tag-less cache entry but still only needs
	// the search page.
	res = p.Get(context.Background(), "tags-off", "Мастер и Маргарита", "", enabled, map[string]bool{SourceLiveLib: true})
	if searchHits != 2 || bookHits != 0 {
		t.Errorf("enabling tags: search %d, book %d", searchHits, bookHits)
	}
	if len(res.Tags) == 0 {
		t.Error("tags were not collected after enabling")
	}
}

func TestLiveLibPicksBookMatchingTitleAndAuthor(t *testing.T) {
	ll := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/book/") {
			t.Errorf("book page must not be requested: %s", r.URL.Path)
		}
		if !strings.Contains(r.URL.Path, "Джонатан Сафран Фоер") {
			t.Errorf("search query does not contain the author: %q", r.URL.Path)
		}
		w.Write([]byte(livelibWrongFirstHTML))
	}))
	defer ll.Close()

	p := New(filepath.Join(t.TempDir(), "cache.json"))
	p.LiveLibBase = ll.URL
	res := p.Get(context.Background(), "wrong-first", "Вот я", "Джонатан Сафран Фоер",
		map[string]bool{SourceLiveLib: true}, map[string]bool{SourceLiveLib: true})
	if res.Primary == nil {
		t.Fatalf("no match: %+v", res)
	}
	got := res.Primary
	if got.Rating != 3.9 || got.RatingsCount != 996 {
		t.Errorf("rating = %v/%d, want 3.9/996", got.Rating, got.RatingsCount)
	}
	if !strings.HasSuffix(got.InfoURL, "/book/1007412814-vot-ya-dzhonatan-safran-foer") {
		t.Errorf("infoUrl = %q", got.InfoURL)
	}
	joined := strings.Join(res.Tags, "|")
	for _, want := range []string{"Современная проза", "Современная зарубежная литература"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing tag %q in %v", want, res.Tags)
		}
	}
	if strings.Contains(joined, "Детская литература") {
		t.Errorf("foreign tags leaked: %v", res.Tags)
	}
}

func TestLiveLibNoConfidentMatch(t *testing.T) {
	ll := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(livelibWrongFirstHTML))
	}))
	defer ll.Close()

	p := New(filepath.Join(t.TempDir(), "cache.json"))
	p.LiveLibBase = ll.URL
	res := p.Get(context.Background(), "no-match", "Вот я", "Другой Автор",
		map[string]bool{SourceLiveLib: true}, map[string]bool{SourceLiveLib: true})
	if !res.Negative || res.Primary != nil {
		t.Fatalf("mismatched author must not enrich: %+v", res)
	}
}

func TestGoogleBooksUsesAPIKey(t *testing.T) {
	var gotKey string
	gb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.URL.Query().Get("key")
		w.Write([]byte(googleJSON))
	}))
	defer gb.Close()

	p := New(filepath.Join(t.TempDir(), "cache.json"))
	p.GoogleBase = gb.URL
	p.SetGoogleAPIKey("secret-key")
	res := p.Get(context.Background(), "google-key", "Мастер и Маргарита", "Булгаков",
		map[string]bool{SourceGoogleBooks: true}, nil)
	if res.Primary == nil || res.Primary.Source != SourceGoogleBooks {
		t.Fatalf("google result: %+v", res)
	}
	if gotKey != "secret-key" {
		t.Errorf("google key = %q", gotKey)
	}
}

func TestGoogleBooksSkippedWithoutKey(t *testing.T) {
	var hits int
	gb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Write([]byte(googleJSON))
	}))
	defer gb.Close()

	p := New(filepath.Join(t.TempDir(), "cache.json"))
	p.GoogleBase = gb.URL
	p.SetGoogleAPIKey("")
	res := p.Get(context.Background(), "google-no-key", "Мастер и Маргарита", "Булгаков",
		map[string]bool{SourceGoogleBooks: true}, nil)
	if hits != 0 {
		t.Errorf("google was queried without a key: %d", hits)
	}
	if !res.Negative {
		t.Errorf("expected negative result: %+v", res)
	}
}

func TestEnrichBackoffSkipMarksPartial(t *testing.T) {
	p := newTestProvider(t, true, true, true)
	p.mu.Lock()
	p.backoff[SourceLiveLib] = time.Now().Add(time.Hour)
	p.mu.Unlock()
	res := p.Get(context.Background(), "partial-backoff", "Мастер и Маргарита", "Булгаков", allEnabled(), allTagSources())
	if res.Negative || len(res.Sources) == 0 {
		t.Fatalf("expected results from available sources: %+v", res)
	}
	p.mu.Lock()
	entry, ok := p.cache["partial-backoff"]
	p.mu.Unlock()
	if !ok || !entry.Partial {
		t.Fatalf("backoff-skipped source must make the cache entry partial: %+v", entry)
	}
}

func TestEnrichTransientNotCached(t *testing.T) {
	p := newTestProvider(t, false, false, false)
	res := p.Get(context.Background(), "3", "Книга", "", allEnabled(), allTagSources())
	if !res.Negative {
		t.Error("all sources down -> negative response")
	}
	if _, cached := p.cache["3"]; cached {
		t.Error("transient failure must not be cached")
	}
}

func TestEnrichCachePersistence(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.json")

	p := newTestProvider(t, true, true, true)
	p.cachePath = cachePath
	p.Get(context.Background(), "5", "Мастер и Маргарита", "", allEnabled(), allTagSources())

	// A new provider reads the cache from disk and does not hit the network.
	p2 := New(cachePath)
	p2.SetGoogleAPIKey("test-key")
	p2.LiveLibBase = "http://127.0.0.1:1" // intentionally unreachable
	res := p2.Get(context.Background(), "5", "Мастер и Маргарита", "", allEnabled(), allTagSources())
	if res.Primary == nil || res.Primary.Rating != 4.5 {
		t.Errorf("cached result = %+v", res.Primary)
	}
}

func TestEnrichCacheJournalReplay(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.json")
	p := newTestProvider(t, true, true, true)
	p.cachePath = cachePath
	p.Get(context.Background(), "j1", "Мастер и Маргарита", "", allEnabled(), allTagSources())
	p.Get(context.Background(), "j2", "Мастер и Маргарита", "", allEnabled(), allTagSources())

	p2 := New(cachePath)
	p2.SetGoogleAPIKey("test-key")
	p2.LiveLibBase = "http://127.0.0.1:1"
	p2.GoogleBase = "http://127.0.0.1:1"
	p2.OpenLibraryBase = "http://127.0.0.1:1"
	if res := p2.Get(context.Background(), "j2", "Мастер и Маргарита", "", allEnabled(), allTagSources()); res.Primary == nil {
		t.Errorf("journal entry was not replayed: %+v", res)
	}
}

func TestEnrichCacheVersionInvalidates(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "cache.json")

	var hits int
	ll := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Write([]byte(livelibHTML))
	}))
	defer ll.Close()

	p := New(cachePath)
	p.LiveLibBase = ll.URL
	enabled := map[string]bool{SourceLiveLib: true}
	p.Get(context.Background(), "v", "Мастер и Маргарита", "", enabled, nil)
	if hits != 1 {
		t.Fatalf("first request hits = %d", hits)
	}

	// Simulate an entry written by an older parser.
	data, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	var cache map[string]cacheEntry
	if err := json.Unmarshal(data, &cache); err != nil {
		t.Fatal(err)
	}
	entry := cache["v"]
	entry.Version = cacheVersion - 1
	cache["v"] = entry
	if data, err = json.Marshal(cache); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachePath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	p2 := New(cachePath)
	p2.LiveLibBase = ll.URL
	p2.Get(context.Background(), "v", "Мастер и Маргарита", "", enabled, nil)
	if hits != 2 {
		t.Errorf("stale cache was reused: hits = %d", hits)
	}
}
