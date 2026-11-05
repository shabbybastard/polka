package tgbot

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestValidBookContent(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	fb2 := write("a.fb2", []byte(`<?xml version="1.0"?><FictionBook></FictionBook>`))
	pdf := write("a.pdf", []byte("%PDF-1.7\n..."))
	txt := write("a.txt", []byte("Обычный текст\nбез управляющих байтов.\n"))
	mobi := make([]byte, 80)
	copy(mobi[60:68], "BOOKMOBI")
	mobiPath := write("a.mobi", mobi)
	bad := write("bad.fb2", []byte("not xml"))

	epubPath := filepath.Join(dir, "a.epub")
	f, err := os.Create(epubPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("META-INF/container.xml")
	w.Write([]byte(`<?xml version="1.0"?><container/>`))
	zw.Close()
	f.Close()

	for _, tc := range []struct {
		path, ext string
		want      bool
	}{
		{fb2, "fb2", true},
		{pdf, "pdf", true},
		{txt, "txt", true},
		{mobiPath, "mobi", true},
		{epubPath, "epub", true},
		{bad, "fb2", false},
		{pdf, "txt", true}, // plain text heuristic accepts printable ASCII
	} {
		if got := validBookContent(tc.path, tc.ext); got != tc.want {
			t.Errorf("%s as %s: got %v, want %v", tc.path, tc.ext, got, tc.want)
		}
	}
}
