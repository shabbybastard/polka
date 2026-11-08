package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/shabbybastard/polka/internal/auth"
	"github.com/shabbybastard/polka/internal/config"
	"github.com/shabbybastard/polka/internal/store"
)

// newAuthServer — a server in auth=required mode with a single administrator.
func newAuthServer(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()

	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	users, err := auth.Open(filepath.Join(dir, "users.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { users.Close() })
	if _, err := users.CreateUser(context.Background(), "admin", "secret123", "", auth.RoleAdmin); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Auth: "required"}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	webFS := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html/>")}}
	srv := New(cfg, log, st, nil, users, nil, webFS)

	ts := httptest.NewServer(srv.Handler)
	t.Cleanup(ts.Close)
	return ts
}

func postJSON(t *testing.T, client *http.Client, url string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := client.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestBasicCacheInvalidatesOnPasswordChange(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	users, err := auth.Open(filepath.Join(dir, "users.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer users.Close()
	u, err := users.CreateUser(context.Background(), "alice", "oldpassword", "", auth.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Auth: "required"}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	webFS := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html/>")}}
	_, app := NewWithApp(cfg, log, st, nil, users, nil, webFS)

	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	if got := app.basicUser(req, "alice", "oldpassword"); got == nil {
		t.Fatal("valid Basic credentials rejected")
	}
	newPassword := "newpassword"
	if err := users.UpdateUser(context.Background(), u.ID, &newPassword, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := app.basicUser(req, "alice", "oldpassword"); got != nil {
		t.Error("old password still accepted from the Basic cache")
	}
	if got := app.basicUser(req, "alice", "newpassword"); got == nil {
		t.Error("new password rejected after a CLI-style password change")
	}
}

func TestAuthFlow(t *testing.T) {
	ts := newAuthServer(t)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	// Without login the catalog is closed, the SPA and /auth/me are open
	resp, _ := client.Get(ts.URL + "/main/getBooks/getConfig")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("getConfig without login -> %d, want 401", resp.StatusCode)
	}
	resp, _ = client.Get(ts.URL + "/")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("SPA must be open: %d", resp.StatusCode)
	}

	var me map[string]any
	resp, _ = client.Get(ts.URL + "/auth/me")
	json.NewDecoder(resp.Body).Decode(&me)
	resp.Body.Close()
	if me["user"] != nil || me["authRequired"] != true {
		t.Errorf("me before login = %v", me)
	}

	// Wrong password
	resp = postJSON(t, client, ts.URL+"/auth/login", map[string]string{"login": "admin", "password": "nope"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("bad password -> %d", resp.StatusCode)
	}

	// Login
	resp = postJSON(t, client, ts.URL+"/auth/login", map[string]string{"login": "admin", "password": "secret123"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login -> %d", resp.StatusCode)
	}

	// The catalog has opened
	resp, _ = client.Get(ts.URL + "/main/getBooks/getConfig")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("getConfig after login -> %d", resp.StatusCode)
	}

	// Admin panel: create a user
	resp = postJSON(t, client, ts.URL+"/admin/users", map[string]any{
		"login": "reader", "password": "readpass1", "role": "user",
	})
	var created map[string]any
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create user -> %d", resp.StatusCode)
	}
	readerID := int(created["user"].(map[string]any)["id"].(float64))

	// Duplicate login
	resp = postJSON(t, client, ts.URL+"/admin/users", map[string]any{"login": "reader", "password": "x1234567"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("duplicate login -> %d, want 409", resp.StatusCode)
	}

	// A regular user is not an admin
	jar2, _ := cookiejar.New(nil)
	reader := &http.Client{Jar: jar2}
	resp = postJSON(t, reader, ts.URL+"/auth/login", map[string]string{"login": "reader", "password": "readpass1"})
	resp.Body.Close()
	resp, _ = reader.Get(ts.URL + "/admin/users")
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("admin list as reader -> %d, want 403", resp.StatusCode)
	}
	resp, _ = reader.Get(ts.URL + "/main/getBooks/getConfig")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("catalog as reader -> %d", resp.StatusCode)
	}

	// Deleting a user
	resp = postJSON(t, client, ts.URL+"/admin/users/"+itoa(readerID)+"/delete", map[string]any{})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("delete user -> %d", resp.StatusCode)
	}
	// Their session died with them
	resp, _ = reader.Get(ts.URL + "/main/getBooks/getConfig")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("deleted user session -> %d, want 401", resp.StatusCode)
	}

	// The last administrator cannot be deleted
	var admins map[string]any
	resp, _ = client.Get(ts.URL + "/admin/users")
	json.NewDecoder(resp.Body).Decode(&admins)
	resp.Body.Close()
	adminID := int(admins["users"].([]any)[0].(map[string]any)["id"].(float64))
	resp = postJSON(t, client, ts.URL+"/admin/users/"+itoa(adminID)+"/delete", map[string]any{})
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("delete last admin -> %d, want 409", resp.StatusCode)
	}

	// Logout
	resp = postJSON(t, client, ts.URL+"/auth/logout", map[string]any{})
	resp.Body.Close()
	resp, _ = client.Get(ts.URL + "/main/getBooks/getConfig")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("after logout -> %d, want 401", resp.StatusCode)
	}
}

func TestBasicAuthCacheRoleInvalidation(t *testing.T) {
	ts := newAuthServer(t)
	jar, _ := cookiejar.New(nil)
	admin := &http.Client{Jar: jar}
	if resp := postJSON(t, admin, ts.URL+"/auth/login", map[string]string{"login": "admin", "password": "secret123"}); resp.StatusCode != 200 {
		t.Fatalf("login -> %d", resp.StatusCode)
	}

	// A second administrator to demote.
	if resp := postJSON(t, admin, ts.URL+"/admin/users",
		map[string]string{"login": "aide", "password": "aidepass1", "role": "admin"}); resp.StatusCode != 200 {
		t.Fatalf("create aide -> %d", resp.StatusCode)
	}

	// Warm the Basic-auth cache: an admin request authenticates.
	basic := func(client *http.Client, login, password string) int {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/admin/users", nil)
		req.SetBasicAuth(login, password)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	plain := &http.Client{Timeout: 5 * time.Second}
	if code := basic(plain, "aide", "aidepass1"); code != 200 {
		t.Fatalf("basic auth as admin -> %d, want 200", code)
	}

	// Demote the aide through the admin HTTP handler.
	var aideID float64
	resp, err := admin.Get(ts.URL + "/admin/users")
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Users []struct {
			ID    float64 `json:"id"`
			Login string  `json:"login"`
			Role  string  `json:"role"`
		} `json:"users"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for _, u := range list.Users {
		if u.Login == "aide" {
			aideID = u.ID
		}
	}
	if aideID == 0 {
		t.Fatal("aide not found in the users list")
	}

	// Demote aide (role user). The Basic cache re-reads the account on every
	// hit, so admin access must be gone immediately, without waiting for
	// the cache TTL.
	target := fmt.Sprintf("/admin/users/%d", int64(aideID))
	if resp := postJSON(t, admin, ts.URL+target, map[string]string{"role": "user"}); resp.StatusCode != 200 {
		t.Fatalf("demote -> %d", resp.StatusCode)
	}
	if code := basic(plain, "aide", "aidepass1"); code == 200 {
		t.Error("demoted administrator still authenticates via cached Basic credentials")
	}
}
