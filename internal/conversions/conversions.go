// Package conversions stores the queue of background FB2 -> EPUB jobs.
package conversions

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/vestigiumincaligne/polka/internal/sqlitedrv"
)

type Status string

const (
	StatusQueued     Status = "queued"
	StatusProcessing Status = "processing"
	StatusDone       Status = "done"
	StatusFailed     Status = "failed"
	StatusSkipped    Status = "skipped"
)

// Job is one conversion task. Source is the inbox file name (relative to
// data-dir/convert-inbox) and is intentionally not exposed through the API.
type Job struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Source    string `json:"-"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	BookID    int64  `json:"bookId,omitempty"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS conversion_jobs (
	id         TEXT PRIMARY KEY,
	name       TEXT NOT NULL DEFAULT '',
	source     TEXT NOT NULL,
	status     TEXT NOT NULL DEFAULT 'queued',
	error      TEXT NOT NULL DEFAULT '',
	book_id    INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_conversion_status ON conversion_jobs (status, created_at);
`

func Open(path string) (*Store, error) {
	db, err := sqlitedrv.Open(path, sqlitedrv.Options{
		WAL: true, SyncNormal: true, BusyTimeout: 10000, ForeignKeys: true,
	})
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("conversions schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Create(ctx context.Context, id, name, source string) error {
	if id == "" || source == "" {
		return errors.New("conversion job needs id and source")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO conversion_jobs (id, name, source, status)
		VALUES (?, ?, ?, ?)`, id, name, source, StatusQueued)
	return err
}

// ClaimNext atomically takes the oldest queued job and marks it processing.
func (s *Store) ClaimNext(ctx context.Context) (*Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var j Job
	err = tx.QueryRowContext(ctx, `
		SELECT id, name, source, status, error, book_id, created_at, updated_at
		FROM conversion_jobs
		WHERE status = ?
		ORDER BY created_at, rowid
		LIMIT 1`, StatusQueued).
		Scan(&j.ID, &j.Name, &j.Source, &j.Status, &j.Error, &j.BookID, &j.CreatedAt, &j.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE conversion_jobs SET status = ?, updated_at = datetime('now') WHERE id = ?`,
		StatusProcessing, j.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	j.Status = string(StatusProcessing)
	j.UpdatedAt = time.Now().UTC().Format("2006-01-02 15:04:05")
	return &j, nil
}

// ResetProcessing makes jobs interrupted by a restart runnable again.
func (s *Store) ResetProcessing(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE conversion_jobs
		SET status = ?, updated_at = datetime('now')
		WHERE status = ?`, StatusQueued, StatusProcessing)
	return err
}

func (s *Store) SetStatus(ctx context.Context, id string, status Status, errText string, bookID int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE conversion_jobs
		SET status = ?, error = ?, book_id = ?, updated_at = datetime('now')
		WHERE id = ?`, status, errText, bookID, id)
	return err
}

func (s *Store) Get(ctx context.Context, id string) (*Job, error) {
	var j Job
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, source, status, error, book_id, created_at, updated_at
		FROM conversion_jobs WHERE id = ?`, id).
		Scan(&j.ID, &j.Name, &j.Source, &j.Status, &j.Error, &j.BookID, &j.CreatedAt, &j.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

func (s *Store) List(ctx context.Context, limit int) ([]Job, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, source, status, error, book_id, created_at, updated_at
		FROM conversion_jobs
		ORDER BY created_at DESC, rowid DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Job, 0)
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.Name, &j.Source, &j.Status, &j.Error, &j.BookID, &j.CreatedAt, &j.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

var ErrNotFound = errors.New("conversion job not found")

// InboxPath returns the conventional inbox path for a job source file.
func InboxPath(dataDir, source string) string {
	return filepath.Join(dataDir, "convert-inbox", filepath.Base(source))
}
