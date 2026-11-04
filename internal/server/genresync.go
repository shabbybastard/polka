package server

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Background loading of external genres for the whole library. Books are
// walked in id order, so the job can resume from the last processed id if
// it is ever restarted manually.

type genreSyncState struct {
	mu        sync.Mutex
	running   bool
	processed int
	total     int
	updated   int
	err       string
}

func (st *genreSyncState) snapshot() map[string]any {
	st.mu.Lock()
	defer st.mu.Unlock()
	return map[string]any{
		"running":   st.running,
		"processed": st.processed,
		"total":     st.total,
		"updated":   st.updated,
		"error":     st.err,
	}
}

// POST /admin/genres/sync — start loading external genres for every book.
func (s *Server) handleGenreSyncStart(w http.ResponseWriter, r *http.Request) {
	sources := s.enabledTagSources(r.Context())
	anyEnabled := false
	for _, on := range sources {
		if on {
			anyEnabled = true
			break
		}
	}
	if !anyEnabled {
		http.Error(w, "enable at least one external genre source first", http.StatusConflict)
		return
	}

	s.genreSync.mu.Lock()
	if s.genreSync.running {
		s.genreSync.mu.Unlock()
		http.Error(w, "genre loading is already running", http.StatusConflict)
		return
	}
	total, err := s.st.BookCount(r.Context())
	if err != nil {
		s.genreSync.mu.Unlock()
		s.apiError(w, err)
		return
	}
	s.genreSync.running = true
	s.genreSync.processed = 0
	s.genreSync.total = total
	s.genreSync.updated = 0
	s.genreSync.err = ""
	s.genreSync.mu.Unlock()

	go s.runGenreSync()
	writeJSON(w, map[string]any{"started": true})
}

// GET /admin/genres/status — progress of the background job.
func (s *Server) handleGenreSyncStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.genreSync.snapshot())
}

func (s *Server) runGenreSync() {
	ctx := context.Background()
	enabled := s.enrichEnabledCtx(ctx)
	afterID := int64(0)
	const batchSize = 50

	finish := func(err error) {
		s.invalidateGenreCache()
		s.genreSync.mu.Lock()
		s.genreSync.running = false
		if err != nil {
			s.genreSync.err = err.Error()
		}
		s.genreSync.mu.Unlock()
	}

	for {
		books, err := s.st.BooksForEnrichment(ctx, afterID, batchSize)
		if err != nil {
			finish(err)
			return
		}
		if len(books) == 0 {
			finish(nil)
			return
		}
		for _, book := range books {
			// Re-read the toggles so disabling a source stops adding
			// its genres mid-run.
			tagSources := s.enabledTagSources(ctx)
			anyEnabled := false
			for _, on := range tagSources {
				if on {
					anyEnabled = true
					break
				}
			}
			if !anyEnabled {
				finish(nil)
				return
			}
			res := s.enrich.Get(ctx, strconv.FormatInt(book.ID, 10), book.Title, book.AuthorNames, enabled, tagSources)
			updated := false
			for _, src := range res.Sources {
				if len(src.Tags) == 0 {
					continue
				}
				if err := s.st.SaveBookGenres(ctx, book.ID, src.Source, src.Tags); err != nil {
					s.log.Warn("load external genres", "book", book.ID, "source", src.Source, "error", err)
					continue
				}
				updated = true
			}
			afterID = book.ID
			s.genreSync.mu.Lock()
			s.genreSync.processed++
			if updated {
				s.genreSync.updated++
			}
			s.genreSync.mu.Unlock()
			// Be gentle with the external sites; the job runs in the
			// background and the administrator sees the progress.
			time.Sleep(100 * time.Millisecond)
		}
	}
}
