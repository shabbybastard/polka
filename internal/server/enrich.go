package server

import (
	"context"

	"encoding/json"
	"github.com/vestigiumincaligne/polka/internal/collections/sources"
	"net/http"
	"strconv"
	"strings"

	"github.com/vestigiumincaligne/polka/internal/enrich"
)

// enrichEnabled builds a map of enabled sources from settings.
func (s *Server) enrichEnabledCtx(ctx context.Context) map[string]bool {
	enabled := make(map[string]bool, len(enrich.Sources))
	for _, source := range enrich.Sources {
		enabled[source] = s.users.GetSetting(ctx, "enrich."+source, "1") == "1"
	}
	return enabled
}

func (s *Server) enrichEnabled(r *http.Request) map[string]bool {
	return s.enrichEnabledCtx(r.Context())
}

// tagSourceList is the fixed set of external sources that can contribute
// catalog tags. LiveLib genres are localized; Open Library subjects are
// usually English, hence the separate toggles.
var tagSourceList = []string{enrich.SourceLiveLib, enrich.SourceOpenLibrary}

func (s *Server) enabledTagSources(ctx context.Context) map[string]bool {
	// Backwards compatibility: before per-source switches existed, a
	// single "enrich.tags" flag enabled tags from every source.
	legacy := s.users.GetSetting(ctx, "enrich.tags", "")
	out := make(map[string]bool, len(tagSourceList))
	for _, source := range tagSourceList {
		v := s.users.GetSetting(ctx, "tags."+source, "")
		out[source] = v == "1" || (v == "" && legacy == "1")
	}
	return out
}

// GET /main/getBooks/getExternalEnrichment?bookId&title&author&isbn
func (s *Server) handleGetEnrichment(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	bookID := q.Get("bookId")
	title := strings.TrimSpace(strings.ReplaceAll(q.Get("title"), "+", " "))
	author := strings.TrimSpace(strings.ReplaceAll(q.Get("author"), "+", " "))
	if bookID == "" || title == "" {
		http.Error(w, "bookId and title are required", http.StatusBadRequest)
		return
	}
	tagSources := s.enabledTagSources(r.Context())
	res := s.enrich.Get(r.Context(), bookID, title, author, s.enrichEnabled(r), tagSources)
	if id, err := strconv.ParseInt(bookID, 10, 64); err == nil {
		for _, src := range res.Sources {
			if len(src.Tags) == 0 {
				continue
			}
			if err := s.st.SaveBookGenres(r.Context(), id, src.Source, src.Tags); err != nil {
				s.log.Warn("save external genres", "book", id, "source", src.Source, "error", err)
			} else {
				s.invalidateGenreCache()
			}
		}
	}
	writeJSON(w, res)
}

// --- Settings (administrator only) ---

func (s *Server) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	enrichment := map[string]bool{}
	for source, on := range s.enrichEnabled(r) {
		enrichment[source] = on
	}
	cfg := s.similarConfig(r)
	writeJSON(w, map[string]any{
		"enrichment": enrichment,
		"similar": map[string]bool{
			"fantlab":   cfg.FantLab,
			"tastedive": cfg.TasteDive,
		},
		"tastediveKey":      cfg.TasteDiveKey,
		"tagSources":        s.enabledTagSources(r.Context()),
		"opdsEnabled":       s.opdsEnabled(r),
		"homeHero":          s.users.GetSetting(r.Context(), "home.hero", "1") == "1",
		"smtp":              s.smtpSettings(r),
		"collectionSources": s.sourcesJSON(r.Context()),
		"library":           s.libraryStatus(),
	})
}

// libraryStatus reports the state of the library root for the admin panel:
// the path, whether it is set, and how many entries are visible. An empty
// directory with a non-empty catalog means the archives are not where the
// server looks for them.
func (s *Server) libraryStatus() map[string]any {
	h := s.lib.Health()
	dir := h.Root
	if dir == "" {
		dir = s.cfg.LibraryDir
	}
	return map[string]any{
		"dir":        dir,
		"configured": s.lib != nil,
		"exists":     h.Exists,
		"entries":    h.Entries,
	}
}

func (s *Server) handleSettingsSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enrichment   map[string]bool `json:"enrichment"`
		Similar      map[string]bool `json:"similar"`
		TasteDiveKey *string         `json:"tastediveKey"`
		TagSources   map[string]bool `json:"tagSources"`
		TagsEnabled  *bool           `json:"tagsEnabled"` // legacy single switch
		OpdsEnabled  *bool           `json:"opdsEnabled"`
		HomeHero     *bool           `json:"homeHero"`
		Smtp         *smtpInput      `json:"smtp"`
		Sources      map[string]bool `json:"collectionSources"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	boolVal := func(on bool) string {
		if on {
			return "1"
		}
		return "0"
	}
	for _, source := range enrich.Sources {
		if on, ok := req.Enrichment[source]; ok {
			if err := s.users.SetSetting(r.Context(), "enrich."+source, boolVal(on)); err != nil {
				s.apiError(w, err)
				return
			}
		}
	}
	for _, source := range []string{"fantlab", "tastedive"} {
		if on, ok := req.Similar[source]; ok {
			if err := s.users.SetSetting(r.Context(), "enrich.similar_"+source, boolVal(on)); err != nil {
				s.apiError(w, err)
				return
			}
		}
	}
	if req.TasteDiveKey != nil {
		if err := s.users.SetSetting(r.Context(), "tastedive_key", strings.TrimSpace(*req.TasteDiveKey)); err != nil {
			s.apiError(w, err)
			return
		}
	}
	if req.OpdsEnabled != nil {
		if err := s.users.SetSetting(r.Context(), "opds.enabled", boolVal(*req.OpdsEnabled)); err != nil {
			s.apiError(w, err)
			return
		}
	}
	if req.HomeHero != nil {
		if err := s.users.SetSetting(r.Context(), "home.hero", boolVal(*req.HomeHero)); err != nil {
			s.apiError(w, err)
			return
		}
	}
	if req.TagSources != nil {
		for _, source := range tagSourceList {
			on, ok := req.TagSources[source]
			if !ok {
				continue
			}
			if err := s.users.SetSetting(r.Context(), "tags."+source, boolVal(on)); err != nil {
				s.apiError(w, err)
				return
			}
			if !on {
				// The source is off: do not keep its leftovers in the
				// catalog, otherwise they would reappear when the
				// catalog queries are rebuilt.
				if err := s.st.DeleteBookGenresBySource(r.Context(), source); err != nil {
					s.apiError(w, err)
					return
				}
				s.invalidateGenreCache()
			}
		}
	}
	if req.TagsEnabled != nil {
		// Legacy clients: one switch controlled every tag source.
		if err := s.users.SetSetting(r.Context(), "enrich.tags", boolVal(*req.TagsEnabled)); err != nil {
			s.apiError(w, err)
			return
		}
		for _, source := range tagSourceList {
			if err := s.users.SetSetting(r.Context(), "tags."+source, boolVal(*req.TagsEnabled)); err != nil {
				s.apiError(w, err)
				return
			}
			if !*req.TagsEnabled {
				if err := s.st.DeleteBookGenresBySource(r.Context(), source); err != nil {
					s.apiError(w, err)
					return
				}
				s.invalidateGenreCache()
			}
		}
	}
	if req.Smtp != nil {
		if err := s.saveSmtpSettings(r, *req.Smtp); err != nil {
			s.apiError(w, err)
			return
		}
	}
	for _, src := range sources.All() {
		on, ok := req.Sources[src.ID()]
		if !ok {
			continue
		}
		if err := s.users.SetSetting(r.Context(), sourceSettingKey(src.ID()), boolVal(on)); err != nil {
			s.apiError(w, err)
			return
		}
		if on {
			go s.syncSources(context.Background(), src.ID()) // enabled: crawl right away
		}
	}
	s.handleSettingsGet(w, r)
}

// --- Book ratings ---

// POST /api/v1/books/{id}/rating {rating: 0..5} — 0 removes the rating.
func (s *Server) handleRateBook(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	if u == nil {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	bookID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if _, err := s.st.BookFile(r.Context(), bookID); err != nil {
		s.apiError(w, err)
		return
	}
	var req struct {
		Rating int `json:"rating"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if req.Rating < 0 || req.Rating > 5 {
		http.Error(w, "rating must be 0..5", http.StatusBadRequest)
		return
	}
	if err := s.users.RateBook(r.Context(), u.ID, bookID, req.Rating); err != nil {
		s.apiError(w, err)
		return
	}
	avg, count, err := s.users.BookRating(r.Context(), bookID)
	if err != nil {
		s.apiError(w, err)
		return
	}
	writeJSON(w, map[string]any{
		"userRating":  req.Rating,
		"polkaRating": map[string]any{"rating": avg, "count": count},
	})
}
