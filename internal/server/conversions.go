package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/vestigiumincaligne/polka/internal/conversions"
	"github.com/vestigiumincaligne/polka/internal/fb2epub"
	"github.com/vestigiumincaligne/polka/internal/library"
	"github.com/vestigiumincaligne/polka/internal/store"
)

const (
	conversionInboxDir   = "convert-inbox"
	conversionSourcesDir = "fb2-sources"
	conversionFailedDir  = "fb2-failed"
	convertedFolder      = "converted"
	conversionPollDelay  = time.Second
)

func (s *Server) fb2ToEPUBEnabled(ctx context.Context) bool {
	return s.conv != nil && s.users.GetSetting(ctx, "convert.fb2_to_epub", "0") == "1"
}

func (s *Server) keepConversionSource(ctx context.Context) bool {
	return s.users.GetSetting(ctx, "convert.keep_source", "1") == "1"
}

// queueFB2Conversion stores an uploaded FB2 in the inbox and creates a job.
// The file is not added to the catalog until the background worker finishes.
func (s *Server) queueFB2Conversion(ctx context.Context, fh *multipart.FileHeader, lang string) uploadResult {
	res := uploadResult{Name: fh.Filename}
	if s.conv == nil || s.cfg.DataDir == "" {
		res.Error = tr(lang, "upload.savefail")
		return res
	}
	inbox := filepath.Join(s.cfg.DataDir, conversionInboxDir)
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		s.log.Error("conversion inbox", "error", err)
		res.Error = tr(lang, "upload.savefail")
		return res
	}
	id := newJobID()
	source := id + ".fb2"
	dstPath := filepath.Join(inbox, source)

	src, err := fh.Open()
	if err != nil {
		res.Error = tr(lang, "upload.readfail")
		return res
	}
	defer src.Close()
	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, uploadFilePerms)
	if err != nil {
		s.log.Error("conversion inbox", "error", err)
		res.Error = tr(lang, "upload.savefail")
		return res
	}
	_, copyErr := io.Copy(dst, src)
	closeErr := dst.Close()
	if copyErr != nil || closeErr != nil {
		os.Remove(dstPath)
		s.log.Error("conversion inbox", "error", errors.Join(copyErr, closeErr))
		res.Error = tr(lang, "upload.savefail")
		return res
	}
	if err := s.conv.Create(ctx, id, fh.Filename, source); err != nil {
		os.Remove(dstPath)
		s.log.Error("conversion queue", "error", err)
		res.Error = tr(lang, "upload.savefail")
		return res
	}
	res.JobID = id
	res.Status = string(conversions.StatusQueued)
	s.log.Info("fb2 queued for conversion", "file", fh.Filename, "job", id)
	return res
}

func (s *Server) conversionLoop() {
	for {
		job, err := s.conv.ClaimNext(context.Background())
		if err != nil {
			s.log.Error("conversion claim", "error", err)
			time.Sleep(2 * time.Second)
			continue
		}
		if job == nil {
			time.Sleep(conversionPollDelay)
			continue
		}
		s.processConversion(job)
	}
}

func (s *Server) processConversion(job *conversions.Job) {
	ctx := context.Background()
	defer func() {
		if recovered := recover(); recovered != nil {
			err := fmt.Errorf("conversion panic: %v", recovered)
			s.failConversion(job, err)
		}
	}()

	inbox := conversions.InboxPath(s.cfg.DataDir, job.Source)
	data, err := os.ReadFile(inbox)
	if err != nil {
		s.failConversion(job, fmt.Errorf("read source: %w", err))
		return
	}
	meta, err := library.ParseFB2(bytes.NewReader(data), true)
	if err != nil {
		s.failConversion(job, fmt.Errorf("parse metadata: %w", err))
		return
	}
	sourceHash := sha256.Sum256(data)
	contentHash, err := library.ContentHash(bytes.NewReader(data))
	if err != nil {
		s.failConversion(job, fmt.Errorf("content hash: %w", err))
		return
	}
	title := strings.TrimSpace(meta.Title)
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(job.Name), filepath.Ext(job.Name))
		meta.Title = title
	}
	if dup, reason, err := s.findConversionDuplicate(ctx, title, meta.Authors, hex.EncodeToString(sourceHash[:]), contentHash); err != nil {
		s.failConversion(job, err)
		return
	} else if dup != nil {
		s.log.Info("conversion skipped duplicate", "job", job.ID, "book", dup.ID, "reason", reason)
		if err := s.conv.SetStatus(ctx, job.ID, conversions.StatusSkipped, "", dup.ID); err != nil {
			s.log.Warn("conversion status", "job", job.ID, "error", err)
		}
		s.finishConversionSource(job)
		return
	}

	epub, err := fb2epub.Convert(data, meta)
	if err != nil {
		s.failConversion(job, fmt.Errorf("convert: %w", err))
		return
	}
	if err := fb2epub.Validate(epub); err != nil {
		s.failConversion(job, fmt.Errorf("validate epub: %w", err))
		return
	}

	convertedDir := filepath.Join(s.cfg.LibraryDir, convertedFolder)
	if err := os.MkdirAll(convertedDir, 0o755); err != nil {
		s.failConversion(job, fmt.Errorf("create converted dir: %w", err))
		return
	}
	base := sanitizeFileName(strings.TrimSuffix(filepath.Base(job.Name), filepath.Ext(job.Name)))
	if base == "" {
		base = sanitizeFileName(title)
	}
	if base == "" {
		base = "book"
	}
	out, base, err := createUnique(convertedDir, base, "epub")
	if err != nil {
		s.failConversion(job, fmt.Errorf("create output: %w", err))
		return
	}
	if _, err := out.Write(epub); err != nil {
		out.Close()
		os.Remove(out.Name())
		s.failConversion(job, fmt.Errorf("write output: %w", err))
		return
	}
	if err := out.Close(); err != nil {
		os.Remove(out.Name())
		s.failConversion(job, fmt.Errorf("close output: %w", err))
		return
	}

	epubHash := sha256.Sum256(epub)
	year, _ := strconv.Atoi(strings.TrimSpace(meta.Year))
	input := &store.BookInput{
		Title:       title,
		Authors:     conversionAuthors(meta.Authors),
		Series:      meta.Series,
		SeriesNum:   meta.SeriesNum,
		Genres:      meta.Genres,
		Folder:      convertedFolder,
		File:        base,
		Ext:         "epub",
		Size:        int64(len(epub)),
		Lang:        meta.Lang,
		Year:        year,
		Added:       time.Now().Format("2006-01-02"),
		FileHash:    hex.EncodeToString(epubHash[:]),
		ContentHash: contentHash,
		ISBN:        meta.ISBN,
	}
	bookID, err := s.st.AddBook(ctx, input)
	if err != nil {
		os.Remove(out.Name())
		s.failConversion(job, fmt.Errorf("add book: %w", err))
		return
	}
	if err := s.conv.SetStatus(ctx, job.ID, conversions.StatusDone, "", bookID); err != nil {
		s.log.Warn("conversion status", "job", job.ID, "error", err)
	}
	s.finishConversionSource(job)
	s.log.Info("fb2 converted", "job", job.ID, "book", bookID, "file", base+".epub")
}

func (s *Server) findConversionDuplicate(ctx context.Context, title string, authors []library.PersonName, fileHash, contentHash string) (*store.Book, string, error) {
	if book, err := s.st.FindByHash(ctx, "file_hash", fileHash); err == nil {
		return book, "file", nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, "", err
	}
	if book, err := s.st.FindByHash(ctx, "content_hash", contentHash); err == nil {
		return book, "content", nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, "", err
	}
	if title == "" {
		return nil, "", nil
	}
	lastNames := make([]string, 0, len(authors))
	for _, a := range authors {
		lastNames = append(lastNames, a.Last)
	}
	similar, err := s.st.FindSimilar(ctx, title, lastNames)
	if err != nil {
		return nil, "", err
	}
	if len(similar) > 0 {
		return &similar[0], "metadata", nil
	}
	return nil, "", nil
}

func conversionAuthors(authors []library.PersonName) []store.AuthorName {
	out := make([]store.AuthorName, 0, len(authors))
	for _, a := range authors {
		out = append(out, store.AuthorName{Last: a.Last, First: a.First, Middle: a.Middle})
	}
	return out
}

func (s *Server) failConversion(job *conversions.Job, err error) {
	ctx := context.Background()
	s.log.Warn("conversion failed", "job", job.ID, "error", err)
	if updateErr := s.conv.SetStatus(ctx, job.ID, conversions.StatusFailed, err.Error(), 0); updateErr != nil {
		s.log.Warn("conversion status", "job", job.ID, "error", updateErr)
	}
	s.moveConversionSource(job.Source, conversionFailedDir)
}

func (s *Server) finishConversionSource(job *conversions.Job) {
	if s.keepConversionSource(context.Background()) {
		s.moveConversionSource(job.Source, conversionSourcesDir)
		return
	}
	if err := os.Remove(conversions.InboxPath(s.cfg.DataDir, job.Source)); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.log.Warn("remove converted source", "job", job.ID, "error", err)
	}
}

func (s *Server) moveConversionSource(source, dir string) {
	inbox := conversions.InboxPath(s.cfg.DataDir, source)
	dstDir := filepath.Join(s.cfg.DataDir, dir)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		s.log.Warn("conversion archive", "dir", dir, "error", err)
		return
	}
	if err := os.Rename(inbox, filepath.Join(dstDir, filepath.Base(source))); err != nil {
		s.log.Warn("conversion archive", "dir", dir, "error", err)
	}
}

// GET /admin/conversions — queue state for the admin panel.
func (s *Server) handleConversionList(w http.ResponseWriter, r *http.Request) {
	if s.conv == nil {
		writeJSON(w, map[string]any{"jobs": []conversions.Job{}})
		return
	}
	jobs, err := s.conv.List(r.Context(), 100)
	if err != nil {
		s.apiError(w, err)
		return
	}
	writeJSON(w, map[string]any{"jobs": jobs})
}

func newJobID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("job-%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
