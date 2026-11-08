// Package fb2epub converts an FB2 book into a self-contained EPUB 3 file.
// It uses the library parsers for metadata, chapters, footnotes and images,
// then assembles the standard EPUB container (OPF, nav, NCX and XHTML).
package fb2epub

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"golang.org/x/net/html/charset"

	"github.com/shabbybastard/polka/internal/library"
)

type imageFile struct {
	name string
	mime string
	data []byte
}

type chapterFile struct {
	file  string
	title string
}

// Convert builds an EPUB 3 from FB2 bytes. The optional metadata may be
// passed in when the caller already parsed it (the worker needs it for
// duplicate checks); otherwise Convert parses it itself.
func Convert(fb2 []byte, parsedMeta ...*library.FB2Meta) ([]byte, error) {
	var meta *library.FB2Meta
	if len(parsedMeta) > 0 {
		meta = parsedMeta[0]
	}
	var err error
	if meta == nil {
		meta, err = library.ParseFB2(bytes.NewReader(fb2), true)
		if err != nil {
			return nil, fmt.Errorf("parse fb2 metadata: %w", err)
		}
	}

	images, imageByID, err := parseBinaries(fb2)
	if err != nil {
		return nil, err
	}
	imgURL := func(id string) string {
		if name, ok := imageByID[id]; ok {
			return "../images/" + name
		}
		return ""
	}
	text, err := library.ParseFB2Text(bytes.NewReader(fb2), imgURL)
	if err != nil {
		return nil, fmt.Errorf("parse fb2 text: %w", err)
	}
	if len(text.Chapters) == 0 {
		return nil, fmt.Errorf("fb2 has no body sections")
	}

	title := strings.TrimSpace(meta.Title)
	if title == "" {
		title = "Untitled"
	}
	lang := strings.TrimSpace(meta.Lang)
	if lang == "" {
		lang = "und"
	}
	bookID := strings.TrimSpace(meta.ISBN)
	if bookID != "" {
		bookID = "urn:isbn:" + bookID
	} else {
		bookID = "urn:uuid:" + newUUID()
	}

	cover := coverImage(meta)
	if cover != nil {
		// The cover usually lives in the FB2 binary list as well. Reuse
		// that file instead of writing the same bytes twice.
		for _, img := range images {
			if bytes.Equal(img.data, cover.data) {
				cover.name = img.name
				cover.mime = img.mime
				break
			}
		}
	}
	notes := make(map[string]string, len(text.Notes))
	noteKeys := make([]string, 0, len(text.Notes))
	for id := range text.Notes {
		noteKeys = append(noteKeys, id)
	}
	sort.Strings(noteKeys)
	noteIDs := make(map[string]string, len(noteKeys))
	for i, id := range noteKeys {
		noteIDs[id] = fmt.Sprintf("n%d", i+1)
		notes[noteIDs[id]] = replaceNoteRefs(text.Notes[id], noteIDs)
	}

	chapters := make([]chapterFile, 0, len(text.Chapters))
	chapterBodies := make([][]byte, 0, len(text.Chapters))
	for _, ch := range text.Chapters {
		body := stripEmptyImages(replaceNoteRefs(ch.HTML, noteIDs))
		chTitle := strings.TrimSpace(ch.Title)
		if chTitle == "" && strings.TrimSpace(body) == "" {
			continue
		}
		visibleBody := body
		if chTitle != "" {
			visibleBody = `<h1 class="chapter-title">` + xmlEscape(chTitle) + "</h1>\n" + body
		}
		index := len(chapters)
		file := fmt.Sprintf("chapter-%d.xhtml", index+1)
		chapters = append(chapters, chapterFile{
			file:  file,
			title: chapterLabel(chTitle, index, lang),
		})
		chapterBodies = append(chapterBodies, []byte(xhtmlPage(chTitle, "../style.css", visibleBody)))
	}
	if len(chapters) == 0 {
		return nil, fmt.Errorf("fb2 has no readable chapters")
	}

	modified := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	nav := navXHTML(title, chapters, len(notes) > 0, lang)
	ncx := ncxXML(title, chapters, len(notes) > 0, lang, bookID)
	opf := opfXML(title, lang, bookID, modified, meta, chapters, notes, cover, images)

	files := []epubFile{
		{name: "mimetype", data: []byte("application/epub+zip"), store: true},
		{name: "META-INF/container.xml", data: []byte(containerXML)},
		{name: "OEBPS/content.opf", data: []byte(opf)},
		{name: "OEBPS/nav.xhtml", data: []byte(nav)},
		{name: "OEBPS/toc.ncx", data: []byte(ncx)},
		{name: "OEBPS/style.css", data: []byte(epubCSS)},
	}
	if cover != nil {
		files = append(files, epubFile{name: "OEBPS/text/cover.xhtml", data: []byte(coverXHTML(title, cover))})
		coverIsImage := false
		for _, img := range images {
			if img.name == cover.name {
				coverIsImage = true
				break
			}
		}
		if !coverIsImage {
			files = append(files, epubFile{name: "OEBPS/images/" + cover.name, data: cover.data})
		}
	}
	for i := range chapters {
		files = append(files, epubFile{
			name: "OEBPS/text/" + chapters[i].file,
			data: chapterBodies[i],
		})
	}
	if len(notes) > 0 {
		files = append(files, epubFile{name: "OEBPS/text/notes.xhtml", data: []byte(notesXHTML(notes, lang))})
	}
	for _, img := range images {
		files = append(files, epubFile{name: "OEBPS/images/" + img.name, data: img.data})
	}

	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, f := range files {
		method := zip.Deflate
		if f.store {
			method = zip.Store
		}
		hdr := &zip.FileHeader{Name: f.name, Method: method, Modified: time.Now()}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(f.data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

type epubFile struct {
	name  string
	data  []byte
	store bool
}

const containerXML = `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>
`

const epubCSS = `body { font-family: serif; line-height: 1.55; margin: 1.4em; }
h1, h2, h3, h4 { line-height: 1.25; margin: 1.2em 0 0.6em; }
p { margin: 0 0 0.75em; text-indent: 1.2em; }
p.fb2-verse, .fb2-text-author { text-indent: 0; }
blockquote, .fb2-epigraph, .fb2-cite { margin: 1em 2em; font-style: italic; }
.fb2-poem, .fb2-stanza { margin: 1em 0 1em 1.5em; }
.fb2-img { display: block; max-width: 100%; height: auto; margin: 1em auto; }
.fb2-note-ref a { text-decoration: none; }
.note { margin: 1.5em 0; padding-top: 0.5em; border-top: 1px solid #ddd; }
.cover { margin: 0; padding: 0; text-align: center; }
.cover img { max-width: 100%; max-height: 100%; }
`

func xhtmlPage(title, cssHref, body string) string {
	headTitle := xmlEscape(title)
	if headTitle == "" {
		headTitle = " "
	}
	return `<?xml version="1.0" encoding="utf-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml">
<head>
<meta charset="utf-8"/>
<title>` + headTitle + `</title>
<link rel="stylesheet" type="text/css" href="` + cssHref + `"/>
</head>
<body>
` + body + `
</body>
</html>
`
}

func coverXHTML(title string, cover *imageFile) string {
	body := `<div class="cover"><img src="../images/` + xmlEscape(cover.name) + `" alt="` + xmlEscape(title) + `"/></div>`
	return xhtmlPage(title, "../style.css", body)
}

func notesXHTML(notes map[string]string, lang string) string {
	heading := "Notes"
	if isRussian(lang) {
		heading = "Примечания"
	}
	ids := make([]string, 0, len(notes))
	for id := range notes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return noteNumber(ids[i]) < noteNumber(ids[j])
	})
	var b strings.Builder
	b.WriteString("<h1>" + xmlEscape(heading) + "</h1>\n")
	for _, id := range ids {
		b.WriteString(`<section class="note" id="` + xmlEscape(id) + `">` + notes[id] + "</section>\n")
	}
	return xhtmlPage(heading, "../style.css", b.String())
}

func navXHTML(title string, chapters []chapterFile, withNotes bool, lang string) string {
	heading := "Contents"
	if isRussian(lang) {
		heading = "Оглавление"
	}
	var b strings.Builder
	b.WriteString("<h1>" + xmlEscape(heading) + "</h1>\n<nav epub:type=\"toc\" id=\"toc\">\n<ol>\n")
	for _, ch := range chapters {
		b.WriteString(`<li><a href="text/` + xmlEscape(ch.file) + `">` + xmlEscape(ch.title) + "</a></li>\n")
	}
	if withNotes {
		b.WriteString(`<li><a href="text/notes.xhtml">` + xmlEscape(notesLabel(lang)) + "</a></li>\n")
	}
	b.WriteString("</ol>\n</nav>\n")
	return `<?xml version="1.0" encoding="utf-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops">
<head><meta charset="utf-8"/><title>` + xmlEscape(title) + `</title></head>
<body>` + b.String() + `</body>
</html>
`
}

func ncxXML(title string, chapters []chapterFile, withNotes bool, lang, bookID string) string {
	var b strings.Builder
	play := 1
	for _, ch := range chapters {
		fmt.Fprintf(&b, `<navPoint id="navPoint-%d" playOrder="%d"><navLabel><text>%s</text></navLabel><content src="text/%s"/></navPoint>`+"\n",
			play, play, xmlEscape(ch.title), xmlEscape(ch.file))
		play++
	}
	if withNotes {
		fmt.Fprintf(&b, `<navPoint id="navPoint-%d" playOrder="%d"><navLabel><text>%s</text></navLabel><content src="text/notes.xhtml"/></navPoint>`+"\n",
			play, play, xmlEscape(notesLabel(lang)))
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1">
<head>
<meta name="dtb:uid" content="` + xmlEscape(bookID) + `"/>
<meta name="dtb:depth" content="1"/>
<meta name="dtb:totalPageCount" content="0"/>
<meta name="dtb:maxPageNumber" content="0"/>
</head>
<docTitle><text>` + xmlEscape(title) + `</text></docTitle>
<navMap>
` + b.String() + `</navMap>
</ncx>
`
}

func opfXML(title, lang, bookID, modified string, meta *library.FB2Meta, chapters []chapterFile, notes map[string]string, cover *imageFile, images []imageFile) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="bookid" xml:lang="` + xmlEscape(lang) + `">
<metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
<dc:identifier id="bookid">` + xmlEscape(bookID) + `</dc:identifier>
<dc:title>` + xmlEscape(title) + `</dc:title>
<dc:language>` + xmlEscape(lang) + `</dc:language>
`)
	for _, a := range meta.Authors {
		name := strings.TrimSpace(a.First + " " + a.Middle + " " + a.Last)
		if name != "" {
			b.WriteString("<dc:creator>" + xmlEscape(name) + "</dc:creator>\n")
		}
	}
	if meta.Publisher != "" {
		b.WriteString("<dc:publisher>" + xmlEscape(meta.Publisher) + "</dc:publisher>\n")
	}
	if meta.Year != "" {
		b.WriteString("<dc:date>" + xmlEscape(meta.Year) + "</dc:date>\n")
	}
	if desc := plainText(meta.AnnotationHTML); desc != "" {
		b.WriteString("<dc:description>" + xmlEscape(desc) + "</dc:description>\n")
	}
	for _, genre := range meta.Genres {
		if genre = strings.TrimSpace(genre); genre != "" {
			b.WriteString("<dc:subject>" + xmlEscape(genre) + "</dc:subject>\n")
		}
	}
	if meta.Series != "" {
		b.WriteString(`<meta property="belongs-to-collection" id="series">` + xmlEscape(meta.Series) + `</meta>` + "\n")
		b.WriteString(`<meta refines="#series" property="collection-type">series</meta>` + "\n")
		if meta.SeriesNum > 0 {
			b.WriteString(`<meta refines="#series" property="group-position">` + fmt.Sprint(meta.SeriesNum) + `</meta>` + "\n")
		}
	}
	if cover != nil {
		b.WriteString(`<meta name="cover" content="cover-image"/>` + "\n")
	}
	b.WriteString(`<meta property="dcterms:modified">` + modified + `</meta>` + "\n")
	b.WriteString(`</metadata>
<manifest>
<item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
<item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/>
<item id="css" href="style.css" media-type="text/css"/>
`)
	if cover != nil {
		b.WriteString(`<item id="cover-page" href="text/cover.xhtml" media-type="application/xhtml+xml"/>` + "\n")
		b.WriteString(`<item id="cover-image" href="images/` + xmlEscape(cover.name) + `" media-type="` + xmlEscape(cover.mime) + `" properties="cover-image"/>` + "\n")
	}
	for i := range chapters {
		fmt.Fprintf(&b, `<item id="ch%d" href="text/%s" media-type="application/xhtml+xml"/>`+"\n", i+1, xmlEscape(chapters[i].file))
	}
	if len(notes) > 0 {
		b.WriteString(`<item id="notes" href="text/notes.xhtml" media-type="application/xhtml+xml"/>` + "\n")
	}
	for i, img := range images {
		if cover != nil && img.name == cover.name {
			// The cover-image manifest item already points at this file.
			continue
		}
		fmt.Fprintf(&b, `<item id="img%d" href="images/%s" media-type="%s"/>`+"\n", i+1, xmlEscape(img.name), xmlEscape(img.mime))
	}
	b.WriteString(`</manifest>
<spine toc="ncx">
<itemref idref="nav" linear="no"/>
`)
	if cover != nil {
		b.WriteString(`<itemref idref="cover-page" linear="no"/>` + "\n")
	}
	for i := range chapters {
		fmt.Fprintf(&b, `<itemref idref="ch%d"/>`+"\n", i+1)
	}
	if len(notes) > 0 {
		b.WriteString(`<itemref idref="notes"/>` + "\n")
	}
	b.WriteString(`</spine>
</package>
`)
	return b.String()
}

func chapterLabel(title string, index int, lang string) string {
	if title = strings.TrimSpace(title); title != "" {
		return title
	}
	if isRussian(lang) {
		if index == 0 {
			return "Начало"
		}
		return fmt.Sprintf("Глава %d", index+1)
	}
	if index == 0 {
		return "Beginning"
	}
	return fmt.Sprintf("Chapter %d", index+1)
}

func notesLabel(lang string) string {
	if isRussian(lang) {
		return "Примечания"
	}
	return "Notes"
}

func isRussian(lang string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(lang)), "ru")
}

func noteNumber(id string) int {
	var n int
	fmt.Sscanf(id, "n%d", &n)
	return n
}

var (
	noteRefRE    = regexp.MustCompile(`data-note="([^"]*)"`)
	emptyImgRE   = regexp.MustCompile(`(?s)<img[^>]*src=""[^>]*>`)
	htmlTagRE    = regexp.MustCompile(`(?s)<[^>]*>`)
	whitespaceRE = regexp.MustCompile(`\s+`)
)

func replaceNoteRefs(s string, ids map[string]string) string {
	return noteRefRE.ReplaceAllStringFunc(s, func(m string) string {
		raw := strings.TrimSuffix(strings.TrimPrefix(m, `data-note="`), `"`)
		id, ok := ids[raw]
		if !ok {
			id = "n0"
		}
		return `href="notes.xhtml#` + id + `"`
	})
}

func stripEmptyImages(s string) string { return emptyImgRE.ReplaceAllString(s, "") }

func plainText(s string) string {
	s = htmlTagRE.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	return strings.TrimSpace(whitespaceRE.ReplaceAllString(s, " "))
}

func xmlEscape(s string) string { return html.EscapeString(s) }

func coverImage(meta *library.FB2Meta) *imageFile {
	if len(meta.Cover) == 0 {
		return nil
	}
	mimeType := strings.ToLower(strings.TrimSpace(meta.CoverMime))
	if mimeType == "" {
		mimeType = http.DetectContentType(meta.Cover)
	}
	if !strings.HasPrefix(mimeType, "image/") {
		return nil
	}
	return &imageFile{name: "cover." + imageExt(mimeType), mime: mimeType, data: meta.Cover}
}

func imageExt(mimeType string) string {
	mimeType = strings.ToLower(strings.TrimSpace(strings.SplitN(mimeType, ";", 2)[0]))
	switch mimeType {
	case "image/jpeg", "image/jpg":
		return "jpg"
	case "image/png":
		return "png"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	case "image/svg+xml":
		return "svg"
	case "image/bmp":
		return "bmp"
	case "image/tiff":
		return "tiff"
	}
	if exts, _ := mime.ExtensionsByType(mimeType); len(exts) > 0 {
		return strings.TrimPrefix(exts[0], ".")
	}
	return "bin"
}

func parseBinaries(fb2 []byte) ([]imageFile, map[string]string, error) {
	dec := xml.NewDecoder(bytes.NewReader(fb2))
	dec.CharsetReader = charset.NewReaderLabel
	dec.Strict = false

	type candidate struct {
		id, mime string
		data     []byte
	}
	var candidates []candidate
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("parse fb2 binaries: %w", err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "binary" {
			continue
		}
		var id, mimeType string
		for _, a := range start.Attr {
			switch a.Name.Local {
			case "id":
				id = a.Value
			case "content-type":
				mimeType = a.Value
			}
		}
		b64, err := library.ReadElementTextLimited(dec, start, library.MaxBinaryB64)
		if err != nil {
			return nil, nil, fmt.Errorf("read fb2 binary %q: %w", id, err)
		}
		data, err := base64.StdEncoding.DecodeString(removeWhitespace(b64))
		if err != nil {
			return nil, nil, fmt.Errorf("decode fb2 binary %q: %w", id, err)
		}
		if mimeType == "" {
			mimeType = mime.TypeByExtension(strings.ToLower(filepath.Ext(id)))
		}
		if mimeType == "" {
			mimeType = http.DetectContentType(data)
		}
		if !strings.HasPrefix(strings.ToLower(mimeType), "image/") {
			continue
		}
		candidates = append(candidates, candidate{id: id, mime: mimeType, data: data})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].id < candidates[j].id })
	images := make([]imageFile, 0, len(candidates))
	byID := make(map[string]string, len(candidates))
	for i, c := range candidates {
		name := fmt.Sprintf("img-%d.%s", i+1, imageExt(c.mime))
		images = append(images, imageFile{name: name, mime: c.mime, data: c.data})
		byID[c.id] = name
	}
	return images, byID, nil
}

func removeWhitespace(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

func newUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Validate checks the EPUB container, mimetype, OPF manifest and spine well
// enough to reject truncated or malformed converter output before adding it
// to the catalog.
func Validate(data []byte) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("open epub: %w", err)
	}
	if len(zr.File) == 0 {
		return fmt.Errorf("empty epub")
	}
	files := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		files[f.Name] = f
	}
	first := zr.File[0]
	if first.Name != "mimetype" || first.Method != zip.Store {
		return fmt.Errorf("epub mimetype must be the first uncompressed entry")
	}
	mimetype, err := readZipFile(first)
	if err != nil || string(mimetype) != "application/epub+zip" {
		return fmt.Errorf("invalid epub mimetype")
	}
	containerFile, ok := files["META-INF/container.xml"]
	if !ok {
		return fmt.Errorf("epub has no container.xml")
	}
	var container struct {
		Rootfiles []struct {
			FullPath string `xml:"full-path,attr"`
		} `xml:"rootfiles>rootfile"`
	}
	containerData, err := readZipFile(containerFile)
	if err != nil {
		return fmt.Errorf("read epub container.xml: %w", err)
	}
	if err := xml.Unmarshal(containerData, &container); err != nil || len(container.Rootfiles) == 0 {
		return fmt.Errorf("invalid epub container.xml")
	}
	opfPath := container.Rootfiles[0].FullPath
	opfFile, ok := files[opfPath]
	if !ok {
		return fmt.Errorf("epub package %q is missing", opfPath)
	}
	var pkg struct {
		Manifest struct {
			Items []struct {
				ID         string `xml:"id,attr"`
				Href       string `xml:"href,attr"`
				MediaType  string `xml:"media-type,attr"`
				Properties string `xml:"properties,attr"`
			} `xml:"item"`
		} `xml:"manifest"`
		Spine struct {
			Refs []struct {
				IDRef string `xml:"idref,attr"`
			} `xml:"itemref"`
		} `xml:"spine"`
	}
	opfData, err := readZipFile(opfFile)
	if err != nil {
		return fmt.Errorf("read epub package: %w", err)
	}
	if err := xml.Unmarshal(opfData, &pkg); err != nil {
		return fmt.Errorf("invalid epub package: %w", err)
	}
	ids := make(map[string]bool, len(pkg.Manifest.Items))
	hasNav, hasNCX := false, false
	for _, item := range pkg.Manifest.Items {
		ids[item.ID] = true
		if strings.Contains(" "+item.Properties+" ", " nav ") {
			hasNav = true
		}
		if item.MediaType == "application/x-dtbncx+xml" {
			hasNCX = true
		}
		href := item.Href
		if strings.Contains(href, "://") || strings.HasPrefix(href, "data:") {
			continue
		}
		if i := strings.IndexByte(href, '#'); i >= 0 {
			href = href[:i]
		}
		if href == "" {
			continue
		}
		if _, ok := files[path.Join(path.Dir(opfPath), href)]; !ok {
			return fmt.Errorf("epub manifest item %q is missing", item.Href)
		}
	}
	if !hasNav || !hasNCX {
		return fmt.Errorf("epub has no navigation document or NCX")
	}
	if len(pkg.Spine.Refs) == 0 {
		return fmt.Errorf("epub spine is empty")
	}
	for _, ref := range pkg.Spine.Refs {
		if !ids[ref.IDRef] {
			return fmt.Errorf("epub spine references unknown item %q", ref.IDRef)
		}
	}
	return nil
}

func readZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}
