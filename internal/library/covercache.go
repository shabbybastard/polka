package library

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Disk cover cache layout:
//
//	<data-dir>/cover-cache/<shard>/<book-id>.<ext>
//	<data-dir>/cover-cache/<shard>/<book-id>.none
//
// The shard is the low byte of the book id, keeping directories small even
// for very large catalogs. A ".none" marker remembers books without a cover
// so a restart does not unpack the same PPMd book again.
var coverDiskExts = []string{"jpg", "png", "gif", "webp", "bmp", "img"}

// SetCoverCacheDir enables the persistent cover cache. Call before serving
// requests; an empty directory disables it (used by tests).
func (l *Library) SetCoverCacheDir(dir string) {
	if l == nil {
		return
	}
	l.coverCacheDir = dir
}

func (l *Library) coverCacheShard(bookID int64) (dir, base string) {
	if l == nil || l.coverCacheDir == "" {
		return "", ""
	}
	shard := fmt.Sprintf("%02x", uint64(bookID)&0xff)
	return filepath.Join(l.coverCacheDir, shard), strconv.FormatInt(bookID, 10)
}

func (l *Library) coverLock(bookID int64) *sync.Mutex {
	return &l.coverLocks[uint64(bookID)%uint64(len(l.coverLocks))]
}

func coverCacheExt(mime string, data []byte) string {
	m := strings.ToLower(strings.TrimSpace(mime))
	if m == "" || m == "application/octet-stream" {
		m = sniffImageMime(data)
	}
	switch m {
	case "image/jpeg", "image/jpg":
		return "jpg"
	case "image/png":
		return "png"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	case "image/bmp":
		return "bmp"
	}
	return "img"
}

// loadDiskCover returns a cached cover, a negative marker, or neither.
func (l *Library) loadDiskCover(bookID int64) (data []byte, mime string, found, negative bool) {
	dir, base := l.coverCacheShard(bookID)
	if dir == "" {
		return nil, "", false, false
	}
	for _, ext := range coverDiskExts {
		path := filepath.Join(dir, base+"."+ext)
		fi, err := os.Stat(path)
		if err != nil || fi.Size() == 0 || fi.Size() > maxCoverSize {
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil || len(b) == 0 {
			continue
		}
		// Mark the entry as used so age-based cleanup measures inactivity,
		// not the date the cover was first extracted.
		now := time.Now()
		_ = os.Chtimes(path, now, now)
		m := coverImageMime["."+ext]
		if m == "" {
			m = sniffImageMime(b)
		}
		return b, m, true, false
	}
	nonePath := filepath.Join(dir, base+".none")
	if _, err := os.Stat(nonePath); err == nil {
		now := time.Now()
		_ = os.Chtimes(nonePath, now, now)
		return nil, "", true, true
	}
	return nil, "", false, false
}

// saveDiskCover writes a cover atomically and drops stale variants/markers.
func (l *Library) saveDiskCover(bookID int64, data []byte, mime string) {
	dir, base := l.coverCacheShard(bookID)
	if dir == "" || len(data) == 0 || len(data) > maxCoverSize {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	ext := coverCacheExt(mime, data)
	if err := writeFileAtomic(filepath.Join(dir, base+"."+ext), data); err != nil {
		return
	}
	l.removeDiskCoverFiles(dir, base, ext)
}

// saveNoCover remembers that a book has no cover.
func (l *Library) saveNoCover(bookID int64) {
	dir, base := l.coverCacheShard(bookID)
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	if err := writeFileAtomic(filepath.Join(dir, base+".none"), nil); err != nil {
		return
	}
	l.removeDiskCoverFiles(dir, base, "none")
}

// removeDiskCoverFiles deletes every cached representation except keepExt.
func (l *Library) removeDiskCoverFiles(dir, base, keepExt string) {
	for _, ext := range coverDiskExts {
		if ext != keepExt {
			os.Remove(filepath.Join(dir, base+"."+ext))
		}
	}
	if keepExt != "none" {
		os.Remove(filepath.Join(dir, base+".none"))
	}
}

func (l *Library) removeDiskCover(bookID int64) {
	dir, base := l.coverCacheShard(bookID)
	if dir == "" {
		return
	}
	l.removeDiskCoverFiles(dir, base, "")
}

func (l *Library) clearDiskCovers() {
	if l == nil || l.coverCacheDir == "" {
		return
	}
	os.RemoveAll(l.coverCacheDir)
}

// writeFileAtomic writes data to target via a temporary file in the same
// directory. On Windows rename over an existing file is not atomic, so the
// destination is removed first (callers hold the per-book lock).
func writeFileAtomic(target string, data []byte) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".cover-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, target); err != nil {
		if removeErr := os.Remove(target); removeErr != nil && !os.IsNotExist(removeErr) {
			return err
		}
		return os.Rename(name, target)
	}
	return nil
}
