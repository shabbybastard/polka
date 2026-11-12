// Package tgbot is an optional, isolated Telegram bot for Polka.
// It talks to Telegram over long polling and reaches Polka through the
// Backend interface, so it never duplicates search, storage or ingestion.
package tgbot

import (
	"context"
	"io"
	"time"
)

// User is the minimal account view the bot needs. It is re-read from
// users.db for every update.
type User struct {
	ID          int64
	Login       string
	DisplayName string
	Role        string
	Disabled    bool
	TelegramID  int64
}

func (u *User) IsAdmin() bool { return u != nil && u.Role == "admin" }

// Book is a list row or a full card.
type Book struct {
	ID           int64
	Title        string
	Authors      string
	Series       string
	SeqNumber    int
	Year         int
	LibRate      float64
	Ext          string
	Size         int64
	Lang         string
	ISBN         string
	Genres       []string
	Annotation   string
	HasRating    bool
	Rating       float64
	RatingCount  int
	External     []ExternalRating
	HasProgress  bool
	Progress     float64
	IsFinished   bool
	ReadingState string // "reading", "finished" or "" (not started)
}

// ExternalRating is a rating already present in Polka's enrichment cache.
type ExternalRating struct {
	Source    string
	Rating    float64
	RatingMax int
	Count     int
}

type Author struct {
	ID    int64
	Name  string
	Books int
}

type Series struct {
	ID    int64
	Title string
	Books int
}

type Stats struct {
	Books    int
	Users    int
	Version  string
	Uptime   time.Duration
	DiskOK   bool
	DiskFree int64
	DiskUsed int64
}

type ImportStatus struct {
	Running   bool
	Phase     string
	Processed int
	Books     int
	Error     string
	Warning   string
}

type ConversionStatus struct {
	Queued       int
	Processing   int
	Done         int
	Failed       int
	Skipped      int
	RecentErrors []string
}

type Status struct {
	Import      ImportStatus
	Conversions ConversionStatus
}

const (
	UploadAdded     = "added"
	UploadDuplicate = "duplicate"
	UploadQueued    = "queued"
	UploadError     = "error"
)

type UploadResult struct {
	Status         string
	BookID         int64
	JobID          string
	Title          string
	Authors        string
	Error          string
	DuplicateID    int64
	DuplicateTitle string
	DuplicateWhy   string
}

// ConversionJob is the bot's view of one background FB2 -> EPUB job.
type ConversionJob struct {
	ID     string
	Name   string
	Status string
	Error  string
	BookID int64
}

// Backend is implemented by the server package. All methods map to
// existing Polka services.
type Backend interface {
	UserByTelegramID(ctx context.Context, telegramID int64) (*User, error)
	Language(ctx context.Context, userID int64) string
	SetLanguage(ctx context.Context, userID int64, lang string) error

	Search(ctx context.Context, query string, limit, offset int) ([]Book, error)
	SearchTitles(ctx context.Context, query string, limit, offset int) ([]Book, error)
	SearchAuthors(ctx context.Context, query string, limit int) ([]Author, error)
	AuthorBooks(ctx context.Context, authorID int64, limit, offset int) ([]Book, string, error)
	SearchSeries(ctx context.Context, query string, limit int) ([]Series, error)
	SeriesBooks(ctx context.Context, seriesID int64, limit, offset int) ([]Book, string, error)
	Book(ctx context.Context, userID, bookID int64, lang string) (*Book, error)
	Cover(ctx context.Context, bookID int64) ([]byte, string, error)
	Recent(ctx context.Context, limit, offset int) ([]Book, error)
	Random(ctx context.Context, userID int64, lang string) (*Book, error)

	Stats(ctx context.Context) (*Stats, error)
	Status(ctx context.Context) (*Status, error)
	UploadLimit(ctx context.Context) int
	Upload(ctx context.Context, userID int64, filename string, r io.Reader, size int64, lang string) (*UploadResult, error)
	Conversion(ctx context.Context, jobID string) (*ConversionJob, error)
}
