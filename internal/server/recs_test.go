package server

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/shabbybastard/polka/internal/auth"
	"github.com/shabbybastard/polka/internal/store"
)

func TestUserSeedsPreferReadingAndRead(t *testing.T) {
	f := newFullServer(t, "required")
	ctx := context.Background()
	users, err := f.users.ListUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var admin *auth.User
	for i := range users {
		if users[i].Login == "admin" {
			admin = &users[i]
		}
	}
	if admin == nil {
		t.Fatal("admin not found")
	}

	if err := f.users.SaveProgress(ctx, admin.ID, f.bookID, auth.Progress{Overall: 0.5}); err != nil {
		t.Fatal(err)
	}
	if err := f.users.SaveProgress(ctx, admin.ID, f.bookID2, auth.Progress{Overall: 1}); err != nil {
		t.Fatal(err)
	}
	liked, err := f.st.AddBook(ctx, &store.BookInput{
		Title: "Liked", Folder: ".", File: "liked", Ext: "fb2",
		Size: 1, Added: "2026-11-12",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.users.RateBook(ctx, admin.ID, liked, 5); err != nil {
		t.Fatal(err)
	}

	seeds, exclude := f.app.userSeeds(httptest.NewRequest("GET", "/", nil), admin.ID)
	if len(seeds) != 3 || seeds[0] != f.bookID || seeds[1] != f.bookID2 || seeds[2] != liked {
		t.Fatalf("seeds = %v, want [%d %d %d]", seeds, f.bookID, f.bookID2, liked)
	}
	for _, want := range []int64{f.bookID, f.bookID2, liked} {
		found := false
		for _, id := range exclude {
			if id == want {
				found = true
			}
		}
		if !found {
			t.Errorf("exclude does not contain %d: %v", want, exclude)
		}
	}
}
