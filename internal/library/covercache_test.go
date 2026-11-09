package library

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func coverCacheFile(cacheDir string, bookID int64, ext string) string {
	return filepath.Join(cacheDir, fmt.Sprintf("%02x", uint64(bookID)&0xff), fmt.Sprintf("%d.%s", bookID, ext))
}

func TestCoverDiskCachePersists(t *testing.T) {
	root := t.TempDir()
	writeZip(t, filepath.Join(root, "a.zip"), map[string][]byte{"1.fb2": []byte(testFB2)})
	cacheDir := t.TempDir()

	l := New(root)
	l.SetCoverCacheDir(cacheDir)
	data, mime, err := l.Cover(1, "a.zip", "1", "fb2")
	if err != nil || mime != "image/png" || !bytes.Equal(data, pngBytes) {
		t.Fatalf("cover: %d %q %v", len(data), mime, err)
	}
	if _, err := os.Stat(coverCacheFile(cacheDir, 1, "png")); err != nil {
		t.Fatalf("cover not saved to disk: %v", err)
	}

	// A new library with an empty book root must serve the cover from disk
	// without trying to unpack the (now missing) archive.
	empty := New(t.TempDir())
	empty.SetCoverCacheDir(cacheDir)
	data, mime, err = empty.Cover(1, "a.zip", "1", "fb2")
	if err != nil || mime != "image/png" || !bytes.Equal(data, pngBytes) {
		t.Fatalf("cached cover: %d %q %v", len(data), mime, err)
	}
}

func TestCoverDiskCacheNegative(t *testing.T) {
	root := t.TempDir()
	noCover := `<?xml version="1.0" encoding="UTF-8"?><FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0">
<description><title-info><book-title>Без обложки</book-title></title-info></description><body><section><p>Текст.</p></section></body></FictionBook>`
	writeZip(t, filepath.Join(root, "a.zip"), map[string][]byte{"2.fb2": []byte(noCover)})
	cacheDir := t.TempDir()

	l := New(root)
	l.SetCoverCacheDir(cacheDir)
	if _, _, err := l.Cover(2, "a.zip", "2", "fb2"); !errors.Is(err, ErrNoFile) {
		t.Fatalf("no cover: %v", err)
	}
	if _, err := os.Stat(coverCacheFile(cacheDir, 2, "none")); err != nil {
		t.Fatalf("negative marker not saved: %v", err)
	}

	// The marker makes a fresh process return immediately, even without the
	// book archive.
	fresh := New(t.TempDir())
	fresh.SetCoverCacheDir(cacheDir)
	if _, _, err := fresh.Cover(2, "missing.zip", "2", "fb2"); !errors.Is(err, ErrNoFile) {
		t.Fatalf("cached negative: %v", err)
	}
}

func TestCoverCacheInvalidation(t *testing.T) {
	root := t.TempDir()
	writeZip(t, filepath.Join(root, "a.zip"), map[string][]byte{"1.fb2": []byte(testFB2)})
	cacheDir := t.TempDir()
	l := New(root)
	l.SetCoverCacheDir(cacheDir)
	if _, _, err := l.Cover(1, "a.zip", "1", "fb2"); err != nil {
		t.Fatal(err)
	}

	l.ForgetCover(1)
	if _, err := os.Stat(coverCacheFile(cacheDir, 1, "png")); !os.IsNotExist(err) {
		t.Fatalf("disk cover survived ForgetCover: %v", err)
	}

	if _, _, err := l.Cover(1, "a.zip", "1", "fb2"); err != nil {
		t.Fatalf("re-extract after ForgetCover: %v", err)
	}
	l.ForgetAllCovers()
	if _, err := os.Stat(cacheDir); !os.IsNotExist(err) {
		t.Fatalf("cover cache directory survived ForgetAllCovers: %v", err)
	}
}

func TestCoverDiskCacheWebP(t *testing.T) {
	root := t.TempDir()
	// A sidecar cover with a WebP signature; the extension must be .webp.
	webp := append([]byte("RIFF\x24\x00\x00\x00WEBPVP8 "), make([]byte, 16)...)
	writeZip(t, filepath.Join(root, "covers", "a.zip"), map[string][]byte{"1": webp})
	cacheDir := t.TempDir()
	l := New(root)
	l.SetCoverCacheDir(cacheDir)
	data, mime, err := l.Cover(1, "a.zip", "1", "fb2")
	if err != nil || mime != "image/webp" || !bytes.Equal(data, webp) {
		t.Fatalf("webp cover: %d %q %v", len(data), mime, err)
	}
	if _, err := os.Stat(coverCacheFile(cacheDir, 1, "webp")); err != nil {
		t.Fatalf("webp not saved with .webp: %v", err)
	}
}

func TestCoverDiskCacheTouchOnRead(t *testing.T) {
	cacheDir := t.TempDir()
	path := coverCacheFile(cacheDir, 1, "png")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pngBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-20 * 24 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	l := New(t.TempDir())
	l.SetCoverCacheDir(cacheDir)
	if _, _, err := l.Cover(1, "missing.zip", "1", "fb2"); err != nil {
		t.Fatalf("disk hit: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(fi.ModTime()) > time.Minute {
		t.Fatalf("mtime was not refreshed: %v", fi.ModTime())
	}
}

func TestEmbeddedWebPCover(t *testing.T) {
	webp := append([]byte("RIFF\x24\x00\x00\x00WEBPVP8 "), make([]byte, 16)...)
	b64 := base64.StdEncoding.EncodeToString(webp)
	// The optimized Flibusta distribution stores 500px WebP covers as
	// <binary> inside the FB2. Test both a declared content-type and the
	// fallback sniff when the attribute is missing.
	mkFB2 := func(ctype string) string {
		attr := ""
		if ctype != "" {
			attr = ` content-type="` + ctype + `"`
		}
		return `<?xml version="1.0" encoding="UTF-8"?>
<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0" xmlns:l="http://www.w3.org/1999/xlink">
<description><title-info><book-title>WebP</book-title><coverpage><image l:href="#c.webp"/></coverpage></title-info></description>
<body><section><p>Text</p></section></body>
<binary id="c.webp"` + attr + `>` + b64 + `</binary>
</FictionBook>`
	}
	root := t.TempDir()
	writeZip(t, filepath.Join(root, "a.zip"), map[string][]byte{
		"1.fb2": []byte(mkFB2("image/webp")),
		"2.fb2": []byte(mkFB2("")),
	})
	cacheDir := t.TempDir()
	l := New(root)
	l.SetCoverCacheDir(cacheDir)
	for _, id := range []int64{1, 2} {
		data, mime, err := l.Cover(id, "a.zip", fmt.Sprint(id), "fb2")
		if err != nil || mime != "image/webp" || !bytes.Equal(data, webp) {
			t.Fatalf("embedded webp %d: %d %q %v", id, len(data), mime, err)
		}
		if _, err := os.Stat(coverCacheFile(cacheDir, id, "webp")); err != nil {
			t.Fatalf("embedded webp %d not saved with .webp: %v", id, err)
		}
	}
}
