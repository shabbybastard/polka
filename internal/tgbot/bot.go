package tgbot

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	pageSize        = 5
	maxUploadBytes  = 20 << 20
	maxPhotoBytes   = 10 << 20
	maxTextRunes    = 4000
	callbackTimeout = 20 * time.Second
)

var supportedExt = map[string]bool{
	"fb2": true, "epub": true, "pdf": true, "txt": true, "mobi": true, "azw3": true,
}

// activeTokens prevents two bot loops from polling with the same token in
// one process. Telegram itself also rejects a second getUpdates consumer.
var activeTokens sync.Map

type session struct {
	awaiting string
	mode     string
	query    string
	page     int
	authorID int64
	seriesID int64
}

type Bot struct {
	api     API
	backend Backend
	log     *slog.Logger

	mu       sync.Mutex
	sessions map[int64]*session
	uploads  map[int64][]time.Time
	menus    map[int64][]int64 // bot messages that contain inline menus
}

func New(api API, backend Backend, log *slog.Logger) *Bot {
	if log == nil {
		log = slog.Default()
	}
	return &Bot{
		api:      api,
		backend:  backend,
		log:      log,
		sessions: make(map[int64]*session),
		uploads:  make(map[int64][]time.Time),
		menus:    make(map[int64][]int64),
	}
}

// Run starts the long-polling loop. An empty token is a no-op so Polka can
// keep working exactly as before when Telegram is not configured.
func Run(ctx context.Context, token string, backend Backend, log *slog.Logger) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	botID, secret, ok := strings.Cut(token, ":")
	if !ok || strings.TrimSpace(botID) == "" || strings.TrimSpace(secret) == "" {
		return errors.New("invalid telegram bot token")
	}
	if _, loaded := activeTokens.LoadOrStore(token, struct{}{}); loaded {
		return errors.New("telegram bot is already running for this token")
	}
	defer activeTokens.Delete(token)
	return New(NewClient(token), backend, log).Run(ctx)
}

// Run polls until the context is cancelled. It deliberately uses long
// polling rather than a webhook, so no public URL is required.
func (b *Bot) Run(ctx context.Context) error {
	menuCtx, cancelMenu := context.WithTimeout(ctx, 10*time.Second)
	b.configureMenu(menuCtx)
	cancelMenu()
	var offset int64
	for {
		if ctx.Err() != nil {
			return nil
		}
		updates, err := b.api.GetUpdates(ctx, offset, 30)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			b.log.Warn("telegram getUpdates", "error", err)
			if !sleepCtx(ctx, 3*time.Second) {
				return nil
			}
			continue
		}
		for _, update := range updates {
			offset = update.UpdateID + 1
			b.handleUpdate(ctx, update)
		}
	}
}

// configureMenu populates Telegram's built-in menu button (the side command
// list in clients). The default language is Russian; English clients get the
// localized command descriptions through language_code=en.
func (b *Bot) configureMenu(ctx context.Context) {
	if err := b.api.SetChatMenuButton(ctx); err != nil {
		b.log.Warn("telegram menu button", "error", err)
	}
	if err := b.api.SetMyCommands(ctx, botCommands("ru"), ""); err != nil {
		b.log.Warn("telegram commands", "error", err)
	}
	if err := b.api.SetMyCommands(ctx, botCommands("en"), "en"); err != nil {
		b.log.Warn("telegram commands", "error", err)
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (b *Bot) handleUpdate(ctx context.Context, u Update) {
	defer func() {
		if recovered := recover(); recovered != nil {
			b.log.Error("telegram update panic", "error", recovered)
		}
	}()
	if u.CallbackQuery != nil {
		b.handleCallback(ctx, u.CallbackQuery)
		return
	}
	if u.Message != nil {
		b.handleMessage(ctx, u.Message)
	}
}

func (b *Bot) handleMessage(ctx context.Context, msg *Message) {
	if msg.From == nil {
		return
	}
	if msg.Chat.Type != "" && msg.Chat.Type != "private" {
		b.sendText(ctx, msg.Chat.ID, b.tr(langFromUser(msg.From), "privateOnly"), SendOptions{})
		return
	}
	user, lang, ok := b.authorize(ctx, msg.From)
	if !ok {
		b.sendText(ctx, msg.Chat.ID, b.tr(lang, "accessDenied", msg.From.ID), SendOptions{})
		return
	}
	if msg.Document != nil {
		b.handleDocument(ctx, msg, user, lang)
		return
	}

	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}
	if strings.HasPrefix(text, "/") {
		b.handleCommand(ctx, msg, user, lang, text)
		return
	}
	if kind := b.takeAwaiting(msg.Chat.ID); kind != "" {
		b.runQuery(ctx, msg.Chat.ID, 0, lang, kind, text, false)
		return
	}
	b.sendText(ctx, msg.Chat.ID, b.tr(lang, "unknown"), SendOptions{})
}

func (b *Bot) authorize(ctx context.Context, from *TGUser) (*User, string, bool) {
	fallback := langFromUser(from)
	if from == nil {
		return nil, fallback, false
	}
	u, err := b.backend.UserByTelegramID(ctx, from.ID)
	if err != nil || u == nil || u.Disabled {
		return nil, fallback, false
	}
	lang := b.backend.Language(ctx, u.ID)
	if lang != "ru" && lang != "en" {
		lang = fallback
	}
	return u, lang, true
}

func langFromUser(u *TGUser) string {
	if u != nil && strings.HasPrefix(strings.ToLower(strings.TrimSpace(u.LanguageCode)), "ru") {
		return "ru"
	}
	return "en"
}

func (b *Bot) handleCommand(ctx context.Context, msg *Message, user *User, lang, text string) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return
	}
	cmd := strings.ToLower(strings.SplitN(fields[0], "@", 2)[0])
	arg := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
	b.clearAwaiting(msg.Chat.ID)
	// Any explicit command starts a fresh screen: drop stale inline keyboards
	// left by previous searches or menus.
	b.clearMenus(ctx, msg.Chat.ID)

	switch cmd {
	case "/start":
		// The global menu lives in Telegram's side command list, configured
		// with setMyCommands/setChatMenuButton when the bot starts. Removing
		// the old persistent keyboard upgrades existing chats seamlessly.
		b.sendText(ctx, msg.Chat.ID, b.tr(lang, "start"), SendOptions{RemoveKeyboard: true})
	case "/help":
		b.sendText(ctx, msg.Chat.ID, b.tr(lang, "help"), SendOptions{})
	case "/search", "/title", "/author", "/series":
		kind := strings.TrimPrefix(cmd, "/")
		if arg == "" {
			b.setAwaiting(msg.Chat.ID, kind)
			b.sendText(ctx, msg.Chat.ID, b.tr(lang, kind+"Prompt"), SendOptions{})
			return
		}
		b.runQuery(ctx, msg.Chat.ID, 0, lang, kind, arg, false)
	case "/random":
		b.sendRandom(ctx, msg.Chat.ID, user.ID, lang)
	case "/recent":
		b.runQuery(ctx, msg.Chat.ID, 0, lang, "recent", "", false)
	case "/stats":
		if !user.IsAdmin() {
			b.sendText(ctx, msg.Chat.ID, b.tr(lang, "adminOnly"), SendOptions{})
			return
		}
		b.sendStats(ctx, msg.Chat.ID, lang)
	case "/status":
		if !user.IsAdmin() {
			b.sendText(ctx, msg.Chat.ID, b.tr(lang, "adminOnly"), SendOptions{})
			return
		}
		b.sendStatus(ctx, msg.Chat.ID, lang)
	case "/id":
		b.sendID(ctx, msg.Chat.ID, user, lang)
	case "/language":
		b.sendLanguageMenu(ctx, msg.Chat.ID, lang)
	case "/cancel":
		b.sendText(ctx, msg.Chat.ID, b.tr(lang, "cancelled"), SendOptions{})
	default:
		b.sendText(ctx, msg.Chat.ID, b.tr(lang, "unknown"), SendOptions{})
	}
}

func (b *Bot) handleCallback(ctx context.Context, cq *CallbackQuery) {
	if cq.From == nil {
		return
	}
	if cq.Message != nil && cq.Message.Chat.Type != "" && cq.Message.Chat.Type != "private" {
		b.answer(ctx, cq.ID, b.tr(langFromUser(cq.From), "privateOnly"), true)
		return
	}
	user, lang, ok := b.authorize(ctx, cq.From)
	if !ok {
		text := truncateRunes(b.tr(lang, "accessDenied", cq.From.ID), 190)
		b.answer(ctx, cq.ID, text, true)
		return
	}
	b.answer(ctx, cq.ID, "", false)
	data := cq.Data
	messageID := int64(0)
	var chatID int64
	if cq.Message != nil {
		chatID = cq.Message.Chat.ID
		messageID = cq.Message.MessageID
	}

	switch {
	case data == "menu:main":
		b.editOrSend(ctx, chatID, messageID, cq, b.tr(lang, "start"), nil)
	case data == "menu:search":
		b.setAwaiting(chatID, "search")
		b.editOrSend(ctx, chatID, messageID, cq, b.tr(lang, "searchPrompt"), nil)
	case data == "menu:random":
		b.sendRandom(ctx, chatID, user.ID, lang)
	case data == "menu:recent":
		b.runQuery(ctx, chatID, messageID, lang, "recent", "", true)
	case data == "menu:stats":
		if !user.IsAdmin() {
			b.sendText(ctx, chatID, b.tr(lang, "adminOnly"), SendOptions{})
			return
		}
		b.sendStats(ctx, chatID, lang)
	case data == "menu:status":
		if !user.IsAdmin() {
			b.sendText(ctx, chatID, b.tr(lang, "adminOnly"), SendOptions{})
			return
		}
		b.sendStatus(ctx, chatID, lang)
	case data == "menu:lang":
		b.editOrSend(ctx, chatID, messageID, cq, b.tr(lang, "languageMenu"), b.languageMenu())
	case data == "menu:help":
		b.editOrSend(ctx, chatID, messageID, cq, b.tr(lang, "help"), nil)
	case strings.HasPrefix(data, "lang:"):
		next := strings.TrimPrefix(data, "lang:")
		if next != "ru" && next != "en" {
			return
		}
		if err := b.backend.SetLanguage(ctx, user.ID, next); err != nil {
			b.log.Warn("telegram language", "error", err)
			return
		}
		lang = next
		b.editOrSend(ctx, chatID, messageID, cq, b.tr(lang, "languageSaved", languageTitle(lang)), nil)
	case strings.HasPrefix(data, "book:"):
		id, err := strconv.ParseInt(strings.TrimPrefix(data, "book:"), 10, 64)
		if err == nil {
			b.sendBook(ctx, chatID, user.ID, lang, id)
		}
	case strings.HasPrefix(data, "author:"):
		id, err := strconv.ParseInt(strings.TrimPrefix(data, "author:"), 10, 64)
		if err == nil {
			b.showAuthorBooks(ctx, chatID, messageID, cq, lang, id, 0, false)
		}
	case strings.HasPrefix(data, "series:"):
		id, err := strconv.ParseInt(strings.TrimPrefix(data, "series:"), 10, 64)
		if err == nil {
			b.showSeriesBooks(ctx, chatID, messageID, cq, lang, id, 0, false)
		}
	case strings.HasPrefix(data, "pg:"):
		page, err := strconv.Atoi(strings.TrimPrefix(data, "pg:"))
		if err == nil && page >= 0 {
			b.navigate(ctx, chatID, messageID, cq, lang, page)
		}
	case strings.HasPrefix(data, "menu:"):
		// Unknown menu action: leave the current screen untouched.
	}
}

func (b *Bot) navigate(ctx context.Context, chatID, messageID int64, cq *CallbackQuery, lang string, page int) {
	s := b.getSession(chatID)
	if s == nil || s.mode == "" {
		return
	}
	switch s.mode {
	case "author":
		b.showAuthorBooks(ctx, chatID, messageID, cq, lang, s.authorID, page, true)
	case "series":
		b.showSeriesBooks(ctx, chatID, messageID, cq, lang, s.seriesID, page, true)
	default:
		b.runQuery(ctx, chatID, messageID, lang, s.mode, s.query, true, page)
	}
}

func (b *Bot) runQuery(ctx context.Context, chatID, messageID int64, lang, kind, query string, edit bool, pages ...int) {
	page := 0
	if len(pages) > 0 {
		page = pages[0]
	}
	query = truncateRunes(strings.TrimSpace(query), 256)
	switch kind {
	case "author":
		b.searchAuthors(ctx, chatID, messageID, lang, query, edit)
	case "series":
		b.searchSeries(ctx, chatID, messageID, lang, query, edit)
	default:
		b.showSearch(ctx, chatID, messageID, lang, kind, query, page, edit)
	}
}

func (b *Bot) showSearch(ctx context.Context, chatID, messageID int64, lang, kind, query string, page int, edit bool) {
	var (
		books []Book
		err   error
	)
	switch kind {
	case "title":
		books, err = b.backend.SearchTitles(ctx, query, pageSize+1, page*pageSize)
	case "recent":
		books, err = b.backend.Recent(ctx, pageSize+1, page*pageSize)
	default:
		books, err = b.backend.Search(ctx, query, pageSize+1, page*pageSize)
	}
	if err != nil {
		b.log.Warn("telegram search", "error", err)
		b.reply(ctx, chatID, messageID, edit, b.tr(lang, "noResults"), nil)
		return
	}
	hasMore := len(books) > pageSize
	if hasMore {
		books = books[:pageSize]
	}
	if len(books) == 0 && page == 0 {
		b.reply(ctx, chatID, messageID, edit, b.tr(lang, "noResults"), nil)
		return
	}
	b.setSession(chatID, &session{mode: kind, query: query, page: page})
	title := ""
	switch kind {
	case "title":
		title = b.tr(lang, "titleTitle", query)
	case "recent":
		title = b.tr(lang, "recentTitle")
	default:
		title = b.tr(lang, "searchTitle", query)
	}
	b.reply(ctx, chatID, messageID, edit, b.renderBooks(lang, title, books, page, hasMore), b.listKeyboard(lang, books, page, hasMore))
}

func (b *Bot) searchAuthors(ctx context.Context, chatID, messageID int64, lang, query string, edit bool) {
	authors, err := b.backend.SearchAuthors(ctx, query, pageSize+1)
	if err != nil {
		b.log.Warn("telegram authors", "error", err)
		b.reply(ctx, chatID, messageID, edit, b.tr(lang, "noResults"), nil)
		return
	}
	if len(authors) == 1 {
		b.showAuthorBooks(ctx, chatID, messageID, nil, lang, authors[0].ID, 0, edit)
		return
	}
	if len(authors) == 0 {
		b.reply(ctx, chatID, messageID, edit, b.tr(lang, "noResults"), nil)
		return
	}
	var lines []string
	var rows [][]InlineKeyboardButton
	for i, a := range authors {
		if i >= pageSize {
			break
		}
		lines = append(lines, fmt.Sprintf("%d. %s (%d)", i+1, a.Name, a.Books))
		rows = append(rows, []InlineKeyboardButton{{
			Text:         truncateRunes(a.Name, 50),
			CallbackData: "author:" + strconv.FormatInt(a.ID, 10),
		}})
	}
	lines = append(lines, b.tr(lang, "hintRefine"))
	rows = append(rows, b.menuRow(lang))
	b.reply(ctx, chatID, messageID, edit, b.tr(lang, "authorsTitle", query)+"\n\n"+strings.Join(lines, "\n"), &InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) searchSeries(ctx context.Context, chatID, messageID int64, lang, query string, edit bool) {
	series, err := b.backend.SearchSeries(ctx, query, pageSize+1)
	if err != nil {
		b.log.Warn("telegram series", "error", err)
		b.reply(ctx, chatID, messageID, edit, b.tr(lang, "noResults"), nil)
		return
	}
	if len(series) == 1 {
		b.showSeriesBooks(ctx, chatID, messageID, nil, lang, series[0].ID, 0, edit)
		return
	}
	if len(series) == 0 {
		b.reply(ctx, chatID, messageID, edit, b.tr(lang, "noResults"), nil)
		return
	}
	var lines []string
	var rows [][]InlineKeyboardButton
	for i, s := range series {
		if i >= pageSize {
			break
		}
		lines = append(lines, fmt.Sprintf("%d. %s (%d)", i+1, s.Title, s.Books))
		rows = append(rows, []InlineKeyboardButton{{
			Text:         truncateRunes(s.Title, 50),
			CallbackData: "series:" + strconv.FormatInt(s.ID, 10),
		}})
	}
	lines = append(lines, b.tr(lang, "hintRefine"))
	rows = append(rows, b.menuRow(lang))
	b.reply(ctx, chatID, messageID, edit, b.tr(lang, "seriesTitle", query)+"\n\n"+strings.Join(lines, "\n"), &InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) showAuthorBooks(ctx context.Context, chatID, messageID int64, cq *CallbackQuery, lang string, authorID int64, page int, edit bool) {
	books, name, err := b.backend.AuthorBooks(ctx, authorID, pageSize+1, page*pageSize)
	if err != nil {
		b.log.Warn("telegram author books", "error", err)
		b.reply(ctx, chatID, messageID, edit, b.tr(lang, "noResults"), nil)
		return
	}
	if len(books) == 0 {
		b.reply(ctx, chatID, messageID, edit, b.tr(lang, "noResults"), nil)
		return
	}
	hasMore := len(books) > pageSize
	if hasMore {
		books = books[:pageSize]
	}
	b.setSession(chatID, &session{mode: "author", authorID: authorID, page: page})
	kb := b.listKeyboard(lang, books, page, hasMore)
	if edit && cq != nil {
		b.editOrSend(ctx, chatID, messageID, cq, b.renderBooks(lang, b.tr(lang, "booksAuthor", name), books, page, hasMore), kb)
		return
	}
	b.reply(ctx, chatID, messageID, edit, b.renderBooks(lang, b.tr(lang, "booksAuthor", name), books, page, hasMore), kb)
}

func (b *Bot) showSeriesBooks(ctx context.Context, chatID, messageID int64, cq *CallbackQuery, lang string, seriesID int64, page int, edit bool) {
	books, title, err := b.backend.SeriesBooks(ctx, seriesID, pageSize+1, page*pageSize)
	if err != nil {
		b.log.Warn("telegram series books", "error", err)
		b.reply(ctx, chatID, messageID, edit, b.tr(lang, "noResults"), nil)
		return
	}
	if len(books) == 0 {
		b.reply(ctx, chatID, messageID, edit, b.tr(lang, "noResults"), nil)
		return
	}
	hasMore := len(books) > pageSize
	if hasMore {
		books = books[:pageSize]
	}
	b.setSession(chatID, &session{mode: "series", seriesID: seriesID, page: page})
	kb := b.listKeyboard(lang, books, page, hasMore)
	if edit && cq != nil {
		b.editOrSend(ctx, chatID, messageID, cq, b.renderBooks(lang, b.tr(lang, "booksSeries", title), books, page, hasMore), kb)
		return
	}
	b.reply(ctx, chatID, messageID, edit, b.renderBooks(lang, b.tr(lang, "booksSeries", title), books, page, hasMore), kb)
}

func (b *Bot) renderBooks(lang, title string, books []Book, page int, hasMore bool) string {
	var sb strings.Builder
	sb.WriteString(title)
	sb.WriteString("\n")
	sb.WriteString(b.tr(lang, "page", page+1))
	sb.WriteString("\n\n")
	for i, book := range books {
		sb.WriteString(fmt.Sprintf("%d. %s", i+1, book.Title))
		if book.Authors != "" {
			sb.WriteString(" — ")
			sb.WriteString(book.Authors)
		}
		if book.Year > 0 {
			sb.WriteString(fmt.Sprintf(" (%d)", book.Year))
		}
		sb.WriteString("\n")
	}
	_ = hasMore
	return strings.TrimRight(sb.String(), "\n")
}

func (b *Bot) listKeyboard(lang string, books []Book, page int, hasMore bool) *InlineKeyboardMarkup {
	var rows [][]InlineKeyboardButton
	for i, book := range books {
		label := fmt.Sprintf("%d. %s", i+1, truncateRunes(book.Title, 48))
		rows = append(rows, []InlineKeyboardButton{{
			Text:         label,
			CallbackData: "book:" + strconv.FormatInt(book.ID, 10),
		}})
	}
	var nav []InlineKeyboardButton
	if page > 0 {
		nav = append(nav, InlineKeyboardButton{Text: "‹ " + b.tr(lang, "prev"), CallbackData: fmt.Sprintf("pg:%d", page-1)})
	}
	if hasMore {
		nav = append(nav, InlineKeyboardButton{Text: b.tr(lang, "next") + " ›", CallbackData: fmt.Sprintf("pg:%d", page+1)})
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows, b.menuRow(lang))
	return &InlineKeyboardMarkup{InlineKeyboard: rows}
}

func (b *Bot) sendRandom(ctx context.Context, chatID, userID int64, lang string) {
	book, err := b.backend.Random(ctx, userID, lang)
	if err != nil || book == nil {
		b.sendText(ctx, chatID, b.tr(lang, "noResults"), SendOptions{})
		return
	}
	b.sendBookData(ctx, chatID, lang, book)
}

func (b *Bot) sendBook(ctx context.Context, chatID, userID int64, lang string, bookID int64) {
	book, err := b.backend.Book(ctx, userID, bookID, lang)
	if err != nil || book == nil {
		b.sendText(ctx, chatID, b.tr(lang, "bookNotFound"), SendOptions{})
		return
	}
	b.sendBookData(ctx, chatID, lang, book)
}

func (b *Bot) sendBookData(ctx context.Context, chatID int64, lang string, book *Book) {
	// The cover and the complete card are one Telegram message. Captions are
	// limited to 1024 characters, so the annotation is truncated to fit.
	if cover, mime, err := b.backend.Cover(ctx, book.ID); err == nil && len(cover) > 0 && len(cover) <= maxPhotoBytes {
		ext := "jpg"
		switch {
		case strings.Contains(mime, "png"):
			ext = "png"
		case strings.Contains(mime, "webp"):
			ext = "webp"
		}
		caption := b.bookCardHTML(lang, book, 1000)
		if err := b.api.SendPhoto(ctx, chatID, cover, fmt.Sprintf("cover-%d.%s", book.ID, ext), PhotoOptions{
			Caption: caption, ParseMode: "HTML",
		}); err == nil {
			return
		} else {
			b.log.Warn("telegram cover", "book", book.ID, "error", err)
		}
	}
	// No usable cover: still one formatted message, with a larger budget.
	b.sendText(ctx, chatID, b.bookCardHTML(lang, book, 3900), SendOptions{ParseMode: "HTML"})
}

// bookCardHTML renders the card with Telegram HTML formatting. budget is the
// total character budget: 1024 for a photo caption, 4096 for plain text.
func (b *Bot) bookCardHTML(lang string, book *Book, budget int) string {
	parts := make([]string, 0, 14)
	used := 0
	add := func(line string) {
		if line == "" {
			return
		}
		extra := runeLen(line)
		if len(parts) > 0 {
			extra++
		}
		if used+extra > budget {
			return
		}
		parts = append(parts, line)
		used += extra
	}
	label := func(key, value string) string {
		return "<b>" + escHTML(b.tr(lang, key)) + ":</b> " + escHTML(value)
	}

	add("<b>" + escHTML(truncateRunes(book.Title, 180)) + "</b>")
	if book.Authors != "" {
		add("<i>" + escHTML(truncateRunes(book.Authors, 200)) + "</i>")
	}
	if book.Series != "" {
		series := book.Series
		if book.SeqNumber > 0 {
			series += fmt.Sprintf(" #%d", book.SeqNumber)
		}
		add(label("series", truncateRunes(series, 140)))
	}
	add(label("bookID", strconv.FormatInt(book.ID, 10)))
	if len(book.Genres) > 0 {
		add(label("genres", truncateRunes(strings.Join(book.Genres, ", "), 220)))
	}
	if book.Lang != "" {
		add(label("language", book.Lang))
	}
	if book.Ext != "" {
		fmtLine := strings.ToUpper(book.Ext)
		if book.Size > 0 {
			fmtLine += " · " + formatBytes(book.Size)
		}
		add(label("format", fmtLine))
	}
	if book.HasRating {
		value := formatRating(book.Rating, 5, lang)
		if book.RatingCount > 0 {
			value += fmt.Sprintf(" (%s)", b.tr(lang, "ratingCount", book.RatingCount))
		}
		add(label("rating", value))
	} else {
		add(label("rating", b.tr(lang, "noRating")))
	}
	switch {
	case book.IsFinished:
		add("<b>" + escHTML(b.tr(lang, "finished")) + "</b>")
	case book.HasProgress:
		add(fmt.Sprintf(b.tr(lang, "reading"), book.Progress*100))
	default:
		add(escHTML(b.tr(lang, "notStarted")))
	}
	for _, ext := range book.External {
		if ext.Rating <= 0 {
			continue
		}
		value := formatRating(ext.Rating, ext.RatingMax, lang)
		if ext.Count > 0 {
			value += fmt.Sprintf(" (%s)", b.tr(lang, "ratingCount", ext.Count))
		}
		add(label(externalSourceName(ext.Source), value))
	}
	if annotation := strings.TrimSpace(plainText(book.Annotation)); annotation != "" {
		header := "\n<b>" + escHTML(b.tr(lang, "annotation")) + ":</b>\n"
		remaining := budget - used - runeLen(header) - 2
		if remaining > 80 {
			add(header + escHTML(truncateRunes(annotation, remaining)))
		}
	}
	return strings.Join(parts, "\n")
}

func (b *Bot) sendID(ctx context.Context, chatID int64, user *User, lang string) {
	role := b.tr(lang, "roleUser")
	if user.IsAdmin() {
		role = b.tr(lang, "roleAdmin")
	}
	text := strings.Join([]string{
		b.tr(lang, "idTitle"),
		"",
		b.tr(lang, "idLogin", user.Login),
		b.tr(lang, "idRole", role),
		b.tr(lang, "idUserID", user.ID),
		b.tr(lang, "idTelegram", user.TelegramID),
	}, "\n")
	b.sendText(ctx, chatID, text, SendOptions{})
}

func (b *Bot) sendStats(ctx context.Context, chatID int64, lang string) {
	stats, err := b.backend.Stats(ctx)
	if err != nil || stats == nil {
		b.sendText(ctx, chatID, b.tr(lang, "unknown"), SendOptions{})
		return
	}
	free, used := b.tr(lang, "statsDiskNA"), b.tr(lang, "statsDiskNA")
	if stats.DiskOK {
		free, used = formatBytes(stats.DiskFree), formatBytes(stats.DiskUsed)
	}
	text := fmt.Sprintf(b.tr(lang, "statsText"),
		stats.Books, stats.Users, stats.Version, formatDuration(stats.Uptime), used, free)
	b.sendText(ctx, chatID, text, SendOptions{})
}

func (b *Bot) sendStatus(ctx context.Context, chatID int64, lang string) {
	status, err := b.backend.Status(ctx)
	if err != nil || status == nil {
		b.sendText(ctx, chatID, b.tr(lang, "unknown"), SendOptions{})
		return
	}
	imp := status.Import
	importLine := b.tr(lang, "statusIdle")
	switch {
	case imp.Running:
		importLine = b.tr(lang, "statusRun", imp.Phase, imp.Processed)
	case imp.Error != "":
		importLine = b.tr(lang, "statusErr", truncateRunes(imp.Error, 200))
	case imp.Phase == "done":
		importLine = b.tr(lang, "statusDone", imp.Books)
	}
	errs := status.Conversions.RecentErrors
	errText := b.tr(lang, "noErrors")
	if len(errs) > 0 {
		clean := make([]string, 0, len(errs))
		for _, e := range errs {
			clean = append(clean, "- "+truncateRunes(plainText(e), 180))
		}
		errText = strings.Join(clean, "\n")
	}
	text := fmt.Sprintf(b.tr(lang, "statusText"),
		importLine,
		status.Conversions.Queued,
		status.Conversions.Processing,
		status.Conversions.Failed,
		errText,
	)
	b.sendText(ctx, chatID, text, SendOptions{})
}

func (b *Bot) sendLanguageMenu(ctx context.Context, chatID int64, lang string) {
	b.sendText(ctx, chatID, b.tr(lang, "languageMenu"), SendOptions{Inline: b.languageMenu()})
}

func (b *Bot) languageMenu() *InlineKeyboardMarkup {
	return &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
		{{Text: "Русский", CallbackData: "lang:ru"}, {Text: "English", CallbackData: "lang:en"}},
	}}
}

func languageTitle(lang string) string {
	if lang == "ru" {
		return "Русский"
	}
	return "English"
}

func (b *Bot) menuRow(lang string) []InlineKeyboardButton {
	return []InlineKeyboardButton{{Text: "‹ " + b.tr(lang, "home"), CallbackData: "menu:main"}}
}

func (b *Bot) sendText(ctx context.Context, chatID int64, text string, opts SendOptions) {
	if strings.TrimSpace(text) == "" {
		return
	}
	if opts.Inline != nil {
		b.clearMenus(ctx, chatID)
	}
	chunks := splitText(text, maxTextRunes)
	for i, chunk := range chunks {
		o := SendOptions{}
		if i == len(chunks)-1 {
			o = opts
		}
		msg, err := b.api.SendMessage(ctx, chatID, chunk, o)
		if err != nil {
			b.log.Warn("telegram send", "error", err)
			return
		}
		if i == len(chunks)-1 && opts.Inline != nil {
			b.trackMenu(chatID, msg.MessageID)
		}
	}
}

func (b *Bot) reply(ctx context.Context, chatID, messageID int64, edit bool, text string, kb *InlineKeyboardMarkup) {
	if edit && messageID > 0 && len([]rune(text)) <= maxTextRunes {
		if err := b.api.EditMessageText(ctx, chatID, messageID, text, kb); err == nil {
			return
		}
	}
	b.sendText(ctx, chatID, text, SendOptions{Inline: kb})
}

func (b *Bot) editOrSend(ctx context.Context, chatID, messageID int64, cq *CallbackQuery, text string, kb *InlineKeyboardMarkup) {
	if cq != nil && messageID > 0 && cq.Message != nil && cq.Message.Text != "" && len([]rune(text)) <= maxTextRunes {
		if err := b.api.EditMessageText(ctx, chatID, messageID, text, kb); err == nil {
			return
		}
	}
	b.sendText(ctx, chatID, text, SendOptions{Inline: kb})
}

func (b *Bot) answer(ctx context.Context, id, text string, alert bool) {
	if err := b.api.AnswerCallbackQuery(ctx, id, text, alert); err != nil {
		b.log.Warn("telegram callback", "error", err)
	}
}

func splitText(s string, max int) []string {
	if len([]rune(s)) <= max {
		return []string{s}
	}
	var out []string
	var cur strings.Builder
	count := 0
	for _, r := range s {
		if count >= max {
			out = append(out, cur.String())
			cur.Reset()
			count = 0
		}
		cur.WriteRune(r)
		count++
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func (b *Bot) setAwaiting(chatID int64, kind string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[chatID]
	if s == nil {
		s = &session{}
		b.sessions[chatID] = s
	}
	s.awaiting = kind
}

func (b *Bot) takeAwaiting(chatID int64) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[chatID]
	if s == nil || s.awaiting == "" {
		return ""
	}
	kind := s.awaiting
	s.awaiting = ""
	return kind
}

func (b *Bot) clearAwaiting(chatID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s := b.sessions[chatID]; s != nil {
		s.awaiting = ""
	}
}

func (b *Bot) setSession(chatID int64, s *session) {
	b.mu.Lock()
	b.sessions[chatID] = s
	b.mu.Unlock()
}

func (b *Bot) getSession(chatID int64) *session {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s := b.sessions[chatID]; s != nil {
		copy := *s
		return &copy
	}
	return nil
}

func (b *Bot) trackMenu(chatID, messageID int64) {
	if messageID <= 0 {
		return
	}
	b.mu.Lock()
	ids := append(b.menus[chatID], messageID)
	if len(ids) > 10 {
		ids = ids[len(ids)-10:]
	}
	b.menus[chatID] = ids
	b.mu.Unlock()
}

// clearMenus removes previous bot messages that carried inline menus, so a
// long chat does not turn into a pile of stale keyboards. Telegram allows
// bots to delete their own messages for 48 hours.
func (b *Bot) clearMenus(ctx context.Context, chatID int64) {
	b.mu.Lock()
	ids := b.menus[chatID]
	delete(b.menus, chatID)
	b.mu.Unlock()
	for _, id := range ids {
		if err := b.api.DeleteMessage(ctx, chatID, id); err != nil {
			b.log.Warn("telegram delete menu", "message", id, "error", err)
		}
	}
}

func (b *Bot) allowUpload(userID int64, limit int) (bool, time.Duration) {
	if limit <= 0 {
		limit = 50
	}
	now := time.Now()
	cutoff := now.Add(-time.Hour)
	b.mu.Lock()
	defer b.mu.Unlock()
	list := b.uploads[userID]
	kept := list[:0]
	for _, t := range list {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= limit {
		b.uploads[userID] = kept
		retry := kept[0].Add(time.Hour).Sub(now)
		if retry < 0 {
			retry = 0
		}
		return false, retry
	}
	kept = append(kept, now)
	b.uploads[userID] = kept
	return true, 0
}

func (b *Bot) handleDocument(ctx context.Context, msg *Message, user *User, lang string) {
	if !user.IsAdmin() {
		b.sendText(ctx, msg.Chat.ID, b.tr(lang, "uploadOnlyAdmin"), SendOptions{})
		return
	}
	doc := msg.Document
	filename := strings.TrimSpace(doc.FileName)
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), "."))
	if filename == "" || !supportedExt[ext] {
		b.sendText(ctx, msg.Chat.ID, b.tr(lang, "uploadUnsupported"), SendOptions{})
		return
	}
	if doc.FileSize > maxUploadBytes {
		b.sendText(ctx, msg.Chat.ID, b.tr(lang, "uploadTooLarge"), SendOptions{})
		return
	}
	limit := b.backend.UploadLimit(ctx)
	allowed, retry := b.allowUpload(user.ID, limit)
	if !allowed {
		b.sendText(ctx, msg.Chat.ID, b.tr(lang, "uploadLimitWait", formatDuration(retry)), SendOptions{})
		return
	}
	b.sendText(ctx, msg.Chat.ID, b.tr(lang, "uploadDownload"), SendOptions{})

	file, err := b.api.GetFile(ctx, doc.FileID)
	if err != nil || file == nil || file.FilePath == "" {
		b.sendText(ctx, msg.Chat.ID, b.tr(lang, "uploadError", b.tr(lang, "uploadBadContent")), SendOptions{})
		return
	}
	if file.FileSize > maxUploadBytes {
		b.sendText(ctx, msg.Chat.ID, b.tr(lang, "uploadTooLarge"), SendOptions{})
		return
	}
	tmp, err := os.CreateTemp("", "polka-tg-*."+ext)
	if err != nil {
		b.sendText(ctx, msg.Chat.ID, b.tr(lang, "uploadError", b.tr(lang, "uploadEmpty")), SendOptions{})
		return
	}
	path := tmp.Name()
	defer os.Remove(path)
	n, downloadErr := b.api.DownloadFile(ctx, file.FilePath, tmp, maxUploadBytes)
	closeErr := tmp.Close()
	if errors.Is(downloadErr, ErrFileTooLarge) {
		b.sendText(ctx, msg.Chat.ID, b.tr(lang, "uploadTooLarge"), SendOptions{})
		return
	}
	if downloadErr != nil || closeErr != nil {
		b.sendText(ctx, msg.Chat.ID, b.tr(lang, "uploadError", b.tr(lang, "uploadBadContent")), SendOptions{})
		return
	}
	if n == 0 {
		b.sendText(ctx, msg.Chat.ID, b.tr(lang, "uploadEmpty"), SendOptions{})
		return
	}
	if !validBookContent(path, ext) {
		b.sendText(ctx, msg.Chat.ID, b.tr(lang, "uploadBadContent"), SendOptions{})
		return
	}
	f, err := os.Open(path)
	if err != nil {
		b.sendText(ctx, msg.Chat.ID, b.tr(lang, "uploadError", b.tr(lang, "uploadBadContent")), SendOptions{})
		return
	}
	defer f.Close()
	res, err := b.backend.Upload(ctx, user.ID, filename, f, n, lang)
	if err != nil || res == nil {
		b.sendText(ctx, msg.Chat.ID, b.tr(lang, "uploadError", b.tr(lang, "uploadBadContent")), SendOptions{})
		return
	}
	b.sendUploadResult(ctx, msg.Chat.ID, lang, res)
}

func (b *Bot) sendUploadResult(ctx context.Context, chatID int64, lang string, res *UploadResult) {
	var text string
	switch res.Status {
	case UploadAdded:
		text = b.tr(lang, "uploadAdded", res.Title)
	case UploadDuplicate:
		title := res.DuplicateTitle
		if title == "" {
			title = res.Title
		}
		text = b.tr(lang, "uploadDuplicate", title)
	case UploadQueued:
		text = b.tr(lang, "uploadQueued", res.Title)
	default:
		msg := res.Error
		if msg == "" {
			msg = b.tr(lang, "unknown")
		}
		text = b.tr(lang, "uploadError", msg)
	}
	b.sendText(ctx, chatID, text, SendOptions{})
}

func validBookContent(path, ext string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return false
	}
	head := make([]byte, 8192)
	n, _ := io.ReadFull(f, head)
	head = head[:n]

	switch ext {
	case "fb2":
		return bytes.Contains(bytes.ToLower(head), []byte("<fictionbook"))
	case "epub":
		zr, err := zip.OpenReader(path)
		if err != nil {
			return false
		}
		defer zr.Close()
		for _, name := range zr.File {
			if strings.EqualFold(name.Name, "META-INF/container.xml") {
				return true
			}
		}
		return false
	case "pdf":
		return bytes.HasPrefix(head, []byte("%PDF-"))
	case "txt":
		if len(head) == 0 || bytes.IndexByte(head, 0) >= 0 {
			return false
		}
		printable := 0
		for _, c := range head {
			if c == '\n' || c == '\r' || c == '\t' || c >= 0x20 {
				printable++
			}
		}
		return printable*100/len(head) >= 95
	case "mobi", "azw3":
		if len(head) < 68 {
			return false
		}
		return string(head[60:68]) == "BOOKMOBI" || string(head[60:68]) == "TEXtREAd"
	}
	return false
}
