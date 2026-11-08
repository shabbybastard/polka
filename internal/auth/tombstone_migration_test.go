package auth

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/shabbybastard/polka/internal/sqlitedrv"
)

// Databases created before tombstones must open and gain the new columns.
func TestOpenMigratesTombstoneColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.db")
	raw, err := sqlitedrv.Open(path, sqlitedrv.Options{WAL: true, SyncNormal: true, BusyTimeout: 10000, ForeignKeys: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE users (
			id INTEGER PRIMARY KEY, login TEXT NOT NULL UNIQUE COLLATE NOCASE,
			password_hash TEXT NOT NULL, display_name TEXT NOT NULL DEFAULT '',
			role TEXT NOT NULL DEFAULT 'user', disabled INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL DEFAULT (datetime('now')))`,
		`CREATE TABLE reading_progress (
			user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
			book_id INTEGER NOT NULL, chapter INTEGER NOT NULL DEFAULT 0,
			position REAL NOT NULL DEFAULT 0, updated_at TEXT NOT NULL DEFAULT (datetime('now')),
			PRIMARY KEY (user_id, book_id)) WITHOUT ROWID`,
		`CREATE TABLE book_ratings (
			user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
			book_id INTEGER NOT NULL, rating INTEGER NOT NULL CHECK (rating BETWEEN 1 AND 5),
			updated_at TEXT NOT NULL DEFAULT (datetime('now')), PRIMARY KEY (user_id, book_id)) WITHOUT ROWID`,
		`CREATE TABLE lists (
			id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
			name TEXT NOT NULL, builtin TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT (datetime('now')), UNIQUE (user_id, name))`,
		`CREATE TABLE list_books (
			list_id INTEGER NOT NULL REFERENCES lists (id) ON DELETE CASCADE,
			book_id INTEGER NOT NULL, added_at TEXT NOT NULL DEFAULT (datetime('now')),
			PRIMARY KEY (list_id, book_id)) WITHOUT ROWID`,
	} {
		if _, err := raw.Exec(q); err != nil {
			raw.Close()
			t.Fatal(err)
		}
	}
	raw.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	u, err := s.CreateUser(ctx, "reader", "password", "", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RateBook(ctx, u.ID, 1, 5); err != nil {
		t.Fatal(err)
	}
	if err := s.RateBook(ctx, u.ID, 1, 0); err != nil {
		t.Fatal(err)
	}
	if r := s.UserRating(ctx, u.ID, 1); r != 0 {
		t.Errorf("rating tombstone = %d, want 0", r)
	}
	list, err := s.CreateList(ctx, u.ID, "favorites")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddToList(ctx, u.ID, list.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveFromList(ctx, u.ID, list.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteList(ctx, u.ID, list.ID); err != nil {
		t.Fatal(err)
	}
}
