package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const livelibHealthHTML = `<html><body>
<div class="object-wrapper">
  <a class="title" href="/book/1-dune">Дюна</a>
  <span itemprop="ratingValue">4.5</span>
</div>
</body></html>`

func TestSourcesCheckAdminOnly(t *testing.T) {
	f := newFullServer(t, "required")
	if got := code(t, f.reader, "POST", f.ts.URL+"/admin/sources/check", ""); got != http.StatusForbidden {
		t.Errorf("reader sources check -> %d, want 403", got)
	}
}

func TestSourcesCheckDisabled(t *testing.T) {
	f := newFullServer(t, "required")
	resp := postJSON(t, f.admin, f.ts.URL+"/admin/settings", map[string]any{
		"enrichment": map[string]bool{
			"livelib": false, "fantlab": false, "google_books": false, "open_library": false,
		},
		"similar": map[string]bool{"fantlab": false, "tastedive": false},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("disable sources -> %d", resp.StatusCode)
	}

	resp = postJSON(t, f.admin, f.ts.URL+"/admin/sources/check", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sources check -> %d", resp.StatusCode)
	}
	var body struct {
		CheckedAt string `json:"checkedAt"`
		Sources   []struct {
			Source string `json:"source"`
			State  string `json:"state"`
		} `json:"sources"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.CheckedAt == "" {
		t.Error("checkedAt is empty")
	}
	if len(body.Sources) != 5 {
		t.Fatalf("sources = %d, want 5", len(body.Sources))
	}
	for _, check := range body.Sources {
		if check.State != "disabled" {
			t.Errorf("%s = %q, want disabled", check.Source, check.State)
		}
	}
}

func TestSourcesCheckReportsOK(t *testing.T) {
	f := newFullServer(t, "required")

	ll := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(livelibHealthHTML))
	}))
	fl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"matches":[{"work_id":1}]}`))
	}))
	gb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"items":[{"volumeInfo":{}}]}`))
	}))
	ol := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"docs":[{"key":"/works/OL1W"}]}`))
	}))
	td := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"similar":{"results":[{"name":"Dune Messiah"}]}}`))
	}))
	t.Cleanup(ll.Close)
	t.Cleanup(fl.Close)
	t.Cleanup(gb.Close)
	t.Cleanup(ol.Close)
	t.Cleanup(td.Close)

	f.app.enrich.LiveLibBase = ll.URL
	f.app.enrich.FantLabBase = fl.URL
	f.app.enrich.GoogleBase = gb.URL
	f.app.enrich.OpenLibraryBase = ol.URL
	f.app.enrich.TasteDiveBase = td.URL

	resp := postJSON(t, f.admin, f.ts.URL+"/admin/settings", map[string]any{
		"enrichment": map[string]bool{
			"livelib": true, "fantlab": true, "google_books": true, "open_library": true,
		},
		"similar":        map[string]bool{"fantlab": true, "tastedive": true},
		"googleBooksKey": "google-test-key",
		"tastediveKey":   "tastedive-test-key",
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("enable sources -> %d", resp.StatusCode)
	}

	resp = postJSON(t, f.admin, f.ts.URL+"/admin/sources/check", nil)
	defer resp.Body.Close()
	var body struct {
		Sources []struct {
			Source string `json:"source"`
			State  string `json:"state"`
			Status int    `json:"status"`
		} `json:"sources"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Sources) != 5 {
		t.Fatalf("sources = %d, want 5", len(body.Sources))
	}
	for _, check := range body.Sources {
		if check.State != "ok" || check.Status != http.StatusOK {
			t.Errorf("%s = %+v, want ok/200", check.Source, check)
		}
	}
}

func TestRefreshBookEnrichment(t *testing.T) {
	f := newFullServer(t, "required")

	fl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/search-works"):
			w.Write([]byte(`{"matches":[{
			  "work_id":1,
			  "rusname":"Война и мир",
			  "name":"War and Peace",
			  "name_eng":"novel",
			  "work_type_id":1,
			  "work_type_name":"роман",
			  "autor_rusname":"Лев Толстой",
			  "all_autor_rusname":"Лев Толстой"
			}]}`))
		case r.URL.Path == "/work/1":
			w.Write([]byte(`{
			  "rating":{"rating":"8.03","true_rating":"7.95","voters":100},
			  "val_midmark_by_weight":8.03,
			  "val_voters":100,
			  "work_type_id":1,
			  "work_type_name":"novel"
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fl.Close)
	f.app.enrich.FantLabBase = fl.URL

	resp := postJSON(t, f.admin, f.ts.URL+"/admin/settings", map[string]any{
		"enrichment": map[string]bool{
			"livelib": false, "fantlab": true, "google_books": false, "open_library": false,
		},
		"similar": map[string]bool{"fantlab": false, "tastedive": false},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("enable fantlab -> %d", resp.StatusCode)
	}

	url := f.ts.URL + "/api/v1/books/" + itoa64(f.bookID) + "/refresh-enrichment"
	resp = postJSON(t, f.admin, url, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("refresh enrichment -> %d", resp.StatusCode)
	}
	var view struct {
		Primary *struct {
			Source string  `json:"source"`
			Rating float64 `json:"rating"`
		} `json:"primary"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	if view.Primary == nil || view.Primary.Source != "fantlab" || view.Primary.Rating != 4.015 {
		t.Fatalf("primary = %+v, want fantlab 4.015", view.Primary)
	}

	if got := code(t, f.reader, "POST", url, ""); got != http.StatusForbidden {
		t.Errorf("reader refresh enrichment -> %d, want 403", got)
	}
}
