package server

import (
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shabbybastard/polka/internal/enrich"
	"github.com/shabbybastard/polka/internal/genres"
	"github.com/shabbybastard/polka/internal/store"
)

// Search extensions: genres by Russian names and ISBN.

// genreCache — genre counters change rarely, so we cache them.
type genreCache struct {
	mu      sync.Mutex
	counts  []store.GenreCount
	fetched time.Time
}

type genreEntry struct {
	Code  string
	Name  string
	Books int
}

const genreCacheTTL = 5 * time.Minute

// searchStatsCache avoids re-running three FTS count queries for every
// keystroke. Stats are exact but short-lived; the catalog changes rarely.
type searchStatsCache struct {
	mu      sync.Mutex
	entries map[string]searchStatsEntry
}

type searchStatsEntry struct {
	stats   store.SearchStats
	expires time.Time
}

const (
	searchStatsTTL   = 30 * time.Second
	searchStatsLimit = 256
)

func (c *searchStatsCache) get(key string) (store.SearchStats, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expires) {
		if ok {
			delete(c.entries, key)
		}
		return store.SearchStats{}, false
	}
	return e.stats, true
}

func (c *searchStatsCache) put(key string, stats store.SearchStats) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]searchStatsEntry, 32)
	}
	if len(c.entries) >= searchStatsLimit {
		for k, v := range c.entries {
			if time.Now().After(v.expires) {
				delete(c.entries, k)
			}
		}
		for k := range c.entries {
			if len(c.entries) < searchStatsLimit {
				break
			}
			delete(c.entries, k)
		}
	}
	c.entries[key] = searchStatsEntry{stats: stats, expires: time.Now().Add(searchStatsTTL)}
}

func (s *Server) invalidateSearchStats() {
	s.searchStats.mu.Lock()
	s.searchStats.entries = nil
	s.searchStats.mu.Unlock()
}

func (s *Server) invalidateGenreCache() {
	s.genres.mu.Lock()
	s.genres.counts = nil
	s.genres.fetched = time.Time{}
	s.genres.mu.Unlock()
}

func (s *Server) genreList(r *http.Request) []genreEntry {
	lang := reqLang(r)
	s.genres.mu.Lock()
	defer s.genres.mu.Unlock()
	if time.Since(s.genres.fetched) >= genreCacheTTL || s.genres.counts == nil {
		if counts, err := s.st.GenresWithCounts(r.Context()); err == nil {
			s.genres.counts = counts
			s.genres.fetched = time.Now()
		}
	}
	// Only counters are cached; names are localized (or taken from the
	// external source) on the fly.
	enabled := s.enabledTagSources(r.Context())
	list := make([]genreEntry, 0, len(s.genres.counts))
	for _, g := range s.genres.counts {
		if g.Source != "" && !enabled[g.Source] {
			continue
		}
		name := g.Name
		if name == "" {
			name = genres.NameLang(g.Code, lang)
		}
		list = append(list, genreEntry{Code: g.Code, Name: name, Books: g.Books})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Books > list[j].Books })
	return list
}

// searchGenres filters genres by a substring of the Russian name or the code.
func (s *Server) searchGenres(r *http.Request, query string) []genreEntry {
	q := strings.ToLower(strings.ReplaceAll(query, "ё", "е"))
	var out []genreEntry
	for _, g := range s.genreList(r) {
		name := strings.ToLower(strings.ReplaceAll(g.Name, "ё", "е"))
		if strings.Contains(name, q) || strings.Contains(strings.ToLower(g.Code), q) {
			out = append(out, g)
		}
	}
	return out
}

// GET /main/getBooks/getSearchGenres?search=
func (s *Server) handleGetSearchGenres(w http.ResponseWriter, r *http.Request) {
	query := searchParam(r)
	list := make([]map[string]any, 0)
	if query != "" {
		for _, g := range s.searchGenres(r, query) {
			list = append(list, map[string]any{
				"GenreCode": g.Code, "GenreName": g.Name, "Books": g.Books,
			})
		}
	}
	writeJSON(w, map[string]any{"genresList": list})
}

// isbnBooks — ISBN search: the local column first, then external title
// resolution followed by a collection search.
func (s *Server) isbnBooks(r *http.Request, query string) []store.Book {
	books, err := s.st.SearchByISBN(r.Context(), query)
	if err == nil && len(books) > 0 {
		return books
	}
	enabled := s.enrichEnabled(r)
	if !enabled[enrich.SourceOpenLibrary] && !enabled[enrich.SourceGoogleBooks] {
		return nil
	}
	info, err := s.enrich.ResolveISBN(r.Context(), store.NormalizeISBN(query))
	if err != nil || info == nil {
		return nil
	}
	if b, err := s.st.MatchBook(r.Context(), info.Title, info.Author); err == nil {
		return []store.Book{*b}
	}
	// Resolution succeeded but there is no exact match — show close title matches.
	if loose, err := s.st.SearchTitles(r.Context(), info.Title, 10); err == nil {
		return loose
	}
	return nil
}
