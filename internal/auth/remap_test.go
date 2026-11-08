package auth

import (
	"context"
	"path/filepath"
	"testing"
)

func TestRemapBookIDsHandlesCyclesAndDeletesUnmapped(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	u, err := s.CreateUser(ctx, "reader", "password", "", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProgress(ctx, u.ID, 1, Progress{Chapter: 1, Position: 0.1, Overall: 0.2}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProgress(ctx, u.ID, 2, Progress{Chapter: 2, Position: 0.3, Overall: 0.4}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProgress(ctx, u.ID, 3, Progress{Chapter: 3, Position: 0.5, Overall: 0.6}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProgress(ctx, u.ID, 99, Progress{Chapter: 9, Position: 0.9, Overall: 0.9}); err != nil {
		t.Fatal(err)
	}
	if err := s.RateBook(ctx, u.ID, 1, 5); err != nil {
		t.Fatal(err)
	}
	if err := s.RateBook(ctx, u.ID, 2, 4); err != nil {
		t.Fatal(err)
	}
	list, err := s.CreateList(ctx, u.ID, "favorites")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddToList(ctx, u.ID, list.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.AddToList(ctx, u.ID, list.ID, 3); err != nil {
		t.Fatal(err)
	}

	// 1 <-> 2 swap, 3 disappears.
	if err := s.RemapBookIDs(ctx, map[int64]int64{1: 2, 2: 1, 3: 0}); err != nil {
		t.Fatal(err)
	}
	p1, err := s.GetProgress(ctx, u.ID, 1)
	if err != nil || p1.Chapter != 2 {
		t.Errorf("progress for new book 1 = %+v, %v; want chapter 2", p1, err)
	}
	p2, err := s.GetProgress(ctx, u.ID, 2)
	if err != nil || p2.Chapter != 1 {
		t.Errorf("progress for new book 2 = %+v, %v; want chapter 1", p2, err)
	}
	if _, err := s.GetProgress(ctx, u.ID, 3); err == nil {
		t.Error("progress for removed book 3 survived remap")
	}
	if _, err := s.GetProgress(ctx, u.ID, 99); err == nil {
		t.Error("progress for an unknown foreign book id survived remap")
	}
	if r := s.UserRating(ctx, u.ID, 1); r != 4 {
		t.Errorf("rating for new book 1 = %d, want 4", r)
	}
	if r := s.UserRating(ctx, u.ID, 2); r != 5 {
		t.Errorf("rating for new book 2 = %d, want 5", r)
	}
	ids, err := s.ListBookIDs(ctx, u.ID, list.ID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 2 {
		t.Errorf("list books = %v, want [2]", ids)
	}
}
