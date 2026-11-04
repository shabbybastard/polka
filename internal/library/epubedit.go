package library

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/beevik/etree"
)

// EPUBEdit is the metadata and cover payload written into an EPUB.
type EPUBEdit struct {
	Title       string
	Authors     []string
	Language    string
	Publisher   string
	Year        string
	ISBN        string
	Description string
	Cover       []byte
	CoverMime   string
}

type epubEntry struct {
	hdr  zip.FileHeader
	data []byte
}

// EditEPUB rewrites OPF metadata and, optionally, the cover image. The
// original archive is kept intact; only the returned byte slice is new.
func EditEPUB(data []byte, edit EPUBEdit) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("open epub: %w", err)
	}
	opfPath, err := epubRootFile(zr)
	if err != nil {
		return nil, err
	}

	entries := make([]epubEntry, 0, len(zr.File))
	byName := make(map[string]int, len(zr.File))
	for _, f := range zr.File {
		if strings.EqualFold(f.Name, "META-INF/encryption.xml") {
			return nil, fmt.Errorf("epub is encrypted")
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		content, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
		byName[strings.ToLower(f.Name)] = len(entries)
		entries = append(entries, epubEntry{hdr: f.FileHeader, data: content})
	}

	opfIndex, ok := byName[strings.ToLower(opfPath)]
	if !ok {
		return nil, fmt.Errorf("epub: %s not found", opfPath)
	}
	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(entries[opfIndex].data); err != nil {
		return nil, fmt.Errorf("parse opf: %w", err)
	}
	root := doc.Root()
	metadata := findDescendant(root, "metadata")
	manifest := findDescendant(root, "manifest")
	if metadata == nil || manifest == nil {
		return nil, fmt.Errorf("epub: opf has no metadata/manifest")
	}
	ensureNamespace(root, "dc", "http://purl.org/dc/elements/1.1/")
	ensureNamespace(root, "opf", "http://www.idpf.org/2007/opf")

	updateMetadata(metadata, edit)
	coverChanged, oldCoverPath, newCoverPath, err := updateCover(root, metadata, manifest, opfPath, edit)
	if err != nil {
		return nil, err
	}
	opfData, err := doc.WriteToBytes()
	if err != nil {
		return nil, err
	}
	entries[opfIndex].data = opfData

	coverReplaced := false
	if coverChanged && newCoverPath != "" {
		for i := range entries {
			if strings.EqualFold(entries[i].hdr.Name, newCoverPath) {
				entries[i].data = edit.Cover
				coverReplaced = true
				break
			}
		}
	}

	// Repack with mimetype first and uncompressed, as required by EPUB.
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	mimeHeader := &zip.FileHeader{Name: "mimetype", Method: zip.Store, Modified: time.Now()}
	mw, err := zw.CreateHeader(mimeHeader)
	if err != nil {
		return nil, err
	}
	if _, err := io.WriteString(mw, "application/epub+zip"); err != nil {
		return nil, err
	}

	for i := range entries {
		e := entries[i]
		name := strings.ToLower(e.hdr.Name)
		if name == "mimetype" {
			continue
		}
		if coverChanged && oldCoverPath != "" && name == strings.ToLower(oldCoverPath) && oldCoverPath != newCoverPath {
			continue
		}
		hdr := e.hdr
		w, err := zw.CreateHeader(&hdr)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(e.data); err != nil {
			return nil, err
		}
	}

	if coverChanged && newCoverPath != "" && !coverReplaced {
		hdr := &zip.FileHeader{Name: newCoverPath, Method: zip.Deflate, Modified: time.Now()}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(edit.Cover); err != nil {
			return nil, err
		}
	}

	if err := zw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func findDescendant(e *etree.Element, tag string) *etree.Element {
	if e == nil {
		return nil
	}
	if e.Tag == tag {
		return e
	}
	for _, child := range e.ChildElements() {
		if found := findDescendant(child, tag); found != nil {
			return found
		}
	}
	return nil
}

func ensureNamespace(root *etree.Element, prefix, uri string) {
	if root.SelectAttr("xmlns:"+prefix) == nil {
		root.CreateAttr("xmlns:"+prefix, uri)
	}
}

func updateMetadata(metadata *etree.Element, edit EPUBEdit) {
	setMetadataText(metadata, "title", edit.Title)
	setMetadataText(metadata, "language", edit.Language)
	setMetadataText(metadata, "publisher", edit.Publisher)
	setMetadataText(metadata, "date", edit.Year)
	setMetadataText(metadata, "description", edit.Description)

	// Replace creators, preserving the namespace prefix used by the OPF.
	prefix := "dc"
	for _, el := range metadata.ChildElements() {
		if el.Tag == "creator" {
			prefix = el.Space
			break
		}
	}
	for _, el := range metadata.SelectElements("creator") {
		metadata.RemoveChild(el)
	}
	for _, author := range edit.Authors {
		if author = strings.TrimSpace(author); author == "" {
			continue
		}
		el := etree.NewElement("creator")
		el.Space = prefix
		el.SetText(author)
		metadata.AddChild(el)
	}

	// ISBN identifier: update an existing ISBN-like identifier, otherwise
	// add a fresh one. UUID identifiers stay untouched.
	if edit.ISBN != "" {
		var target *etree.Element
		for _, el := range metadata.SelectElements("identifier") {
			value := strings.TrimSpace(el.Text())
			scheme := el.SelectAttrValue("opf:scheme", "")
			if strings.EqualFold(scheme, "ISBN") || looksLikeISBN(value) {
				target = el
				break
			}
		}
		if target == nil {
			target = metadata.CreateElement("dc:identifier")
		}
		target.SetText(edit.ISBN)
		if target.SelectAttr("opf:scheme") == nil {
			target.CreateAttr("opf:scheme", "ISBN")
		}
	}

	// EPUB3 requires dcterms:modified; EPUB2 readers ignore it harmlessly.
	var modified *etree.Element
	for _, el := range metadata.SelectElements("meta") {
		if strings.EqualFold(el.SelectAttrValue("property", ""), "dcterms:modified") {
			modified = el
			break
		}
	}
	if modified == nil {
		modified = metadata.CreateElement("meta")
		modified.CreateAttr("property", "dcterms:modified")
	}
	modified.SetText(time.Now().UTC().Format("2006-01-02T15:04:05Z"))
}

func setMetadataText(metadata *etree.Element, tag, value string) {
	value = strings.TrimSpace(value)
	var target *etree.Element
	for _, el := range metadata.ChildElements() {
		if el.Tag == tag {
			target = el
			break
		}
	}
	if value == "" {
		if target != nil {
			metadata.RemoveChild(target)
		}
		return
	}
	if target == nil {
		target = metadata.CreateElement("dc:" + tag)
	}
	target.SetText(value)
}

func updateCover(root, metadata, manifest *etree.Element, opfPath string, edit EPUBEdit) (changed bool, oldPath, newPath string, err error) {
	if len(edit.Cover) == 0 {
		return false, "", "", nil
	}
	mime := edit.CoverMime
	if mime == "" {
		mime = sniffImageMime(edit.Cover)
	}
	ext, ok := coverExtForMime(mime)
	if !ok {
		return false, "", "", fmt.Errorf("unsupported cover image: %s", mime)
	}

	var item *etree.Element
	for _, el := range manifest.SelectElements("item") {
		if strings.Contains(" "+el.SelectAttrValue("properties", "")+" ", " cover-image ") {
			item = el
			break
		}
	}
	if item == nil {
		coverID := ""
		for _, el := range metadata.SelectElements("meta") {
			if strings.EqualFold(el.SelectAttrValue("name", ""), "cover") {
				coverID = el.SelectAttrValue("content", "")
				break
			}
		}
		if coverID != "" {
			for _, el := range manifest.SelectElements("item") {
				if el.SelectAttrValue("id", "") == coverID {
					item = el
					break
				}
			}
		}
	}
	if item == nil {
		// No cover yet: add a new manifest item and, for EPUB2, the
		// legacy meta pointer.
		item = manifest.CreateElement("item")
		item.CreateAttr("id", "polka-cover")
		item.CreateAttr("href", "polka-cover."+ext)
		item.CreateAttr("media-type", mime)
		if strings.HasPrefix(strings.TrimSpace(root.SelectAttrValue("version", "")), "3") {
			item.CreateAttr("properties", "cover-image")
		} else {
			meta := metadata.CreateElement("meta")
			meta.CreateAttr("name", "cover")
			meta.CreateAttr("content", "polka-cover")
		}
		oldPath, newPath = "", path.Join(path.Dir(opfPath), "polka-cover."+ext)
		return true, oldPath, newPath, nil
	}

	oldHref := item.SelectAttrValue("href", "")
	oldPath = path.Join(path.Dir(opfPath), oldHref)
	newHref := oldHref
	if strings.ToLower(path.Ext(oldHref)) != "."+ext {
		newHref = strings.TrimSuffix(oldHref, path.Ext(oldHref)) + "." + ext
	}
	item.CreateAttr("href", newHref)
	item.CreateAttr("media-type", mime)
	if strings.EqualFold(root.SelectAttrValue("version", ""), "3.0") || item.SelectAttr("properties") != nil {
		props := strings.Fields(item.SelectAttrValue("properties", ""))
		found := false
		for _, p := range props {
			if p == "cover-image" {
				found = true
			}
		}
		if !found {
			props = append(props, "cover-image")
			item.CreateAttr("properties", strings.Join(props, " "))
		}
	}
	newPath = path.Join(path.Dir(opfPath), newHref)
	if oldPath == newPath {
		oldPath = newPath
	}
	return true, oldPath, newPath, nil
}

func coverExtForMime(mime string) (string, bool) {
	switch strings.ToLower(mime) {
	case "image/jpeg":
		return "jpg", true
	case "image/png":
		return "png", true
	case "image/webp":
		return "webp", true
	}
	return "", false
}
