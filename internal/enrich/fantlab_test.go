package enrich

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

const fantlabRatingSearch = `{"matches":[
  {"work_id":1,"rusname":"Дюна","name":"Dune","name_eng":"review","work_type_id":52,"all_autor_rusname":"Фрэнк Герберт"},
  {"work_id":4325,"rusname":"Дюна","name":"Dune","name_eng":"novel","work_type_id":1,"all_autor_rusname":"Фрэнк Герберт"},
  {"work_id":999,"rusname":"Дюна","name":"Dune","name_eng":"novel","work_type_id":1,"all_autor_rusname":"Стивен Кинг"}
]}`

const fantlabWork4325 = `{
  "rating":{"rating":"8.61","true_rating":"8.52","voters":9770},
  "val_midmark_by_weight":8.61,
  "val_voters":9770,
  "work_type_id":1,
  "work_type_name":"novel"
}`

func fantlabServer(t *testing.T, workBody string) (*httptest.Server, *int) {
	t.Helper()
	workHits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/search-works"):
			w.Write([]byte(fantlabRatingSearch))
		case r.URL.Path == "/work/4325":
			workHits++
			w.Write([]byte(workBody))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &workHits
}

func TestFantLabRating(t *testing.T) {
	fl, _ := fantlabServer(t, fantlabWork4325)
	p := New(filepath.Join(t.TempDir(), "cache.json"))
	p.FantLabBase = fl.URL

	res := p.Get(context.Background(), "fantlab-dune", "Дюна", "Фрэнк Герберт",
		map[string]bool{SourceFantLab: true}, nil)
	if res.Primary == nil || res.Primary.Source != SourceFantLab {
		t.Fatalf("primary = %+v", res.Primary)
	}
	got := res.Primary
	if got.Rating != 4.305 || got.RatingMax != 5 || got.RatingsCount != 9770 {
		t.Errorf("rating = %+v", got)
	}
	if got.InfoURL != "https://fantlab.ru/work4325" {
		t.Errorf("infoUrl = %q", got.InfoURL)
	}
}

func TestFantLabUsesDisplayedRating(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/search-works"):
			w.Write([]byte(`{"matches":[
			  {"work_id":1435,"rusname":"Колыбель для кошки","name":"Cat's Cradle","name_eng":"novel","work_type_id":1,"work_type_name":"роман","all_autor_rusname":"Курт Воннегут"}
			]}`))
		case r.URL.Path == "/work/1435":
			w.Write([]byte(`{
			  "rating":{"rating":"8.03","true_rating":"7.95","voters":2878},
			  "val_midmark_by_weight":8.03,
			  "val_midmark":8.06,
			  "val_voters":2878,
			  "work_type_id":1,
			  "work_type_name":"novel"
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	p := New(filepath.Join(t.TempDir(), "cache.json"))
	p.FantLabBase = srv.URL

	res := p.Get(context.Background(), "fantlab-cat", "Колыбель для кошки", "Курт Воннегут",
		map[string]bool{SourceFantLab: true}, nil)
	if res.Primary == nil || res.Primary.Source != SourceFantLab {
		t.Fatalf("primary = %+v", res.Primary)
	}
	if got := res.Primary.Rating; got != 4.015 {
		t.Errorf("rating = %v, want 4.015 (8.03/2)", got)
	}
}

func TestFantLabFallbackRating(t *testing.T) {
	fl, _ := fantlabServer(t, `{
		"rating":{"rating":"0","true_rating":"0","voters":0},
		"val_midmark_by_weight":8.52,
		"val_voters":123,
		"work_type_id":1,
		"work_type_name":"novel"
	}`)
	p := New(filepath.Join(t.TempDir(), "cache.json"))
	p.FantLabBase = fl.URL

	res := p.Get(context.Background(), "fantlab-fallback", "Дюна", "Фрэнк Герберт",
		map[string]bool{SourceFantLab: true}, nil)
	if res.Primary == nil || res.Primary.Rating != 4.26 || res.Primary.RatingsCount != 123 {
		t.Fatalf("fallback rating = %+v", res.Primary)
	}
}

func TestFantLabFallbackPrefersWeightedRating(t *testing.T) {
	fl, _ := fantlabServer(t, `{
		"rating":{"rating":"0","true_rating":"8.4","voters":10},
		"val_midmark_by_weight":7.9,
		"val_midmark":8.4,
		"val_voters":10,
		"work_type_id":1,
		"work_type_name":"novel"
	}`)
	p := New(filepath.Join(t.TempDir(), "cache.json"))
	p.FantLabBase = fl.URL

	res := p.Get(context.Background(), "fantlab-fallback-weight", "Дюна", "Фрэнк Герберт",
		map[string]bool{SourceFantLab: true}, nil)
	if res.Primary == nil || res.Primary.Rating != 3.95 {
		t.Fatalf("fallback rating = %+v, want 3.95 (7.9/2)", res.Primary)
	}
}

func TestFantLabWrongAuthor(t *testing.T) {
	fl, hits := fantlabServer(t, fantlabWork4325)
	p := New(filepath.Join(t.TempDir(), "cache.json"))
	p.FantLabBase = fl.URL

	res := p.Get(context.Background(), "fantlab-wrong-author", "Дюна", "Неизвестный Автор",
		map[string]bool{SourceFantLab: true}, nil)
	if !res.Negative || res.Primary != nil {
		t.Fatalf("wrong author result = %+v", res)
	}
	if *hits != 0 {
		t.Errorf("work endpoint was queried for a wrong author: %d", *hits)
	}
}

func TestFantLabZeroRating(t *testing.T) {
	fl, _ := fantlabServer(t, `{
		"rating":{"rating":"0","true_rating":"0","voters":10},
		"val_midmark_by_weight":0,
		"val_voters":10,
		"work_type_id":1,
		"work_type_name":"novel"
	}`)
	p := New(filepath.Join(t.TempDir(), "cache.json"))
	p.FantLabBase = fl.URL

	res := p.Get(context.Background(), "fantlab-zero", "Дюна", "Фрэнк Герберт",
		map[string]bool{SourceFantLab: true}, nil)
	if !res.Negative || res.Primary != nil {
		t.Fatalf("zero rating result = %+v", res)
	}
}

const fantlabVonnegutSearch = `{"matches":[
  {"work_id":1432,"rusname":"Сирены Титана","name":"The Sirens of Titan","name_eng":"novel","work_type_id":1,"work_type_name":"роман","all_autor_rusname":"Курт Воннегут","autor_rusname":"Курт Воннегут"},
  {"work_id":1086394,"rusname":"Сирены Титана","name":"","name_eng":"shortstory","work_type_id":45,"work_type_name":"рассказ","all_autor_rusname":"Антон Соловьёв","autor_rusname":"Антон Соловьёв"}
]}`

const fantlabWork1432 = `{
  "rating":{"rating":"8.04","true_rating":"8.17","voters":1894},
  "val_midmark_by_weight":8.12,
  "val_voters":1894,
  "work_type_id":1,
  "work_type_name":"роман"
}`

func fantlabVonnegutServer(t *testing.T, workHits *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/search-works"):
			w.Write([]byte(fantlabVonnegutSearch))
		case r.URL.Path == "/work/1432":
			if workHits != nil {
				*workHits++
			}
			w.Write([]byte(fantlabWork1432))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFantLabVonnegutSirens(t *testing.T) {
	hits := 0
	fl := fantlabVonnegutServer(t, &hits)
	p := New(filepath.Join(t.TempDir(), "cache.json"))
	p.FantLabBase = fl.URL

	res := p.Get(context.Background(), "fantlab-vonnegut", "Сирены Титана", "Воннегут Курт",
		map[string]bool{SourceFantLab: true}, nil)
	if res.Primary == nil || res.Primary.Source != SourceFantLab {
		t.Fatalf("primary = %+v", res.Primary)
	}
	if !strings.Contains(res.Primary.InfoURL, "1432") {
		t.Errorf("infoUrl = %q, want work 1432", res.Primary.InfoURL)
	}
	if hits != 1 {
		t.Errorf("work endpoint hits = %d, want 1", hits)
	}
}

func TestFantLabCacheInvalidation(t *testing.T) {
	fl, _ := fantlabServer(t, fantlabWork4325)
	p := newTestProvider(t, true, true, true)
	p.FantLabBase = fl.URL

	res := p.Get(context.Background(), "fantlab-cache", "Дюна", "Фрэнк Герберт",
		map[string]bool{SourceOpenLibrary: true}, nil)
	if len(res.Sources) != 1 || res.Sources[0].Source != SourceOpenLibrary {
		t.Fatalf("first result = %+v", res.Sources)
	}

	res = p.Get(context.Background(), "fantlab-cache", "Дюна", "Фрэнк Герберт",
		map[string]bool{SourceOpenLibrary: true, SourceFantLab: true}, nil)
	found := false
	for _, src := range res.Sources {
		if src.Source == SourceFantLab {
			found = true
		}
	}
	if !found {
		t.Fatalf("FantLab was not fetched after enabling: %+v", res.Sources)
	}
}
