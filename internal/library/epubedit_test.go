package library

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

func TestEditEPUBMetadataAndCover(t *testing.T) {
	original := epubZip(t, false, true)
	updated, err := EditEPUB(original, EPUBEdit{
		Title:       "Edited Title",
		Authors:     []string{"New Author"},
		Language:    "ru",
		Publisher:   "Test Press",
		Year:        "2024",
		ISBN:        "978-5-17-080090-2",
		Description: "A new annotation.",
		Cover:       pngBytes,
		CoverMime:   "image/png",
	})
	if err != nil {
		t.Fatal(err)
	}

	zr, err := zip.NewReader(bytes.NewReader(updated), int64(len(updated)))
	if err != nil {
		t.Fatal(err)
	}
	if zr.File[0].Name != "mimetype" || zr.File[0].Method != zip.Store {
		t.Errorf("mimetype must be first and stored: %q method=%d", zr.File[0].Name, zr.File[0].Method)
	}

	meta, err := ParseEPUB(bytes.NewReader(updated), int64(len(updated)))
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "Edited Title" || len(meta.Authors) != 1 || meta.Authors[0] != "New Author" ||
		meta.Language != "ru" || meta.Publisher != "Test Press" || meta.Year != "2024" ||
		meta.ISBN != "978-5-17-080090-2" || meta.Description != "A new annotation." {
		t.Errorf("edited meta: %+v", meta)
	}
	cover, mime, err := EPUBCover(bytes.NewReader(updated), int64(len(updated)))
	if err != nil || mime != "image/png" || !bytes.Equal(cover, pngBytes) {
		t.Errorf("cover: %d %q %v", len(cover), mime, err)
	}

	opf := ""
	for _, f := range zr.File {
		if strings.EqualFold(f.Name, "OEBPS/content.opf") {
			rc, _ := f.Open()
			var b bytes.Buffer
			b.ReadFrom(rc)
			rc.Close()
			opf = b.String()
		}
	}
	for _, want := range []string{"Edited Title", "New Author", "Test Press", "978-5-17-080090-2", "dcterms:modified"} {
		if !strings.Contains(opf, want) {
			t.Errorf("opf missing %q: %s", want, opf)
		}
	}
}

func TestEditEPUBEPUB2Cover(t *testing.T) {
	original := epubZip(t, true, true)
	updated, err := EditEPUB(original, EPUBEdit{
		Title:     "EPUB2",
		Authors:   []string{"Author"},
		Cover:     pngBytes,
		CoverMime: "image/png",
	})
	if err != nil {
		t.Fatal(err)
	}
	cover, mime, err := EPUBCover(bytes.NewReader(updated), int64(len(updated)))
	if err != nil || mime != "image/png" || !bytes.Equal(cover, pngBytes) {
		t.Errorf("epub2 cover: %d %q %v", len(cover), mime, err)
	}
}

func TestEditEPUBAddsCoverToBookWithoutOne(t *testing.T) {
	original := epubZip(t, false, false)
	updated, err := EditEPUB(original, EPUBEdit{
		Title:     "No Cover",
		Authors:   []string{"Author"},
		Cover:     pngBytes,
		CoverMime: "image/png",
	})
	if err != nil {
		t.Fatal(err)
	}
	cover, mime, err := EPUBCover(bytes.NewReader(updated), int64(len(updated)))
	if err != nil || mime != "image/png" || len(cover) == 0 {
		t.Errorf("added cover: %d %q %v", len(cover), mime, err)
	}
}
