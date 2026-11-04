package library

import (
	"bytes"
	"image/jpeg"
	"testing"
)

func TestNormalizeCover(t *testing.T) {
	out, mime, err := NormalizeCover(pngBytes)
	if err != nil {
		t.Fatal(err)
	}
	if mime != "image/jpeg" {
		t.Errorf("mime = %q", mime)
	}
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() > maxCoverSide || img.Bounds().Dy() > maxCoverSide {
		t.Errorf("cover too large: %v", img.Bounds())
	}
}
