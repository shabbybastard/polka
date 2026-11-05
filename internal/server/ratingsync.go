package server

import (
	"context"
	"net/http"
	"strconv"
	"sync"

	"github.com/vestigiumincaligne/polka/internal/enrich"
)

// Manual warming of the external rating cache for the whole library.
// It uses the same throttled provider as the genre job, so sources are not
// hammered during a bulk run.

type ratingSyncState struct {
	mu        sync.Mutex
	running   bool
	mode      string
	processed int
	total     int
	updated   int
	err       string
}

func (st *ratingSyncState) snapshot() map[string]any {
	st.mu.Lock()
	defer st.mu.Unlock()
	return map[string]any{
		"running":   st.running,
		"mode":      st.mode,
		"processed": st.processed,
		"total":     st.total,
		"updated":   st.updated,
		"error":     st.err,
	}
}

// POST /admin/ratings/sync?mode=all|empty — start loading ratings.
func (s *Server) handleRatingSyncStart(w http.ResponseWriter, r *http.Request) {
	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = "all"
	}
	if mode != "all" && mode != "empty" {
		http.Error(w, "mode must be all or empty", http.StatusBadRequest)
		return
	}

	enabled := s.enrichEnabledCtx(r.Context())
	anyEnabled := false
	for _, on := range enabled {
		if on {
			anyEnabled = true
			break
		}
	}
	if !anyEnabled {
		http.Error(w, "enable at least one rating source first", http.StatusConflict)
		return
	}

	s.ratingSync.mu.Lock()
	if s.ratingSync.running {
		s.ratingSync.mu.Unlock()
		http.Error(w, "rating loading is already running", http.StatusConflict)
		return
	}
	var total int
	var err error
	if mode == "empty" {
		total, err = s.countBooksWithoutRatings(r.Context(), enabled)
	} else {
		total, err = s.st.BookCount(r.Context())
	}
	if err != nil {
		s.ratingSync.mu.Unlock()
		s.apiError(w, err)
		return
	}
	s.ratingSync.running = true
	s.ratingSync.mode = mode
	s.ratingSync.processed = 0
	s.ratingSync.total = total
	s.ratingSync.updated = 0
	s.ratingSync.err = ""
	s.ratingSync.mu.Unlock()

	go s.runRatingSync(context.Background(), enabled, mode)
	writeJSON(w, map[string]any{"started": true, "mode": mode})
}

// GET /admin/ratings/status — progress of the manual rating loading job.
func (s *Server) handleRatingSyncStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.ratingSync.snapshot())
}

func (s *Server) countBooksWithoutRatings(ctx context.Context, enabled map[string]bool) (int, error) {
	afterID := int64(0)
	const batchSize = 200
	total := 0
	for {
		books, err := s.st.BooksForEnrichment(ctx, afterID, batchSize, false)
		if err != nil {
			return 0, err
		}
		if len(books) == 0 {
			return total, nil
		}
		for _, book := range books {
			if !s.enrich.HasCachedRating(strconv.FormatInt(book.ID, 10), enabled) {
				total++
			}
			afterID = book.ID
		}
	}
}

func (s *Server) runRatingSync(ctx context.Context, enabled map[string]bool, mode string) {
	defer func() {
		s.ratingSync.mu.Lock()
		s.ratingSync.running = false
		s.ratingSync.mu.Unlock()
	}()

	afterID := int64(0)
	const batchSize = 50
	for {
		books, err := s.st.BooksForEnrichment(ctx, afterID, batchSize, false)
		if err != nil {
			s.ratingSync.mu.Lock()
			s.ratingSync.err = err.Error()
			s.ratingSync.mu.Unlock()
			return
		}
		if len(books) == 0 {
			return
		}
		for _, book := range books {
			key := strconv.FormatInt(book.ID, 10)
			afterID = book.ID
			if mode == "empty" && s.enrich.HasCachedRating(key, enabled) {
				continue
			}
			var res *enrich.Enrichment
			if mode == "empty" {
				// Empty mode is meant to retry books without a rating; bypass
				// a cached negative result instead of trusting it again.
				res = s.enrich.RefreshThrottled(ctx, key, book.Title, book.AuthorNames, enabled, nil)
			} else {
				res = s.enrich.GetThrottled(ctx, key, book.Title, book.AuthorNames, enabled, nil)
			}
			hasRating := false
			for _, src := range res.Sources {
				if src.Rating > 0 {
					hasRating = true
					break
				}
			}
			s.ratingSync.mu.Lock()
			s.ratingSync.processed++
			if hasRating {
				s.ratingSync.updated++
			}
			s.ratingSync.mu.Unlock()
		}
	}
}
