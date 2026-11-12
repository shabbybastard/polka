package library

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestScanAndNormalizeEPUB(t *testing.T) {
	data := minimalEPUB(t, `<?xml version="1.0" encoding="UTF-8"?><html xmlns="http://www.w3.org/1999/xhtml"><head><title/></head><body><p>Hello maintenance</p></body></html>`)
	changed, err := ScanEPUB(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("ScanEPUB did not detect a self-closing title")
	}

	var out bytes.Buffer
	changed, err = NormalizeEPUB(bytes.NewReader(data), int64(len(data)), &out)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("NormalizeEPUB did not report a change")
	}

	text, err := EPUBText(bytes.NewReader(out.Bytes()), int64(out.Len()), func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if len(text.Chapters) != 1 || !strings.Contains(text.Chapters[0].HTML, "Hello maintenance") {
		t.Fatalf("rewritten chapters = %+v", text.Chapters)
	}

	zr, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if zr.File[0].Name != "mimetype" || zr.File[0].Method != zip.Store {
		t.Errorf("mimetype is not first/uncompressed: %+v", zr.File[0])
	}
	var style, chapter string
	for _, f := range zr.File {
		switch f.Name {
		case "OPS/style.css":
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			b, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				t.Fatal(err)
			}
			style = string(b)
		case "OPS/ch1.xhtml":
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			b, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				t.Fatal(err)
			}
			chapter = string(b)
		}
	}
	if style != "body { margin: 0; }" {
		t.Errorf("style.css was not preserved: %q", style)
	}
	if strings.Contains(chapter, "<title/>") || !strings.Contains(chapter, "<title></title>") {
		t.Errorf("chapter title was not normalized: %q", chapter)
	}
}

func TestNormalizeXHTMLSkipsRawContent(t *testing.T) {
	in := `<!-- <title/> --><script>const s = "</scripture><title/>";</script><style>.x{content:"<title/>"}</style><textarea><title/></textarea><div data-x="<title/>"><title/></div>`
	out := string(normalizeXHTML([]byte(in)))
	for _, unchanged := range []string{
		`<!-- <title/> -->`,
		`<script>const s = "</scripture><title/>";</script>`,
		`<style>.x{content:"<title/>"}</style>`,
		`<textarea><title/></textarea>`,
		`<div data-x="<title/>">`,
	} {
		if !strings.Contains(out, unchanged) {
			t.Errorf("normalizer changed raw content %q:\n%s", unchanged, out)
		}
	}
	if !strings.Contains(out, `<div data-x="<title/>"><title></title></div>`) {
		t.Errorf("standalone <title/> was not normalized:\n%s", out)
	}
}

func TestScanEPUBClean(t *testing.T) {
	data := epubZip(t, false, true)
	changed, err := ScanEPUB(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("clean EPUB was reported as needing repair")
	}
}
