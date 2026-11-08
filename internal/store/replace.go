package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// catalogTables is ordered so that child tables are cleared before their
// parents and parents are copied before their children. The catalog has no
// foreign keys, but keeping the order explicit makes the operation safe if
// they are added later.
var catalogDeleteOrder = []string{
	"book_search", "authors_search", "series_search",
	"book_authors", "book_genres", "book_keywords", "book_digests",
	"books", "authors", "series", "genres", "keywords", "folders", "meta",
}

var catalogCopyOrder = []string{
	"folders", "series", "authors", "genres", "keywords", "books",
	"book_authors", "book_genres", "book_keywords", "book_digests",
	"book_search", "authors_search", "series_search", "meta",
}

// ReplaceFrom atomically replaces the live catalog with the contents of a
// fully imported staging database. The old catalog remains visible until the
// transaction commits, so a failed or interrupted import never destroys the
// only copy. The staging database must be closed before this call.
// CatalogGeneration identifies the current catalog contents. It is bumped
// atomically by ReplaceFrom and used to decide whether a pending user-data
// remap belongs to the catalog that is currently live.
func (s *Store) CatalogGeneration(ctx context.Context) (int64, error) {
	var raw string
	err := s.db.QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key = 'catalog_generation'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n, perr := strconv.ParseInt(raw, 10, 64)
	if perr != nil {
		return 0, perr
	}
	return n, nil
}

func (s *Store) ReplaceFrom(ctx context.Context, stagingPath string) error {
	generation, err := s.CatalogGeneration(ctx)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DETACH DATABASE staging`); err == nil {
		// A previous attempt left the alias attached; it is now detached.
	}
	if _, err := s.db.ExecContext(ctx, `ATTACH DATABASE ? AS staging`, stagingPath); err != nil {
		return fmt.Errorf("attach staging: %w", err)
	}
	defer s.db.ExecContext(context.Background(), `DETACH DATABASE staging`)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, table := range catalogDeleteOrder {
		if _, err := tx.ExecContext(ctx, `DELETE FROM main.`+table); err != nil {
			return fmt.Errorf("clear %s: %w", table, err)
		}
	}
	for _, table := range catalogCopyOrder {
		var q string
		switch table {
		case "book_search":
			q = `INSERT INTO main.book_search (rowid, title, authors, series)
				SELECT rowid, title, authors, series FROM staging.book_search`
		case "authors_search":
			q = `INSERT INTO main.authors_search (rowid, name)
				SELECT rowid, name FROM staging.authors_search`
		case "series_search":
			q = `INSERT INTO main.series_search (rowid, title)
				SELECT rowid, title FROM staging.series_search`
		default:
			q = `INSERT INTO main.` + table + ` SELECT * FROM staging.` + table
		}
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("copy %s: %w", table, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO main.meta (key, value) VALUES ('catalog_generation', ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		strconv.FormatInt(generation+1, 10)); err != nil {
		return fmt.Errorf("catalog generation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

// BookKeys returns a stable key for every catalog book: LibID when present,
// otherwise the archive/file path. The keys are used to remap user data after
// a re-import whose book ids may have changed.
func (s *Store) BookKeys(ctx context.Context) (map[int64]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT b.id, b.lib_id, f.name, b.file, b.ext
		FROM books b JOIN folders f ON f.id = b.folder_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]string)
	for rows.Next() {
		var id int64
		var libID, folder, file, ext string
		if err := rows.Scan(&id, &libID, &folder, &file, &ext); err != nil {
			return nil, err
		}
		out[id] = catalogBookKey(libID, folder, file, ext)
	}
	return out, rows.Err()
}

func catalogBookKey(libID, folder, file, ext string) string {
	if libID != "" {
		return "lib:" + strings.ToLower(strings.TrimSpace(libID))
	}
	return "file:" + strings.ToLower(folder+"/"+file+"."+ext)
}

// MapBookIDs matches old catalog ids to new ones by their stable key. Keys
// that occur more than once on either side are considered ambiguous and map
// to 0, so user data is dropped instead of being attached to an arbitrary
// duplicate.
func MapBookIDs(oldKeys, newKeys map[int64]string) map[int64]int64 {
	newByKey := make(map[string]int64, len(newKeys))
	ambiguous := make(map[string]bool)
	for id, key := range newKeys {
		if _, exists := newByKey[key]; exists {
			ambiguous[key] = true
			continue
		}
		newByKey[key] = id
	}
	for key := range ambiguous {
		delete(newByKey, key)
	}
	oldSeen := make(map[string]bool, len(oldKeys))
	oldAmbiguous := make(map[string]bool)
	for _, key := range oldKeys {
		if oldSeen[key] {
			oldAmbiguous[key] = true
		}
		oldSeen[key] = true
	}
	out := make(map[int64]int64, len(oldKeys))
	for oldID, key := range oldKeys {
		if oldAmbiguous[key] {
			out[oldID] = 0
			continue
		}
		out[oldID] = newByKey[key] // zero when the book disappeared
	}
	return out
}
