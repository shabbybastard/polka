package auth

import (
	"context"
	"path/filepath"
	"testing"
)

func TestWishlistAdoptsExistingListName(t *testing.T) {
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
	if _, err := s.CreateList(ctx, u.ID, "Хочу прочитать"); err != nil {
		t.Fatal(err)
	}
	wl, err := s.Wishlist(ctx, u.ID)
	if err != nil {
		t.Fatalf("Wishlist after ordinary list with the same name: %v", err)
	}
	if wl.Builtin != BuiltinWishlist {
		t.Errorf("wishlist builtin = %q", wl.Builtin)
	}
	lists, err := s.Lists(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(lists) != 1 || lists[0].ID != wl.ID {
		t.Errorf("lists = %+v, want only the adopted wishlist", lists)
	}
}
