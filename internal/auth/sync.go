package auth

import (
	"context"
	"strings"
	"time"
)

// User data state for syncing the desktop client with the server:
// timestamped snapshots, last-write-wins merge.

type ProgressState struct {
	BookID    int64   `json:"bookId"`
	Chapter   int     `json:"chapter"`
	Position  float64 `json:"position"`
	Overall   float64 `json:"overall"`
	Locator   string  `json:"locator"`
	Deleted   bool    `json:"deleted,omitempty"`
	UpdatedAt string  `json:"updatedAt"` // UTC, SQLite datetime format
}

type RatingState struct {
	BookID    int64  `json:"bookId"`
	Rating    int    `json:"rating"`
	UpdatedAt string `json:"updatedAt"`
}

type ListState struct {
	Name    string          `json:"name"`
	Builtin string          `json:"builtin"`
	Books   []ListBookState `json:"books"`
}

type ListBookState struct {
	BookID  int64  `json:"bookId"`
	AddedAt string `json:"addedAt"`
}

type SyncState struct {
	Progress []ProgressState `json:"progress"`
	Ratings  []RatingState   `json:"ratings"`
	Lists    []ListState     `json:"lists"`
}

// ExportState collects all user data with timestamps.
func (s *Service) ExportState(ctx context.Context, userID int64) (*SyncState, error) {
	state := &SyncState{}

	rows, err := s.db.QueryContext(ctx, `
		SELECT book_id, chapter, position, overall, locator, deleted, updated_at
		FROM reading_progress WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p ProgressState
		var deleted int
		if err := rows.Scan(&p.BookID, &p.Chapter, &p.Position, &p.Overall, &p.Locator, &deleted, &p.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		p.Deleted = deleted != 0
		state.Progress = append(state.Progress, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.db.QueryContext(ctx, `
		SELECT book_id, rating, updated_at FROM book_ratings WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var r RatingState
		if err := rows.Scan(&r.BookID, &r.Rating, &r.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		state.Ratings = append(state.Ratings, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	lists, err := s.Lists(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, l := range lists {
		ls := ListState{Name: l.Name, Builtin: l.Builtin}
		brows, err := s.db.QueryContext(ctx,
			`SELECT book_id, added_at FROM list_books WHERE list_id = ?`, l.ID)
		if err != nil {
			return nil, err
		}
		for brows.Next() {
			var b ListBookState
			if err := brows.Scan(&b.BookID, &b.AddedAt); err != nil {
				brows.Close()
				return nil, err
			}
			ls.Books = append(ls.Books, b)
		}
		brows.Close()
		if err := brows.Err(); err != nil {
			return nil, err
		}
		state.Lists = append(state.Lists, ls)
	}
	return state, nil
}

// syncTimeLayouts are the timestamp formats accepted in a SyncState.
// The first is the format the server itself writes (SQLite's strftime
// output, optionally with fractional seconds); the second is RFC3339 as
// used by some non-bundled clients.
var syncTimeLayouts = []string{
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04:05.999999999Z07:00",
}

// normalizeSyncTime validates a merged timestamp and converts it to the
// SQLite datetime format the server stores, so the last-write-wins string
// comparison stays consistent. Everything else (garbage, empty) is dropped.
func normalizeSyncTime(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var t time.Time
	var err error
	for _, l := range syncTimeLayouts {
		if t, err = time.Parse(l, s); err == nil {
			return t.UTC().Format("2006-01-02 15:04:05.000")
		}
	}
	return ""
}

// MergeState merges foreign state: progress and ratings — last-write-wins
// by updated_at, lists — union (create missing ones, add books).
func (s *Service) MergeState(ctx context.Context, userID int64, in *SyncState) error {
	for _, p := range in.Progress {
		if p.BookID <= 0 {
			continue
		}
		updatedAt := normalizeSyncTime(p.UpdatedAt)
		if updatedAt == "" {
			continue
		}
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO reading_progress (user_id, book_id, chapter, position, overall, locator, deleted, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (user_id, book_id) DO UPDATE SET
				chapter = excluded.chapter, position = excluded.position,
				overall = excluded.overall, locator = excluded.locator,
				deleted = excluded.deleted, updated_at = excluded.updated_at
			WHERE excluded.updated_at > reading_progress.updated_at`,
			userID, p.BookID, p.Chapter, p.Position, p.Overall, p.Locator, boolToInt(p.Deleted), updatedAt); err != nil {
			return err
		}
	}

	for _, r := range in.Ratings {
		if r.BookID <= 0 || r.Rating < 1 || r.Rating > 5 {
			continue
		}
		rUpdatedAt := normalizeSyncTime(r.UpdatedAt)
		if rUpdatedAt == "" {
			continue
		}
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO book_ratings (user_id, book_id, rating, updated_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT (user_id, book_id) DO UPDATE SET
				rating = excluded.rating, updated_at = excluded.updated_at
			WHERE excluded.updated_at > book_ratings.updated_at`,
			userID, r.BookID, r.Rating, rUpdatedAt); err != nil {
			return err
		}
	}

	for _, ls := range in.Lists {
		// Same validation as CreateList: an empty (whitespace-only) name is
		// the only rejection. Any stricter cap here would drop legitimate
		// lists on merge, since the UI itself has no length limit.
		if strings.TrimSpace(ls.Name) == "" {
			continue
		}
		var listID int64
		if ls.Builtin == BuiltinWishlist {
			wl, err := s.Wishlist(ctx, userID)
			if err != nil {
				return err
			}
			listID = wl.ID
		} else {
			err := s.db.QueryRowContext(ctx,
				`SELECT id FROM lists WHERE user_id = ? AND name = ?`, userID, ls.Name).Scan(&listID)
			if err != nil {
				created, cerr := s.CreateList(ctx, userID, ls.Name)
				if cerr != nil {
					return cerr
				}
				listID = created.ID
			}
		}
		for _, b := range ls.Books {
			if b.BookID <= 0 {
				continue
			}
			if _, err := s.db.ExecContext(ctx, `
				INSERT OR IGNORE INTO list_books (list_id, book_id, added_at) VALUES (?, ?, ?)`,
				listID, b.BookID, b.AddedAt); err != nil {
				return err
			}
		}
	}
	return nil
}
