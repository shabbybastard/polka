package library

import (
	"archive/zip"
	"bytes"
	"testing"
)

func TestEPUBPercentEncodedPaths(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	write := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	write("META-INF/container.xml", `<?xml version="1.0"?>
<container><rootfiles><rootfile full-path="OEBPS/content.opf"/></rootfiles></container>`)
	write("OEBPS/content.opf", `<?xml version="1.0"?>
<package><metadata><title>Paths</title></metadata><manifest>
<item id="ch1" href="chapter%201.xhtml" media-type="application/xhtml+xml"/>
</manifest><spine><itemref idref="ch1"/></spine></package>`)
	write("OEBPS/chapter 1.xhtml", `<html><body><p>Hello</p><img src="img/50%25off.png"/></body></html>`)
	write("OEBPS/img/50%off.png", "PNGDATA")
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()

	var imagePath string
	text, err := EPUBText(bytes.NewReader(data), int64(len(data)), func(p string) string {
		imagePath = p
		return "/img/" + p
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(text.Chapters) != 1 || text.Chapters[0].HTML == "" {
		t.Fatalf("chapters = %+v", text.Chapters)
	}
	if imagePath != "OEBPS%2Fimg%2F50%25off.png" {
		t.Errorf("image path = %q, want OEBPS%%2Fimg%%2F50%%25off.png", imagePath)
	}
	// net/http has already unescaped PathValue; EPUBBinary must not do it
	// again, or the literal '%' breaks the lookup.
	if got, _, err := EPUBBinary(bytes.NewReader(data), int64(len(data)), "OEBPS/img/50%off.png"); err != nil || string(got) != "PNGDATA" {
		t.Errorf("EPUBBinary = %q, %v", got, err)
	}
}
