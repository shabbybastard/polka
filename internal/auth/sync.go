package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	maxSyncItems = 100_000
	maxSyncLists = 10_000
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
	Deleted   bool   `json:"deleted,omitempty"`
	UpdatedAt string `json:"updatedAt"`
}

type ListState struct {
	Name      string          `json:"name"`
	Builtin   string          `json:"builtin"`
	Deleted   bool            `json:"deleted,omitempty"`
	UpdatedAt string          `json:"updatedAt,omitempty"`
	Books     []ListBookState `json:"books"`
}

type ListBookState struct {
	BookID  int64  `json:"bookId"`
	Deleted bool   `json:"deleted,omitempty"`
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
		SELECT book_id, rating, deleted, updated_at FROM book_ratings WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var r RatingState
		var deleted int
		if err := rows.Scan(&r.BookID, &r.Rating, &deleted, &r.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		r.Deleted = deleted != 0
		state.Ratings = append(state.Ratings, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	lists, err := s.listsForSync(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, l := range lists {
		ls := ListState{Name: l.Name, Builtin: l.Builtin, Deleted: l.Deleted, UpdatedAt: l.UpdatedAt}
		brows, err := s.db.QueryContext(ctx,
			`SELECT book_id, deleted, added_at FROM list_books WHERE list_id = ?`, l.ID)
		if err != nil {
			return nil, err
		}
		for brows.Next() {
			var b ListBookState
			var deleted int
			if err := brows.Scan(&b.BookID, &deleted, &b.AddedAt); err != nil {
				brows.Close()
				return nil, err
			}
			b.Deleted = deleted != 0
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

// listsForSync returns deleted lists too: their tombstones must travel to
// other devices, otherwise a list deleted here is recreated by their older
// snapshot.
func (s *Service) listsForSync(ctx context.Context, userID int64) ([]List, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, builtin, deleted, updated_at,
		       (SELECT count(*) FROM list_books lb WHERE lb.list_id = l.id AND lb.deleted = 0)
		FROM lists l WHERE l.user_id = ? ORDER BY l.builtin DESC, l.name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []List
	for rows.Next() {
		var l List
		var deleted int
		if err := rows.Scan(&l.ID, &l.Name, &l.Builtin, &deleted, &l.UpdatedAt, &l.Books); err != nil {
			return nil, err
		}
		l.Deleted = deleted != 0
		out = append(out, l)
	}
	return out, rows.Err()
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
	if len(in.Progress) > maxSyncItems || len(in.Ratings) > maxSyncItems || len(in.Lists) > maxSyncLists {
		return fmt.Errorf("sync state is too large")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, p := range in.Progress {
		if p.BookID <= 0 {
			continue
		}
		updatedAt := normalizeSyncTime(p.UpdatedAt)
		if updatedAt == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
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
		if r.BookID <= 0 {
			continue
		}
		deleted := r.Deleted || r.Rating == 0
		rating := r.Rating
		if deleted {
			rating = 1
		} else if rating < 1 || rating > 5 {
			continue
		}
		rUpdatedAt := normalizeSyncTime(r.UpdatedAt)
		if rUpdatedAt == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO book_ratings (user_id, book_id, rating, deleted, updated_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (user_id, book_id) DO UPDATE SET
				rating = excluded.rating, deleted = excluded.deleted, updated_at = excluded.updated_at
			WHERE excluded.updated_at >= book_ratings.updated_at`,
			userID, r.BookID, rating, boolToInt(deleted), rUpdatedAt); err != nil {
			return err
		}
	}

	for _, ls := range in.Lists {
		// Same validation as CreateList: an empty (whitespace-only) name is
		// the only rejection. Any stricter cap here would drop legitimate
		// lists on merge, since the UI itself has no length limit.
		name := strings.TrimSpace(ls.Name)
		if name == "" {
			continue
		}
		updatedAt := normalizeSyncTime(ls.UpdatedAt)
		if updatedAt == "" {
			if ls.Deleted {
				continue // cannot order a deletion without a timestamp
			}
			updatedAt = time.Now().UTC().Format("2006-01-02 15:04:05.000")
		}
		var listID int64
		var existingDeleted int
		var existingUpdated string
		err := tx.QueryRowContext(ctx,
			`SELECT id, deleted, updated_at FROM lists WHERE user_id = ? AND name = ?`, userID, name).
			Scan(&listID, &existingDeleted, &existingUpdated)
		if errors.Is(err, sql.ErrNoRows) {
			res, ierr := tx.ExecContext(ctx, `
				INSERT INTO lists (user_id, name, builtin, deleted, updated_at)
				VALUES (?, ?, ?, ?, ?)`,
				userID, name, ls.Builtin, boolToInt(ls.Deleted), updatedAt)
			if ierr != nil {
				return ierr
			}
			listID, _ = res.LastInsertId()
		} else if err != nil {
			return err
		} else if updatedAt >= existingUpdated {
			if _, err := tx.ExecContext(ctx, `
				UPDATE lists SET builtin = ?, deleted = ?, updated_at = ? WHERE id = ?`,
				ls.Builtin, boolToInt(ls.Deleted), updatedAt, listID); err != nil {
				return err
			}
		} else if existingDeleted != 0 {
			continue // older snapshot of a deleted list
		}
		if ls.Deleted {
			if _, err := tx.ExecContext(ctx, `
				UPDATE list_books SET deleted = 1, added_at = ?
				WHERE list_id = ? AND added_at < ?`, updatedAt, listID, updatedAt); err != nil {
				return err
			}
			continue
		}
		for _, b := range ls.Books {
			if b.BookID <= 0 {
				continue
			}
			addedAt := normalizeSyncTime(b.AddedAt)
			if addedAt == "" {
				addedAt = updatedAt
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO list_books (list_id, book_id, deleted, added_at)
				VALUES (?, ?, ?, ?)
				ON CONFLICT (list_id, book_id) DO UPDATE SET
					deleted = excluded.deleted, added_at = excluded.added_at
				WHERE excluded.added_at >= list_books.added_at`,
				listID, b.BookID, boolToInt(b.Deleted), addedAt); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
