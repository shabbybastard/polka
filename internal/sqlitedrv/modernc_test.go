//go:build !android

package sqlitedrv

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestDSNPathForm(t *testing.T) {
	o := Options{WAL: true, SyncNormal: true, BusyTimeout: 10_000, ForeignKeys: true}

	// These must survive verbatim: a Windows drive-letter path and a UNC path
	// keep their backslashes as the URI path; url.URL-style building would
	// move them into the URI authority and SQLite would reject the DSN.
	for _, path := range []string{
		`C:\Users\u\AppData\polka\polka.db`,
		`\\server\share\polka.db`,
	} {
		if got := dsn(path, o); !strings.HasPrefix(got, "file:"+path+"?") {
			t.Errorf("dsn(%q) = %q, want prefix file:%q?", path, got, path)
		}
	}

	// URI-special characters are percent-escaped; every other byte stays.
	cases := map[string]string{
		"/home/u/.polka/polka.db": "file:/home/u/.polka/polka.db?",
		"/data/polk?/a.db":        "file:/data/polk%3F/a.db?",
		"/data/a#b.db":            "file:/data/a%23b.db?",
		"/data/100%.db":           "file:/data/100%25.db?",
		"/data/looks%2Fhexish.db": "file:/data/looks%252Fhexish.db?",
	}
	for path, want := range cases {
		if got := dsn(path, o); !strings.HasPrefix(got, want) {
			t.Errorf("dsn(%q) = %q, want prefix %q", path, got, want)
		}
	}
}

func TestOpenSpecialFilenames(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"pol?'1#.db", "with space.db", "100%.db", "looks%2Fhexish.db"} {
		path := filepath.Join(dir, name)
		db, err := Open(path, Options{WAL: true, SyncNormal: true, BusyTimeout: 1000})
		if err != nil {
			t.Fatalf("open %q: %v", name, err)
		}
		if _, err := db.ExecContext(context.Background(), `CREATE TABLE IF NOT EXISTS t (v TEXT)`); err != nil {
			t.Fatalf("create table in %q: %v", name, err)
		}
		if _, err := db.ExecContext(context.Background(), `INSERT INTO t (v) VALUES (?)`, "x"); err != nil {
			t.Fatalf("insert in %q: %v", name, err)
		}
		var v string
		if err := db.QueryRow(`SELECT v FROM t LIMIT 1`).Scan(&v); err != nil || v != "x" {
			t.Fatalf("query in %q: %q, %v", name, v, err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close %q: %v", name, err)
		}
	}
}
