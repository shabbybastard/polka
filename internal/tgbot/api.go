package tgbot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Telegram Bot API types (only the fields used by this bot).

type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

type Message struct {
	MessageID int64     `json:"message_id"`
	From      *TGUser   `json:"from,omitempty"`
	Chat      Chat      `json:"chat"`
	Text      string    `json:"text,omitempty"`
	Caption   string    `json:"caption,omitempty"`
	Document  *Document `json:"document,omitempty"`
}

type TGUser struct {
	ID           int64  `json:"id"`
	IsBot        bool   `json:"is_bot"`
	FirstName    string `json:"first_name,omitempty"`
	LastName     string `json:"last_name,omitempty"`
	Username     string `json:"username,omitempty"`
	LanguageCode string `json:"language_code,omitempty"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type,omitempty"`
}

type Document struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	FileSize int64  `json:"file_size,omitempty"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    *TGUser  `json:"from"`
	Message *Message `json:"message,omitempty"`
	Data    string   `json:"data,omitempty"`
}

type File struct {
	FileID   string `json:"file_id"`
	FilePath string `json:"file_path,omitempty"`
	FileSize int64  `json:"file_size,omitempty"`
}

type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
}

type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

type KeyboardButton struct {
	Text string `json:"text"`
}

type ReplyKeyboardMarkup struct {
	Keyboard       [][]KeyboardButton `json:"keyboard"`
	ResizeKeyboard bool               `json:"resize_keyboard"`
	IsPersistent   bool               `json:"is_persistent"`
}

type SendOptions struct {
	Inline *InlineKeyboardMarkup
	Reply  *ReplyKeyboardMarkup
}

// API is the subset of the Telegram Bot API used by the bot and handy for
// deterministic tests.
type API interface {
	GetUpdates(ctx context.Context, offset int64, timeout int) ([]Update, error)
	SendMessage(ctx context.Context, chatID int64, text string, opts SendOptions) (Message, error)
	EditMessageText(ctx context.Context, chatID, messageID int64, text string, kb *InlineKeyboardMarkup) error
	SendPhoto(ctx context.Context, chatID int64, data []byte, filename, caption string) error
	AnswerCallbackQuery(ctx context.Context, id, text string, alert bool) error
	GetFile(ctx context.Context, fileID string) (*File, error)
	DownloadFile(ctx context.Context, filePath string, dst io.Writer, limit int64) (int64, error)
}

// ErrFileTooLarge is returned by DownloadFile when Telegram's declared or
// actual size exceeds the caller's limit.
var ErrFileTooLarge = errors.New("telegram file exceeds the size limit")

type APIError struct {
	Code        int
	Description string
}

func (e *APIError) Error() string {
	if e.Description == "" {
		return fmt.Sprintf("telegram api error %d", e.Code)
	}
	return fmt.Sprintf("telegram api: %s", e.Description)
}

type Client struct {
	token    string
	http     *http.Client
	base     string
	fileBase string
}

func NewClient(token string) *Client {
	return &Client{
		token:    token,
		http:     &http.Client{Timeout: 60 * time.Second},
		base:     "https://api.telegram.org/bot" + token + "/",
		fileBase: "https://api.telegram.org/file/bot" + token + "/",
	}
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
}

func (c *Client) call(ctx context.Context, method string, payload any) (json.RawMessage, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+method, bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("telegram request construction failed")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, errors.New("telegram request failed")
	}
	defer resp.Body.Close()
	var out apiResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return nil, errors.New("telegram response is not valid JSON")
	}
	if !out.OK {
		return nil, &APIError{Code: out.ErrorCode, Description: out.Description}
	}
	return out.Result, nil
}

func (c *Client) GetUpdates(ctx context.Context, offset int64, timeout int) ([]Update, error) {
	raw, err := c.call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         timeout,
		"allowed_updates": []string{"message", "callback_query"},
	})
	if err != nil {
		return nil, err
	}
	var updates []Update
	if err := json.Unmarshal(raw, &updates); err != nil {
		return nil, errors.New("telegram updates are invalid")
	}
	return updates, nil
}

func (c *Client) SendMessage(ctx context.Context, chatID int64, text string, opts SendOptions) (Message, error) {
	payload := map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"disable_web_page_preview": true,
	}
	if opts.Inline != nil {
		payload["reply_markup"] = opts.Inline
	} else if opts.Reply != nil {
		payload["reply_markup"] = opts.Reply
	}
	raw, err := c.call(ctx, "sendMessage", payload)
	if err != nil {
		return Message{}, err
	}
	var msg Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		return Message{}, errors.New("telegram message response is invalid")
	}
	return msg, nil
}

func (c *Client) EditMessageText(ctx context.Context, chatID, messageID int64, text string, kb *InlineKeyboardMarkup) error {
	payload := map[string]any{
		"chat_id":                  chatID,
		"message_id":               messageID,
		"text":                     text,
		"disable_web_page_preview": true,
	}
	if kb != nil {
		payload["reply_markup"] = kb
	}
	_, err := c.call(ctx, "editMessageText", payload)
	return err
}

func (c *Client) AnswerCallbackQuery(ctx context.Context, id, text string, alert bool) error {
	payload := map[string]any{"callback_query_id": id, "show_alert": alert}
	if text != "" {
		payload["text"] = text
	}
	_, err := c.call(ctx, "answerCallbackQuery", payload)
	return err
}

func (c *Client) GetFile(ctx context.Context, fileID string) (*File, error) {
	raw, err := c.call(ctx, "getFile", map[string]any{"file_id": fileID})
	if err != nil {
		return nil, err
	}
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, errors.New("telegram file response is invalid")
	}
	return &f, nil
}

func (c *Client) SendPhoto(ctx context.Context, chatID int64, data []byte, filename, caption string) error {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
		return err
	}
	if caption != "" {
		if err := mw.WriteField("caption", caption); err != nil {
			return err
		}
	}
	part, err := mw.CreateFormFile("photo", filename)
	if err != nil {
		return err
	}
	if _, err := part.Write(data); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"sendPhoto", &body)
	if err != nil {
		return errors.New("telegram request construction failed")
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := c.http.Do(req)
	if err != nil {
		return errors.New("telegram request failed")
	}
	defer resp.Body.Close()
	var out apiResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return errors.New("telegram response is not valid JSON")
	}
	if !out.OK {
		return &APIError{Code: out.ErrorCode, Description: out.Description}
	}
	return nil
}

func (c *Client) DownloadFile(ctx context.Context, filePath string, dst io.Writer, limit int64) (int64, error) {
	parts := strings.Split(strings.TrimPrefix(filePath, "/"), "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	u := c.fileBase + strings.Join(parts, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, errors.New("telegram file request failed")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, errors.New("telegram file download failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, &APIError{Code: resp.StatusCode, Description: "file download failed"}
	}
	n, err := io.Copy(dst, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return n, err
	}
	if n > limit {
		return n, ErrFileTooLarge
	}
	return n, nil
}
