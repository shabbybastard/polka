package auth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/shabbybastard/polka/internal/sqlitedrv"
)

func TestTelegramLink(t *testing.T) {
	s := newService(t)
	ctx := context.Background()

	admin, err := s.CreateUserWithTelegram(ctx, "admin", "pass1234", "", RoleAdmin, 111)
	if err != nil {
		t.Fatal(err)
	}
	if admin.TelegramID != 111 {
		t.Fatalf("telegram id = %d, want 111", admin.TelegramID)
	}
	got, err := s.GetByTelegramID(ctx, 111)
	if err != nil || got.ID != admin.ID {
		t.Fatalf("GetByTelegramID: %v %v", got, err)
	}
	if got.Role != RoleAdmin {
		t.Errorf("role lost: %+v", got)
	}

	reader, err := s.CreateUser(ctx, "reader", "pass1234", "", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetTelegramID(ctx, reader.ID, 222); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetByTelegramID(ctx, 222); got == nil || got.ID != reader.ID {
		t.Fatalf("reader link: %+v", got)
	}

	// The same Telegram ID cannot be linked twice.
	if err := s.SetTelegramID(ctx, reader.ID, 111); !errors.Is(err, ErrTelegramTaken) {
		t.Errorf("duplicate link: %v", err)
	}
	if _, err := s.CreateUserWithTelegram(ctx, "dup", "pass1234", "", RoleUser, 111); !errors.Is(err, ErrTelegramTaken) {
		t.Errorf("duplicate create: %v", err)
	}

	// Zero unlinks.
	if err := s.SetTelegramID(ctx, reader.ID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetByTelegramID(ctx, 222); !errors.Is(err, ErrNotFound) {
		t.Errorf("unlink still visible: %v", err)
	}
	if _, err := s.GetByTelegramID(ctx, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("zero id must not resolve: %v", err)
	}
	if err := s.SetTelegramID(ctx, reader.ID, -1); !errors.Is(err, ErrInvalidTelegramID) {
		t.Errorf("negative id: %v", err)
	}
}

func TestTelegramMigrationFromOldUsersDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.db")
	db, err := sqlitedrv.Open(path, sqlitedrv.Options{WAL: true, SyncNormal: true, BusyTimeout: 10000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		CREATE TABLE users (
			id            INTEGER PRIMARY KEY,
			login         TEXT NOT NULL UNIQUE COLLATE NOCASE,
			password_hash TEXT NOT NULL,
			display_name  TEXT NOT NULL DEFAULT '',
			role          TEXT NOT NULL DEFAULT 'user',
			disabled      INTEGER NOT NULL DEFAULT 0,
			created_at    TEXT NOT NULL DEFAULT (datetime('now'))
		);
		INSERT INTO users (login, password_hash) VALUES ('legacy', '!');`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	defer s.Close()
	u, err := s.GetByLogin(context.Background(), "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if u.TelegramID != 0 {
		t.Errorf("telegram id = %d, want 0", u.TelegramID)
	}
	if err := s.SetTelegramID(context.Background(), u.ID, 777); err != nil {
		t.Errorf("set after migration: %v", err)
	}
}
