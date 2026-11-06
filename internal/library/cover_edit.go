package library

import (
	"bytes"
	"fmt"
	_ "golang.org/x/image/webp"
	"image"
	"image/draw"
	"image/jpeg"
	_ "image/png"

	xdraw "golang.org/x/image/draw"
)

const (
	maxCoverSide = 1600
	coverQuality = 75
)

// NormalizeCover decodes a JPEG/PNG/WebP image, fits it into a 1600 px
// box and encodes it as a compact JPEG. Transparency is composited over
// white, which is what e-ink and most EPUB readers expect.
func NormalizeCover(data []byte) ([]byte, string, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("cover: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 8192 || cfg.Height > 8192 {
		return nil, "", fmt.Errorf("cover: invalid dimensions")
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("cover: %w", err)
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, "", fmt.Errorf("cover: empty image")
	}

	nw, nh := w, h
	if w > maxCoverSide || h > maxCoverSide {
		if w >= h {
			nw = maxCoverSide
			nh = h * maxCoverSide / w
		} else {
			nh = maxCoverSide
			nw = w * maxCoverSide / h
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src)
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)

	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: coverQuality}); err != nil {
		return nil, "", fmt.Errorf("cover encode: %w", err)
	}
	return out.Bytes(), "image/jpeg", nil
}
