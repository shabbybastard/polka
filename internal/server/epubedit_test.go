package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vestigiumincaligne/polka/internal/library"
	"github.com/vestigiumincaligne/polka/internal/store"
)

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	img.Set(1, 0, color.RGBA{G: 255, A: 255})
	img.Set(0, 1, color.RGBA{B: 255, A: 255})
	img.Set(1, 1, color.RGBA{R: 255, G: 255, A: 255})
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func bareEPUB() []byte {
	opf := `<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0">
<metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Old title</dc:title><dc:creator>Old Author</dc:creator><dc:language>en</dc:language></metadata>
<manifest><item id="ch1" href="ch1.xhtml" media-type="application/xhtml+xml"/></manifest>
<spine><itemref idref="ch1"/></spine></package>`
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	w.Write([]byte("application/epub+zip"))
	add := func(name, data string) {
		w, _ := zw.Create(name)
		w.Write([]byte(data))
	}
	add("META-INF/container.xml", `<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`)
	add("OEBPS/content.opf", opf)
	add("OEBPS/ch1.xhtml", `<html xmlns="http://www.w3.org/1999/xhtml"><body><p>Hello</p></body></html>`)
	zw.Close()
	return buf.Bytes()
}

func TestEditBookEPUB(t *testing.T) {
	ts, client, st, dir := newManageServerDir(t)
	libDir := filepath.Join(dir, "lib")
	if err := os.WriteFile(filepath.Join(libDir, "test.epub"), bareEPUB(), 0o644); err != nil {
		t.Fatal(err)
	}
	id, err := st.AddBook(context.Background(), &store.BookInput{
		Title: "Old title", Authors: []store.AuthorName{{Last: "Old", First: "Author"}},
		Folder: "", File: "test", Ext: "epub", Size: int64(len(bareEPUB())),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetBookDigest(context.Background(), strings.Repeat("a", 32), id); err != nil {
		t.Fatal(err)
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fields := map[string]string{
		"title":       "New title",
		"authors":     `[{"last":"Ivanov","first":"Ivan","middle":""}]`,
		"language":    "ru",
		"publisher":   "New Press",
		"year":        "2024",
		"isbn":        "978-5-17-080090-2",
		"description": "New annotation",
	}
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	fw, _ := mw.CreateFormFile("cover", "cover.png")
	fw.Write(tinyPNG(t))
	mw.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/admin/books/"+itoa64(id)+"/epub", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("edit epub -> %d", resp.StatusCode)
	}

	meta, err := library.ParseEPUBFile(filepath.Join(libDir, "test.epub"))
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "New title" || len(meta.Authors) != 1 || meta.Authors[0] != "Ivanov Ivan" ||
		meta.Language != "ru" || meta.Publisher != "New Press" || meta.Year != "2024" ||
		meta.ISBN != "978-5-17-080090-2" || meta.Description != "New annotation" {
		t.Errorf("edited metadata: %+v", meta)
	}
	edited, err := os.ReadFile(filepath.Join(libDir, "test.epub"))
	if err != nil {
		t.Fatal(err)
	}
	cover, mime, err := library.EPUBCover(bytes.NewReader(edited), int64(len(edited)))
	if err != nil || mime != "image/jpeg" || len(cover) == 0 {
		t.Errorf("edited cover: %d %q %v", len(cover), mime, err)
	}
	if _, err := library.EPUBPlainText(bytes.NewReader(bareEPUB()), int64(len(bareEPUB()))); err != nil {
		t.Error(err)
	}

	var title, lang, isbn string
	var year int
	if err := st.DB().QueryRow(`SELECT title, lang, year, isbn FROM books WHERE id = ?`, id).
		Scan(&title, &lang, &year, &isbn); err != nil {
		t.Fatal(err)
	}
	if title != "New title" || lang != "ru" || year != 2024 || isbn != "9785170800902" {
		t.Errorf("db after edit: %q %q %d %q", title, lang, year, isbn)
	}
	var digests int
	st.DB().QueryRow(`SELECT count(*) FROM book_digests WHERE book_id = ?`, id).Scan(&digests)
	if digests != 0 {
		t.Errorf("old book digest survived edit: %d", digests)
	}

	backups, err := os.ReadDir(filepath.Join(dir, "epub-backups", itoa64(id)))
	if err != nil || len(backups) != 1 {
		t.Errorf("backup: %v %v", backups, err)
	}

	var form map[string]any
	resp, _ = client.Get(ts.URL + "/main/getBooks/getBookForm?selectedItemID=" + itoa64(id))
	json.NewDecoder(resp.Body).Decode(&form)
	resp.Body.Close()
	if form["publisher"] != "New Press" || form["year"] != "2024" || form["language"] != "ru" {
		t.Errorf("book form metadata: %v", form)
	}
}
