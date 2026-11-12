package library

import (
	"archive/zip"
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestEPUBPlainText(t *testing.T) {
	data := epubZip(t, false, true)
	text, err := EPUBPlainText(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Hello epub world") {
		t.Errorf("plain text: %q", text)
	}
	if strings.Contains(text, "<p>") || strings.Contains(text, "<b>") {
		t.Errorf("markup leaked into plain text: %q", text)
	}
}

func minimalEPUB(t *testing.T, chapter string) []byte {
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
	add("OPS/content.opf", `<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="2.0"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Self-closing title</dc:title><dc:language>ru</dc:language></metadata><manifest><item id="ch1" href="ch1.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="ch1"/></spine></package>`)
	add("OPS/style.css", "body { margin: 0; }")
	add("OPS/ch1.xhtml", chapter)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestEPUBTextSelfClosingTitle(t *testing.T) {
	data := minimalEPUB(t, `<?xml version="1.0" encoding="UTF-8"?><html xmlns="http://www.w3.org/1999/xhtml"><head><title/><link rel="stylesheet" href="style.css"/></head><body><p>Hello self-closing title</p></body></html>`)
	text, err := EPUBText(bytes.NewReader(data), int64(len(data)), func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if len(text.Chapters) != 1 {
		t.Fatalf("chapters = %d, want 1", len(text.Chapters))
	}
	if !strings.Contains(text.Chapters[0].HTML, "Hello self-closing title") {
		t.Errorf("chapter HTML = %q", text.Chapters[0].HTML)
	}

	plain, err := EPUBPlainText(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plain, "Hello self-closing title") {
		t.Errorf("plain text = %q", plain)
	}
}

func TestEPUBTextSelfClosingTitleWithAttributes(t *testing.T) {
	data := minimalEPUB(t, `<?xml version="1.0" encoding="UTF-8"?><html xmlns="http://www.w3.org/1999/xhtml"><head><title id="t" data-x="a > b"/><script type="text/javascript" data-y='c > d'/></head><body><p>Hello attributed title</p></body></html>`)
	text, err := EPUBText(bytes.NewReader(data), int64(len(data)), func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if len(text.Chapters) != 1 || !strings.Contains(text.Chapters[0].HTML, "Hello attributed title") {
		t.Fatalf("chapters = %+v", text.Chapters)
	}
}

func TestEPUBTextSelfClosingPlaintext(t *testing.T) {
	data := minimalEPUB(t, `<?xml version="1.0" encoding="UTF-8"?><html xmlns="http://www.w3.org/1999/xhtml"><head><title/></head><body><p>Before plaintext</p><plaintext data-x="a > b"/><p>After plaintext</p></body></html>`)
	text, err := EPUBText(bytes.NewReader(data), int64(len(data)), func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if len(text.Chapters) != 1 {
		t.Fatalf("chapters = %d, want 1", len(text.Chapters))
	}
	if !strings.Contains(text.Chapters[0].HTML, "<p>Before plaintext</p>") ||
		!strings.Contains(text.Chapters[0].HTML, "<p>After plaintext</p>") {
		t.Errorf("chapter HTML = %q", text.Chapters[0].HTML)
	}
}

func TestEPUBTextOnRealFile(t *testing.T) {
	path := os.Getenv("EPUB")
	if path == "" {
		t.Skip("set EPUB env to a real file")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, _ := f.Stat()
	text, err := EPUBText(f, st.Size(), func(p string) string { return "/img/" + p })
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("chapters: %d, first title: %q", len(text.Chapters), text.Chapters[0].Title)
	if len(text.Chapters) < 3 {
		t.Errorf("too few chapters: %d", len(text.Chapters))
	}
	all := ""
	for _, ch := range text.Chapters {
		all += ch.HTML
	}
	for _, bad := range []string{"<script", "<style", "onclick="} {
		if strings.Contains(strings.ToLower(all), bad) {
			t.Errorf("unsanitized %q in output", bad)
		}
	}
}
