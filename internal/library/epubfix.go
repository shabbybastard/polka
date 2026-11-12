package library

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"path"
	"strings"
)

// EPUB maintenance: detect and repair XHTML written with self-closing
// raw-text tags. The online reader already normalizes these tags on the fly;
// this code rewrites the file on disk so other readers and downloads get the
// fixed version too.

func isEPUBHTMLName(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".xhtml", ".html", ".htm":
		return true
	}
	return false
}

// ScanEPUB reports whether an EPUB contains XHTML entries that
// normalizeXHTML would change.
func ScanEPUB(r io.ReaderAt, size int64) (bool, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return false, fmt.Errorf("open epub: %w", err)
	}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !isEPUBHTMLName(f.Name) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return false, fmt.Errorf("open %s: %w", f.Name, err)
		}
		data, err := readLimited(rc, maxArchiveEntrySize)
		rc.Close()
		if err != nil {
			return false, fmt.Errorf("read %s: %w", f.Name, err)
		}
		if !bytes.Equal(normalizeXHTML(data), data) {
			return true, nil
		}
	}
	return false, nil
}

// NormalizeEPUB rewrites an EPUB into w, normalizing every XHTML entry.
// The mimetype entry is written first and uncompressed; unchanged entries are
// copied byte-for-byte (including their compression method). The return value
// reports whether any XHTML entry changed.
func NormalizeEPUB(r io.ReaderAt, size int64, w io.Writer) (bool, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return false, fmt.Errorf("open epub: %w", err)
	}
	zw := zip.NewWriter(w)
	changed := false

	var mimetype *zip.File
	for _, f := range zr.File {
		if strings.EqualFold(f.Name, "mimetype") {
			mimetype = f
			break
		}
	}
	mimeHeader := &zip.FileHeader{Name: "mimetype", Method: zip.Store}
	mimeData := []byte("application/epub+zip")
	if mimetype != nil {
		rc, err := mimetype.Open()
		if err != nil {
			zw.Close()
			return false, fmt.Errorf("open mimetype: %w", err)
		}
		data, err := readLimited(rc, 4096)
		rc.Close()
		if err != nil {
			zw.Close()
			return false, fmt.Errorf("read mimetype: %w", err)
		}
		if len(data) > 0 {
			mimeData = data
		}
		hdr := mimetype.FileHeader
		hdr.Name = "mimetype"
		hdr.Method = zip.Store
		mimeHeader = &hdr
	} else {
		changed = true
	}
	mw, err := zw.CreateHeader(mimeHeader)
	if err != nil {
		zw.Close()
		return false, fmt.Errorf("write mimetype: %w", err)
	}
	if _, err := mw.Write(mimeData); err != nil {
		zw.Close()
		return false, fmt.Errorf("write mimetype: %w", err)
	}

	for _, f := range zr.File {
		if strings.EqualFold(f.Name, "mimetype") {
			continue
		}
		if !f.FileInfo().IsDir() && isEPUBHTMLName(f.Name) {
			rc, err := f.Open()
			if err != nil {
				zw.Close()
				return false, fmt.Errorf("open %s: %w", f.Name, err)
			}
			data, err := readLimited(rc, maxArchiveEntrySize)
			rc.Close()
			if err != nil {
				zw.Close()
				return false, fmt.Errorf("read %s: %w", f.Name, err)
			}
			normalized := normalizeXHTML(data)
			if !bytes.Equal(normalized, data) {
				changed = true
				hdr := f.FileHeader
				hdr.Method = zip.Deflate
				hdr.CompressedSize = 0
				hdr.CompressedSize64 = 0
				hdr.UncompressedSize = 0
				hdr.UncompressedSize64 = 0
				hdr.CRC32 = 0
				cw, err := zw.CreateHeader(&hdr)
				if err != nil {
					zw.Close()
					return false, fmt.Errorf("write %s: %w", f.Name, err)
				}
				if _, err := cw.Write(normalized); err != nil {
					zw.Close()
					return false, fmt.Errorf("write %s: %w", f.Name, err)
				}
				continue
			}
		}
		if err := zw.Copy(f); err != nil {
			zw.Close()
			return false, fmt.Errorf("copy %s: %w", f.Name, err)
		}
	}
	if err := zw.Close(); err != nil {
		return false, fmt.Errorf("close epub: %w", err)
	}
	return changed, nil
}
