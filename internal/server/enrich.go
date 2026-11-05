package server

import (
	"context"

	"encoding/json"
	"github.com/shabbybastard/polka/internal/collections/sources"
	"net/http"
	"strconv"
	"strings"

	"github.com/shabbybastard/polka/internal/enrich"
)

// enrichEnabled builds a map of enabled sources from settings.
func (s *Server) enrichEnabledCtx(ctx context.Context) map[string]bool {
	enabled := make(map[string]bool, len(enrich.Sources))
	for _, source := range enrich.Sources {
		def := "1"
		if source == enrich.SourceFantLab {
			def = "0"
		}
		enabled[source] = s.users.GetSetting(ctx, "enrich."+source, def) == "1"
	}
	return enabled
}

func (s *Server) enrichEnabled(r *http.Request) map[string]bool {
	return s.enrichEnabledCtx(r.Context())
}

const telegramUploadRateLimitKey = "telegram.upload_rate_limit"

// telegramUploadRateLimit is the per-user hourly Telegram upload quota.
// Out-of-range values fall back to the documented default of 50.
func (s *Server) telegramUploadRateLimit(ctx context.Context) int {
	n, err := strconv.Atoi(s.users.GetSetting(ctx, telegramUploadRateLimitKey, "50"))
	if err != nil || n < 1 || n > 10000 {
		return 50
	}
	return n
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
	enabled := s.enrichEnabled(r)
	s.enrich.SetGoogleAPIKey(s.users.GetSetting(r.Context(), "enrich.google_api_key", ""))
	res := s.enrich.Get(r.Context(), bookID, title, author, enabled, tagSources)
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
	writeJSON(w, s.enrichmentView(res, enabled))
}

// enrichmentView removes results from disabled rating sources and picks the
// first enabled source with a rating as primary. Tag-only sources may still
// contribute genres, but must not show a rating in the book page.
func (s *Server) enrichmentView(res *enrich.Enrichment, enabled map[string]bool) *enrich.Enrichment {
	view := *res
	view.Sources = make([]enrich.Result, 0, len(res.Sources))
	view.Primary = nil
	view.CoverURL = ""
	for _, src := range res.Sources {
		if !enabled[src.Source] {
			continue
		}
		view.Sources = append(view.Sources, src)
		if view.CoverURL == "" {
			view.CoverURL = src.CoverURL
		}
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
	return &view
}

// POST /admin/books/{id}/forget-enrichment — remove external ratings, genres
// and links for one book. Native genres and Polka's own rating are kept.
func (s *Server) handleForgetBookEnrichment(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	removed, err := s.st.DeleteBookExternalGenres(r.Context(), id)
	if err != nil {
		s.apiError(w, err)
		return
	}
	s.enrich.ForgetBook(strconv.FormatInt(id, 10))
	s.invalidateGenreCache()
	writeJSON(w, map[string]any{"ok": true, "removedGenres": removed})
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
		"tastediveKey":   cfg.TasteDiveKey,
		"googleBooksKey": s.users.GetSetting(r.Context(), "enrich.google_api_key", ""),
		"tagSources":     s.enabledTagSources(r.Context()),
		"opdsEnabled":    s.opdsEnabled(r),
		"homeHero":       s.users.GetSetting(r.Context(), "home.hero", "1") == "1",
		"convert": map[string]bool{
			"fb2ToEpub":  s.users.GetSetting(r.Context(), "convert.fb2_to_epub", "0") == "1",
			"keepSource": s.keepConversionSource(r.Context()),
		},
		"smtp": s.smtpSettings(r),
		"telegram": map[string]any{
			"uploadRateLimit": s.telegramUploadRateLimit(r.Context()),
		},
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
		Enrichment     map[string]bool `json:"enrichment"`
		Similar        map[string]bool `json:"similar"`
		TasteDiveKey   *string         `json:"tastediveKey"`
		GoogleBooksKey *string         `json:"googleBooksKey"`
		TagSources     map[string]bool `json:"tagSources"`
		TagsEnabled    *bool           `json:"tagsEnabled"` // legacy single switch
		OpdsEnabled    *bool           `json:"opdsEnabled"`
		HomeHero       *bool           `json:"homeHero"`
		Convert        *struct {
			FB2ToEPUB  *bool `json:"fb2ToEpub"`
			KeepSource *bool `json:"keepSource"`
		} `json:"convert"`
		Smtp     *smtpInput `json:"smtp"`
		Telegram *struct {
			UploadRateLimit *int `json:"uploadRateLimit"`
		} `json:"telegram"`
		Sources map[string]bool `json:"collectionSources"`
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
	if req.GoogleBooksKey != nil {
		key := strings.TrimSpace(*req.GoogleBooksKey)
		if err := s.users.SetSetting(r.Context(), "enrich.google_api_key", key); err != nil {
			s.apiError(w, err)
			return
		}
		s.enrich.SetGoogleAPIKey(key)
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
	if req.Convert != nil {
		if req.Convert.FB2ToEPUB != nil {
			if err := s.users.SetSetting(r.Context(), "convert.fb2_to_epub", boolVal(*req.Convert.FB2ToEPUB)); err != nil {
				s.apiError(w, err)
				return
			}
		}
		if req.Convert.KeepSource != nil {
			if err := s.users.SetSetting(r.Context(), "convert.keep_source", boolVal(*req.Convert.KeepSource)); err != nil {
				s.apiError(w, err)
				return
			}
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
	if req.Telegram != nil && req.Telegram.UploadRateLimit != nil {
		n := *req.Telegram.UploadRateLimit
		if n < 1 || n > 10000 {
			http.Error(w, "telegram upload rate limit must be 1..10000", http.StatusBadRequest)
			return
		}
		if err := s.users.SetSetting(r.Context(), telegramUploadRateLimitKey, strconv.Itoa(n)); err != nil {
			s.apiError(w, err)
			return
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
