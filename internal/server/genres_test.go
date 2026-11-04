package server

import (
	"net/http"
	"net/url"
	"testing"
)

func TestGenreSyncRequiresSource(t *testing.T) {
	f := newFullServer(t, "required")

	if got := code(t, f.admin, "POST", f.ts.URL+"/admin/genres/sync", ""); got != http.StatusConflict {
		t.Errorf("sync without sources -> %d, want 409", got)
	}
	if got := code(t, f.admin, "POST", f.ts.URL+"/admin/genres/sync?mode=empty", ""); got != http.StatusConflict {
		t.Errorf("empty sync without sources -> %d, want 409", got)
	}
	if got := code(t, f.admin, "POST", f.ts.URL+"/admin/genres/sync?mode=bogus", ""); got != http.StatusBadRequest {
		t.Errorf("invalid mode -> %d, want 400", got)
	}
	status := getJSONWith(t, f.admin, f.ts.URL+"/admin/genres/status")
	if status["running"] != false {
		t.Errorf("status: %v", status)
	}
}

func TestExternalGenresInCatalog(t *testing.T) {
	f := newFullServer(t, "required")

	resp := postJSON(t, f.admin, f.ts.URL+"/admin/settings", map[string]any{
		"tagSources": map[string]bool{"livelib": true, "open_library": false},
	})
	var settings map[string]any
	decode(t, resp, &settings)
	sources := settings["tagSources"].(map[string]any)
	if sources["livelib"] != true || sources["open_library"] != false {
		t.Fatalf("tag sources: %v", sources)
	}

	if err := f.st.SaveBookGenres(t.Context(), f.bookID, "livelib", []string{"Классика", "Роман"}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SaveBookGenres(t.Context(), f.bookID2, "livelib", []string{"Классика"}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SaveBookGenres(t.Context(), f.bookID, "open_library", []string{"Classics"}); err != nil {
		t.Fatal(err)
	}
	var code string
	if err := f.st.DB().QueryRow(
		`SELECT code FROM genres WHERE name = 'Классика' AND source = 'livelib'`).Scan(&code); err != nil {
		t.Fatal(err)
	}

	genres := getJSONWith(t, f.admin, f.ts.URL+"/main/getBooks/getSearchGenres?search="+url.QueryEscape("классика"))
	list := genres["genresList"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["GenreName"] != "Классика" ||
		list[0].(map[string]any)["Books"].(float64) != 2 {
		t.Errorf("external genre search: %v", list)
	}

	shelf := getJSONWith(t, f.admin,
		f.ts.URL+"/main/getBooks/getShelfBooks?shelfId="+url.QueryEscape("genre_"+code))
	if books := shelf["titlesList"].([]any); len(books) != 2 || shelf["title"] != "Классика" {
		t.Errorf("external genre shelf: %v / %v", shelf["title"], books)
	}

	form := getJSONWith(t, f.admin, f.ts.URL+"/main/getBooks/getBookForm?selectedItemID="+itoa64(f.bookID))
	names := map[string]bool{}
	for _, raw := range form["genresList"].([]any) {
		names[raw.(map[string]any)["name"].(string)] = true
	}
	if !names["Классика"] || !names["Роман"] {
		t.Errorf("book form genres: %v", form["genresList"])
	}

	// Switching LiveLib off removes its genres, while other external and
	// native genres stay.
	resp = postJSON(t, f.admin, f.ts.URL+"/admin/settings", map[string]any{
		"tagSources": map[string]bool{"livelib": false},
	})
	resp.Body.Close()
	var n int
	f.st.DB().QueryRow(`SELECT count(*) FROM genres WHERE source = 'livelib'`).Scan(&n)
	if n != 0 {
		t.Errorf("livelib genres left after disable: %d", n)
	}
	f.st.DB().QueryRow(`SELECT count(*) FROM genres WHERE source = 'open_library'`).Scan(&n)
	if n != 1 {
		t.Errorf("open library genres were removed too: %d", n)
	}
	genres = getJSONWith(t, f.admin, f.ts.URL+"/main/getBooks/getSearchGenres?search="+url.QueryEscape("классика"))
	if list := genres["genresList"].([]any); len(list) != 0 {
		t.Errorf("disabled source still searchable: %v", list)
	}
}
