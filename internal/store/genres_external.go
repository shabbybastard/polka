package store

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
)

// External genre sources (LiveLib, Open Library) are folded into the
// regular genre catalog. Their rows are marked with source != "" so they
// can be removed when the administrator switches the source off.

func externalGenreCode(source, name string) string {
	sum := sha1.Sum([]byte(strings.ToLower(strings.TrimSpace(name))))
	return "ext_" + source + "_" + hex.EncodeToString(sum[:])[:12]
}

// SaveBookGenres replaces the genres a book received from one external
// source. Genres from other sources and the book's native genres are left
// untouched.
func (s *Store) SaveBookGenres(ctx context.Context, bookID int64, source string, names []string) error {
	source = strings.TrimSpace(source)
	if source == "" {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		DELETE FROM book_genres WHERE book_id = ? AND genre_id IN
			(SELECT id FROM genres WHERE source = ?)`, bookID, source); err != nil {
		return err
	}

	seen := map[string]bool{}
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" || len([]rune(name)) > 80 {
			continue
		}
		key := strings.ToLower(name)
		if seen[key] {
			continue
		}
		seen[key] = true

		// Reuse a migrated genre when one already exists for this name
		// and source; otherwise give it a deterministic external code.
		var genreID int64
		err := tx.QueryRowContext(ctx,
			`SELECT id FROM genres WHERE name = ? AND source = ?`, name, source).Scan(&genreID)
		if errors.Is(err, sql.ErrNoRows) {
			code := externalGenreCode(source, name)
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO genres (code, name, source) VALUES (?, ?, ?)
				ON CONFLICT (code) DO UPDATE SET name = excluded.name, source = excluded.source`,
				code, name, source); err != nil {
				return err
			}
			if err := tx.QueryRowContext(ctx,
				`SELECT id FROM genres WHERE code = ?`, code).Scan(&genreID); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO book_genres (book_id, genre_id) VALUES (?, ?)`, bookID, genreID); err != nil {
			return err
		}
	}

	// Drop genres this source no longer contributes.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM genres WHERE source = ?
		AND id NOT IN (SELECT genre_id FROM book_genres)`, source); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteBookGenresBySource removes every genre contributed by one external
// source, including its book links. Native genres have source = ”.
func (s *Store) DeleteBookGenresBySource(ctx context.Context, source string) error {
	source = strings.TrimSpace(source)
	if source == "" {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		DELETE FROM book_genres WHERE genre_id IN (SELECT id FROM genres WHERE source = ?)`, source); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM genres WHERE source = ?`, source); err != nil {
		return err
	}
	return tx.Commit()
}
