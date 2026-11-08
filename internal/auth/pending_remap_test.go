package auth

import (
	"context"
	"path/filepath"
	"testing"
)

func TestPendingRemapAppliesOnlyForMatchingGeneration(t *testing.T) {
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
	if err := s.SaveProgress(ctx, u.ID, 1, Progress{Chapter: 4}); err != nil {
		t.Fatal(err)
	}

	if err := s.SavePendingRemap(ctx, 2, map[int64]int64{1: 7}); err != nil {
		t.Fatal(err)
	}
	// Crash before the catalog swap: generation is still 1, mapping is stale.
	if err := s.ApplyPendingRemap(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetProgress(ctx, u.ID, 1); err != nil {
		t.Errorf("stale pending remap touched user data: %v", err)
	}

	if err := s.SavePendingRemap(ctx, 2, map[int64]int64{1: 7}); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyPendingRemap(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if p, err := s.GetProgress(ctx, u.ID, 7); err != nil || p.Chapter != 4 {
		t.Errorf("pending remap not applied: %+v, %v", p, err)
	}
	if _, err := s.GetProgress(ctx, u.ID, 1); err == nil {
		t.Error("old book id survived a successful pending remap")
	}
}
