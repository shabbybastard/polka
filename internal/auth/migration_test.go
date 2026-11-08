package auth

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/shabbybastard/polka/internal/sqlitedrv"
)

// Old users tables used a plain INTEGER PRIMARY KEY. Open must rebuild them
// with AUTOINCREMENT, otherwise SQLite reuses the maximum id after deletion
// and a new account inherits the previous user's settings.
func TestOpenMigratesUsersToAutoincrement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.db")
	raw, err := sqlitedrv.Open(path, sqlitedrv.Options{WAL: true, SyncNormal: true, BusyTimeout: 10000, ForeignKeys: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`
		CREATE TABLE users (
			id            INTEGER PRIMARY KEY,
			login         TEXT NOT NULL UNIQUE COLLATE NOCASE,
			password_hash TEXT NOT NULL,
			display_name  TEXT NOT NULL DEFAULT '',
			role          TEXT NOT NULL DEFAULT 'user',
			disabled      INTEGER NOT NULL DEFAULT 0,
			created_at    TEXT NOT NULL DEFAULT (datetime('now'))
		)`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO users (id, login, password_hash) VALUES (1, 'old', '!')`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	raw.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows, err := s.db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		t.Error("foreign_key_check failed after users table rebuild")
	}
	rows.Close()
	ctx := context.Background()

	for _, key := range []string{"reader_email:1", "reader_prefs:1", "kosync_key:1", "telegram.lang.1"} {
		if err := s.SetSetting(ctx, key, "value"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteUser(ctx, 1); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"reader_email:1", "reader_prefs:1", "kosync_key:1", "telegram.lang.1"} {
		if got := s.GetSetting(ctx, key, ""); got != "" {
			t.Errorf("setting %q survived DeleteUser: %q", key, got)
		}
	}

	first, err := s.CreateUser(ctx, "first", "password", "", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != 2 {
		t.Errorf("first new user id = %d, want 2 (AUTOINCREMENT after old id 1)", first.ID)
	}
	if err := s.DeleteUser(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateUser(ctx, "second", "password", "", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != 3 {
		t.Errorf("second new user id = %d, want 3 (deleted ids must not be reused)", second.ID)
	}
}
