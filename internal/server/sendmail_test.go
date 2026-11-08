package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

func fakeSMTP(t *testing.T) (host string, port int, got chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got = make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		r := bufio.NewReader(conn)
		w := func(s string) { conn.Write([]byte(s + "\r\n")) }
		w("220 fake")
		var data strings.Builder
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if inData {
				if line == ".\r\n" {
					inData = false
					w("250 OK")
					got <- data.String()
					continue
				}
				data.WriteString(line)
				continue
			}
			cmd := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				w("250-fake")
				w("250 OK")
			case strings.HasPrefix(cmd, "DATA"):
				w("354 ok")
				inData = true
			case strings.HasPrefix(cmd, "QUIT"):
				w("221 bye")
				return
			default:
				w("250 OK")
			}
		}
	}()
	h, p, _ := net.SplitHostPort(ln.Addr().String())
	port = 0
	for _, c := range p {
		port = port*10 + int(c-'0')
	}
	return h, port, got
}

func TestSendBookByEmail(t *testing.T) {
	ts, client, _ := newManageServer(t)

	resp := uploadFiles(t, client, ts.URL+"/admin/books/upload", map[string][]byte{
		"voyna.fb2": []byte(opdsFB2),
	}, nil)
	var up map[string][]uploadResult
	json.NewDecoder(resp.Body).Decode(&up)
	resp.Body.Close()
	bookID := up["results"][0].BookID

	send := func() int {
		r, err := client.Post(ts.URL+"/api/v1/books/"+itoa64(bookID)+"/send", "application/json", nil)
		if err != nil {
			t.Fatalf("send request: %v", err)
		}
		defer r.Body.Close()
		return r.StatusCode
	}

	// No SMTP configured -> 503
	if code := send(); code != http.StatusServiceUnavailable {
		t.Fatalf("send without smtp -> %d", code)
	}

	// Configure SMTP at the fake server + enable
	host, port, got := fakeSMTP(t)
	postJSON(t, client, ts.URL+"/admin/settings", map[string]any{
		"smtp": map[string]any{
			"enabled": true, "host": host, "port": port,
			"from": "lib@polka.test", "security": "none",
		},
	}).Body.Close()

	// No reader email yet -> 400
	if code := send(); code != http.StatusBadRequest {
		t.Fatalf("send without reader email -> %d", code)
	}

	// Set reader email
	postJSON(t, client, ts.URL+"/api/v1/me/reader-email", map[string]any{"email": "me@kindle.com"}).Body.Close()

	if code := send(); code != http.StatusOK {
		t.Fatalf("send -> %d, want 200", code)
	}
	select {
	case msg := <-got:
		for _, want := range []string{"To: me@kindle.com", "multipart/mixed", "base64"} {
			if !strings.Contains(msg, want) {
				t.Errorf("sent message missing %q", want)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fake smtp received nothing")
	}
}

func TestReaderEmailLimits(t *testing.T) {
	ts, client, _ := newManageServer(t)

	var prefs map[string]any
	resp, _ := client.Get(ts.URL + "/api/v1/me/reader-email")
	decode(t, resp, &prefs)
	if prefs["maxMessageBytes"].(float64) != float64(14<<20) {
		t.Errorf("maxMessageBytes = %v", prefs["maxMessageBytes"])
	}
	maxMessage := int64(14 << 20)
	wantAttachment := float64(int64(float64(maxMessage-(64<<10)) / 1.37))
	if prefs["maxAttachmentBytes"].(float64) != wantAttachment {
		t.Errorf("maxAttachmentBytes = %v, want %v", prefs["maxAttachmentBytes"], wantAttachment)
	}

	if got := code(t, client, "POST", ts.URL+"/api/v1/me/reader-email", `{"email":"not-an-email"}`); got != http.StatusBadRequest {
		t.Errorf("invalid reader email -> %d, want 400", got)
	}
	resp = postJSON(t, client, ts.URL+"/api/v1/me/reader-email", map[string]any{"email": "me@kindle.com"})
	decode(t, resp, &prefs)
	if prefs["email"] != "me@kindle.com" {
		t.Errorf("reader email = %v", prefs["email"])
	}
}

func TestSendBookOverLimit(t *testing.T) {
	ts, client, _ := newManageServer(t)

	resp := uploadFiles(t, client, ts.URL+"/admin/books/upload", map[string][]byte{
		"voyna.fb2": []byte(opdsFB2),
	}, nil)
	var up map[string][]uploadResult
	json.NewDecoder(resp.Body).Decode(&up)
	resp.Body.Close()
	bookID := up["results"][0].BookID

	postJSON(t, client, ts.URL+"/admin/settings", map[string]any{
		"smtp": map[string]any{
			"enabled": true, "host": "127.0.0.1", "port": 1,
			"from": "lib@polka.test", "security": "none",
			// Leaves only ~72 bytes for the raw attachment.
			"maxMessageBytes": 64*1024 + 100,
		},
	}).Body.Close()
	postJSON(t, client, ts.URL+"/api/v1/me/reader-email", map[string]any{"email": "me@kindle.com"}).Body.Close()

	if got := code(t, client, "POST", ts.URL+"/api/v1/books/"+itoa64(bookID)+"/send", ""); got != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized send -> %d, want 413", got)
	}
}

func TestSMTPTooLargeMapping(t *testing.T) {
	if !isSMTPTooLarge(&textproto.Error{Code: 552}) {
		t.Error("552 must map to 413")
	}
	if !isSMTPTooLarge(fmt.Errorf("DATA: %w", &textproto.Error{Code: 552, Msg: "too large"})) {
		t.Error("wrapped 552 must map to 413")
	}
	if isSMTPTooLarge(&textproto.Error{Code: 550}) {
		t.Error("550 must not map to 413")
	}
	if isSMTPTooLarge(fmt.Errorf("dial tcp 127.0.0.1:55201: connection refused")) {
		t.Error("a port/host containing 552 must not map to 413")
	}
}

func TestSecretRoundtrip(t *testing.T) {
	s := &Server{secret: make([]byte, 32)}
	for i := range s.secret {
		s.secret[i] = byte(i)
	}
	enc := s.encryptSecret("hunter2")
	if enc == "" || enc == "hunter2" {
		t.Fatal("not encrypted")
	}
	if dec := s.decryptSecret(enc); dec != "hunter2" {
		t.Errorf("roundtrip = %q", dec)
	}
}
