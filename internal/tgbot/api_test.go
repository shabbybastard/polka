package tgbot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPIClientCalls(t *testing.T) {
	var gotMethod string
	var gotPayload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/docs/a.fb2" {
			w.Write([]byte("file-content"))
			return
		}
		gotMethod = strings.TrimPrefix(r.URL.Path, "/")
		gotPayload = map[string]any{}
		json.NewDecoder(r.Body).Decode(&gotPayload)
		switch gotMethod {
		case "getUpdates":
			w.Write([]byte(`{"ok":true,"result":[{"update_id":5,"message":{"message_id":1,"chat":{"id":7},"text":"/start"}}]}`))
		case "sendMessage":
			w.Write([]byte(`{"ok":true,"result":{"message_id":2,"chat":{"id":7}}}`))
		case "getFile":
			w.Write([]byte(`{"ok":true,"result":{"file_id":"f","file_path":"docs/a.fb2","file_size":3}}`))
		default:
			w.Write([]byte(`{"ok":true,"result":true}`))
		}
	}))
	defer srv.Close()

	c := &Client{token: "t", http: srv.Client(), base: srv.URL + "/", fileBase: srv.URL + "/"}
	updates, err := c.GetUpdates(context.Background(), 1, 30)
	if err != nil || len(updates) != 1 || updates[0].UpdateID != 5 {
		t.Fatalf("updates: %+v %v", updates, err)
	}
	if gotMethod != "getUpdates" || gotPayload["allowed_updates"] == nil {
		t.Errorf("payload: %s %+v", gotMethod, gotPayload)
	}

	kb := &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{{{Text: "go", CallbackData: "x"}}}}
	if _, err := c.SendMessage(context.Background(), 7, "<b>hello</b>", SendOptions{Inline: kb, ParseMode: "HTML"}); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "sendMessage" || gotPayload["text"] != "<b>hello</b>" || gotPayload["reply_markup"] == nil || gotPayload["parse_mode"] != "HTML" {
		t.Errorf("send payload: %s %+v", gotMethod, gotPayload)
	}
	if _, err := c.SendMessage(context.Background(), 7, "plain", SendOptions{RemoveKeyboard: true}); err != nil {
		t.Fatal(err)
	}
	markup, _ := gotPayload["reply_markup"].(map[string]any)
	if markup == nil || markup["remove_keyboard"] != true {
		t.Errorf("remove keyboard payload: %+v", gotPayload)
	}
	if err := c.SetMyCommands(context.Background(), []BotCommand{{Command: "start", Description: "Start"}}, "en"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "setMyCommands" || gotPayload["language_code"] != "en" || gotPayload["commands"] == nil {
		t.Errorf("commands payload: %s %+v", gotMethod, gotPayload)
	}
	if err := c.SetChatMenuButton(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "setChatMenuButton" || gotPayload["menu_button"] == nil {
		t.Errorf("menu button payload: %s %+v", gotMethod, gotPayload)
	}
	if err := c.DeleteMessage(context.Background(), 7, 42); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "deleteMessage" || gotPayload["message_id"] != float64(42) {
		t.Errorf("delete payload: %s %+v", gotMethod, gotPayload)
	}

	f, err := c.GetFile(context.Background(), "f")
	if err != nil || f.FilePath != "docs/a.fb2" {
		t.Fatalf("file: %+v %v", f, err)
	}
	var dst bytes.Buffer
	n, err := c.DownloadFile(context.Background(), f.FilePath, &dst, 20)
	if err != nil || n != int64(len("file-content")) {
		t.Fatalf("download: %d %v", n, err)
	}
}

func TestAPIClientErrorsDoNotEchoToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"ok":false,"error_code":401,"description":"Unauthorized"}`))
	}))
	defer srv.Close()
	c := &Client{token: "secret-token", http: srv.Client(), base: srv.URL + "/"}
	_, err := c.SendMessage(context.Background(), 1, "x", SendOptions{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != 401 {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatal("error leaked the bot token")
	}
}
