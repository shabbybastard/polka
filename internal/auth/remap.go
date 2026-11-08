package auth

import (
	"context"
)

// RemapBookIDs rewrites per-user book references after a catalog re-import.
// mapping maps old book ids to new ones; a new id of 0 means the book no
// longer exists and its user data is removed. All rows are moved to negative
// temporary ids first, so mappings that swap ids (1->2, 2->1) and mappings
// whose target id already has user data cannot corrupt each other.
// ClearBookData removes every per-book user reference. It is the safe
// fallback when a re-import mapping cannot be applied: keeping stale ids
// would attach progress and ratings to unrelated books.
func (s *Service) ClearBookData(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"reading_progress", "book_ratings", "list_books"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Service) RemapBookIDs(ctx context.Context, mapping map[int64]int64) error {
	if len(mapping) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS temp.book_id_map`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		CREATE TEMP TABLE book_id_map (
			old_id INTEGER PRIMARY KEY,
			new_id INTEGER NOT NULL
		)`); err != nil {
		return err
	}
	// Only ids that actually carry user data need a mapping row; a large
	// catalog otherwise inserts hundreds of thousands of unused rows.
	dataIDs := map[int64]bool{}
	rows, err := tx.QueryContext(ctx, `
		SELECT book_id FROM reading_progress
		UNION SELECT book_id FROM book_ratings
		UNION SELECT book_id FROM list_books`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		dataIDs[id] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	covered := make(map[int64]bool, len(mapping))
	for oldID := range mapping {
		covered[oldID] = true
	}
	// Drop foreign ids that were never part of the old catalog. They cannot
	// be mapped and could otherwise be revived by a future id reuse.
	for id := range dataIDs {
		if covered[id] || id <= 0 {
			continue
		}
		for _, table := range []string{"reading_progress", "book_ratings", "list_books"} {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE book_id = ?`, id); err != nil {
				return err
			}
		}
	}

	stmt, err := tx.PrepareContext(ctx, `INSERT INTO book_id_map (old_id, new_id) VALUES (?, ?)`)
	if err != nil {
		return err
	}
	for oldID, newID := range mapping {
		if oldID <= 0 || oldID == newID || !dataIDs[oldID] {
			continue
		}
		if _, err := stmt.ExecContext(ctx, oldID, newID); err != nil {
			stmt.Close()
			return err
		}
	}
	stmt.Close()

	for _, table := range []string{"reading_progress", "book_ratings", "list_books"} {
		if _, err := tx.ExecContext(ctx, `
			UPDATE `+table+` SET book_id = -book_id
			WHERE book_id IN (SELECT old_id FROM book_id_map)`); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT OR REPLACE INTO reading_progress
			(user_id, book_id, chapter, position, overall, locator, deleted, updated_at)
		SELECT p.user_id, m.new_id, p.chapter, p.position, p.overall, p.locator, p.deleted, p.updated_at
		FROM reading_progress p
		JOIN book_id_map m ON p.book_id = -m.old_id
		WHERE m.new_id != 0`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT OR REPLACE INTO book_ratings (user_id, book_id, rating, deleted, updated_at)
		SELECT r.user_id, m.new_id, r.rating, r.deleted, r.updated_at
		FROM book_ratings r
		JOIN book_id_map m ON r.book_id = -m.old_id
		WHERE m.new_id != 0`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT OR REPLACE INTO list_books (list_id, book_id, deleted, added_at)
		SELECT lb.list_id, m.new_id, lb.deleted, lb.added_at
		FROM list_books lb
		JOIN book_id_map m ON lb.book_id = -m.old_id
		WHERE m.new_id != 0`); err != nil {
		return err
	}
	for _, table := range []string{"reading_progress", "book_ratings", "list_books"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE book_id < 0`); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE temp.book_id_map`); err != nil {
		return err
	}
	return tx.Commit()
}
