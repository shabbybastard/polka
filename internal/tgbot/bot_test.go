package tgbot

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type sentMessage struct {
	chatID int64
	text   string
	opts   SendOptions
}

type fakeAPI struct {
	mu          sync.Mutex
	sent        []sentMessage
	edits       []sentMessage
	photos      int
	photoText   string
	photoMode   string
	answers     []string
	updates     []Update
	fileData    []byte
	commands    map[string][]BotCommand
	menuButtons int
	deleted     []int64
}

func (f *fakeAPI) GetUpdates(ctx context.Context, _ int64, _ int) ([]Update, error) {
	f.mu.Lock()
	if len(f.updates) > 0 {
		out := f.updates
		f.updates = nil
		f.mu.Unlock()
		return out, nil
	}
	f.mu.Unlock()
	<-ctx.Done()
	return nil, ctx.Err()
}

func (f *fakeAPI) SendMessage(_ context.Context, chatID int64, text string, opts SendOptions) (Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sentMessage{chatID: chatID, text: text, opts: opts})
	return Message{MessageID: int64(len(f.sent)), Chat: Chat{ID: chatID}}, nil
}

func (f *fakeAPI) EditMessageText(_ context.Context, chatID, _ int64, text string, kb *InlineKeyboardMarkup) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edits = append(f.edits, sentMessage{chatID: chatID, text: text, opts: SendOptions{Inline: kb}})
	return nil
}

func (f *fakeAPI) DeleteMessage(_ context.Context, _ int64, messageID int64) error {
	f.mu.Lock()
	f.deleted = append(f.deleted, messageID)
	f.mu.Unlock()
	return nil
}

func (f *fakeAPI) SendPhoto(_ context.Context, _ int64, _ []byte, _ string, opts PhotoOptions) error {
	f.mu.Lock()
	f.photos++
	f.photoText = opts.Caption
	f.photoMode = opts.ParseMode
	f.mu.Unlock()
	return nil
}

func (f *fakeAPI) SetMyCommands(_ context.Context, commands []BotCommand, languageCode string) error {
	f.mu.Lock()
	if f.commands == nil {
		f.commands = map[string][]BotCommand{}
	}
	f.commands[languageCode] = commands
	f.mu.Unlock()
	return nil
}

func (f *fakeAPI) SetChatMenuButton(context.Context) error {
	f.mu.Lock()
	f.menuButtons++
	f.mu.Unlock()
	return nil
}

func (f *fakeAPI) AnswerCallbackQuery(_ context.Context, _, text string, _ bool) error {
	f.mu.Lock()
	f.answers = append(f.answers, text)
	f.mu.Unlock()
	return nil
}

func (f *fakeAPI) GetFile(context.Context, string) (*File, error) {
	return &File{FilePath: "documents/file", FileSize: int64(len(f.fileData))}, nil
}

func (f *fakeAPI) DownloadFile(_ context.Context, _ string, dst io.Writer, limit int64) (int64, error) {
	if int64(len(f.fileData)) > limit {
		return 0, errors.New("too large")
	}
	n, err := dst.Write(f.fileData)
	return int64(n), err
}

func (f *fakeAPI) lastText() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return ""
	}
	return f.sent[len(f.sent)-1].text
}

func (f *fakeAPI) lastOpts() SendOptions {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return SendOptions{}
	}
	return f.sent[len(f.sent)-1].opts
}

type fakeBackend struct {
	mu          sync.Mutex
	users       map[int64]*User
	langs       map[int64]string
	books       map[int64]*Book
	search      []Book
	authors     []Author
	series      []Series
	authorBooks []Book
	seriesBooks []Book
	stats       *Stats
	status      *Status
	uploadLimit int
	uploadCalls int
	uploadRes   *UploadResult
	cover       []byte
	coverMime   string
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{
		users:       map[int64]*User{},
		langs:       map[int64]string{},
		books:       map[int64]*Book{},
		uploadLimit: 50,
		stats:       &Stats{Books: 10, Users: 2, Version: "test", Uptime: time.Hour, DiskOK: true, DiskFree: 5 << 30, DiskUsed: 3 << 30},
		status:      &Status{},
	}
}

func (f *fakeBackend) UserByTelegramID(_ context.Context, id int64) (*User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u := f.users[id]
	if u == nil {
		return nil, errors.New("not found")
	}
	copy := *u
	return &copy, nil
}

func (f *fakeBackend) Language(_ context.Context, id int64) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.langs[id]
}

func (f *fakeBackend) SetLanguage(_ context.Context, id int64, lang string) error {
	f.mu.Lock()
	f.langs[id] = lang
	f.mu.Unlock()
	return nil
}

func (f *fakeBackend) Search(_ context.Context, _ string, limit, offset int) ([]Book, error) {
	return sliceBooks(f.search, limit, offset), nil
}

func (f *fakeBackend) SearchTitles(_ context.Context, _ string, limit, offset int) ([]Book, error) {
	return sliceBooks(f.search, limit, offset), nil
}

func sliceBooks(books []Book, limit, offset int) []Book {
	if offset >= len(books) {
		return nil
	}
	books = books[offset:]
	if limit > 0 && len(books) > limit {
		books = books[:limit]
	}
	return books
}

func (f *fakeBackend) SearchAuthors(context.Context, string, int) ([]Author, error) {
	return f.authors, nil
}

func (f *fakeBackend) AuthorBooks(_ context.Context, _ int64, limit, offset int) ([]Book, string, error) {
	return sliceBooks(f.authorBooks, limit, offset), "Толстой Лев", nil
}

func (f *fakeBackend) SearchSeries(context.Context, string, int) ([]Series, error) {
	return f.series, nil
}

func (f *fakeBackend) SeriesBooks(_ context.Context, _ int64, limit, offset int) ([]Book, string, error) {
	return sliceBooks(f.seriesBooks, limit, offset), "Классика", nil
}

func (f *fakeBackend) Book(_ context.Context, _ int64, id int64, _ string) (*Book, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b := f.books[id]
	if b == nil {
		return nil, errors.New("not found")
	}
	copy := *b
	return &copy, nil
}

func (f *fakeBackend) Cover(context.Context, int64) ([]byte, string, error) {
	if len(f.cover) == 0 {
		return nil, "", errors.New("no cover")
	}
	return f.cover, f.coverMime, nil
}

func (f *fakeBackend) Recent(_ context.Context, limit, offset int) ([]Book, error) {
	return sliceBooks(f.search, limit, offset), nil
}

func (f *fakeBackend) Random(context.Context, int64, string) (*Book, error) {
	for _, b := range f.books {
		copy := *b
		return &copy, nil
	}
	return nil, errors.New("empty")
}

func (f *fakeBackend) Stats(context.Context) (*Stats, error)   { return f.stats, nil }
func (f *fakeBackend) Status(context.Context) (*Status, error) { return f.status, nil }
func (f *fakeBackend) UploadLimit(context.Context) int         { return f.uploadLimit }

func (f *fakeBackend) Upload(_ context.Context, _ int64, _ string, r io.Reader, _ int64, _ string) (*UploadResult, error) {
	io.Copy(io.Discard, r)
	f.mu.Lock()
	f.uploadCalls++
	res := f.uploadRes
	f.mu.Unlock()
	if res == nil {
		return &UploadResult{Status: UploadAdded, BookID: 1, Title: "Книга"}, nil
	}
	copy := *res
	return &copy, nil
}

func testBot(t *testing.T) (*Bot, *fakeAPI, *fakeBackend, *User) {
	t.Helper()
	api := &fakeAPI{}
	backend := newFakeBackend()
	user := &User{ID: 7, Login: "reader", Role: "user", TelegramID: 555}
	backend.users[555] = user
	bot := New(api, backend, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return bot, api, backend, user
}

func msg(chatID int64, from int64, text string) *Message {
	return &Message{
		MessageID: 1,
		From:      &TGUser{ID: from, LanguageCode: "en"},
		Chat:      Chat{ID: chatID},
		Text:      text,
	}
}

func TestStartUsesSideMenu(t *testing.T) {
	bot, api, _, _ := testBot(t)
	bot.handleUpdate(context.Background(), Update{Message: msg(100, 555, "/start")})
	if len(api.sent) != 1 {
		t.Fatalf("start messages = %d, want 1", len(api.sent))
	}
	if api.sent[0].opts.Inline != nil || api.sent[0].opts.Reply != nil {
		t.Fatalf("start must not attach an inline/reply menu: %+v", api.sent[0].opts)
	}
	if !api.sent[0].opts.RemoveKeyboard {
		t.Error("start must remove the old reply keyboard")
	}

	bot.configureMenu(context.Background())
	api.mu.Lock()
	defer api.mu.Unlock()
	if api.menuButtons != 1 {
		t.Errorf("chat menu button calls = %d", api.menuButtons)
	}
	if len(api.commands[""]) == 0 || api.commands[""][0].Command != "start" {
		t.Errorf("default commands = %+v", api.commands[""])
	}
	if len(api.commands["en"]) == 0 || api.commands["en"][0].Command != "start" {
		t.Errorf("english commands = %+v", api.commands["en"])
	}
}

func TestPreviousMenuMessagesAreDeleted(t *testing.T) {
	bot, api, _, _ := testBot(t)
	bot.handleUpdate(context.Background(), Update{Message: msg(100, 555, "/language")})
	bot.handleUpdate(context.Background(), Update{Message: msg(100, 555, "/language")})
	api.mu.Lock()
	deleted := append([]int64(nil), api.deleted...)
	api.mu.Unlock()
	if len(deleted) != 1 || deleted[0] != 1 {
		t.Fatalf("deleted menu messages = %v, want [1]", deleted)
	}
}

func TestAccessDeniedShowsTelegramID(t *testing.T) {
	bot, api, _, _ := testBot(t)
	bot.handleUpdate(context.Background(), Update{Message: msg(100, 999, "/start")})
	text := api.lastText()
	if !strings.Contains(text, "Access denied") || !strings.Contains(text, "999") {
		t.Fatalf("denial = %q", text)
	}
}

func TestDisabledUserIsDenied(t *testing.T) {
	bot, api, backend, user := testBot(t)
	user.Disabled = true
	backend.users[555] = user
	bot.handleUpdate(context.Background(), Update{Message: msg(100, 555, "/start")})
	if !strings.Contains(api.lastText(), "Access denied") {
		t.Fatalf("disabled user got %q", api.lastText())
	}
}

func TestSearchPaginationAndBookCard(t *testing.T) {
	bot, api, backend, _ := testBot(t)
	backend.langs[7] = "ru"
	for i := int64(1); i <= 6; i++ {
		backend.search = append(backend.search, Book{ID: i, Title: "Книга " + string(rune('0'+i)), Authors: "Автор"})
	}
	backend.books[2] = &Book{
		ID: 2, Title: "Книга 2", Authors: "Автор", Series: "Серия", SeqNumber: 1,
		Year: 2020, Genres: []string{"Проза"}, Lang: "ru", Ext: "fb2", Size: 2048,
		HasRating: true, Rating: 4.5, RatingCount: 10, HasProgress: true, Progress: 0.42,
		Annotation: "<p>Описание <b>книги</b>.</p>",
	}

	bot.handleUpdate(context.Background(), Update{Message: msg(100, 555, "/search война")})
	text := api.lastText()
	for i := 1; i <= 5; i++ {
		if !strings.Contains(text, "Книга") {
			t.Fatalf("search text = %q", text)
		}
	}
	kb := api.lastOpts().Inline
	if kb == nil {
		t.Fatal("no inline keyboard")
	}
	foundNext := false
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			if b.CallbackData == "pg:1" {
				foundNext = true
			}
		}
	}
	if !foundNext {
		t.Fatalf("no next page button: %+v", kb)
	}

	bot.handleUpdate(context.Background(), Update{CallbackQuery: &CallbackQuery{
		ID: "cb1", From: &TGUser{ID: 555, LanguageCode: "en"},
		Message: &Message{MessageID: 42, Chat: Chat{ID: 100}, Text: "old"},
		Data:    "pg:1",
	}})
	if len(api.edits) == 0 || !strings.Contains(api.edits[len(api.edits)-1].text, "Книга 6") {
		t.Fatalf("page 2 edit = %+v", api.edits)
	}

	bot.handleUpdate(context.Background(), Update{CallbackQuery: &CallbackQuery{
		ID: "cb2", From: &TGUser{ID: 555, LanguageCode: "en"},
		Message: &Message{MessageID: 43, Chat: Chat{ID: 100}, Text: "old"},
		Data:    "book:2",
	}})
	card := api.lastText()
	for _, want := range []string{"Книга 2", "Автор", "Серия #1", "4,5/5", "оценок: 10", "Прочитано: 42%", "Описание книги"} {
		if !strings.Contains(card, want) {
			t.Errorf("card missing %q:\n%s", want, card)
		}
	}
}

func TestBookCardIsOneFormattedMessage(t *testing.T) {
	bot, api, backend, _ := testBot(t)
	backend.langs[7] = "ru"
	book := &Book{
		ID: 1, Title: "Книга <1>", Authors: "Толстой Лев", Series: "Серия", SeqNumber: 2,
		Year: 2020, Genres: []string{"Проза", "Классика"}, Lang: "ru", Ext: "epub", Size: 4096,
		HasRating: true, Rating: 4.25, RatingCount: 8, HasProgress: true, Progress: 0.5,
		Annotation: strings.Repeat("Очень длинная аннотация. ", 200),
	}
	backend.books[1] = book
	backend.cover = []byte("cover")
	backend.coverMime = "image/png"

	bot.sendBookData(context.Background(), 100, "ru", book)
	if api.photos != 1 || len(api.sent) != 0 {
		t.Fatalf("want one photo message, photos=%d sent=%d", api.photos, len(api.sent))
	}
	if api.photoMode != "HTML" || !strings.Contains(api.photoText, "<b>Книга &lt;1&gt;</b>") {
		t.Fatalf("caption = %q mode=%q", api.photoText, api.photoMode)
	}
	if !strings.Contains(api.photoText, "4,25/5") || !strings.Contains(api.photoText, "Прочитано: 50%") {
		t.Errorf("caption lost card fields: %q", api.photoText)
	}
	if runeLen(api.photoText) > 1024 {
		t.Errorf("caption is %d chars, Telegram allows 1024", runeLen(api.photoText))
	}

	// Without a cover the whole card is still one formatted text message.
	backend.cover = nil
	bot.sendBookData(context.Background(), 100, "ru", book)
	if len(api.sent) != 1 || api.sent[0].opts.ParseMode != "HTML" {
		t.Fatalf("text card = %+v", api.sent)
	}
	if runeLen(api.sent[0].text) > maxTextRunes {
		t.Errorf("text card is too long: %d", runeLen(api.sent[0].text))
	}
}

func TestStatsAndStatusAreAdminOnly(t *testing.T) {
	bot, api, backend, user := testBot(t)
	bot.handleUpdate(context.Background(), Update{Message: msg(100, 555, "/stats")})
	if !strings.Contains(api.lastText(), "administrators only") {
		t.Fatalf("reader stats = %q", api.lastText())
	}
	user.Role = "admin"
	backend.users[555] = user
	bot.handleUpdate(context.Background(), Update{Message: msg(100, 555, "/stats")})
	if !strings.Contains(api.lastText(), "Books: 10") || !strings.Contains(api.lastText(), "5.0 GB") {
		t.Fatalf("admin stats = %q", api.lastText())
	}
	backend.status = &Status{
		Import:      ImportStatus{Running: true, Phase: "loading", Processed: 5},
		Conversions: ConversionStatus{Queued: 2, Processing: 1, Failed: 1, RecentErrors: []string{"a.fb2: broken"}},
	}
	bot.handleUpdate(context.Background(), Update{Message: msg(100, 555, "/status")})
	status := api.lastText()
	for _, want := range []string{"loading", "2 queued", "a.fb2: broken"} {
		if !strings.Contains(status, want) {
			t.Errorf("status missing %q:\n%s", want, status)
		}
	}
}

func TestLanguagePersists(t *testing.T) {
	bot, api, backend, _ := testBot(t)
	bot.handleUpdate(context.Background(), Update{Message: msg(100, 555, "/language")})
	bot.handleUpdate(context.Background(), Update{CallbackQuery: &CallbackQuery{
		ID: "lang", From: &TGUser{ID: 555, LanguageCode: "en"},
		Message: &Message{MessageID: 5, Chat: Chat{ID: 100}, Text: "old"},
		Data:    "lang:ru",
	}})
	if backend.langs[7] != "ru" {
		t.Fatalf("saved language = %q", backend.langs[7])
	}
	bot.handleUpdate(context.Background(), Update{Message: msg(100, 555, "/id")})
	if !strings.Contains(api.lastText(), "Учётная запись") {
		t.Fatalf("language not applied: %q", api.lastText())
	}
}

func TestUploadAdminAndValidation(t *testing.T) {
	bot, api, backend, user := testBot(t)
	backend.uploadRes = &UploadResult{Status: UploadAdded, BookID: 3, Title: "Новая книга"}
	api.fileData = []byte(`<?xml version="1.0"?><FictionBook><description></description></FictionBook>`)

	doc := &Message{
		MessageID: 1, From: &TGUser{ID: 555, LanguageCode: "en"}, Chat: Chat{ID: 100},
		Document: &Document{FileID: "f", FileName: "book.fb2", FileSize: int64(len(api.fileData))},
	}
	bot.handleUpdate(context.Background(), Update{Message: doc})
	if backend.uploadCalls != 0 {
		t.Fatal("reader must not upload")
	}
	if !strings.Contains(api.lastText(), "administrators only") {
		t.Fatalf("reader upload = %q", api.lastText())
	}

	user.Role = "admin"
	backend.users[555] = user
	bot.handleUpdate(context.Background(), Update{Message: doc})
	if backend.uploadCalls != 1 || !strings.Contains(api.lastText(), "Added: Новая книга") {
		t.Fatalf("admin upload calls=%d text=%q", backend.uploadCalls, api.lastText())
	}

	// Invalid extension and content never reach ingestion.
	badExt := *doc
	badExt.Document = &Document{FileID: "f", FileName: "book.exe", FileSize: 10}
	bot.handleUpdate(context.Background(), Update{Message: &badExt})
	if !strings.Contains(api.lastText(), "Unsupported format") {
		t.Fatalf("bad extension = %q", api.lastText())
	}
	api.fileData = []byte("not an fb2")
	bot.handleUpdate(context.Background(), Update{Message: doc})
	if !strings.Contains(api.lastText(), "does not look") {
		t.Fatalf("bad content = %q", api.lastText())
	}
	if backend.uploadCalls != 1 {
		t.Errorf("invalid upload reached ingestion: %d", backend.uploadCalls)
	}

	large := *doc
	large.Document = &Document{FileID: "f", FileName: "big.fb2", FileSize: maxUploadBytes + 1}
	bot.handleUpdate(context.Background(), Update{Message: &large})
	if !strings.Contains(api.lastText(), "larger than 20 MB") {
		t.Fatalf("oversized upload = %q", api.lastText())
	}
	if backend.uploadCalls != 1 {
		t.Errorf("oversized upload reached ingestion: %d", backend.uploadCalls)
	}
}

func TestUploadRateLimit(t *testing.T) {
	bot, api, backend, user := testBot(t)
	user.Role = "admin"
	backend.users[555] = user
	backend.uploadLimit = 2
	api.fileData = []byte(`<?xml version="1.0"?><FictionBook></FictionBook>`)
	doc := &Message{
		MessageID: 1, From: &TGUser{ID: 555, LanguageCode: "en"}, Chat: Chat{ID: 100},
		Document: &Document{FileID: "f", FileName: "book.fb2", FileSize: int64(len(api.fileData))},
	}
	for i := 0; i < 2; i++ {
		bot.handleUpdate(context.Background(), Update{Message: doc})
	}
	bot.handleUpdate(context.Background(), Update{Message: doc})
	if backend.uploadCalls != 2 {
		t.Fatalf("uploads = %d, want 2", backend.uploadCalls)
	}
	if !strings.Contains(api.lastText(), "limit reached") {
		t.Fatalf("limit text = %q", api.lastText())
	}
}

func TestUploadResults(t *testing.T) {
	cases := []struct {
		res  UploadResult
		want string
	}{
		{UploadResult{Status: UploadDuplicate, DuplicateTitle: "Дубль"}, "Duplicate: Дубль"},
		{UploadResult{Status: UploadQueued, Title: "Очередь"}, "Queued for conversion: Очередь"},
		{UploadResult{Status: UploadError, Error: "сломалось"}, "Error: сломалось"},
	}
	for _, tc := range cases {
		bot, api, backend, user := testBot(t)
		user.Role = "admin"
		backend.users[555] = user
		backend.uploadRes = &tc.res
		api.fileData = []byte(`<?xml version="1.0"?><FictionBook></FictionBook>`)
		bot.handleUpdate(context.Background(), Update{Message: &Message{
			From: &TGUser{ID: 555, LanguageCode: "en"}, Chat: Chat{ID: 100},
			Document: &Document{FileID: "f", FileName: "book.fb2", FileSize: 40},
		}})
		if !strings.Contains(api.lastText(), tc.want) {
			t.Errorf("%s -> %q", tc.want, api.lastText())
		}
	}
}

func TestAuthorSeriesRecentRandomAndID(t *testing.T) {
	bot, api, backend, _ := testBot(t)
	book := Book{ID: 1, Title: "Книга", Authors: "Толстой Лев"}
	backend.search = []Book{book}
	backend.books[1] = &book
	backend.authors = []Author{{ID: 3, Name: "Толстой Лев", Books: 1}}
	backend.authorBooks = []Book{book}
	backend.series = []Series{{ID: 4, Title: "Классика", Books: 1}}
	backend.seriesBooks = []Book{book}

	bot.handleUpdate(context.Background(), Update{Message: msg(100, 555, "/author Толстой")})
	if !strings.Contains(api.lastText(), "Книга") {
		t.Fatalf("author = %q", api.lastText())
	}
	bot.handleUpdate(context.Background(), Update{Message: msg(100, 555, "/series Классика")})
	if !strings.Contains(api.lastText(), "Книга") {
		t.Fatalf("series = %q", api.lastText())
	}
	bot.handleUpdate(context.Background(), Update{Message: msg(100, 555, "/recent")})
	if !strings.Contains(api.lastText(), "Recently added") {
		t.Fatalf("recent = %q", api.lastText())
	}
	bot.handleUpdate(context.Background(), Update{Message: msg(100, 555, "/random")})
	if !strings.Contains(api.lastText(), "Книга") {
		t.Fatalf("random = %q", api.lastText())
	}
	bot.handleUpdate(context.Background(), Update{Message: msg(100, 555, "/id")})
	if !strings.Contains(api.lastText(), "User ID: 7") {
		t.Fatalf("id = %q", api.lastText())
	}
}

func TestRunWithoutTokenAndCancellation(t *testing.T) {
	backend := newFakeBackend()
	if err := Run(context.Background(), "", backend, nil); err != nil {
		t.Fatalf("empty token: %v", err)
	}
	if err := Run(context.Background(), "not-a-token", backend, nil); err == nil {
		t.Fatal("invalid token must be rejected")
	}
	activeTokens.Store("1:test-token", struct{}{})
	err := Run(context.Background(), "1:test-token", backend, nil)
	activeTokens.Delete("1:test-token")
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("duplicate bot instance: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	api := &fakeAPI{}
	bot := New(api, backend, nil)
	done := make(chan error, 1)
	go func() { done <- bot.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bot did not stop after context cancellation")
	}
}
