package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// SavePendingRemap records an old→new book-id mapping before the catalog is
// swapped. If the process dies after the swap but before the remap is
// applied, the next startup can complete it instead of losing user data.
func (s *Service) SavePendingRemap(ctx context.Context, generation int64, mapping map[int64]int64) error {
	data, err := json.Marshal(mapping)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO pending_book_remap (id, generation, mapping)
		VALUES (1, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			generation = excluded.generation,
			mapping = excluded.mapping,
			created_at = datetime('now')`, generation, string(data))
	return err
}

func (s *Service) loadPendingRemap(ctx context.Context) (int64, map[int64]int64, error) {
	var generation int64
	var raw string
	err := s.db.QueryRowContext(ctx,
		`SELECT generation, mapping FROM pending_book_remap WHERE id = 1`).
		Scan(&generation, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil, ErrNotFound
	}
	if err != nil {
		return 0, nil, err
	}
	mapping := map[int64]int64{}
	if err := json.Unmarshal([]byte(raw), &mapping); err != nil {
		return 0, nil, fmt.Errorf("pending remap JSON: %w", err)
	}
	return generation, mapping, nil
}

func (s *Service) DeletePendingRemap(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM pending_book_remap WHERE id = 1`)
	return err
}

// HasPendingRemap reports whether a crash left an unfinished catalog remap.
func (s *Service) HasPendingRemap(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM pending_book_remap WHERE id = 1`).Scan(&n)
	return n > 0, err
}

// ApplyPendingRemap applies a saved mapping only if it targets the live
// catalog generation. A stale mapping (crash before ReplaceFrom) is dropped.
func (s *Service) ApplyPendingRemap(ctx context.Context, catalogGeneration int64) error {
	generation, mapping, err := s.loadPendingRemap(ctx)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if generation != catalogGeneration {
		return s.DeletePendingRemap(ctx)
	}
	if err := s.RemapBookIDs(ctx, mapping); err != nil {
		return err
	}
	return s.DeletePendingRemap(ctx)
}
