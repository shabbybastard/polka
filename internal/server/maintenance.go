package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/shabbybastard/polka/internal/library"
	"github.com/shabbybastard/polka/internal/store"
)

// EPUB maintenance: scan bare EPUB files for XHTML that needs normalization,
// and optionally rewrite them in place. Books inside ZIP/7z archives are
// reported but never modified.

type epubMaintenanceState struct {
	mu            sync.Mutex
	running       bool
	mode          string
	deep          bool
	startedAt     int64
	processed     int
	total         int
	needsFix      int
	fixed         int
	archived      int
	unreadable    int
	issues        int
	missingCovers int
	coverErrors   int
	failed        int
	err           string
	stopped       bool
	examples      []string
	cancel        context.CancelFunc
}

func (st *epubMaintenanceState) snapshot() map[string]any {
	st.mu.Lock()
	defer st.mu.Unlock()
	examples := make([]string, len(st.examples))
	copy(examples, st.examples)
	return map[string]any{
		"running":       st.running,
		"mode":          st.mode,
		"deep":          st.deep,
		"startedAt":     st.startedAt,
		"processed":     st.processed,
		"total":         st.total,
		"needsFix":      st.needsFix,
		"fixed":         st.fixed,
		"archived":      st.archived,
		"unreadable":    st.unreadable,
		"issues":        st.issues,
		"missingCovers": st.missingCovers,
		"coverErrors":   st.coverErrors,
		"failed":        st.failed,
		"error":         st.err,
		"stopped":       st.stopped,
		"examples":      examples,
	}
}

func (st *epubMaintenanceState) note(format string, args ...any) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.examples) < 50 {
		st.examples = append(st.examples, fmt.Sprintf(format, args...))
	}
}

func (st *epubMaintenanceState) fail(f *store.BookFile, err error) {
	st.mu.Lock()
	st.failed++
	st.mu.Unlock()
	st.note("%s (#%d): %v", f.Title, f.ID, err)
}

// POST /admin/maintenance/epub?mode=scan|fix|metadata|covers&deep=1
func (s *Server) handleEPUBMaintenanceStart(w http.ResponseWriter, r *http.Request) {
	if s.lib == nil {
		http.Error(w, "library-dir is not configured", http.StatusConflict)
		return
	}
	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = "scan"
	}
	switch mode {
	case "scan", "fix", "metadata", "covers":
	default:
		http.Error(w, "mode must be scan, fix, metadata or covers", http.StatusBadRequest)
		return
	}
	deep := r.URL.Query().Get("deep") == "1"

	ctx, cancel := context.WithCancel(context.Background())
	st := &s.epubFix
	st.mu.Lock()
	if st.running {
		st.mu.Unlock()
		cancel()
		http.Error(w, "maintenance is already running", http.StatusConflict)
		return
	}
	var total int
	var err error
	if mode == "scan" || mode == "fix" {
		total, err = s.st.EPUBCount(ctx)
	} else {
		total, err = s.st.BookCount(ctx)
	}
	if err != nil {
		st.mu.Unlock()
		cancel()
		s.apiError(w, err)
		return
	}
	st.running = true
	st.mode = mode
	st.deep = deep
	st.startedAt = time.Now().UnixMilli()
	st.processed = 0
	st.total = total
	st.needsFix = 0
	st.fixed = 0
	st.archived = 0
	st.unreadable = 0
	st.issues = 0
	st.missingCovers = 0
	st.coverErrors = 0
	st.failed = 0
	st.err = ""
	st.stopped = false
	st.examples = nil
	st.cancel = cancel
	st.mu.Unlock()

	switch mode {
	case "scan", "fix":
		go s.runEPUBMaintenance(ctx, mode, deep)
	case "metadata":
		go s.runMetadataMaintenance(ctx)
	case "covers":
		go s.runCoverMaintenance(ctx)
	}
	writeJSON(w, map[string]any{"started": true, "mode": mode, "total": total})
}

// GET /admin/maintenance/epub/status
func (s *Server) handleEPUBMaintenanceStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.epubFix.snapshot())
}

// POST /admin/maintenance/epub/stop
func (s *Server) handleEPUBMaintenanceStop(w http.ResponseWriter, _ *http.Request) {
	st := &s.epubFix
	st.mu.Lock()
	cancel := st.cancel
	st.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	writeJSON(w, map[string]any{"stopping": cancel != nil})
}

func (s *Server) runEPUBMaintenance(ctx context.Context, mode string, deep bool) {
	st := &s.epubFix
	defer func() {
		st.mu.Lock()
		st.running = false
		st.cancel = nil
		st.mu.Unlock()
	}()

	afterID := int64(0)
	const batchSize = 25
	for {
		if ctx.Err() != nil {
			st.mu.Lock()
			st.stopped = true
			st.mu.Unlock()
			return
		}
		files, err := s.st.BooksForEPUBMaintenance(ctx, afterID, batchSize)
		if err != nil {
			if ctx.Err() != nil {
				st.mu.Lock()
				st.stopped = true
				st.mu.Unlock()
				return
			}
			st.mu.Lock()
			st.err = err.Error()
			st.mu.Unlock()
			return
		}
		if len(files) == 0 {
			return
		}
		for i := range files {
			if ctx.Err() != nil {
				st.mu.Lock()
				st.stopped = true
				st.mu.Unlock()
				return
			}
			afterID = files[i].ID
			s.maintenanceEPUB(ctx, &files[i], mode, deep)
			st.mu.Lock()
			st.processed++
			st.mu.Unlock()
		}
	}
}

func (st *epubMaintenanceState) finish() {
	st.mu.Lock()
	st.running = false
	st.cancel = nil
	st.mu.Unlock()
}

func (st *epubMaintenanceState) markStopped() {
	st.mu.Lock()
	st.stopped = true
	st.mu.Unlock()
}

// runMetadataMaintenance checks catalog fields only: it never opens book
// files, so it is fast even for large libraries.
func (s *Server) runMetadataMaintenance(ctx context.Context) {
	st := &s.epubFix
	defer st.finish()

	afterID := int64(0)
	const batchSize = 100
	for {
		if ctx.Err() != nil {
			st.markStopped()
			return
		}
		books, err := s.st.BooksForMetadataCheck(ctx, afterID, batchSize)
		if err != nil {
			if ctx.Err() != nil {
				st.markStopped()
				return
			}
			st.mu.Lock()
			st.err = err.Error()
			st.mu.Unlock()
			return
		}
		if len(books) == 0 {
			return
		}
		for i := range books {
			if ctx.Err() != nil {
				st.markStopped()
				return
			}
			afterID = books[i].ID
			problems := metadataProblems(&books[i])
			if len(problems) > 0 {
				st.mu.Lock()
				st.issues++
				st.mu.Unlock()
				st.note("%s (#%d): %s", books[i].Title, books[i].ID, strings.Join(problems, "; "))
			}
			st.mu.Lock()
			st.processed++
			st.mu.Unlock()
		}
	}
}

func metadataProblems(b *store.BookMetadataCheck) []string {
	var out []string
	if strings.TrimSpace(b.Title) == "" {
		out = append(out, "empty title")
	}
	if strings.TrimSpace(b.Authors) == "" {
		out = append(out, "no authors")
	}
	year := time.Now().Year()
	if b.Year < 1000 || b.Year > year+1 {
		out = append(out, fmt.Sprintf("invalid year %d", b.Year))
	}
	if !validLanguage(b.Lang) {
		out = append(out, fmt.Sprintf("invalid language %q", b.Lang))
	}
	if strings.TrimSpace(b.ISBN) != "" && !validISBN(b.ISBN) {
		out = append(out, fmt.Sprintf("invalid ISBN %q", b.ISBN))
	}
	if b.Genres == 0 {
		out = append(out, "no genres")
	}
	if hasCorruptText(b.Title) || hasCorruptText(b.Authors) {
		out = append(out, "corrupted text")
	}
	return out
}

func validLanguage(lang string) bool {
	lang = strings.TrimSpace(lang)
	if lang == "" {
		return false
	}
	lang = strings.ReplaceAll(lang, "_", "-")
	parts := strings.Split(lang, "-")
	if len(parts) == 0 || len(parts[0]) < 2 || len(parts[0]) > 3 {
		return false
	}
	for i, part := range parts {
		if len(part) == 0 || len(part) > 8 {
			return false
		}
		letters, digits := true, true
		for _, r := range part {
			letter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
			digit := r >= '0' && r <= '9'
			if !letter {
				letters = false
			}
			if !digit {
				digits = false
			}
			if !letter && !digit {
				return false
			}
		}
		if i == 0 {
			if !letters || len(part) < 2 || len(part) > 3 {
				return false
			}
			continue
		}
		switch {
		case len(part) == 2 && letters: // region
		case len(part) == 3 && (letters || digits): // region / extlang
		case len(part) == 4 && (letters || digits): // script / variant
		case len(part) >= 5 && len(part) <= 8: // variant
		default:
			return false
		}
	}
	return true
}

func validISBN(raw string) bool {
	n := store.NormalizeISBN(raw)
	switch len(n) {
	case 10:
		sum := 0
		for i := 0; i < 10; i++ {
			d := 0
			switch {
			case n[i] >= '0' && n[i] <= '9':
				d = int(n[i] - '0')
			case i == 9 && n[i] == 'X':
				d = 10
			default:
				return false
			}
			sum += (10 - i) * d
		}
		return sum%11 == 0
	case 13:
		sum := 0
		for i := 0; i < 13; i++ {
			if n[i] < '0' || n[i] > '9' {
				return false
			}
			d := int(n[i] - '0')
			if i%2 == 1 {
				sum += 3 * d
			} else {
				sum += d
			}
		}
		return sum%10 == 0
	default:
		return false
	}
}

func hasCorruptText(s string) bool {
	if !utf8.ValidString(s) || strings.ContainsRune(s, utf8.RuneError) {
		return true
	}
	for _, r := range s {
		if r < 0x20 && r != '\n' && r != '\r' && r != '\t' {
			return true
		}
	}
	return false
}

// runCoverMaintenance checks whether a cover can be extracted for every
// visible book. It never rewrites files and bypasses the cover cache.
func (s *Server) runCoverMaintenance(ctx context.Context) {
	st := &s.epubFix
	defer st.finish()

	afterID := int64(0)
	const batchSize = 50
	for {
		if ctx.Err() != nil {
			st.markStopped()
			return
		}
		files, err := s.st.BooksForMaintenance(ctx, afterID, batchSize)
		if err != nil {
			if ctx.Err() != nil {
				st.markStopped()
				return
			}
			st.mu.Lock()
			st.err = err.Error()
			st.mu.Unlock()
			return
		}
		if len(files) == 0 {
			return
		}
		for i := range files {
			if ctx.Err() != nil {
				st.markStopped()
				return
			}
			afterID = files[i].ID
			err := s.lib.CheckCover(files[i].Folder, files[i].File, files[i].Ext)
			if err != nil {
				if errors.Is(err, library.ErrNoFile) {
					st.mu.Lock()
					st.missingCovers++
					st.mu.Unlock()
					st.note("%s (#%d): cover not found", files[i].Title, files[i].ID)
				} else {
					st.mu.Lock()
					st.coverErrors++
					st.mu.Unlock()
					st.note("%s (#%d): %v", files[i].Title, files[i].ID, err)
				}
			}
			st.mu.Lock()
			st.processed++
			st.mu.Unlock()
		}
	}
}

func (s *Server) maintenanceEPUB(ctx context.Context, f *store.BookFile, mode string, deep bool) {
	st := &s.epubFix
	path, err := s.lib.BareFilePath(f.Folder, f.File, f.Ext)
	if err != nil {
		// A book inside a ZIP/7z archive cannot be rewritten. Check the
		// archive entry metadata (no decompression) to distinguish it from a
		// genuinely missing file.
		if s.lib.HasArchive(f.Folder) {
			if _, sizeErr := s.lib.EntrySize(f.Folder, f.File, f.Ext); sizeErr == nil {
				st.mu.Lock()
				st.archived++
				st.mu.Unlock()
				return
			}
		}
		st.fail(f, fmt.Errorf("file not found: %w", err))
		return
	}

	orig, err := os.Open(path)
	if err != nil {
		st.fail(f, err)
		return
	}
	origInfo, err := orig.Stat()
	if err != nil {
		orig.Close()
		st.fail(f, err)
		return
	}

	changed, err := library.ScanEPUB(orig, origInfo.Size())
	if err != nil {
		orig.Close()
		st.fail(f, err)
		return
	}
	if !changed {
		if mode == "scan" && deep {
			if _, err := library.EPUBText(orig, origInfo.Size(), func(string) string { return "" }); err != nil {
				st.mu.Lock()
				st.unreadable++
				st.mu.Unlock()
				st.note("%s (#%d): %v", f.Title, f.ID, err)
			}
		}
		orig.Close()
		return
	}

	st.mu.Lock()
	st.needsFix++
	st.mu.Unlock()
	if mode == "scan" {
		if deep {
			// Check whether the known tag issue is the only problem: normalize
			// in memory and see whether the rewritten EPUB becomes readable.
			var buf bytes.Buffer
			if _, err := library.NormalizeEPUB(orig, origInfo.Size(), &buf); err != nil {
				st.mu.Lock()
				st.unreadable++
				st.mu.Unlock()
				st.note("%s (#%d): %v", f.Title, f.ID, err)
			} else if _, err := library.EPUBText(bytes.NewReader(buf.Bytes()), int64(buf.Len()), func(string) string { return "" }); err != nil {
				st.mu.Lock()
				st.unreadable++
				st.mu.Unlock()
				st.note("%s (#%d): %v", f.Title, f.ID, err)
			}
		}
		orig.Close()
		return
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".polka-epubfix-*")
	if err != nil {
		orig.Close()
		st.fail(f, err)
		return
	}
	tmpName := tmp.Name()
	removeTmp := true
	defer func() {
		if removeTmp {
			os.Remove(tmpName)
		}
	}()

	if _, err := library.NormalizeEPUB(orig, origInfo.Size(), tmp); err != nil {
		orig.Close()
		tmp.Close()
		st.fail(f, err)
		return
	}
	orig.Close()
	if err := tmp.Close(); err != nil {
		st.fail(f, err)
		return
	}

	vf, err := os.Open(tmpName)
	if err != nil {
		st.fail(f, err)
		return
	}
	vst, err := vf.Stat()
	if err != nil {
		vf.Close()
		st.fail(f, err)
		return
	}
	if _, err := library.EPUBText(vf, vst.Size(), func(string) string { return "" }); err != nil {
		vf.Close()
		st.fail(f, fmt.Errorf("rewritten epub is not readable: %w", err))
		return
	}
	vf.Close()

	hash, err := fileSHA256(tmpName)
	if err != nil {
		st.fail(f, err)
		return
	}
	if ctx.Err() != nil {
		st.mu.Lock()
		st.stopped = true
		st.mu.Unlock()
		return
	}
	if err := s.backupEPUBFile(f.ID, path); err != nil {
		st.fail(f, fmt.Errorf("backup: %w", err))
		return
	}
	if ctx.Err() != nil {
		st.mu.Lock()
		st.stopped = true
		st.mu.Unlock()
		return
	}
	if err := s.lib.ReplaceBareEPUBFile(f.Folder, f.File, f.Ext, tmpName); err != nil {
		st.fail(f, err)
		return
	}
	removeTmp = false

	// Once the file is replaced, the metadata updates must not be aborted by
	// a stop request: the new file is already on disk.
	dbCtx, dbCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer dbCancel()
	if err := s.st.UpdateBookFileSizeHash(dbCtx, f.ID, vst.Size(), hash); err != nil {
		s.log.Warn("maintenance: update book file metadata", "book", f.ID, "error", err)
		st.note("%s (#%d): file fixed, but metadata update failed: %v", f.Title, f.ID, err)
	}
	if err := s.st.DeleteBookDigests(dbCtx, f.ID); err != nil {
		s.log.Warn("maintenance: delete book digests", "book", f.ID, "error", err)
	}
	s.reader.forget(f.ID)
	s.lib.ForgetCover(f.ID)

	st.mu.Lock()
	st.fixed++
	st.mu.Unlock()
	s.log.Info("maintenance: epub fixed", "book", f.ID, "file", path)
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
