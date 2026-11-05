package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/shabbybastard/polka/internal/auth"
	"github.com/shabbybastard/polka/internal/conversions"
	"github.com/shabbybastard/polka/internal/genres"
	"github.com/shabbybastard/polka/internal/store"
	"github.com/shabbybastard/polka/internal/tgbot"
)

// telegramBackend is the adapter between the isolated bot package and the
// existing Polka services. It contains no SQL, search or ingestion logic of
// its own.
type telegramBackend struct {
	s *Server
}

// TelegramBackend returns the adapter used by cmd/polka when the optional
// bot token is configured.
func (s *Server) TelegramBackend() tgbot.Backend {
	return &telegramBackend{s: s}
}

func (b *telegramBackend) UserByTelegramID(ctx context.Context, telegramID int64) (*tgbot.User, error) {
	u, err := b.s.users.GetByTelegramID(ctx, telegramID)
	if err != nil {
		return nil, err
	}
	return telegramUser(u), nil
}

func telegramUser(u *auth.User) *tgbot.User {
	return &tgbot.User{
		ID: u.ID, Login: u.Login, DisplayName: u.DisplayName,
		Role: u.Role, Disabled: u.Disabled, TelegramID: u.TelegramID,
	}
}

func telegramLangKey(userID int64) string {
	return fmt.Sprintf("telegram.lang.%d", userID)
}

func (b *telegramBackend) Language(ctx context.Context, userID int64) string {
	return b.s.users.GetSetting(ctx, telegramLangKey(userID), "")
}

func (b *telegramBackend) SetLanguage(ctx context.Context, userID int64, lang string) error {
	if lang != "ru" && lang != "en" {
		return errors.New("unsupported language")
	}
	return b.s.users.SetSetting(ctx, telegramLangKey(userID), lang)
}

func (b *telegramBackend) Search(ctx context.Context, query string, limit, offset int) ([]tgbot.Book, error) {
	books, err := b.s.st.SearchBooks(ctx, query, limit, offset)
	return telegramBooks(books), err
}

func (b *telegramBackend) SearchTitles(ctx context.Context, query string, limit, offset int) ([]tgbot.Book, error) {
	books, err := b.s.st.SearchTitlesPage(ctx, query, limit, offset)
	return telegramBooks(books), err
}

func (b *telegramBackend) SearchAuthors(ctx context.Context, query string, limit int) ([]tgbot.Author, error) {
	rows, err := b.s.st.SearchAuthors(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]tgbot.Author, 0, len(rows))
	for _, a := range rows {
		out = append(out, tgbot.Author{ID: a.ID, Name: a.Name, Books: a.Books})
	}
	return out, nil
}

func (b *telegramBackend) AuthorBooks(ctx context.Context, authorID int64, limit, offset int) ([]tgbot.Book, string, error) {
	books, name, err := b.s.st.AuthorBooks(ctx, authorID)
	if err != nil {
		return nil, "", err
	}
	return telegramBooks(pageSlice(books, limit, offset)), name, nil
}

func (b *telegramBackend) SearchSeries(ctx context.Context, query string, limit int) ([]tgbot.Series, error) {
	rows, err := b.s.st.SearchSeries(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]tgbot.Series, 0, len(rows))
	for _, s := range rows {
		out = append(out, tgbot.Series{ID: s.ID, Title: s.Title, Books: s.Books})
	}
	return out, nil
}

func (b *telegramBackend) SeriesBooks(ctx context.Context, seriesID int64, limit, offset int) ([]tgbot.Book, string, error) {
	books, title, err := b.s.st.SeriesBooks(ctx, seriesID)
	if err != nil {
		return nil, "", err
	}
	return telegramBooks(pageSlice(books, limit, offset)), title, nil
}

func pageSlice(books []store.Book, limit, offset int) []store.Book {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(books) {
		return nil
	}
	books = books[offset:]
	if limit > 0 && len(books) > limit {
		books = books[:limit]
	}
	return books
}

func telegramBooks(books []store.Book) []tgbot.Book {
	out := make([]tgbot.Book, 0, len(books))
	for _, b := range books {
		out = append(out, tgbot.Book{
			ID: b.ID, Title: b.Title, Authors: b.AuthorNames,
			Series: b.SeriesTitle, SeqNumber: b.SeqNumber, Year: b.Year,
			LibRate: b.LibRate, Ext: b.Ext, Size: b.Size,
		})
	}
	return out
}

func (b *telegramBackend) Book(ctx context.Context, userID, bookID int64, lang string) (*tgbot.Book, error) {
	d, err := b.s.st.BookDetails(ctx, bookID)
	if err != nil {
		return nil, err
	}
	book := &tgbot.Book{
		ID: d.ID, Title: d.Title, Authors: d.AuthorNames,
		Series: d.SeriesTitle, SeqNumber: d.SeqNumber, Year: d.Year,
		LibRate: d.LibRate, Ext: d.Ext, Size: d.Size,
		Lang: d.Lang, ISBN: d.ISBN,
	}
	for _, code := range d.Genres {
		name := d.GenreNames[code]
		if name == "" {
			name = genres.NameLang(code, lang)
		}
		if name != "" {
			book.Genres = append(book.Genres, name)
		}
	}

	if b.s.lib != nil {
		if meta, err := b.s.lib.Meta(d.Folder, d.File, d.Ext); err == nil {
			book.Annotation = meta.AnnotationHTML
			if book.Lang == "" {
				book.Lang = meta.Lang
			}
			if book.ISBN == "" {
				book.ISBN = meta.ISBN
			}
			if book.Year == 0 {
				fmt.Sscanf(strings.TrimSpace(meta.Year), "%d", &book.Year)
			}
		}
	}

	if avg, count, err := b.s.users.BookRating(ctx, d.ID); err == nil && count > 0 {
		book.HasRating = true
		book.Rating = avg
		book.RatingCount = count
	}
	if p, err := b.s.users.GetProgress(ctx, userID, d.ID); err == nil {
		book.HasProgress = true
		book.Progress = p.Overall
		if p.Overall >= 0.98 {
			book.IsFinished = true
			book.Progress = 1
		}
	}

	enabled := b.s.enrichEnabledCtx(ctx)
	b.s.enrich.SetGoogleAPIKey(b.s.users.GetSetting(ctx, "enrich.google_api_key", ""))
	if cached := b.s.enrich.Cached(strconv.FormatInt(d.ID, 10), enabled); cached != nil {
		for _, src := range cached.Sources {
			if src.Rating <= 0 {
				continue
			}
			book.External = append(book.External, tgbot.ExternalRating{
				Source: src.Source, Rating: src.Rating, RatingMax: src.RatingMax, Count: src.RatingsCount,
			})
		}
	}
	return book, nil
}

func (b *telegramBackend) Cover(ctx context.Context, bookID int64) ([]byte, string, error) {
	if b.s.lib == nil {
		return nil, "", errors.New("library is not configured")
	}
	f, err := b.s.st.BookFile(ctx, bookID)
	if err != nil {
		return nil, "", err
	}
	return b.s.lib.Cover(f.ID, f.Folder, f.File, f.Ext)
}

func (b *telegramBackend) Recent(ctx context.Context, limit, offset int) ([]tgbot.Book, error) {
	books, _, err := b.s.st.ShelfBooks(ctx, "newest", limit, offset)
	return telegramBooks(books), err
}

func (b *telegramBackend) Random(ctx context.Context, userID int64, lang string) (*tgbot.Book, error) {
	count, err := b.s.st.BookCount(ctx)
	if err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, store.ErrNotFound
	}
	for i := 0; i < 5; i++ {
		books, _, err := b.s.st.ShelfBooks(ctx, "newest", 1, rand.IntN(count))
		if err != nil {
			return nil, err
		}
		if len(books) == 1 {
			return b.Book(ctx, userID, books[0].ID, lang)
		}
	}
	return nil, store.ErrNotFound
}

func (b *telegramBackend) Stats(ctx context.Context) (*tgbot.Stats, error) {
	books, err := b.s.st.BookCount(ctx)
	if err != nil {
		return nil, err
	}
	users, err := b.s.users.UsersCount(ctx)
	if err != nil {
		return nil, err
	}
	stats := &tgbot.Stats{
		Books: books, Users: users, Version: b.s.cfg.Version,
		Uptime: time.Since(b.s.startedAt),
	}
	path := b.s.cfg.DataDir
	if path == "" {
		path = b.s.cfg.LibraryDir
	}
	if total, free, ok := diskSpace(path); ok && total > 0 {
		stats.DiskOK = true
		stats.DiskFree = free
		stats.DiskUsed = total - free
	}
	return stats, nil
}

var statusPathRe = regexp.MustCompile(`(?:[A-Za-z]:)?[\\/][^\s:]+(?:[\\/][^\s:]+)*`)

// cleanStatusError keeps failures useful for an administrator without
// exposing absolute paths or other internal file-system details.
func cleanStatusError(s string) string {
	return strings.TrimSpace(statusPathRe.ReplaceAllString(s, "<path>"))
}

func (b *telegramBackend) Status(ctx context.Context) (*tgbot.Status, error) {
	var status tgbot.Status
	b.s.imp.mu.Lock()
	status.Import = tgbot.ImportStatus{
		Running: b.s.imp.running, Phase: b.s.imp.phase,
		Processed: b.s.imp.processed, Books: b.s.imp.stats.Books,
		Error: b.s.imp.err, Warning: b.s.imp.warning,
	}
	b.s.imp.mu.Unlock()

	if b.s.conv != nil {
		jobs, err := b.s.conv.List(ctx, 200)
		if err != nil {
			return nil, err
		}
		for _, job := range jobs {
			switch conversions.Status(job.Status) {
			case conversions.StatusQueued:
				status.Conversions.Queued++
			case conversions.StatusProcessing:
				status.Conversions.Processing++
			case conversions.StatusDone:
				status.Conversions.Done++
			case conversions.StatusFailed:
				status.Conversions.Failed++
				if job.Error != "" && len(status.Conversions.RecentErrors) < 5 {
					status.Conversions.RecentErrors = append(status.Conversions.RecentErrors, filepath.Base(job.Name)+": "+cleanStatusError(job.Error))
				}
			case conversions.StatusSkipped:
				status.Conversions.Skipped++
			}
		}
	}
	return &status, nil
}

func (b *telegramBackend) UploadLimit(ctx context.Context) int {
	return b.s.telegramUploadRateLimit(ctx)
}

func (b *telegramBackend) Upload(ctx context.Context, userID int64, filename string, r io.Reader, size int64, lang string) (*tgbot.UploadResult, error) {
	res := b.s.ingestReader(ctx, filename, r, false, lang)
	out := &tgbot.UploadResult{
		BookID: res.BookID, Title: res.Title, Authors: res.Authors, Error: res.Error,
	}
	switch {
	case res.Status == "queued":
		out.Status = tgbot.UploadQueued
		if out.Title == "" {
			out.Title = filename
		}
	case res.Duplicate != nil || res.NeedConfirm:
		out.Status = tgbot.UploadDuplicate
		if res.Duplicate != nil {
			out.DuplicateID = res.Duplicate.BookID
			out.DuplicateTitle = res.Duplicate.Title
			out.DuplicateWhy = res.Duplicate.Reason
		}
	case res.BookID > 0:
		out.Status = tgbot.UploadAdded
	default:
		out.Status = tgbot.UploadError
	}
	return out, nil
}
