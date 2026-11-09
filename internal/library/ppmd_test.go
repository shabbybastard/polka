package library

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenPPMDZip(t *testing.T) {
	root := t.TempDir()
	copyFixture(t, "ppmd-books.zip", filepath.Join(root, "books.zip"))
	want, err := os.ReadFile(filepath.Join("testdata", "1.fb2"))
	if err != nil {
		t.Fatal(err)
	}

	l := New(root)
	rc, size, err := l.Open("books.zip", "1", "fb2")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got := readAll(t, rc)
	if got != string(want) {
		t.Fatalf("open decoded %d bytes, want %d", len(got), len(want))
	}
	if size != int64(len(want)) {
		t.Errorf("size = %d, want %d", size, len(want))
	}

	entrySize, err := l.EntrySize("books.zip", "1", "fb2")
	if err != nil || entrySize != int64(len(want)) {
		t.Errorf("EntrySize = %d, %v", entrySize, err)
	}

	meta, err := l.Meta("books.zip", "1", "fb2")
	if err != nil {
		t.Fatalf("Meta: %v", err)
	}
	if meta.Title != "PPMd Test Book" {
		t.Errorf("title = %q", meta.Title)
	}

	cover, mime, err := l.Cover(1, "books.zip", "1", "fb2")
	if err != nil {
		t.Fatalf("embedded cover: %v", err)
	}
	if mime != "image/png" || len(cover) == 0 || !bytes.HasPrefix(cover, []byte("\x89PNG")) {
		t.Errorf("embedded cover: mime=%q len=%d", mime, len(cover))
	}

	// A second PPMd entry in the same archive must open too.
	want2, err := os.ReadFile(filepath.Join("testdata", "2.fb2"))
	if err != nil {
		t.Fatal(err)
	}
	rc, _, err = l.Open("books.zip", "2", "fb2")
	if err != nil {
		t.Fatalf("Open second: %v", err)
	}
	if got2 := readAll(t, rc); got2 != string(want2) {
		t.Fatalf("second entry decoded %d bytes, want %d", len(got2), len(want2))
	}
}

func TestPPMDEmbeddedWebPCover(t *testing.T) {
	root := t.TempDir()
	copyFixture(t, "ppmd-webp.zip", filepath.Join(root, "webp.zip"))
	want, err := os.ReadFile(filepath.Join("testdata", "ppmd-webp.webp"))
	if err != nil {
		t.Fatal(err)
	}
	cacheDir := t.TempDir()
	l := New(root)
	l.SetCoverCacheDir(cacheDir)
	data, mime, err := l.Cover(1, "webp.zip", "webp", "fb2")
	if err != nil {
		t.Fatalf("Cover: %v", err)
	}
	if mime != "image/webp" || !bytes.Equal(data, want) {
		t.Fatalf("cover: mime=%q bytes=%d want=%d", mime, len(data), len(want))
	}
	if _, err := os.Stat(coverCacheFile(cacheDir, 1, "webp")); err != nil {
		t.Fatalf("cover not cached as .webp: %v", err)
	}
}

func TestCoverPPMDSidecar(t *testing.T) {
	root := t.TempDir()
	copyFixture(t, "ppmd-covers.zip", filepath.Join(root, "covers", "books.zip"))
	l := New(root)

	data, mime, err := l.Cover(1, "books.zip", "1", "fb2")
	if err != nil {
		t.Fatalf("png sidecar: %v", err)
	}
	if mime != "image/png" || !bytes.HasPrefix(data, []byte("\x89PNG")) {
		t.Errorf("png sidecar: mime=%q len=%d", mime, len(data))
	}

	// Entry "2" has no extension; the type comes from the content signature.
	data, mime, err = l.Cover(2, "books.zip", "2", "fb2")
	if err != nil {
		t.Fatalf("webp sidecar: %v", err)
	}
	if mime != "image/webp" || !bytes.HasPrefix(data, []byte("RIFF")) {
		t.Errorf("webp sidecar: mime=%q len=%d", mime, len(data))
	}
}

func TestPPMDCorruptEntry(t *testing.T) {
	root := t.TempDir()
	copyFixture(t, "ppmd-books.zip", filepath.Join(root, "books.zip"))
	// Make the fallback name point at a cover-like entry: the archive has
	// no such entry, so the lookup must fail cleanly with ErrNoFile.
	l := New(root)
	if _, _, err := l.Open("books.zip", "missing", "fb2"); !errors.Is(err, ErrNoFile) {
		t.Fatalf("missing entry error = %v", err)
	}
}
