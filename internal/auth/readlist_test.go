package auth

import (
	"context"
	"path/filepath"
	"testing"
)

func openReadService(t *testing.T) (*Service, *User) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	u, err := s.CreateUser(context.Background(), "reader", "password", "", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	return s, u
}

func TestReadListAutoMembership(t *testing.T) {
	s, u := openReadService(t)
	ctx := context.Background()

	rl, err := s.ReadList(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rl.Builtin != BuiltinRead {
		t.Fatalf("read list builtin = %q", rl.Builtin)
	}
	lists, err := s.Lists(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range lists {
		if l.ID == rl.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("read list missing from Lists: %+v", lists)
	}

	if err := s.SaveProgress(ctx, u.ID, 1, Progress{Chapter: 2, Position: 0.4, Overall: 0.979}); err != nil {
		t.Fatal(err)
	}
	if ids, _ := s.ReadBookIDs(ctx, u.ID, 10); len(ids) != 0 {
		t.Fatalf("0.979 should not be read: %v", ids)
	}

	if err := s.SaveProgress(ctx, u.ID, 1, Progress{Chapter: 3, Position: 0.5, Overall: 0.98}); err != nil {
		t.Fatal(err)
	}
	if ids, _ := s.ReadBookIDs(ctx, u.ID, 10); len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("0.98 should be read: %v", ids)
	}
	if p, err := s.ListProgress(ctx, u.ID, 10); err != nil || len(p) != 0 {
		t.Fatalf("finished book still in reading now: %+v, %v", p, err)
	}

	if err := s.SaveProgress(ctx, u.ID, 1, Progress{Chapter: 1, Position: 0.1, Overall: 0.5}); err != nil {
		t.Fatal(err)
	}
	if ids, _ := s.ReadBookIDs(ctx, u.ID, 10); len(ids) != 0 {
		t.Fatalf("0.5 should leave the read list: %v", ids)
	}
}

func TestReadListManualAddRemove(t *testing.T) {
	s, u := openReadService(t)
	ctx := context.Background()
	rl, err := s.ReadList(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProgress(ctx, u.ID, 7, Progress{Chapter: 2, Position: 0.5, Overall: 0.4, Locator: "cfi"}); err != nil {
		t.Fatal(err)
	}

	if err := s.AddToList(ctx, u.ID, rl.ID, 7); err != nil {
		t.Fatal(err)
	}
	p, err := s.GetProgress(ctx, u.ID, 7)
	if err != nil {
		t.Fatal(err)
	}
	if p.Overall != 1 || p.Chapter != 2 || p.Position != 0.5 || p.Locator != "cfi" {
		t.Fatalf("manual read progress = %+v", p)
	}
	if ids, _ := s.ReadBookIDs(ctx, u.ID, 10); len(ids) != 1 || ids[0] != 7 {
		t.Fatalf("manual read list = %v", ids)
	}
	if p, err := s.ListProgress(ctx, u.ID, 10); err != nil || len(p) != 0 {
		t.Fatalf("manual read book in reading now: %+v, %v", p, err)
	}

	if err := s.RemoveFromList(ctx, u.ID, rl.ID, 7); err != nil {
		t.Fatal(err)
	}
	p, err = s.GetProgress(ctx, u.ID, 7)
	if err != nil {
		t.Fatal(err)
	}
	if p.Overall != 0 || p.Chapter != 0 || p.Position != 0 {
		t.Fatalf("manual unread progress = %+v", p)
	}
	if ids, _ := s.ReadBookIDs(ctx, u.ID, 10); len(ids) != 0 {
		t.Fatalf("manual unread list = %v", ids)
	}
	if p, err := s.ListProgress(ctx, u.ID, 10); err != nil || len(p) != 1 || p[0].BookID != 7 {
		t.Fatalf("manual unread should be in reading now: %+v, %v", p, err)
	}
}

func TestReadListDeleteProgress(t *testing.T) {
	s, u := openReadService(t)
	ctx := context.Background()
	if err := s.SaveProgress(ctx, u.ID, 5, Progress{Overall: 1}); err != nil {
		t.Fatal(err)
	}
	if ids, _ := s.ReadBookIDs(ctx, u.ID, 10); len(ids) != 1 {
		t.Fatalf("read list before delete = %v", ids)
	}
	if err := s.DeleteProgress(ctx, u.ID, 5); err != nil {
		t.Fatal(err)
	}
	if ids, _ := s.ReadBookIDs(ctx, u.ID, 10); len(ids) != 0 {
		t.Fatalf("read list after delete = %v", ids)
	}
}

func TestReadListMerge(t *testing.T) {
	s, u := openReadService(t)
	ctx := context.Background()
	if err := s.MergeState(ctx, u.ID, &SyncState{
		Progress: []ProgressState{{BookID: 9, Overall: 0.99, UpdatedAt: "2099-01-01 00:00:00"}},
	}); err != nil {
		t.Fatal(err)
	}
	if ids, _ := s.ReadBookIDs(ctx, u.ID, 10); len(ids) != 1 || ids[0] != 9 {
		t.Fatalf("merge read list = %v", ids)
	}
	if err := s.MergeState(ctx, u.ID, &SyncState{
		Progress: []ProgressState{{BookID: 9, Overall: 0.99, Deleted: true, UpdatedAt: "2100-01-01 00:00:00"}},
	}); err != nil {
		t.Fatal(err)
	}
	if ids, _ := s.ReadBookIDs(ctx, u.ID, 10); len(ids) != 0 {
		t.Fatalf("merge deleted read list = %v", ids)
	}
}

func TestReadListReconcilesExistingProgress(t *testing.T) {
	s, u := openReadService(t)
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO reading_progress (user_id, book_id, chapter, position, overall, locator, deleted, updated_at)
		VALUES (?, 42, 0, 0, 1, '', 0, strftime('%Y-%m-%d %H:%M:%f', 'now'))`, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lists(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if ids, _ := s.ReadBookIDs(ctx, u.ID, 10); len(ids) != 1 || ids[0] != 42 {
		t.Fatalf("existing finished progress was not reconciled: %v", ids)
	}
}

func TestReadListRemovesStaleMembership(t *testing.T) {
	s, u := openReadService(t)
	ctx := context.Background()
	rl, err := s.ReadList(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO list_books (list_id, book_id, deleted, added_at)
		VALUES (?, 77, 0, strftime('%Y-%m-%d %H:%M:%f', 'now'))`, rl.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lists(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if ids, _ := s.ReadBookIDs(ctx, u.ID, 10); len(ids) != 0 {
		t.Fatalf("stale read-list membership survived: %v", ids)
	}
}

func TestReadListAdoptsExistingName(t *testing.T) {
	s, u := openReadService(t)
	ctx := context.Background()
	if _, err := s.CreateList(ctx, u.ID, "Прочитано"); err != nil {
		t.Fatal(err)
	}
	rl, err := s.ReadList(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rl.Builtin != BuiltinRead {
		t.Fatalf("adopted read list builtin = %q", rl.Builtin)
	}
}
