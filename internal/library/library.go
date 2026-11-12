// Package library — access to book files: archives (zip/7z) and folders
// inside the library root, plus cover and FB2 metadata extraction.
package library

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bodgit/sevenzip"
	"github.com/hashicorp/golang-lru/v2"
)

var (
	ErrNoFile      = errors.New("book file not found")
	ErrNoCover     = errors.New("cover not found")
	ErrNotBareFile = errors.New("editing is only supported for a bare EPUB file")
)

// safeArchiveName rejects names that could escape the archive root.
func safeArchiveName(name string) bool {
	if strings.Contains(name, "\\") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

const (
	maxArchiveEntrySize = 256 << 20
	maxCoverSize        = 16 << 20
)

// readLimited reads at most max bytes and reports an error when the input is
// larger. A plain io.LimitReader would silently truncate the data and could
// produce a corrupt EPUB or a broken image.
func readLimited(r io.Reader, max int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("input exceeds %d bytes", max)
	}
	return data, nil
}

type Library struct {
	root string

	covers *lru.Cache[int64, coverEntry] // bounded cover cache
}

type coverEntry struct {
	data []byte
	mime string
}

const (
	coverCacheSize     = 128
	maxCachedCoverSize = 2 << 20
)

func New(root string) *Library {
	if root == "" {
		return nil
	}
	cache, _ := lru.New[int64, coverEntry](coverCacheSize)
	return &Library{root: root, covers: cache}
}

// Health describes the state of the library root: whether it exists and how
// many top-level entries it has. Safe to call on a nil Library.
type Health struct {
	Root    string `json:"root"`
	Exists  bool   `json:"exists"`
	Entries int    `json:"entries"` // top-level entries (0 = empty)
}

func (l *Library) Health() Health {
	if l == nil {
		return Health{}
	}
	h := Health{Root: l.root}
	entries, err := os.ReadDir(l.root)
	if err != nil {
		return h
	}
	h.Exists = true
	h.Entries = len(entries)
	return h
}

// HasArchive reports whether the archive (or folder) for a catalog folder
// reference exists on disk under the library root. Used to verify that the
// collection actually lives where the server looks for it.
func (l *Library) HasArchive(folder string) bool {
	if l == nil || !pathInside(l.root, folder) {
		return false
	}
	path := filepath.Join(l.root, filepath.FromSlash(folder))
	for _, ap := range archiveCandidates(path) {
		if _, err := os.Stat(ap); err == nil {
			return true
		}
	}
	return false
}

// Open returns the contents of a book file: folder is an archive or a
// subfolder in the library root, the file name is file + "." + ext.
func (l *Library) Open(folder, file, ext string) (io.ReadCloser, int64, error) {
	name := file + "." + ext
	// Stay inside the root: folder/file come from catalog metadata
	// (including an imported inpx), so "../" must not let anything read
	// files outside the collection.
	if !pathInside(l.root, folder) || strings.ContainsAny(name, `/\`) {
		return nil, 0, fmt.Errorf("%w: %s/%s", ErrNoFile, folder, name)
	}
	path := filepath.Join(l.root, filepath.FromSlash(folder))

	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		f, err := os.Open(filepath.Join(path, name))
		if err != nil {
			return nil, 0, fmt.Errorf("%w: %s/%s", ErrNoFile, folder, name)
		}
		fi, err := f.Stat()
		if err != nil {
			f.Close()
			return nil, 0, err
		}
		return f, fi.Size(), nil
	}

	// Book archive. inpx often names the archive .zip while the file on
	// disk is .7z (Flibusta dumps), so we try both extensions.
	var tried []string
	for _, ap := range archiveCandidates(path) {
		rc, size, err := openFromArchive(ap, name)
		if err == nil {
			if strings.EqualFold(ext, "epub") {
				return unwrapEPUB(rc, size) // Flibusta dumps pack epub as 7z
			}
			return rc, size, nil
		}
		tried = append(tried, filepath.Base(ap)+": "+err.Error())
	}
	return nil, 0, fmt.Errorf("%w: %q — %s", ErrNoFile, name, strings.Join(tried, "; "))
}

// EntrySize returns the uncompressed size of a book entry without reading
// its data. It lets callers reject an oversized attachment before the EPUB
// unwrap path buffers a 7z-packed book.
func (l *Library) EntrySize(folder, file, ext string) (int64, error) {
	name := file + "." + ext
	if !pathInside(l.root, folder) || strings.ContainsAny(name, `/\`) {
		return 0, fmt.Errorf("%w: %s/%s", ErrNoFile, folder, name)
	}
	path := filepath.Join(l.root, filepath.FromSlash(folder))
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		fi, err := os.Stat(filepath.Join(path, name))
		if err != nil {
			return 0, fmt.Errorf("%w: %s/%s", ErrNoFile, folder, name)
		}
		return fi.Size(), nil
	}
	var tried []string
	for _, ap := range archiveCandidates(path) {
		size, err := archiveEntrySize(ap, name)
		if err == nil {
			return size, nil
		}
		tried = append(tried, filepath.Base(ap)+": "+err.Error())
	}
	return 0, fmt.Errorf("%w: %q — %s", ErrNoFile, name, strings.Join(tried, "; "))
}

func archiveEntrySize(path, name string) (int64, error) {
	if _, err := os.Stat(path); err != nil {
		return 0, fmt.Errorf("not on disk")
	}
	stem := entryStem(name)
	if strings.EqualFold(filepath.Ext(path), ".7z") {
		zr, err := sevenzip.OpenReader(path)
		if err != nil {
			return 0, fmt.Errorf("not a valid 7z: %v", err)
		}
		defer zr.Close()
		for _, entry := range zr.File {
			if strings.EqualFold(entry.Name, name) || strings.EqualFold(entry.Name, stem) {
				return int64(entry.UncompressedSize), nil
			}
		}
		return 0, fmt.Errorf("entry %q not found", name)
	}
	zr, err := zip.OpenReader(path)
	if err != nil {
		return 0, fmt.Errorf("not a valid zip: %v", err)
	}
	defer zr.Close()
	for _, entry := range zr.File {
		if strings.EqualFold(entry.Name, name) || strings.EqualFold(entry.Name, stem) {
			return int64(entry.UncompressedSize64), nil
		}
	}
	return 0, fmt.Errorf("entry %q not found", name)
}

// unwrapEPUB normalizes an epub: in Flibusta dumps the .epub file is itself
// a 7z archive (the epub files live under a subfolder). We repack it into a
// regular zip-epub so everything downstream (reading, cover, download) works
// like a normal epub. A regular zip-epub is returned as is.
type prefixReadCloser struct {
	io.Reader
	closer io.Closer
}

func (p *prefixReadCloser) Close() error { return p.closer.Close() }

func unwrapEPUB(rc io.ReadCloser, size int64) (io.ReadCloser, int64, error) {
	header := make([]byte, 6)
	n, err := io.ReadFull(rc, header)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		rc.Close()
		return nil, 0, err
	}
	// A normal zip-epub is returned without buffering the whole file. Only
	// the Flibusta 7z-wrapped variant needs a full read and repack.
	if n >= 4 && header[0] == 0x37 && header[1] == 0x7A && header[2] == 0xBC && header[3] == 0xAF {
		data, err := readLimited(io.MultiReader(bytes.NewReader(header[:n]), rc), maxArchiveEntrySize)
		rc.Close()
		if err != nil {
			return nil, 0, err
		}
		repacked, err := sevenZToEpub(data)
		if err != nil {
			return nil, 0, fmt.Errorf("unwrap epub 7z: %w", err)
		}
		return io.NopCloser(bytes.NewReader(repacked)), int64(len(repacked)), nil
	}
	return &prefixReadCloser{
		Reader: io.MultiReader(bytes.NewReader(header[:n]), rc),
		closer: rc,
	}, size, nil
}

// sevenZToEpub repacks a 7z-packed epub into a standard zip-epub, stripping
// the common folder prefix and writing mimetype first (uncompressed).
func sevenZToEpub(data []byte) ([]byte, error) {
	zr, err := sevenzip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}

	// Common prefix: files live under "<book name>/...". Compute it only
	// from entries that contain a slash — the bare folder entry
	// "<book name>" without a slash would otherwise reset the prefix to
	// empty (sevenzip does not always flag it as a directory).
	prefix := ""
	prefixSet := false
	for _, e := range zr.File {
		i := strings.IndexByte(e.Name, '/')
		if i < 0 {
			continue
		}
		p := e.Name[:i+1]
		if !prefixSet {
			prefix, prefixSet = p, true
		} else if p != prefix {
			prefix = ""
			break
		}
	}

	type entry struct {
		name string
		data []byte
	}
	var entries []entry
	for _, e := range zr.File {
		// The bare folder entry (the prefix without a slash) is dropped here.
		if prefix != "" && !strings.HasPrefix(e.Name, prefix) {
			continue
		}
		name := strings.TrimPrefix(e.Name, prefix)
		if name == "" || strings.HasSuffix(name, "/") || !safeArchiveName(name) {
			continue
		}
		erc, err := e.Open()
		if err != nil {
			return nil, err
		}
		b, err := readLimited(erc, maxArchiveEntrySize)
		erc.Close()
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry{name, b})
	}

	// mimetype must come first and uncompressed, otherwise the epub is invalid.
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].name == "mimetype" && entries[j].name != "mimetype"
	})

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		hdr := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.name == "mimetype" {
			hdr.Method = zip.Store
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(e.data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// archiveCandidates returns possible archive paths: the path itself and the
// variant with .zip↔.7z swapped (in case inpx and disk disagree).
func archiveCandidates(path string) []string {
	out := []string{path}
	base := path
	if e := strings.ToLower(filepath.Ext(path)); e == ".zip" || e == ".7z" {
		base = strings.TrimSuffix(path, filepath.Ext(path))
	}
	// Always try both extensions: inpx may name a .zip, a .7z or even a
	// name with no extension, while the file on disk is the other variant.
	for _, e := range []string{".7z", ".zip"} {
		if c := base + e; c != path {
			out = append(out, c)
		}
	}
	return out
}

// entryStem drops the extension: "794546.fb2" → "794546".
func entryStem(name string) string {
	return strings.TrimSuffix(name, filepath.Ext(name))
}

// openFromArchive extracts file name from archive path (zip or 7z).
func openFromArchive(path, name string) (io.ReadCloser, int64, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, 0, fmt.Errorf("not on disk")
	}
	if strings.EqualFold(filepath.Ext(path), ".7z") {
		return open7z(path, name)
	}
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, 0, fmt.Errorf("not a valid zip: %v", err)
	}
	// Exact match takes priority; the bare id (an extensionless entry) is a
	// fallback for optimized Flibusta dumps, where books are named by id
	// without ".fb2". Matching against the stem (not just any prefix) keeps
	// a cover "<id>.jpg" from being mistaken for the book.
	stem := entryStem(name)
	var cand *zip.File
	names := make([]string, 0, len(zr.File))
	for _, entry := range zr.File {
		if strings.EqualFold(entry.Name, name) {
			rc, err := entry.Open()
			if err != nil {
				zr.Close()
				return nil, 0, err
			}
			return &zipEntryReader{rc: rc, zr: zr}, int64(entry.UncompressedSize64), nil
		}
		if cand == nil && strings.EqualFold(entry.Name, stem) {
			cand = entry
		}
		names = append(names, entry.Name)
	}
	if cand != nil {
		rc, err := cand.Open()
		if err != nil {
			zr.Close()
			return nil, 0, err
		}
		return &zipEntryReader{rc: rc, zr: zr}, int64(cand.UncompressedSize64), nil
	}
	zr.Close()
	return nil, 0, entryNotFound(name, names)
}

func open7z(path, name string) (io.ReadCloser, int64, error) {
	zr, err := sevenzip.OpenReader(path)
	if err != nil {
		return nil, 0, fmt.Errorf("not a valid 7z: %v", err)
	}
	stem := entryStem(name)
	var cand *sevenzip.File
	names := make([]string, 0, len(zr.File))
	for _, entry := range zr.File {
		if strings.EqualFold(entry.Name, name) {
			rc, err := entry.Open()
			if err != nil {
				zr.Close()
				return nil, 0, err
			}
			return &sevenzEntryReader{rc: rc, zr: zr}, int64(entry.UncompressedSize), nil
		}
		if cand == nil && strings.EqualFold(entry.Name, stem) {
			cand = entry
		}
		names = append(names, entry.Name)
	}
	if cand != nil {
		rc, err := cand.Open()
		if err != nil {
			zr.Close()
			return nil, 0, err
		}
		return &sevenzEntryReader{rc: rc, zr: zr}, int64(cand.UncompressedSize), nil
	}
	zr.Close()
	return nil, 0, entryNotFound(name, names)
}

// entryNotFound builds a diagnostic message with sample entry names.
func entryNotFound(want string, names []string) error {
	sample := names
	if len(sample) > 5 {
		sample = sample[:5]
	}
	return fmt.Errorf("entry %q not found among %d entries (e.g. %s)", want, len(names), strings.Join(sample, ", "))
}

type sevenzEntryReader struct {
	rc io.ReadCloser
	zr *sevenzip.ReadCloser
}

func (z *sevenzEntryReader) Read(p []byte) (int, error) { return z.rc.Read(p) }

func (z *sevenzEntryReader) Close() error {
	z.rc.Close()
	return z.zr.Close()
}

// pathInside checks that root/rel stays under root after cleaning,
// rejecting "../" and absolute components.
func pathInside(root, rel string) bool {
	full := filepath.Join(root, filepath.FromSlash(rel))
	// Resolve symlinks where possible so a link inside the root cannot
	// point outside it. Fall back to the lexical path when a component
	// does not exist yet (e.g. a file being created).
	if rr, err := filepath.EvalSymlinks(root); err == nil {
		root = rr
	}
	if rf, err := filepath.EvalSymlinks(full); err == nil {
		full = rf
	} else {
		if parent, err := filepath.EvalSymlinks(filepath.Dir(full)); err == nil {
			full = filepath.Join(parent, filepath.Base(full))
		}
	}
	r, err := filepath.Rel(root, full)
	if err != nil {
		return false
	}
	return r == "." || (!strings.HasPrefix(r, ".."+string(os.PathSeparator)) && r != "..")
}

type zipEntryReader struct {
	rc io.ReadCloser
	zr *zip.ReadCloser
}

func (z *zipEntryReader) Read(p []byte) (int, error) { return z.rc.Read(p) }

func (z *zipEntryReader) Close() error {
	z.rc.Close()
	return z.zr.Close()
}

// BareFilePath returns the on-disk path of a book stored as a plain file
// (not an entry inside a ZIP/7z archive). Maintenance jobs use it to rewrite
// the file in place.
func (l *Library) BareFilePath(folder, file, ext string) (string, error) {
	name := file + "." + ext
	if l == nil || !pathInside(l.root, folder) || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("%w: %s/%s", ErrNoFile, folder, name)
	}
	dir := filepath.Join(l.root, filepath.FromSlash(folder))
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%w: %s/%s", ErrNoFile, folder, name)
	}
	target := filepath.Join(dir, name)
	fi, err = os.Stat(target)
	if err != nil || !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %s/%s", ErrNoFile, folder, name)
	}
	return target, nil
}

// ReplaceBareEPUBFile atomically replaces a bare EPUB with srcPath. The
// source file must be on the same filesystem (normally a temp file created
// in the book's directory).
func (l *Library) ReplaceBareEPUBFile(folder, file, ext, srcPath string) error {
	if !strings.EqualFold(ext, "epub") {
		return ErrNotBareFile
	}
	target, err := l.BareFilePath(folder, file, ext)
	if err != nil {
		return ErrNotBareFile
	}
	fi, err := os.Stat(target)
	if err != nil {
		return err
	}
	if err := os.Chmod(srcPath, fi.Mode().Perm()); err != nil {
		return err
	}
	return os.Rename(srcPath, target)
}

// ReplaceBareEPUB atomically replaces a bare EPUB file and returns its
// previous contents. Files inside ZIP/7z archives cannot be edited.
func (l *Library) ReplaceBareEPUB(folder, file, ext string, data []byte) ([]byte, error) {
	name := file + "." + ext
	if !strings.EqualFold(ext, "epub") || !pathInside(l.root, folder) || strings.ContainsAny(name, `/\`) {
		return nil, ErrNotBareFile
	}
	dir := filepath.Join(l.root, filepath.FromSlash(folder))
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return nil, ErrNotBareFile
	}
	target := filepath.Join(dir, name)
	fi, err = os.Stat(target)
	if err != nil || !fi.Mode().IsRegular() {
		return nil, ErrNotBareFile
	}
	old, err := os.ReadFile(target)
	if err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(dir, ".polka-epub-*")
	if err != nil {
		return nil, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Chmod(fi.Mode().Perm()); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(tmpName, target); err != nil {
		return nil, err
	}
	return old, nil
}

// ForgetCover drops a cached cover after the book file is edited.
func (l *Library) ForgetCover(bookID int64) {
	l.covers.Remove(bookID)
}

// ForgetAllCovers drops the whole cover cache after a catalog replace.
func (l *Library) ForgetAllCovers() {
	if l == nil {
		return
	}
	l.covers.Purge()
}

func (l *Library) Cover(bookID int64, folder, file, ext string) ([]byte, string, error) {
	if c, ok := l.covers.Get(bookID); ok {
		if c.data == nil {
			return nil, "", ErrNoFile // negative cache
		}
		return c.data, c.mime, nil
	}

	data, mime, err := l.extractCover(folder, file, ext)

	if err == nil {
		// Do not let a few huge covers dominate the LRU's memory budget.
		if len(data) <= maxCachedCoverSize {
			l.covers.Add(bookID, coverEntry{data: data, mime: mime})
		}
	} else if errors.Is(err, ErrNoFile) {
		// Negative cache only for a genuinely missing cover; transient I/O
		// errors must be retried on the next request.
		l.covers.Add(bookID, coverEntry{})
	}

	if err != nil {
		return nil, "", err
	}
	return data, mime, nil
}

// CheckCover extracts a cover without touching the LRU cache. Maintenance
// scans must not leave negative cache entries behind: a cover added later
// should become visible without a restart. Unlike Cover, parse failures are
// returned as errors instead of being folded into ErrNoFile, so the scan can
// distinguish a missing cover from a damaged book.
func (l *Library) CheckCover(folder, file, ext string) error {
	if l == nil {
		return ErrNoFile
	}
	if _, _, ok := l.sidecarCover(folder, file); ok {
		return nil
	}
	switch {
	case strings.EqualFold(ext, "fb2"):
		rc, _, err := l.Open(folder, file, ext)
		if err != nil {
			return err
		}
		defer rc.Close()
		meta, err := ParseFB2(rc, true)
		if err != nil {
			return fmt.Errorf("parse fb2: %w", err)
		}
		if len(meta.Cover) == 0 {
			return ErrNoFile
		}
		return nil
	case strings.EqualFold(ext, "epub"):
		rc, _, err := l.Open(folder, file, ext)
		if err != nil {
			return err
		}
		defer rc.Close()
		raw, err := readLimited(rc, maxArchiveEntrySize)
		if err != nil {
			return err
		}
		if _, _, err := EPUBCover(bytes.NewReader(raw), int64(len(raw))); err != nil {
			if errors.Is(err, ErrNoCover) {
				return ErrNoFile
			}
			return fmt.Errorf("read epub cover: %w", err)
		}
		return nil
	}
	return ErrNoFile
}

// sidecarCover looks for a cover in covers/<archive>.zip|7z (Flibusta layout).
func (l *Library) sidecarCover(folder, file string) ([]byte, string, bool) {
	base := strings.TrimSuffix(filepath.Base(folder), filepath.Ext(folder))
	if base == "" {
		return nil, "", false
	}
	for _, aext := range []string{".zip", ".7z"} {
		ap := filepath.Join(l.root, "covers", base+aext)
		if d, m, ok := coverArchiveEntry(ap, file); ok {
			return d, m, true
		}
	}
	return nil, "", false
}

// sniffImageMime detects an image MIME from its signature (covers/ stores
// covers with no extension); defaults to JPEG (the Flibusta format).
func sniffImageMime(d []byte) string {
	switch {
	case len(d) >= 3 && d[0] == 0xFF && d[1] == 0xD8 && d[2] == 0xFF:
		return "image/jpeg"
	case len(d) >= 8 && string(d[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png"
	case len(d) >= 6 && (string(d[:6]) == "GIF87a" || string(d[:6]) == "GIF89a"):
		return "image/gif"
	case len(d) >= 12 && string(d[:4]) == "RIFF" && string(d[8:12]) == "WEBP":
		return "image/webp"
	case isJXL(d):
		return "image/jxl"
	}
	return "image/jpeg"
}

var coverImageMime = map[string]string{
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png",
	".gif": "image/gif", ".webp": "image/webp", ".jxl": "image/jxl",
}

// coverArchiveEntry pulls a cover named <bookid>.<img> out of an archive.
func coverArchiveEntry(archivePath, bookid string) ([]byte, string, bool) {
	if _, err := os.Stat(archivePath); err != nil {
		return nil, "", false
	}
	match := func(name string, open func() (io.ReadCloser, error)) ([]byte, string, bool) {
		// Covers in the covers archive are named by book id, often WITHOUT
		// an extension (e.g. "294098"), so we detect the type by content.
		if !strings.EqualFold(strings.TrimSuffix(filepath.Base(name), filepath.Ext(name)), bookid) {
			return nil, "", false
		}
		rc, err := open()
		if err != nil {
			return nil, "", false
		}
		defer rc.Close()
		data, err := readLimited(rc, maxArchiveEntrySize)
		if err != nil || len(data) == 0 {
			return nil, "", false
		}
		mime := coverImageMime[strings.ToLower(filepath.Ext(name))]
		if mime == "" {
			mime = sniffImageMime(data)
		}
		return data, mime, true
	}
	if strings.EqualFold(filepath.Ext(archivePath), ".7z") {
		zr, err := sevenzip.OpenReader(archivePath)
		if err != nil {
			return nil, "", false
		}
		defer zr.Close()
		for _, e := range zr.File {
			if d, m, ok := match(e.Name, e.Open); ok {
				return d, m, true
			}
		}
		return nil, "", false
	}
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, "", false
	}
	defer zr.Close()
	for _, e := range zr.File {
		if d, m, ok := match(e.Name, e.Open); ok {
			return d, m, true
		}
	}
	return nil, "", false
}

func (l *Library) extractCover(folder, file, ext string) ([]byte, string, error) {
	// Flibusta sidecar: covers/<archive-name>.zip|7z → <book id>.jpg.
	// Cheaper than unpacking the book for its embedded cover, and it
	// covers books that have no embedded cover at all.
	if data, mime, ok := l.sidecarCover(folder, file); ok {
		data, mime = normalizeCover(data, mime)
		return data, mime, nil
	}
	switch {
	case strings.EqualFold(ext, "fb2"):
		rc, _, err := l.Open(folder, file, ext)
		if err != nil {
			return nil, "", err
		}
		defer rc.Close()
		meta, err := ParseFB2(rc, true)
		if err != nil || len(meta.Cover) == 0 {
			return nil, "", ErrNoFile
		}
		data, mime := normalizeCover(meta.Cover, meta.CoverMime)
		return data, mime, nil

	case strings.EqualFold(ext, "epub"):
		rc, _, err := l.Open(folder, file, ext)
		if err != nil {
			return nil, "", err
		}
		defer rc.Close()
		// zip needs random access; an EPUB fits in memory comfortably
		raw, err := readLimited(rc, maxArchiveEntrySize)
		if err != nil {
			return nil, "", err
		}
		data, mime, err := EPUBCover(bytes.NewReader(raw), int64(len(raw)))
		if err != nil {
			return nil, "", ErrNoFile
		}
		data, mime = normalizeCover(data, mime)
		return data, mime, nil
	}
	return nil, "", ErrNoFile
}

// Text converts a book (fb2, txt or epub) into HTML chapters for reading online.
func (l *Library) Text(folder, file, ext string, imgURL func(string) string) (*FB2Text, error) {
	rc, _, err := l.Open(folder, file, ext)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	switch strings.ToLower(ext) {
	case "fb2":
		return ParseFB2Text(rc, imgURL)
	case "txt":
		return ParseTXT(rc)
	case "epub":
		raw, err := readLimited(rc, maxArchiveEntrySize)
		if err != nil {
			return nil, err
		}
		return EPUBText(bytes.NewReader(raw), int64(len(raw)), imgURL)
	}
	return nil, ErrNoFile
}

// Binary pulls an embedded file (illustration) out of an FB2 by id.
func (l *Library) Binary(folder, file, ext, id string) ([]byte, string, error) {
	switch {
	case strings.EqualFold(ext, "fb2"):
		rc, _, err := l.Open(folder, file, ext)
		if err != nil {
			return nil, "", err
		}
		defer rc.Close()
		return ExtractBinary(rc, id)
	case strings.EqualFold(ext, "epub"):
		rc, _, err := l.Open(folder, file, ext)
		if err != nil {
			return nil, "", err
		}
		defer rc.Close()
		raw, err := readLimited(rc, maxArchiveEntrySize)
		if err != nil {
			return nil, "", err
		}
		return EPUBBinary(bytes.NewReader(raw), int64(len(raw)), id)
	}
	return nil, "", ErrNoFile
}

// Meta extracts FB2 metadata (annotation, publish info) without the cover.
func (l *Library) Meta(folder, file, ext string) (*FB2Meta, error) {
	rc, _, err := l.Open(folder, file, ext)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	switch strings.ToLower(ext) {
	case "fb2":
		return ParseFB2(rc, false)
	case "epub":
		data, err := readLimited(rc, maxArchiveEntrySize)
		if err != nil {
			return nil, err
		}
		meta, err := ParseEPUB(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, err
		}
		return &FB2Meta{
			Title:          meta.Title,
			Lang:           meta.Language,
			AnnotationHTML: descriptionHTML(meta.Description),
			Publisher:      meta.Publisher,
			Year:           meta.Year,
			ISBN:           meta.ISBN,
		}, nil
	default:
		return nil, ErrNoFile
	}
}
