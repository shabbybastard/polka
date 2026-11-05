package store

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"unicode"

	"github.com/vestigiumincaligne/polka/internal/genres"
)

// External genre sources (LiveLib, Open Library) are folded into the
// regular genre catalog. Their rows are marked with source != "" so they
// can be removed when the administrator switches the source off.

func externalGenreCode(source, name string) string {
	sum := sha1.Sum([]byte(strings.ToLower(strings.TrimSpace(name))))
	return "ext_" + source + "_" + hex.EncodeToString(sum[:])[:12]
}

// normalizeGenreName makes display names comparable regardless of case,
// ё/е spelling, punctuation and repeated whitespace.
func normalizeGenreName(name string) string {
	name = strings.ToLower(strings.ReplaceAll(name, "ё", "е"))
	var b strings.Builder
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// nativeGenreNames returns the display names of a book's native genres.
// Standard FB2 codes are compared in both localizations because external
// sources may return either Russian or English names.
func nativeGenreNames(ctx context.Context, tx *sql.Tx, bookID int64) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT g.code, g.name
		FROM book_genres bg
		JOIN genres g ON g.id = bg.genre_id
		WHERE bg.book_id = ? AND g.source = ''`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var code, name string
		if err := rows.Scan(&code, &name); err != nil {
			return nil, err
		}
		for _, label := range []string{name, genres.NameLang(code, "ru"), genres.NameLang(code, "en")} {
			if key := normalizeGenreName(label); key != "" {
				out[key] = true
			}
		}
	}
	return out, rows.Err()
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

	native, err := nativeGenreNames(ctx, tx, bookID)
	if err != nil {
		return err
	}

	seen := map[string]bool{}
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" || len([]rune(name)) > 80 {
			continue
		}
		key := normalizeGenreName(name)
		if key == "" || native[key] || seen[key] {
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

// DeleteBookExternalGenres removes every external genre link from one book
// and drops external genre rows that are no longer used by any book. Native
// genres are left untouched. It returns the number of removed links.
func (s *Store) DeleteBookExternalGenres(ctx context.Context, bookID int64) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
		DELETE FROM book_genres
		WHERE book_id = ? AND genre_id IN (SELECT id FROM genres WHERE source != '')`, bookID)
	if err != nil {
		return 0, err
	}
	removed, _ := res.RowsAffected()
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM genres
		WHERE source != '' AND id NOT IN (SELECT genre_id FROM book_genres)`); err != nil {
		return 0, err
	}
	return removed, tx.Commit()
}

// DeleteBookGenresBySource removes every genre contributed by one external
// source, including its book links. Native genres have source = "".
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
