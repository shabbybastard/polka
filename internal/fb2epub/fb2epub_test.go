package fb2epub

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/vestigiumincaligne/polka/internal/library"
)

const tinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

const fb2Fixture = `<?xml version="1.0" encoding="UTF-8"?>
<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0" xmlns:l="http://www.w3.org/1999/xlink">
<description><title-info>
<genre>sf</genre>
<author><first-name>Лев</first-name><middle-name>Николаевич</middle-name><last-name>Толстой</last-name></author>
<book-title>Тестовая книга</book-title>
<lang>ru</lang>
<publish-info><publisher>Тестиздат</publisher><year>2024</year><isbn>978-5-00000-000-1</isbn></publish-info>
<coverpage><image l:href="#cover.png"/></coverpage>
</title-info></description>
<body>
<section>
<title><p>Глава первая</p></title>
<p>Текст со сноской<a l:href="#n1" type="note">[1]</a>.</p>
<image l:href="#pic.png"/>
</section>
<section><title><p>Глава вторая</p></title><p>Второй текст.</p></section>
</body>
<body name="notes"><section id="n1"><title><p>1</p></title><p>Текст сноски.</p></section></body>
<binary id="cover.png" content-type="image/png">` + tinyPNG + `</binary>
<binary id="pic.png" content-type="image/png">` + tinyPNG + `</binary>
</FictionBook>`

func TestConvert(t *testing.T) {
	epub, err := Convert([]byte(fb2Fixture), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(epub); err != nil {
		t.Fatalf("converted epub is invalid: %v", err)
	}
	parsed, err := library.ParseEPUB(bytes.NewReader(epub), int64(len(epub)))
	if err != nil {
		t.Fatalf("library cannot parse converted epub: %v", err)
	}
	if parsed.Title != "Тестовая книга" || parsed.Language != "ru" || len(parsed.Authors) != 1 {
		t.Errorf("parsed EPUB metadata: %+v", parsed)
	}
	readText, err := library.EPUBText(bytes.NewReader(epub), int64(len(epub)), func(id string) string { return id })
	if err != nil {
		t.Fatalf("library cannot read converted epub text: %v", err)
	}
	if len(readText.Chapters) < 2 {
		t.Errorf("reader chapters = %d, want at least 2", len(readText.Chapters))
	}
	coverData, coverMime, err := library.EPUBCover(bytes.NewReader(epub), int64(len(epub)))
	if err != nil || len(coverData) == 0 || !strings.HasPrefix(coverMime, "image/") {
		t.Errorf("reader cannot extract cover: %d bytes, %q, %v", len(coverData), coverMime, err)
	}

	zr, err := zip.NewReader(bytes.NewReader(epub), int64(len(epub)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = string(data)
	}
	if zr.File[0].Name != "mimetype" || zr.File[0].Method != zip.Store || files["mimetype"] != "application/epub+zip" {
		t.Error("mimetype entry is not first/uncompressed")
	}
	for _, want := range []string{"OEBPS/content.opf", "OEBPS/nav.xhtml", "OEBPS/toc.ncx", "OEBPS/text/cover.xhtml", "OEBPS/text/notes.xhtml"} {
		if _, ok := files[want]; !ok {
			t.Errorf("missing %s", want)
		}
	}
	if !strings.Contains(files["OEBPS/content.opf"], "<dc:title>Тестовая книга</dc:title>") {
		t.Errorf("opf metadata: %s", files["OEBPS/content.opf"])
	}
	if !strings.Contains(files["OEBPS/nav.xhtml"], "Глава первая") ||
		!strings.Contains(files["OEBPS/toc.ncx"], "Глава вторая") {
		t.Error("navigation documents do not contain chapter titles")
	}
	if !strings.Contains(files["OEBPS/text/chapter-1.xhtml"], `href="notes.xhtml#n1"`) ||
		!strings.Contains(files["OEBPS/text/chapter-1.xhtml"], `src="../images/`) {
		t.Errorf("chapter links/images: %s", files["OEBPS/text/chapter-1.xhtml"])
	}
	imageCount := 0
	for name := range files {
		if strings.HasPrefix(name, "OEBPS/images/") {
			imageCount++
		}
	}
	if imageCount != 2 {
		t.Errorf("images = %d, want 2", imageCount)
	}
}

func TestConvertInvalidFB2(t *testing.T) {
	if _, err := Convert([]byte(`<FictionBook><description>`), nil); err == nil {
		t.Fatal("invalid FB2 must fail")
	}
}
