package library

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"image"
	_ "image/gif"  // register decoders for image.DecodeConfig
	_ "image/jpeg" //
	_ "image/png"  //
	"io"
	"net/url"
	"path"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// EPUB online reading: spine documents are converted to sanitized HTML
// chapters, so the regular continuous reader (same as fb2/txt) renders
// them — no iframe-based engine on the client.

// epubAllowedTags is the whitelist for book content. Everything else is
// unwrapped (children kept, tag dropped).
var epubAllowedTags = map[string]bool{
	"p": true, "div": true, "span": true, "section": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"em": true, "i": true, "strong": true, "b": true, "small": true, "sub": true, "sup": true,
	"blockquote": true, "pre": true, "code": true, "cite": true,
	"ul": true, "ol": true, "li": true, "dl": true, "dt": true, "dd": true,
	"table": true, "thead": true, "tbody": true, "tr": true, "td": true, "th": true,
	"figure": true, "figcaption": true,
	"br": true, "hr": true, "img": true,
}

// EPUBText converts an EPUB into chapters for the online reader.
// imgURL maps an image path inside the archive to a public URL.
func EPUBText(r io.ReaderAt, size int64, imgURL func(string) string) (*FB2Text, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("open epub: %w", err)
	}
	opfPath, err := epubRootFile(zr)
	if err != nil {
		return nil, err
	}

	var opf struct {
		Manifest struct {
			Items []struct {
				ID        string `xml:"id,attr"`
				Href      string `xml:"href,attr"`
				MediaType string `xml:"media-type,attr"`
			} `xml:"item"`
		} `xml:"manifest"`
		Spine struct {
			Refs []struct {
				IDRef  string `xml:"idref,attr"`
				Linear string `xml:"linear,attr"`
			} `xml:"itemref"`
		} `xml:"spine"`
	}
	rc, err := openZipFile(zr, opfPath)
	if err != nil {
		return nil, err
	}
	data, err := readLimited(rc, maxOPFBytes)
	rc.Close()
	if err != nil {
		return nil, fmt.Errorf("parse opf: %w", err)
	}
	if err := checkXMLDepth(bytes.NewReader(data), maxXMLDepth); err != nil {
		return nil, fmt.Errorf("parse opf: %w", err)
	}
	err = xml.NewDecoder(bytes.NewReader(data)).Decode(&opf)
	if err != nil {
		return nil, fmt.Errorf("parse opf: %w", err)
	}

	hrefByID := map[string]string{}
	for _, it := range opf.Manifest.Items {
		if strings.Contains(it.MediaType, "html") {
			hrefByID[it.ID] = it.Href
		}
	}

	// imgDims reads just the header of an in-archive image to get its
	// intrinsic size, so chapters can reserve space and the page does not
	// jump as images lazy-load (iOS Safari has no scroll-anchoring).
	imgDims := func(archivePath string) (int, int) {
		rc, err := openZipFile(zr, archivePath)
		if err != nil {
			return 0, 0
		}
		defer rc.Close()
		cfg, _, err := image.DecodeConfig(rc)
		if err != nil {
			return 0, 0
		}
		return cfg.Width, cfg.Height
	}

	opfDir := path.Dir(opfPath)
	res := &FB2Text{}
	chapterByDoc := map[string]int{}
	for _, ref := range opf.Spine.Refs {
		if ref.Linear == "no" {
			continue
		}
		href, ok := hrefByID[ref.IDRef]
		if !ok {
			continue
		}
		docPath := path.Join(opfDir, unescapePath(href))
		rc, err := openZipFile(zr, docPath)
		if err != nil {
			continue
		}
		chapter, err := readLimited(rc, maxArchiveEntrySize)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", docPath, err)
		}
		title, body := epubChapterHTML(chapter, path.Dir(docPath), imgURL, imgDims)
		if strings.TrimSpace(body) == "" {
			continue
		}
		chapterByDoc[docPath] = len(res.Chapters)
		res.Chapters = append(res.Chapters, FB2Chapter{Title: title, HTML: body})
	}
	if len(res.Chapters) == 0 {
		return nil, fmt.Errorf("epub: no readable chapters")
	}
	// Second pass: internal links now resolve to chapter indexes.
	for i := range res.Chapters {
		res.Chapters[i].HTML = resolveDocLinks(res.Chapters[i].HTML, chapterByDoc)
	}
	return res, nil
}

// resolveDocLinks rewrites data-doc placeholders into data-goto chapter
// jumps; links to unknown documents lose the attribute and act as text.
func resolveDocLinks(h string, chapterByDoc map[string]int) string {
	return docLinkRe.ReplaceAllStringFunc(h, func(m string) string {
		target := docLinkRe.FindStringSubmatch(m)[1]
		unescaped := htmlUnescape(target)
		if idx, ok := chapterByDoc[unescaped]; ok {
			return fmt.Sprintf(`<a data-goto="%d">`, idx)
		}
		return "<a>"
	})
}

var docLinkRe = regexp.MustCompile(`<a data-doc="([^"]*)">`)

func htmlUnescape(s string) string {
	r := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&#34;", `"`, "&#39;", "'")
	return r.Replace(s)
}

// EPUBPlainText extracts readable text from an EPUB without images or
// markup. The "text only" download uses it for .epub files, where the FB2
// binary-stripping approach does not apply.
func EPUBPlainText(r io.ReaderAt, size int64) (string, error) {
	text, err := EPUBText(r, size, func(string) string { return "" })
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for _, chapter := range text.Chapters {
		if title := strings.TrimSpace(chapter.Title); title != "" {
			out.WriteString(title)
			out.WriteString("\n\n")
		}
		out.WriteString(htmlFragmentText(chapter.HTML))
		out.WriteString("\n\n")
	}
	return strings.TrimSpace(out.String()), nil
}

var textBlockTags = map[string]bool{
	"p": true, "div": true, "br": true, "li": true, "tr": true,
	"blockquote": true, "pre": true, "h1": true, "h2": true, "h3": true,
	"h4": true, "h5": true, "h6": true,
}

func htmlFragmentText(fragment string) string {
	z := html.NewTokenizer(strings.NewReader(fragment))
	var out strings.Builder
	for {
		switch z.Next() {
		case html.ErrorToken:
			return strings.TrimSpace(out.String())
		case html.TextToken:
			out.WriteString(htmlUnescape(string(z.Text())))
		case html.StartTagToken, html.SelfClosingTagToken, html.EndTagToken:
			name, _ := z.TagName()
			if textBlockTags[string(name)] {
				out.WriteByte('\n')
			}
		}
	}
}

// Some FB2 -> EPUB converters write XHTML with self-closing raw-text tags
// (<title/>, <script/>, ...). The HTML parser does not close these on "/>",
// so everything after <title/> becomes text inside <title> and the real
// <body> stays empty. Expand them to explicit open/close pairs first.
//
// <plaintext> is special: the HTML parser never closes it, so an open/close
// pair would still swallow the rest of the document. An empty self-closing
// <plaintext/> is simply removed.
var (
	// Tags that must be expanded when written as self-closing XHTML. This
	// includes head/template: they are not raw-text elements, but a
	// self-closing form confuses the HTML parser just the same.
	xhtmlSelfClosingTags = map[string]bool{
		"title": true, "script": true, "style": true, "textarea": true,
		"iframe": true, "xmp": true, "noembed": true, "noframes": true,
		"noscript": true, "head": true, "template": true, "plaintext": true,
	}
	// Elements whose content the HTML parser treats as raw text. Their
	// contents must not be scanned for nested tags.
	xhtmlRawTextTags = map[string]bool{
		"title": true, "script": true, "style": true, "textarea": true,
		"iframe": true, "xmp": true, "noembed": true, "noframes": true,
		"noscript": true, "plaintext": true,
	}
)

func normalizeXHTML(data []byte) []byte {
	var out bytes.Buffer
	out.Grow(len(data))
	for i := 0; i < len(data); {
		if data[i] != '<' {
			out.WriteByte(data[i])
			i++
			continue
		}
		// Comments and CDATA are copied verbatim.
		if bytes.HasPrefix(data[i:], []byte("<!--")) {
			end := bytes.Index(data[i+4:], []byte("-->"))
			if end < 0 {
				out.Write(data[i:])
				return out.Bytes()
			}
			end = i + 4 + end + 3
			out.Write(data[i:end])
			i = end
			continue
		}
		if bytes.HasPrefix(data[i:], []byte("<![CDATA[")) {
			end := bytes.Index(data[i+9:], []byte("]]>"))
			if end < 0 {
				out.Write(data[i:])
				return out.Bytes()
			}
			end = i + 9 + end + 3
			out.Write(data[i:end])
			i = end
			continue
		}
		// Doctype / processing instruction / other declaration.
		if i+1 < len(data) && (data[i+1] == '!' || data[i+1] == '?') {
			end := findTagEnd(data, i+1)
			if end < 0 {
				out.Write(data[i:])
				return out.Bytes()
			}
			out.Write(data[i : end+1])
			i = end + 1
			continue
		}
		// Closing tag: copy it as is.
		if i+1 < len(data) && data[i+1] == '/' {
			end := findTagEnd(data, i+1)
			if end < 0 {
				out.Write(data[i:])
				return out.Bytes()
			}
			out.Write(data[i : end+1])
			i = end + 1
			continue
		}
		// Opening tag. Find its end first, respecting quoted attribute
		// values, so a '>' inside title="a > b" does not truncate it.
		j := i + 1
		for j < len(data) && isTagNameByte(data[j]) {
			j++
		}
		name := strings.ToLower(string(data[i+1 : j]))
		end := findTagEnd(data, j)
		if end < 0 {
			out.Write(data[i:])
			return out.Bytes()
		}
		tag := data[i : end+1]
		selfClosing := len(tag) >= 2 && tag[len(tag)-2] == '/'

		if !xhtmlSelfClosingTags[name] {
			// Not one of the tags we care about. Copy the whole tag; this
			// also avoids touching tag-like text inside attribute values.
			out.Write(tag)
			i = end + 1
			continue
		}
		if selfClosing {
			if name == "plaintext" {
				// The HTML parser never closes <plaintext>; an empty
				// self-closing element can simply be dropped.
				i = end + 1
				continue
			}
			// Keep attributes, drop the trailing "/", and add an explicit
			// closing tag: <title id="x"/> -> <title id="x"></title>.
			out.Write(tag[:len(tag)-2])
			out.WriteByte('>')
			out.WriteString("</")
			out.WriteString(name)
			out.WriteByte('>')
			i = end + 1
			continue
		}

		// Open tag. Copy it, then copy the content of raw-text elements
		// unchanged until their closing tag.
		out.Write(tag)
		i = end + 1
		if name == "plaintext" {
			// Everything after <plaintext> is raw text for the HTML parser.
			out.Write(data[i:])
			return out.Bytes()
		}
		if !xhtmlRawTextTags[name] {
			continue
		}
		closeTag := "</" + name
		idx := indexClosingTag(data, i, closeTag)
		if idx < 0 {
			out.Write(data[i:])
			return out.Bytes()
		}
		closeEnd := findTagEnd(data, idx+len(closeTag))
		if closeEnd < 0 {
			out.Write(data[i:])
			return out.Bytes()
		}
		out.Write(data[i : closeEnd+1])
		i = closeEnd + 1
	}
	return out.Bytes()
}

// indexClosingTag finds a closing tag while requiring a tag-name boundary
// after it. This keeps a string like "</scripture>" inside a script from
// being mistaken for "</script>".
func indexClosingTag(data []byte, start int, closeTag string) int {
	for start <= len(data) {
		idx := indexFold(data[start:], closeTag)
		if idx < 0 {
			return -1
		}
		idx += start
		after := idx + len(closeTag)
		if after >= len(data) || isTagBoundary(data[after]) {
			return idx
		}
		start = idx + 1
	}
	return -1
}

func isTagBoundary(c byte) bool {
	return c == '>' || c == '/' || c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// findTagEnd returns the index of the '>' that ends the tag starting at
// start, respecting quoted attribute values. It returns -1 if the tag is
// unterminated.
func findTagEnd(data []byte, start int) int {
	inQuote := byte(0)
	for i := start; i < len(data); i++ {
		c := data[i]
		if inQuote != 0 {
			if c == inQuote {
				inQuote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			inQuote = c
		case '>':
			return i
		}
	}
	return -1
}

// indexFold is a case-insensitive bytes.Index for ASCII tag names.
func indexFold(data []byte, needle string) int {
	if len(needle) == 0 {
		return 0
	}
	for i := 0; i+len(needle) <= len(data); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			a, b := data[i+j], needle[j]
			if a >= 'A' && a <= 'Z' {
				a += 'a' - 'A'
			}
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func isTagNameByte(c byte) bool {
	return c == ':' || c == '-' || c == '_' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// epubChapterHTML extracts the body of a spine document as sanitized
// HTML. The chapter title is taken from the first heading.
func epubChapterHTML(chapter []byte, baseDir string, imgURL func(string) string, imgDims func(string) (int, int)) (title, body string) {
	doc, err := html.Parse(bytes.NewReader(normalizeXHTML(chapter)))
	if err != nil {
		return "", ""
	}
	var sb strings.Builder

	var findBody func(*html.Node) *html.Node
	findBody = func(n *html.Node) *html.Node {
		if n.Type == html.ElementNode && n.Data == "body" {
			return n
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if b := findBody(c); b != nil {
				return b
			}
		}
		return nil
	}
	bodyNode := findBody(doc)
	if bodyNode == nil {
		return "", ""
	}

	var render func(*html.Node)
	render = func(n *html.Node) {
		switch n.Type {
		case html.TextNode:
			sb.WriteString(html.EscapeString(n.Data))
			return
		case html.ElementNode:
			tag := strings.ToLower(n.Data)
			switch {
			case tag == "script" || tag == "style" || tag == "head" || tag == "iframe":
				return // drop entirely
			case tag == "image" || tag == "svg":
				// SVG-wrapped covers: keep nested <image> as <img>
				if tag == "image" {
					if src := nodeAttr(n, "href", "xlink:href"); src != "" {
						writeImg(&sb, baseDir, src, imgURL, imgDims)
					}
					return
				}
			case tag == "img":
				if src := nodeAttr(n, "src"); src != "" {
					writeImg(&sb, baseDir, src, imgURL, imgDims)
				}
				return
			case tag == "a":
				// Internal links (table of contents etc.) survive as
				// chapter jumps; the target is resolved in a second pass.
				if href := nodeAttr(n, "href"); href != "" && !strings.Contains(href, "://") && !strings.HasPrefix(href, "#") {
					target := path.Clean(path.Join(baseDir, unescapePath(strings.SplitN(href, "#", 2)[0])))
					sb.WriteString(`<a data-doc="` + html.EscapeString(target) + `">`)
					for c := n.FirstChild; c != nil; c = c.NextSibling {
						render(c)
					}
					sb.WriteString("</a>")
					return
				}
				// External links and same-page anchors become plain text.
			}
			allowed := epubAllowedTags[tag]
			if title == "" && len(tag) == 2 && tag[0] == 'h' && tag[1] >= '1' && tag[1] <= '4' {
				title = strings.TrimSpace(textContent(n))
			}
			if allowed {
				sb.WriteString("<" + tag + ">")
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				render(c)
			}
			if allowed && !voidTag(tag) {
				sb.WriteString("</" + tag + ">")
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			render(c)
		}
	}
	render(bodyNode)
	return title, sb.String()
}

func writeImg(sb *strings.Builder, baseDir, src string, imgURL func(string) string, imgDims func(string) (int, int)) {
	if strings.Contains(src, "://") {
		return // remote images are dropped
	}
	full := path.Clean(path.Join(baseDir, unescapePath(src)))
	dims := ""
	if w, h := imgDims(full); w > 0 && h > 0 {
		dims = fmt.Sprintf(` width="%d" height="%d"`, w, h)
	}
	sb.WriteString(`<img src="` + html.EscapeString(imgURL(url.PathEscape(full))) + `"` + dims + `/>`)
}

func nodeAttr(n *html.Node, names ...string) string {
	for _, a := range n.Attr {
		for _, want := range names {
			if strings.EqualFold(a.Key, want) {
				return a.Val
			}
		}
	}
	return ""
}

func textContent(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return sb.String()
}

func voidTag(tag string) bool { return tag == "br" || tag == "hr" || tag == "img" }

// unescapePath decodes a URI path from an EPUB manifest/HTML attribute.
// Invalid escapes are left untouched: some real-world EPUBs contain a
// literal '%' in a file name instead of %25.
func unescapePath(s string) string {
	if decoded, err := url.PathUnescape(s); err == nil {
		return decoded
	}
	return s
}

// EPUBBinary returns a file from inside the archive (chapter images).
// name is already unescaped by net/http's path matching; do not unescape it
// a second time or a literal '%' in a file name breaks the lookup.
func EPUBBinary(r io.ReaderAt, size int64, name string) ([]byte, string, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, "", err
	}
	rc, err := openZipFile(zr, name)
	if err != nil {
		return nil, "", ErrNoFile
	}
	defer rc.Close()
	data, err := readLimited(rc, maxCoverSize)
	if err != nil {
		return nil, "", err
	}
	mime := ""
	switch strings.ToLower(path.Ext(name)) {
	case ".jpg", ".jpeg":
		mime = "image/jpeg"
	case ".png":
		mime = "image/png"
	case ".gif":
		mime = "image/gif"
	case ".svg":
		mime = "application/octet-stream"
	case ".webp":
		mime = "image/webp"
	}
	return data, mime, nil
}
