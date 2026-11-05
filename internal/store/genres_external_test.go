package store

import (
	"context"
	"strings"
	"testing"
)

func TestExternalGenres(t *testing.T) {
	st, ids := newLibrary(t)
	ctx := context.Background()
	first := ids["Вишневый сад"]
	second := ids["Анна Каренина"]

	if err := st.SaveBookGenres(ctx, first, "livelib", []string{"Классика", "Роман"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveBookGenres(ctx, second, "livelib", []string{"Классика"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveBookGenres(ctx, first, "open_library", []string{"Russian fiction", "Classics"}); err != nil {
		t.Fatal(err)
	}

	counts, err := st.GenresWithCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]GenreCount{}
	for _, g := range counts {
		if g.Source == "livelib" {
			byName[g.Name] = g
		}
	}
	if len(byName) != 2 || byName["Классика"].Books != 2 || byName["Роман"].Books != 1 {
		t.Errorf("external genre counts: %+v", byName)
	}

	// They are regular genres for shelving and book cards.
	books, title, err := st.ShelfBooks(ctx, "genre_"+byName["Классика"].Code, 10, 0)
	if err != nil || len(books) != 2 || title != "Классика" {
		t.Errorf("external genre shelf: %v %q %v", titles(books), title, err)
	}

	// A book's card exposes both native and external genre names.
	details, err := st.BookDetails(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if details.GenreNames["prose_classic"] != "" {
		t.Errorf("native genre should not have an external name: %+v", details.GenreNames)
	}
	if details.GenreNames[byName["Классика"].Code] != "Классика" {
		t.Errorf("external genre name: %+v", details.GenreNames)
	}

	// Disabling a source removes its genres, but not the native ones.
	if err := st.DeleteBookGenresBySource(ctx, "livelib"); err != nil {
		t.Fatal(err)
	}
	if books, _, _ = st.ShelfBooks(ctx, "genre_"+byName["Классика"].Code, 10, 0); len(books) != 0 {
		t.Errorf("external genre survived deletion: %v", titles(books))
	}
	if counts, _ = st.GenresWithCounts(ctx); hasGenreSource(counts, "livelib") {
		t.Error("livelib genres survived deletion")
	}
	if counts, _ = st.GenresWithCounts(ctx); !hasGenreSource(counts, "open_library") {
		t.Error("other external source was removed")
	}
	if counts, _ = st.GenresWithCounts(ctx); !hasGenreSource(counts, "") {
		t.Error("native genres were removed")
	}
}

func TestExternalGenresSkipNativeDuplicates(t *testing.T) {
	st, ids := newLibrary(t)
	ctx := context.Background()
	bookID := ids["Вишневый сад"] // native genre: dramaturgy

	if err := st.SaveBookGenres(ctx, bookID, "livelib", []string{
		"Драматургия", "Drama", "Роман", "роман!",
	}); err != nil {
		t.Fatal(err)
	}

	counts, err := st.GenresWithCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var external []string
	for _, g := range counts {
		if g.Source == "livelib" {
			external = append(external, g.Name)
		}
	}
	if len(external) != 1 || external[0] != "Роман" {
		t.Errorf("external genres = %v, want only Роман", external)
	}

	details, err := st.BookDetails(ctx, bookID)
	if err != nil {
		t.Fatal(err)
	}
	hasNative, hasRomance := false, false
	for _, code := range details.Genres {
		if code == "dramaturgy" {
			hasNative = true
		}
		if strings.HasPrefix(code, "ext_livelib_") {
			hasRomance = true
		}
	}
	if !hasNative || !hasRomance {
		t.Errorf("book genres = %v, native=%v romance=%v", details.Genres, hasNative, hasRomance)
	}
}

func TestBooksWithoutGenres(t *testing.T) {
	st, _ := newLibrary(t)
	ctx := context.Background()
	id, err := st.AddBook(ctx, &BookInput{
		Title: "No genres", Authors: []AuthorName{{Last: "Author"}},
		Folder: "one.zip", File: "empty", Ext: "fb2",
	})
	if err != nil {
		t.Fatal(err)
	}
	n, err := st.BooksWithoutGenresCount(ctx)
	if err != nil || n != 1 {
		t.Fatalf("empty books count = %d, %v", n, err)
	}
	books, err := st.BooksForEnrichment(ctx, 0, 10, true)
	if err != nil || len(books) != 1 || books[0].ID != id {
		t.Fatalf("empty books: %v, %v", books, err)
	}
}

func hasGenreSource(counts []GenreCount, source string) bool {
	for _, g := range counts {
		if g.Source == source {
			return true
		}
	}
	return false
}
