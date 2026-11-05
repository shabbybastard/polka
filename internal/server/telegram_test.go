package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/shabbybastard/polka/internal/auth"
	"github.com/shabbybastard/polka/internal/tgbot"
)

func TestAdminUsersTelegramID(t *testing.T) {
	f := newFullServer(t, "required")
	ctx := context.Background()

	admin, err := f.users.GetByLogin(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := f.users.GetByLogin(ctx, "reader")
	if err != nil {
		t.Fatal(err)
	}

	resp := postJSON(t, f.admin, f.ts.URL+"/admin/users/"+itoa64(admin.ID), map[string]any{"telegramId": 123456789})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("link admin telegram -> %d", resp.StatusCode)
	}
	resp.Body.Close()
	got, err := f.users.GetByTelegramID(ctx, 123456789)
	if err != nil || got.ID != admin.ID {
		t.Fatalf("linked user: %v %v", got, err)
	}

	// Another account cannot take the same Telegram ID.
	resp = postJSON(t, f.admin, f.ts.URL+"/admin/users/"+itoa64(reader.ID), map[string]any{"telegramId": 123456789})
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("duplicate telegram id -> %d, want 409", resp.StatusCode)
	}
	resp.Body.Close()

	// Zero unbinds.
	resp = postJSON(t, f.admin, f.ts.URL+"/admin/users/"+itoa64(admin.ID), map[string]any{"telegramId": 0})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("unlink -> %d", resp.StatusCode)
	}
	resp.Body.Close()
	if _, err := f.users.GetByTelegramID(ctx, 123456789); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("still linked: %v", err)
	}

	// Readers cannot edit accounts, and negative ids are rejected.
	if code(t, f.reader, "POST", f.ts.URL+"/admin/users/"+itoa64(reader.ID), `{"telegramId":42}`) != http.StatusForbidden {
		t.Error("reader must not link telegram ids")
	}
	if code(t, f.admin, "POST", f.ts.URL+"/admin/users/"+itoa64(reader.ID), `{"telegramId":-5}`) != http.StatusBadRequest {
		t.Error("negative telegram id must be rejected")
	}

	// Creation supports telegramId in one request.
	resp = postJSON(t, f.admin, f.ts.URL+"/admin/users", map[string]any{
		"login": "tguser", "password": "pass1234", "telegramId": 987654321,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create with telegram id -> %d", resp.StatusCode)
	}
	resp.Body.Close()
	if u, err := f.users.GetByTelegramID(ctx, 987654321); err != nil || u.Login != "tguser" {
		t.Errorf("created link: %v %v", u, err)
	}
}

func TestTelegramUploadRateLimitSetting(t *testing.T) {
	f := newFullServer(t, "required")
	settings := getJSONWith(t, f.admin, f.ts.URL+"/admin/settings")
	tg, _ := settings["telegram"].(map[string]any)
	if tg == nil || tg["uploadRateLimit"].(float64) != 50 {
		t.Fatalf("default telegram settings: %v", settings["telegram"])
	}
	resp := postJSON(t, f.admin, f.ts.URL+"/admin/settings", map[string]any{
		"telegram": map[string]any{"uploadRateLimit": 7},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save telegram limit -> %d", resp.StatusCode)
	}
	resp.Body.Close()
	settings = getJSONWith(t, f.admin, f.ts.URL+"/admin/settings")
	if settings["telegram"].(map[string]any)["uploadRateLimit"].(float64) != 7 {
		t.Errorf("saved telegram settings: %v", settings["telegram"])
	}
	if code(t, f.admin, "POST", f.ts.URL+"/admin/settings", `{"telegram":{"uploadRateLimit":0}}`) != http.StatusBadRequest {
		t.Error("zero rate limit must be rejected")
	}
}

func TestTelegramBackendUsesExistingServices(t *testing.T) {
	f := newFullServer(t, "required")
	ctx := context.Background()
	reader, err := f.users.GetByLogin(ctx, "reader")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.users.SetTelegramID(ctx, reader.ID, 42); err != nil {
		t.Fatal(err)
	}
	backend := f.app.TelegramBackend()

	u, err := backend.UserByTelegramID(ctx, 42)
	if err != nil || u.ID != reader.ID || u.Role != auth.RoleUser {
		t.Fatalf("user: %+v %v", u, err)
	}

	books, err := backend.Search(ctx, "Война", 10, 0)
	if err != nil || len(books) != 1 || books[0].Title != "Война и мир" {
		t.Fatalf("search: %+v %v", books, err)
	}
	if _, err := backend.SearchTitles(ctx, "Анна", 10, 0); err != nil {
		t.Fatal(err)
	}
	if authors, err := backend.SearchAuthors(ctx, "Толстой", 10); err != nil || len(authors) != 1 {
		t.Fatalf("authors: %+v %v", authors, err)
	}
	if _, name, err := backend.AuthorBooks(ctx, f.authorID, 10, 0); err != nil || !strings.Contains(name, "Толстой") {
		t.Fatalf("author books: %q %v", name, err)
	}
	if series, err := backend.SearchSeries(ctx, "Классика", 10); err != nil || len(series) != 1 {
		t.Fatalf("series: %+v %v", series, err)
	}
	if _, title, err := backend.SeriesBooks(ctx, f.seriesID, 10, 0); err != nil || title != "Классика" {
		t.Fatalf("series books: %q %v", title, err)
	}
	if recent, err := backend.Recent(ctx, 5, 0); err != nil || len(recent) != 2 {
		t.Fatalf("recent: %+v %v", recent, err)
	}
	if random, err := backend.Random(ctx, reader.ID, "ru"); err != nil || random == nil {
		t.Fatalf("random: %+v %v", random, err)
	}
	if cover, _, err := backend.Cover(ctx, f.bookID); err != nil || len(cover) == 0 {
		t.Fatalf("cover: %d %v", len(cover), err)
	}

	if err := f.users.RateBook(ctx, reader.ID, f.bookID, 5); err != nil {
		t.Fatal(err)
	}
	if err := f.users.SaveProgress(ctx, reader.ID, f.bookID, auth.Progress{Overall: 0.5}); err != nil {
		t.Fatal(err)
	}
	card, err := backend.Book(ctx, reader.ID, f.bookID, "ru")
	if err != nil {
		t.Fatal(err)
	}
	if !card.HasRating || card.Rating != 5 || card.RatingCount != 1 {
		t.Errorf("rating: %+v", card)
	}
	if !card.HasProgress || card.Progress != 0.5 || card.IsFinished {
		t.Errorf("progress: %+v", card)
	}
	if card.Annotation == "" || len(card.Genres) == 0 {
		t.Errorf("card metadata: %+v", card)
	}

	stats, err := backend.Stats(ctx)
	if err != nil || stats.Books != 2 || stats.Users < 2 {
		t.Fatalf("stats: %+v %v", stats, err)
	}
	if status, err := backend.Status(ctx); err != nil || status == nil {
		t.Fatalf("status: %+v %v", status, err)
	}

	fb2 := strings.Replace(testFB2, "Война и мир", "Новая книга", 1)
	res, err := backend.Upload(ctx, reader.ID, "new.fb2", strings.NewReader(fb2), int64(len(fb2)), "ru")
	if err != nil || res == nil || res.Status != tgbot.UploadAdded {
		t.Fatalf("upload: %+v %v", res, err)
	}
	dup, err := backend.Upload(ctx, reader.ID, "new.fb2", strings.NewReader(fb2), int64(len(fb2)), "ru")
	if err != nil || dup == nil || dup.Status != tgbot.UploadDuplicate {
		t.Fatalf("duplicate: %+v %v", dup, err)
	}
	if stats, _ := backend.Stats(ctx); stats.Books != 3 {
		t.Errorf("books after upload = %d", stats.Books)
	}
	if backend.UploadLimit(ctx) != 50 {
		t.Errorf("upload limit = %d", backend.UploadLimit(ctx))
	}
}
