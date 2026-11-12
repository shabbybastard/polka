package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/shabbybastard/polka/internal/library"
	"github.com/shabbybastard/polka/internal/store"
)

// EPUB maintenance: scan bare EPUB files for XHTML that needs normalization,
// and optionally rewrite them in place. Books inside ZIP/7z archives are
// reported but never modified.

type epubMaintenanceState struct {
	mu         sync.Mutex
	running    bool
	mode       string
	deep       bool
	processed  int
	total      int
	needsFix   int
	fixed      int
	archived   int
	unreadable int
	failed     int
	err        string
	stopped    bool
	examples   []string
	cancel     context.CancelFunc
}

func (st *epubMaintenanceState) snapshot() map[string]any {
	st.mu.Lock()
	defer st.mu.Unlock()
	examples := make([]string, len(st.examples))
	copy(examples, st.examples)
	return map[string]any{
		"running":    st.running,
		"mode":       st.mode,
		"deep":       st.deep,
		"processed":  st.processed,
		"total":      st.total,
		"needsFix":   st.needsFix,
		"fixed":      st.fixed,
		"archived":   st.archived,
		"unreadable": st.unreadable,
		"failed":     st.failed,
		"error":      st.err,
		"stopped":    st.stopped,
		"examples":   examples,
	}
}

func (st *epubMaintenanceState) note(format string, args ...any) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.examples) < 10 {
		st.examples = append(st.examples, fmt.Sprintf(format, args...))
	}
}

func (st *epubMaintenanceState) fail(f *store.BookFile, err error) {
	st.mu.Lock()
	st.failed++
	st.mu.Unlock()
	st.note("%s (#%d): %v", f.Title, f.ID, err)
}

// POST /admin/maintenance/epub?mode=scan|fix&deep=1
func (s *Server) handleEPUBMaintenanceStart(w http.ResponseWriter, r *http.Request) {
	if s.lib == nil {
		http.Error(w, "library-dir is not configured", http.StatusConflict)
		return
	}
	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = "scan"
	}
	if mode != "scan" && mode != "fix" {
		http.Error(w, "mode must be scan or fix", http.StatusBadRequest)
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
	total, err := s.st.EPUBCount(ctx)
	if err != nil {
		st.mu.Unlock()
		cancel()
		s.apiError(w, err)
		return
	}
	st.running = true
	st.mode = mode
	st.deep = deep
	st.processed = 0
	st.total = total
	st.needsFix = 0
	st.fixed = 0
	st.archived = 0
	st.unreadable = 0
	st.failed = 0
	st.err = ""
	st.stopped = false
	st.examples = nil
	st.cancel = cancel
	st.mu.Unlock()

	go s.runEPUBMaintenance(ctx, mode, deep)
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
