package server

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shabbybastard/polka/internal/config"
)

func TestCleanupCoverCacheAge(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, coverCacheDirName)
	write := func(rel string) string {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	oldCover := write("01/1.png")
	oldNone := write("01/2.none")
	freshCover := write("02/3.webp")
	old := time.Now().Add(-15 * 24 * time.Hour)
	for _, path := range []string{oldCover, oldNone} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}

	s := &Server{
		cfg: &config.Config{DataDir: base},
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	s.cleanupCoverCache()

	for _, path := range []string{oldCover, oldNone} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("old cache file survived: %s (%v)", path, err)
		}
	}
	if _, err := os.Stat(freshCover); err != nil {
		t.Errorf("fresh cover removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "01")); !os.IsNotExist(err) {
		t.Errorf("empty shard directory survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "02")); err != nil {
		t.Errorf("non-empty shard directory removed: %v", err)
	}
}

func TestPrepareCoverCacheDirGeneration(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "cover-cache")

	if err := prepareCoverCacheDir(dir, 7); err != nil {
		t.Fatal(err)
	}
	cached := filepath.Join(dir, "01", "1.png")
	if err := os.MkdirAll(filepath.Dir(cached), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cached, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Same generation keeps the cache.
	if err := prepareCoverCacheDir(dir, 7); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cached); err != nil {
		t.Fatalf("cache cleared for the same generation: %v", err)
	}

	// A re-import remaps book ids, so the old covers must go.
	if err := prepareCoverCacheDir(dir, 8); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cached); !os.IsNotExist(err) {
		t.Fatalf("stale cover survived generation change: %v", err)
	}
	if b, err := os.ReadFile(dir + ".generation"); err != nil || string(b) != "8" {
		t.Fatalf("generation file = %q, %v", b, err)
	}
}
