package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestSyncMergeDropsUnknownBookIDs(t *testing.T) {
	f := newFullServer(t, "required")
	ctx := context.Background()
	reader, err := f.users.GetByLogin(ctx, "reader")
	if err != nil {
		t.Fatal(err)
	}
	body := `{
		"progress":[
			{"bookId":` + itoa64(f.bookID) + `,"chapter":1,"position":0.1,"overall":0.1,"updatedAt":"2026-11-08 12:00:00.000"},
			{"bookId":999999,"chapter":2,"position":0.2,"overall":0.2,"updatedAt":"2026-11-08 12:00:00.000"}
		],
		"ratings":[
			{"bookId":` + itoa64(f.bookID) + `,"rating":5,"updatedAt":"2026-11-08 12:00:00.000"},
			{"bookId":999999,"rating":4,"updatedAt":"2026-11-08 12:00:00.000"}
		],
		"lists":[
			{"name":"foreign","books":[
				{"bookId":` + itoa64(f.bookID) + `,"addedAt":"2026-11-08 12:00:00.000"},
				{"bookId":999999,"addedAt":"2026-11-08 12:00:00.000"}
			]}
		]
	}`
	resp := do(t, f.reader, http.MethodPost, f.ts.URL+"/api/v1/sync/state", body)
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sync merge -> %d: %s", resp.StatusCode, data)
	}

	if p, err := f.users.GetProgress(ctx, reader.ID, f.bookID); err != nil || p.Chapter != 1 {
		t.Errorf("known progress not merged: %+v, %v", p, err)
	}
	if _, err := f.users.GetProgress(ctx, reader.ID, 999999); err == nil {
		t.Error("progress for unknown book id was stored")
	}
	if r := f.users.UserRating(ctx, reader.ID, f.bookID); r != 5 {
		t.Errorf("known rating = %d, want 5", r)
	}
	if r := f.users.UserRating(ctx, reader.ID, 999999); r != 0 {
		t.Errorf("rating for unknown book id was stored: %d", r)
	}
	lists, err := f.users.Lists(ctx, reader.ID)
	if err != nil {
		t.Fatal(err)
	}
	var foreignID int64
	for _, l := range lists {
		if l.Name == "foreign" {
			foreignID = l.ID
		}
	}
	if foreignID == 0 {
		t.Fatal("foreign list was not created")
	}
	ids, err := f.users.ListBookIDs(ctx, reader.ID, foreignID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != f.bookID {
		t.Errorf("list books = %v, want only known id %d", ids, f.bookID)
	}

	// Also verify the JSON response is valid for clients.
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil || out["ok"] != true {
		t.Errorf("unexpected sync response: %s", data)
	}
}
