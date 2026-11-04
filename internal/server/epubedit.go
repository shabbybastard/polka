package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/vestigiumincaligne/polka/internal/library"
	"github.com/vestigiumincaligne/polka/internal/store"
)

const (
	maxEPUBUpload   = 64 << 20
	maxCoverUpload  = 5 << 20
	maxEPUBAuthors  = 20
	maxEPUBFieldLen = 4096
)

// POST /admin/books/{id}/epub — rewrite EPUB metadata and cover.
func (s *Server) handleBookEPUBEdit(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if s.lib == nil {
		http.Error(w, "library dir is not configured", http.StatusConflict)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxEPUBUpload)
	if err := r.ParseMultipartForm(maxEPUBUpload); err != nil {
		http.Error(w, "upload too large or malformed", http.StatusBadRequest)
		return
	}
	field := func(name string) string {
		if vs := r.MultipartForm.Value[name]; len(vs) > 0 {
			return strings.TrimSpace(vs[0])
		}
		return ""
	}

	title := field("title")
	if title == "" {
		http.Error(w, "title is required", http.StatusBadRequest)
		return
	}
	if len([]rune(title)) > 512 {
		http.Error(w, "title is too long", http.StatusBadRequest)
		return
	}

	var authors []store.AuthorName
	if raw := field("authors"); raw != "" {
		var payload []struct {
			Last   string `json:"last"`
			First  string `json:"first"`
			Middle string `json:"middle"`
		}
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			http.Error(w, "invalid authors", http.StatusBadRequest)
			return
		}
		if len(payload) > maxEPUBAuthors {
			http.Error(w, "too many authors", http.StatusBadRequest)
			return
		}
		for _, a := range payload {
			if len([]rune(a.Last)) > 128 || len([]rune(a.First)) > 128 || len([]rune(a.Middle)) > 128 {
				http.Error(w, "author name is too long", http.StatusBadRequest)
				return
			}
			authors = append(authors, store.AuthorName{Last: a.Last, First: a.First, Middle: a.Middle})
		}
	}

	language := field("language")
	publisher := field("publisher")
	year := field("year")
	isbn := field("isbn")
	description := field("description")
	for _, value := range []string{language, publisher, year, isbn, description} {
		if len([]rune(value)) > maxEPUBFieldLen {
			http.Error(w, "metadata field is too long", http.StatusBadRequest)
			return
		}
	}
	if y := yearFromString(year); y == 0 {
		year = ""
	}

	f, err := s.st.BookFile(r.Context(), id)
	if err != nil {
		s.apiError(w, err)
		return
	}
	if !strings.EqualFold(f.Ext, "epub") {
		http.Error(w, "only EPUB metadata editing is supported", http.StatusUnsupportedMediaType)
		return
	}
	rc, _, err := s.lib.Open(f.Folder, f.File, f.Ext)
	if err != nil {
		s.apiError(w, err)
		return
	}
	original, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		s.apiError(w, err)
		return
	}

	edit := library.EPUBEdit{
		Title: title, Language: language, Publisher: publisher,
		Year: year, ISBN: isbn, Description: description,
	}
	for _, a := range authors {
		name := strings.TrimSpace(a.Last + " " + a.First + " " + a.Middle)
		if name != "" {
			edit.Authors = append(edit.Authors, name)
		}
	}

	if files := r.MultipartForm.File["cover"]; len(files) > 0 {
		fh := files[0]
		if fh.Size > maxCoverUpload {
			http.Error(w, "cover is too large", http.StatusBadRequest)
			return
		}
		cf, err := fh.Open()
		if err != nil {
			s.apiError(w, err)
			return
		}
		cover, err := io.ReadAll(io.LimitReader(cf, maxCoverUpload+1))
		cf.Close()
		if err != nil {
			s.apiError(w, err)
			return
		}
		if len(cover) > maxCoverUpload {
			http.Error(w, "cover is too large", http.StatusBadRequest)
			return
		}
		normalized, mime, err := library.NormalizeCover(cover)
		if err != nil {
			http.Error(w, "unsupported cover image", http.StatusBadRequest)
			return
		}
		edit.Cover, edit.CoverMime = normalized, mime
	}

	updated, err := library.EditEPUB(original, edit)
	if err != nil {
		http.Error(w, "failed to edit EPUB: "+err.Error(), http.StatusBadRequest)
		return
	}

	if err := s.backupEPUB(id, original); err != nil {
		s.apiError(w, err)
		return
	}
	if _, err := s.lib.ReplaceBareEPUB(f.Folder, f.File, f.Ext, updated); err != nil {
		if errors.Is(err, library.ErrNotBareFile) {
			http.Error(w, "editing archived books is not supported", http.StatusConflict)
			return
		}
		s.apiError(w, err)
		return
	}

	// The file is already replaced; restore it if the catalog update fails.
	restore := func() {
		if _, err := s.lib.ReplaceBareEPUB(f.Folder, f.File, f.Ext, original); err != nil {
			s.log.Error("restore epub after failed metadata update", "book", id, "error", err)
		}
	}
	if err := s.st.UpdateBookMetadata(r.Context(), id, title, authors); err != nil {
		restore()
		s.apiError(w, err)
		return
	}
	hash := sha256.Sum256(updated)
	if err := s.st.UpdateBookFile(r.Context(), id, language, yearFromString(year), isbn, int64(len(updated)), hex.EncodeToString(hash[:])); err != nil {
		restore()
		s.apiError(w, err)
		return
	}

	s.st.DeleteBookDigests(r.Context(), id)
	s.reader.forget(id)
	s.lib.ForgetCover(id)
	writeJSON(w, map[string]any{"ok": true})
}

func yearFromString(s string) int {
	s = strings.TrimSpace(s)
	if y, err := strconv.Atoi(s); err == nil && y > 0 {
		return y
	}
	// Accept a full dc:date such as 2024-05-01.
	for i := 0; i+4 <= len(s); i++ {
		if y, err := strconv.Atoi(s[i : i+4]); err == nil && y > 0 {
			return y
		}
	}
	return 0
}
