package auth

import (
	"context"
	"database/sql"
	"errors"
)

// Reading progress lives in users.db: it is personal and must
// survive collection re-import.

const progressSchema = `
CREATE TABLE IF NOT EXISTS reading_progress (
	user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	book_id    INTEGER NOT NULL,
	chapter    INTEGER NOT NULL DEFAULT 0,
	position   REAL NOT NULL DEFAULT 0,
	overall    REAL NOT NULL DEFAULT 0,
	locator    TEXT NOT NULL DEFAULT '',
	deleted    INTEGER NOT NULL DEFAULT 0,
	updated_at TEXT NOT NULL DEFAULT (datetime('now')),
	PRIMARY KEY (user_id, book_id)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_progress_updated ON reading_progress (user_id, updated_at);
`

type Progress struct {
	BookID   int64
	Chapter  int
	Position float64 // scroll fraction of the chapter, 0..1
	Overall  float64 // fraction of the whole book, 0..1
	Locator  string  // format-specific position (CFI for epub, etc.)
}

// SaveProgress stores the position with a millisecond timestamp: the
// sync merge is last-write-wins on updated_at, and with whole seconds two
// saves within the same second (e.g. right before and after going
// offline) would compare equal and the later one would be dropped.
func (s *Service) SaveProgress(ctx context.Context, userID, bookID int64, p Progress) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO reading_progress (user_id, book_id, chapter, position, overall, locator, deleted, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 0, strftime('%Y-%m-%d %H:%M:%f', 'now'))
		ON CONFLICT (user_id, book_id) DO UPDATE
		SET chapter = excluded.chapter, position = excluded.position,
		    overall = excluded.overall, locator = excluded.locator,
		    deleted = 0, updated_at = excluded.updated_at`,
		userID, bookID, p.Chapter, p.Position, p.Overall, p.Locator)
	if err != nil {
		return err
	}
	return s.syncReadListBook(ctx, userID, bookID)
}

// DeleteProgress forgets a book's position for one user. The row is kept
// as a timestamped tombstone so the removal travels to other devices via
// the state sync instead of being resurrected by their older snapshot.
func (s *Service) DeleteProgress(ctx context.Context, userID, bookID int64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO reading_progress (user_id, book_id, chapter, position, overall, locator, deleted, updated_at)
		VALUES (?, ?, 0, 0, 0, '', 1, strftime('%Y-%m-%d %H:%M:%f', 'now'))
		ON CONFLICT (user_id, book_id) DO UPDATE
		SET chapter = 0, position = 0, overall = 0, locator = '',
		    deleted = 1, updated_at = excluded.updated_at`,
		userID, bookID)
	if err != nil {
		return err
	}
	return s.removeReadListBook(ctx, userID, bookID)
}

func (s *Service) GetProgress(ctx context.Context, userID, bookID int64) (Progress, error) {
	p := Progress{BookID: bookID}
	err := s.db.QueryRowContext(ctx,
		`SELECT chapter, position, overall, locator FROM reading_progress
		 WHERE user_id = ? AND book_id = ? AND deleted = 0`,
		userID, bookID).Scan(&p.Chapter, &p.Position, &p.Overall, &p.Locator)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// FinishedThreshold is the overall fraction at which a book is considered
// read: it moves to the built-in "Read" list and disappears from "Reading
// now". Keep it in sync with the UI wording.
const FinishedThreshold = 0.98

// ListProgress returns the user's recently read books (newest first).
// Finished books (overall >= FinishedThreshold) are not shown.
func (s *Service) ListProgress(ctx context.Context, userID int64, limit int) ([]Progress, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT book_id, chapter, position, overall FROM reading_progress
		WHERE user_id = ? AND deleted = 0 AND overall < ?
		ORDER BY updated_at DESC LIMIT ?`, userID, FinishedThreshold, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Progress
	for rows.Next() {
		var p Progress
		if err := rows.Scan(&p.BookID, &p.Chapter, &p.Position, &p.Overall); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ProgressBookIDs returns every book with a live reading position. Used to
// keep recommendations from suggesting books the user has already touched.
func (s *Service) ProgressBookIDs(ctx context.Context, userID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT book_id FROM reading_progress WHERE user_id = ? AND deleted = 0`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// markBookRead is the manual "add to Read" action: it marks the book as
// finished while preserving the last reading position, and puts it into the
// built-in list.
func (s *Service) markBookRead(ctx context.Context, userID, bookID, listID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO reading_progress (user_id, book_id, chapter, position, overall, locator, deleted, updated_at)
		VALUES (?, ?, 0, 0, 1, '', 0, strftime('%Y-%m-%d %H:%M:%f', 'now'))
		ON CONFLICT (user_id, book_id) DO UPDATE
		SET overall = 1, deleted = 0, updated_at = strftime('%Y-%m-%d %H:%M:%f', 'now')`,
		userID, bookID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO list_books (list_id, book_id, deleted, added_at)
		VALUES (?, ?, 0, strftime('%Y-%m-%d %H:%M:%f', 'now'))
		ON CONFLICT (list_id, book_id) DO UPDATE
		SET deleted = 0, added_at = excluded.added_at
		WHERE list_books.deleted = 1`, listID, bookID); err != nil {
		return err
	}
	return tx.Commit()
}

// markBookUnread is the manual "remove from Read" action: it resets the
// reading position, so the book returns to "Reading now" as unread.
func (s *Service) markBookUnread(ctx context.Context, userID, bookID, listID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO reading_progress (user_id, book_id, chapter, position, overall, locator, deleted, updated_at)
		VALUES (?, ?, 0, 0, 0, '', 0, strftime('%Y-%m-%d %H:%M:%f', 'now'))
		ON CONFLICT (user_id, book_id) DO UPDATE
		SET chapter = 0, position = 0, overall = 0, locator = '',
		    deleted = 0, updated_at = strftime('%Y-%m-%d %H:%M:%f', 'now')`,
		userID, bookID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE list_books SET deleted = 1, added_at = strftime('%Y-%m-%d %H:%M:%f', 'now')
		WHERE list_id = ? AND book_id = ? AND deleted = 0`, listID, bookID); err != nil {
		return err
	}
	return tx.Commit()
}

// syncReadListBook updates the built-in Read list for one book after a
// progress change. Membership follows the current overall fraction.
func (s *Service) syncReadListBook(ctx context.Context, userID, bookID int64) error {
	var overall float64
	var deleted int
	err := s.db.QueryRowContext(ctx,
		`SELECT overall, deleted FROM reading_progress WHERE user_id = ? AND book_id = ?`,
		userID, bookID).Scan(&overall, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if deleted != 0 || overall < FinishedThreshold {
		return s.removeReadListBook(ctx, userID, bookID)
	}
	rl, err := s.ReadList(ctx, userID)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO list_books (list_id, book_id, deleted, added_at)
		VALUES (?, ?, 0, strftime('%Y-%m-%d %H:%M:%f', 'now'))
		ON CONFLICT (list_id, book_id) DO UPDATE
		SET deleted = 0, added_at = excluded.added_at
		WHERE list_books.deleted = 1`, rl.ID, bookID)
	return err
}

func (s *Service) removeReadListBook(ctx context.Context, userID, bookID int64) error {
	var listID int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM lists WHERE user_id = ? AND builtin = ? AND deleted = 0`,
		userID, BuiltinRead).Scan(&listID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE list_books SET deleted = 1, added_at = strftime('%Y-%m-%d %H:%M:%f', 'now')
		WHERE list_id = ? AND book_id = ? AND deleted = 0`, listID, bookID)
	return err
}

// syncReadListAll reconciles the whole Read list after a state merge. It is
// intentionally one bulk pass: MergeState can carry thousands of progress
// records.
func (s *Service) syncReadListAll(ctx context.Context, userID int64) error {
	rl, err := s.ReadList(ctx, userID)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		UPDATE list_books SET deleted = 1, added_at = strftime('%Y-%m-%d %H:%M:%f', 'now')
		WHERE list_id = ? AND deleted = 0 AND book_id NOT IN (
			SELECT book_id FROM reading_progress
			WHERE user_id = ? AND deleted = 0 AND overall >= ?
		)`, rl.ID, userID, FinishedThreshold); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO list_books (list_id, book_id, deleted, added_at)
		SELECT ?, book_id, 0, strftime('%Y-%m-%d %H:%M:%f', 'now')
		FROM reading_progress
		WHERE user_id = ? AND deleted = 0 AND overall >= ?
		ON CONFLICT (list_id, book_id) DO UPDATE
		SET deleted = 0, added_at = excluded.added_at
		WHERE list_books.deleted = 1`, rl.ID, userID, FinishedThreshold); err != nil {
		return err
	}
	return tx.Commit()
}
