package server

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shabbybastard/polka/internal/library"
	"github.com/shabbybastard/polka/internal/store"
)

func maintenanceEPUB(t *testing.T) []byte {
	return maintenanceEPUBWithChapter(t, `<?xml version="1.0" encoding="UTF-8"?><html xmlns="http://www.w3.org/1999/xhtml"><head><title/></head><body><p>Hello maintenance</p></body></html>`)
}

func maintenanceEPUBWithChapter(t *testing.T, chapter string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, data string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	add("mimetype", "application/epub+zip")
	add("META-INF/container.xml", `<?xml version="1.0"?><container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`)
	add("OPS/content.opf", `<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="2.0"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Broken</dc:title><dc:language>ru</dc:language></metadata><manifest><item id="ch1" href="ch1.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="ch1"/></spine></package>`)
	add("OPS/ch1.xhtml", chapter)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func waitMaintenance(t *testing.T, f *fullServer) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		status := getJSONWith(t, f.admin, f.ts.URL+"/admin/maintenance/epub/status")
		if status["running"] == false {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatal("maintenance job did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestEPUBMaintenanceDeepScanUnreadable(t *testing.T) {
	f := newFullServer(t, "required")
	ctx := context.Background()
	epub := maintenanceEPUBWithChapter(t, `<?xml version="1.0" encoding="UTF-8"?><html xmlns="http://www.w3.org/1999/xhtml"><head><title/></head><body></body></html>`)
	if err := os.WriteFile(filepath.Join(f.dir, "lib", "empty.epub"), epub, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.AddBook(ctx, &store.BookInput{
		Title: "Empty EPUB", Folder: ".", File: "empty", Ext: "epub",
		Size: int64(len(epub)), Added: "2026-11-12",
	}); err != nil {
		t.Fatal(err)
	}

	resp := postJSON(t, f.admin, f.ts.URL+"/admin/maintenance/epub?mode=scan&deep=1", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("scan start -> %d", resp.StatusCode)
	}
	status := waitMaintenance(t, f)
	if status["needsFix"] != float64(1) || status["unreadable"] != float64(1) || status["failed"] != float64(0) {
		t.Fatalf("deep scan status = %+v", status)
	}
}

func TestEPUBMaintenance(t *testing.T) {
	f := newFullServer(t, "required")
	ctx := context.Background()
	epub := maintenanceEPUB(t)

	barePath := filepath.Join(f.dir, "lib", "broken.epub")
	if err := os.WriteFile(barePath, epub, 0o644); err != nil {
		t.Fatal(err)
	}
	bareID, err := f.st.AddBook(ctx, &store.BookInput{
		Title: "Broken bare EPUB", Folder: ".", File: "broken", Ext: "epub",
		Size: int64(len(epub)), Added: "2026-11-12",
	})
	if err != nil {
		t.Fatal(err)
	}

	archivePath := filepath.Join(f.dir, "lib", "arch-002.zip")
	zf, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(zf)
	w, err := zw.Create("200.epub")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(epub); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zf.Close(); err != nil {
		t.Fatal(err)
	}
	archivedBefore, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.AddBook(ctx, &store.BookInput{
		Title: "Broken archived EPUB", Folder: "arch-002.zip", File: "200", Ext: "epub",
		Size: int64(len(epub)), Added: "2026-11-12",
	}); err != nil {
		t.Fatal(err)
	}

	if got := code(t, f.reader, "POST", f.ts.URL+"/admin/maintenance/epub", ""); got != http.StatusForbidden {
		t.Errorf("reader maintenance start -> %d, want 403", got)
	}
	if got := code(t, f.reader, "GET", f.ts.URL+"/admin/maintenance/epub/status", ""); got != http.StatusForbidden {
		t.Errorf("reader maintenance status -> %d, want 403", got)
	}

	resp := postJSON(t, f.admin, f.ts.URL+"/admin/maintenance/epub?mode=scan&deep=1", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("scan start -> %d", resp.StatusCode)
	}
	status := waitMaintenance(t, f)
	if status["processed"] != float64(2) || status["needsFix"] != float64(1) ||
		status["archived"] != float64(1) || status["failed"] != float64(0) {
		t.Fatalf("scan status = %+v", status)
	}

	resp = postJSON(t, f.admin, f.ts.URL+"/admin/maintenance/epub?mode=fix", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fix start -> %d", resp.StatusCode)
	}
	status = waitMaintenance(t, f)
	if status["processed"] != float64(2) || status["fixed"] != float64(1) ||
		status["archived"] != float64(1) || status["failed"] != float64(0) {
		t.Fatalf("fix status = %+v", status)
	}

	fixed, err := os.Open(barePath)
	if err != nil {
		t.Fatal(err)
	}
	st, err := fixed.Stat()
	if err != nil {
		fixed.Close()
		t.Fatal(err)
	}
	text, err := library.EPUBText(fixed, st.Size(), func(string) string { return "" })
	fixed.Close()
	if err != nil || len(text.Chapters) != 1 {
		t.Fatalf("fixed EPUB: chapters=%v err=%v", text, err)
	}
	if got := code(t, f.admin, "GET", f.ts.URL+"/api/v1/read/"+itoa64(bareID), ""); got != http.StatusOK {
		t.Errorf("read fixed book -> %d", got)
	}

	backups, err := os.ReadDir(filepath.Join(f.dir, "epub-backups", itoa64(bareID)))
	if err != nil || len(backups) == 0 {
		t.Fatalf("backup missing: %v, %v", backups, err)
	}
	bookFile, err := f.st.BookFile(ctx, bareID)
	if err != nil {
		t.Fatal(err)
	}
	if bookFile.Size != st.Size() {
		t.Errorf("catalog size = %d, file size = %d", bookFile.Size, st.Size())
	}

	archivedAfter, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(archivedBefore, archivedAfter) {
		t.Error("archived EPUB was modified")
	}
}
