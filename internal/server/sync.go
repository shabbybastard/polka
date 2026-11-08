package server

import (
	"encoding/json"
	"net/http"

	"github.com/shabbybastard/polka/internal/auth"
)

// GET /api/v1/sync/state — state of user data
// (progress, ratings, lists) with timestamps for LWW merging.
func (s *Server) handleSyncExport(w http.ResponseWriter, r *http.Request) {
	u := s.requireUser(w, r)
	if u == nil {
		return
	}
	state, err := s.users.ExportState(r.Context(), u.ID)
	if err != nil {
		s.apiError(w, err)
		return
	}
	writeJSON(w, state)
}

// POST /api/v1/sync/state — accept the client's state (LWW merge).
func (s *Server) handleSyncMerge(w http.ResponseWriter, r *http.Request) {
	u := s.requireUser(w, r)
	if u == nil {
		return
	}
	var in auth.SyncState
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&in); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if err := s.filterSyncState(r, &in); err != nil {
		s.apiError(w, err)
		return
	}
	if err := s.users.MergeState(r.Context(), u.ID, &in); err != nil {
		s.apiError(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// filterSyncState drops progress/ratings/list entries whose book ids are not
// in the current catalog. Without this, a client could seed data for an id
// that a later re-import assigns to a different book.
func (s *Server) filterSyncState(r *http.Request, in *auth.SyncState) error {
	ids := make([]int64, 0, len(in.Progress)+len(in.Ratings))
	for _, p := range in.Progress {
		ids = append(ids, p.BookID)
	}
	for _, rating := range in.Ratings {
		ids = append(ids, rating.BookID)
	}
	for _, list := range in.Lists {
		for _, b := range list.Books {
			ids = append(ids, b.BookID)
		}
	}
	exists, err := s.st.BookIDsExist(r.Context(), ids)
	if err != nil {
		return err
	}
	progress := in.Progress[:0]
	for _, p := range in.Progress {
		if exists[p.BookID] {
			progress = append(progress, p)
		}
	}
	in.Progress = progress
	ratings := in.Ratings[:0]
	for _, rating := range in.Ratings {
		if exists[rating.BookID] {
			ratings = append(ratings, rating)
		}
	}
	in.Ratings = ratings
	for i := range in.Lists {
		books := in.Lists[i].Books[:0]
		for _, b := range in.Lists[i].Books {
			if exists[b.BookID] {
				books = append(books, b)
			}
		}
		in.Lists[i].Books = books
	}
	return nil
}
