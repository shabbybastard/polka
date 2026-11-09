package server

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/shabbybastard/polka/internal/auth"
	"github.com/shabbybastard/polka/internal/config"
	"github.com/shabbybastard/polka/internal/library"
	"github.com/shabbybastard/polka/internal/store"
)

const testFB2 = `<?xml version="1.0" encoding="UTF-8"?>
<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0" xmlns:l="http://www.w3.org/1999/xlink">
<description>
  <title-info>
    <book-title>Война и мир</book-title>
    <annotation><p>Роман-эпопея.</p></annotation>
    <coverpage><image l:href="#c.png"/></coverpage>
  </title-info>
  <publish-info><publisher>Тестиздат</publisher><year>1869</year></publish-info>
</description>
<body><section><p>Текст.</p></section></body>
<binary id="c.png" content-type="image/png">iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==</binary>
</FictionBook>`

// newTestServer starts a server with one book in a synthetic library.
func newTestServer(t *testing.T) (*httptest.Server, int64) {
	t.Helper()
	dir := t.TempDir()

	// Archive containing the book
	libDir := filepath.Join(dir, "lib")
	os.MkdirAll(libDir, 0o755)
	zf, err := os.Create(filepath.Join(libDir, "arch-001.zip"))
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(zf)
	w, _ := zw.Create("100.fb2")
	w.Write([]byte(testFB2))
	zw.Close()
	zf.Close()

	// Database with this book
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	ctx := context.Background()
	session, err := st.NewImport(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = session.Add(&store.BookInput{
		Title:   "Война и мир",
		Authors: []store.AuthorName{{Last: "Толстой", First: "Лев"}},
		Genres:  []string{"prose_classic"},
		Series:  "Классика", SeriesNum: 1,
		Folder: "arch-001.zip", File: "100", Ext: "fb2",
		Size: 1000, Lang: "ru", Added: "2024-01-01", Rate: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Finish(); err != nil {
		t.Fatal(err)
	}

	var bookID int64
	if err := st.DB().QueryRow(`SELECT id FROM books`).Scan(&bookID); err != nil {
		t.Fatal(err)
	}

	users, err := auth.Open(filepath.Join(dir, "users.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { users.Close() })

	cfg := &config.Config{LibraryDir: libDir, Auth: "public"}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	webFS := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html/>")}}
	srv := New(cfg, log, st, library.New(libDir), users, nil, webFS)

	ts := httptest.NewServer(srv.Handler)
	t.Cleanup(ts.Close)
	return ts, bookID
}

func getJSON(t *testing.T, url string) map[string]any {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("X-Polka-Lang", "ru") // tests assert Russian responses
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s -> %d", url, resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAPIEndToEnd(t *testing.T) {
	ts, bookID := newTestServer(t)

	// getConfig
	cfg := getJSON(t, ts.URL+"/main/getBooks/getConfig")
	if cfg["numberOfBooks"].(float64) != 1 {
		t.Errorf("numberOfBooks = %v", cfg["numberOfBooks"])
	}

	// getHomeShelves: a book rated 5 must land in top_rated
	shelves := getJSON(t, ts.URL+"/main/getBooks/getHomeShelves?limit=10")["shelves"].([]any)
	found := false
	for _, sh := range shelves {
		m := sh.(map[string]any)
		if m["id"] == "top_rated" && len(m["books"].([]any)) == 1 {
			book := m["books"].([]any)[0].(map[string]any)
			if book["Title"] != "Война и мир" || book["AuthorsNames"] != "Толстой Лев" {
				t.Errorf("book json = %v", book)
			}
			found = true
		}
	}
	if !found {
		t.Error("top_rated shelf is empty")
	}

	// Search
	stats := getJSON(t, ts.URL+"/main/getBooks/getSearchStats?search=войн")["searchStats"].(map[string]any)
	if stats["bookTitles"].(float64) != 1 {
		t.Errorf("searchStats = %v", stats)
	}
	titles := getJSON(t, ts.URL+"/main/getBooks/getSearchTitles?search=войн")["titlesList"].([]any)
	if len(titles) != 1 {
		t.Fatalf("titlesList = %v", titles)
	}
	authors := getJSON(t, ts.URL+"/main/getBooks/getSearchAuthors?search=толст")["authorsList"].([]any)
	if len(authors) != 1 || authors[0].(map[string]any)["Books"].(float64) != 1 {
		t.Errorf("authorsList = %v", authors)
	}
	series := getJSON(t, ts.URL+"/main/getBooks/getSearchSeries?search=класси")["seriesList"].([]any)
	if len(series) != 1 {
		t.Errorf("seriesList = %v", series)
	}

	// Author's books
	authorID := int(authors[0].(map[string]any)["AuthorID"].(float64))
	ab := getJSON(t, ts.URL+"/main/getBooks/getSearchAuthorBooks?selectedItemID="+itoa(authorID))
	if ab["title"] != "Толстой Лев" || len(ab["titlesList"].([]any)) != 1 {
		t.Errorf("author books = %v", ab)
	}

	// Book card: annotation and publication data from the FB2 in the archive
	form := getJSON(t, ts.URL+"/main/getBooks/getBookForm?selectedItemID="+itoa64(bookID))
	bf := form["bookForm"].(map[string]any)
	if bf["Genres"] != "Классическая проза" || bf["Ext"] != ".fb2" {
		t.Errorf("bookForm = %v", bf)
	}
	if !strings.Contains(form["annotation"].(string), "Роман-эпопея") {
		t.Errorf("annotation = %v", form["annotation"])
	}
	if form["publisher"] != "Тестиздат" || form["year"] != "1869" {
		t.Errorf("publish info: %v / %v", form["publisher"], form["year"])
	}

	// Cover
	resp, err := http.Get(ts.URL + "/Images/covers/" + itoa64(bookID))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" {
		t.Errorf("cover: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	// fb2 download
	resp, err = http.Get(ts.URL + "/Images/fb2/" + itoa64(bookID))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Disposition"), "100.fb2") {
		t.Errorf("fb2 download: %d %s", resp.StatusCode, resp.Header.Get("Content-Disposition"))
	}

	// compact download
	resp, err = http.Get(ts.URL + "/Images/fb2compact/" + itoa64(bookID))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("fb2compact -> %d", resp.StatusCode)
	}

	// The per-book ZIP export was removed; the old URL must not fall back to
	// the SPA index.html.
	resp, err = http.Get(ts.URL + "/Images/zip/" + itoa64(bookID))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("removed zip download -> %d, want 404", resp.StatusCode)
	}

	// Nonexistent book
	resp, _ = http.Get(ts.URL + "/main/getBooks/getBookForm?selectedItemID=99999")
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("missing book -> %d, want 404", resp.StatusCode)
	}
}

// TestPPMDZIPEndpoints is an end-to-end smoke test for PPMd-compressed ZIP
// archives (ZIP method 98): download, cover and compact text all go through
// library.Open and must work without any external 7-Zip binary.
func TestPPMDZIPEndpoints(t *testing.T) {
	dir := t.TempDir()
	libDir := filepath.Join(dir, "lib")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(filepath.Join("..", "library", "testdata", "ppmd-books.zip"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(libDir, "books.zip"), fixture, 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	session, err := st.NewImport(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Add(&store.BookInput{
		Title: "PPMd Test Book", Folder: "books.zip", File: "1", Ext: "fb2",
		Size: 671, Lang: "ru", Added: "2024-01-01",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Finish(); err != nil {
		t.Fatal(err)
	}
	var bookID int64
	if err := st.DB().QueryRow(`SELECT id FROM books`).Scan(&bookID); err != nil {
		t.Fatal(err)
	}

	users, err := auth.Open(filepath.Join(dir, "users.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { users.Close() })
	dataDir := filepath.Join(dir, "data")
	cfg := &config.Config{LibraryDir: libDir, DataDir: dataDir, Auth: "public"}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	webFS := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html/>")}}
	srv := New(cfg, log, st, library.New(libDir), users, nil, webFS)
	ts := httptest.NewServer(srv.Handler)
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/Images/fb2/" + itoa64(bookID))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "PPMd-compressed FB2") {
		t.Fatalf("fb2 download: status=%d body=%q", resp.StatusCode, body)
	}

	resp, err = http.Get(ts.URL + "/Images/covers/" + itoa64(bookID))
	if err != nil {
		t.Fatal(err)
	}
	cover, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/png" ||
		!strings.HasPrefix(string(cover), "\x89PNG") {
		t.Fatalf("cover: status=%d type=%q bytes=%d", resp.StatusCode, resp.Header.Get("Content-Type"), len(cover))
	}
	cached, err := filepath.Glob(filepath.Join(dataDir, "cover-cache", "*", fmt.Sprintf("%d.*", bookID)))
	if err != nil || len(cached) != 1 {
		t.Fatalf("cover cache files = %v, %v", cached, err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "cover-cache.generation")); err != nil {
		t.Fatalf("cover cache generation: %v", err)
	}

	resp, err = http.Get(ts.URL + "/Images/fb2compact/" + itoa64(bookID))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("compact download: status=%d", resp.StatusCode)
	}
}

func itoa(v int) string { return itoa64(int64(v)) }

func itoa64(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}
