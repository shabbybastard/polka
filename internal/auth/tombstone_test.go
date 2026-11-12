package auth

import (
	"context"
	"path/filepath"
	"testing"
)

func TestRatingAndListTombstonesSurviveSync(t *testing.T) {
	ctx := context.Background()
	open := func(name string) (*Service, *User) {
		s, err := Open(filepath.Join(t.TempDir(), name))
		if err != nil {
			t.Fatal(err)
		}
		u, err := s.CreateUser(ctx, "reader", "password", "", RoleUser)
		if err != nil {
			s.Close()
			t.Fatal(err)
		}
		return s, u
	}
	a, ua := open("a.db")
	defer a.Close()
	b, ub := open("b.db")
	defer b.Close()

	if err := a.RateBook(ctx, ua.ID, 10, 5); err != nil {
		t.Fatal(err)
	}
	state, err := a.ExportState(ctx, ua.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.MergeState(ctx, ub.ID, state); err != nil {
		t.Fatal(err)
	}
	if r := b.UserRating(ctx, ub.ID, 10); r != 5 {
		t.Fatalf("merged rating = %d, want 5", r)
	}
	if err := a.RateBook(ctx, ua.ID, 10, 0); err != nil {
		t.Fatal(err)
	}
	if r := a.UserRating(ctx, ua.ID, 10); r != 0 {
		t.Fatalf("rating after delete = %d, want 0", r)
	}
	state, err = a.ExportState(ctx, ua.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.MergeState(ctx, ub.ID, state); err != nil {
		t.Fatal(err)
	}
	if r := b.UserRating(ctx, ub.ID, 10); r != 0 {
		t.Fatalf("deleted rating resurrected on peer: %d", r)
	}

	list, err := a.CreateList(ctx, ua.ID, "favorites")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.AddToList(ctx, ua.ID, list.ID, 10); err != nil {
		t.Fatal(err)
	}
	if err := a.RemoveFromList(ctx, ua.ID, list.ID, 10); err != nil {
		t.Fatal(err)
	}
	state, err = a.ExportState(ctx, ua.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.MergeState(ctx, ub.ID, state); err != nil {
		t.Fatal(err)
	}
	bl, err := b.Lists(ctx, ub.ID)
	if err != nil {
		t.Fatal(err)
	}
	var custom *List
	for i := range bl {
		if bl[i].Name == "favorites" {
			custom = &bl[i]
		}
	}
	if custom == nil {
		t.Fatalf("peer lists = %+v, want favorites", bl)
	}
	ids, err := b.ListBookIDs(ctx, ub.ID, custom.ID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("removed book resurrected on peer: %v", ids)
	}

	if err := a.DeleteList(ctx, ua.ID, list.ID); err != nil {
		t.Fatal(err)
	}
	state, err = a.ExportState(ctx, ua.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.MergeState(ctx, ub.ID, state); err != nil {
		t.Fatal(err)
	}
	if bl, err := b.Lists(ctx, ub.ID); err != nil {
		t.Fatal(err)
	} else {
		for _, l := range bl {
			if l.Name == "favorites" {
				t.Fatalf("deleted list resurrected on peer: %+v", bl)
			}
		}
	}
}
