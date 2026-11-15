package agent

import (
	"html"
	"net/url"
	"strings"
)

// A small, forgiving HTML tokenizer and a readable-text extractor. The
// standard library has no HTML parser and the harness must stay stdlib-only,
// so this handles what pages need for reading: tags, attributes, comments,
// raw-text elements (script, style, title, textarea) and entities.

type tokKind int

const (
	tokText tokKind = iota
	tokStart
	tokEnd
)

type htmlToken struct {
	kind      tokKind
	name      string // lower-case tag name
	attrs     []htmlAttr
	text      string // text tokens: raw text, entities not decoded
	selfClose bool
}

type htmlAttr struct{ key, val string }

func (t htmlToken) attr(key string) string {
	for _, a := range t.attrs {
		if a.key == key {
			return a.val
		}
	}
	return ""
}

func (t htmlToken) hasClass(c string) bool {
	for _, f := range strings.Fields(t.attr("class")) {
		if f == c {
			return true
		}
	}
	return false
}

// rawTextElems hold text that isn't parsed for tags.
var rawTextElems = map[string]bool{"script": true, "style": true, "title": true, "textarea": true, "xmp": true, "noscript": true}

type htmlTokenizer struct {
	s   string
	i   int
	raw string // inside this raw-text element
}

func newTokenizer(s string) *htmlTokenizer { return &htmlTokenizer{s: s} }

func isLetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' }

// next returns the next token, or false at the end.
func (z *htmlTokenizer) next() (htmlToken, bool) {
	for z.i < len(z.s) {
		if z.raw != "" {
			end := indexFold(z.s[z.i:], "</"+z.raw)
			z.raw = ""
			if end < 0 {
				t := htmlToken{kind: tokText, text: z.s[z.i:]}
				z.i = len(z.s)
				return t, true
			}
			if end > 0 {
				t := htmlToken{kind: tokText, text: z.s[z.i : z.i+end]}
				z.i += end
				return t, true
			}
			continue
		}
		if z.s[z.i] != '<' {
			j := strings.IndexByte(z.s[z.i:], '<')
			if j < 0 {
				j = len(z.s) - z.i
			}
			t := htmlToken{kind: tokText, text: z.s[z.i : z.i+j]}
			z.i += j
			return t, true
		}
		rest := z.s[z.i:]
		switch {
		case strings.HasPrefix(rest, "<!--"):
			end := strings.Index(rest[4:], "-->")
			if end < 0 {
				z.i = len(z.s)
			} else {
				z.i += 4 + end + 3
			}
			continue
		case strings.HasPrefix(rest, "<!") || strings.HasPrefix(rest, "<?"):
			end := strings.IndexByte(rest, '>')
			if end < 0 {
				z.i = len(z.s)
			} else {
				z.i += end + 1
			}
			continue
		case strings.HasPrefix(rest, "</") && len(rest) > 2 && isLetter(rest[2]):
			j := 2
			for j < len(rest) && !isSpace(rest[j]) && rest[j] != '>' && rest[j] != '/' {
				j++
			}
			name := strings.ToLower(rest[2:j])
			end := strings.IndexByte(rest[j:], '>')
			if end < 0 {
				z.i = len(z.s)
			} else {
				z.i += j + end + 1
			}
			return htmlToken{kind: tokEnd, name: name}, true
		case len(rest) > 1 && isLetter(rest[1]):
			t, n := parseStartTag(rest)
			z.i += n
			if rawTextElems[t.name] && !t.selfClose {
				z.raw = t.name
			}
			return t, true
		default:
			z.i++
			return htmlToken{kind: tokText, text: "<"}, true
		}
	}
	return htmlToken{}, false
}

// parseStartTag parses "<name attr=val ...>" at the start of s and returns the
// token and the bytes consumed.
func parseStartTag(s string) (htmlToken, int) {
	j := 1
	for j < len(s) && !isSpace(s[j]) && s[j] != '>' && s[j] != '/' {
		j++
	}
	t := htmlToken{kind: tokStart, name: strings.ToLower(s[1:j])}
	for j < len(s) {
		for j < len(s) && isSpace(s[j]) {
			j++
		}
		if j >= len(s) {
			break
		}
		if s[j] == '>' {
			return t, j + 1
		}
		if s[j] == '/' {
			if j+1 < len(s) && s[j+1] == '>' {
				t.selfClose = true
				return t, j + 2
			}
			j++
			continue
		}
		k := j
		for k < len(s) && !isSpace(s[k]) && s[k] != '=' && s[k] != '>' && !(s[k] == '/' && k+1 < len(s) && s[k+1] == '>') {
			k++
		}
		key := strings.ToLower(s[j:k])
		j = k
		for j < len(s) && isSpace(s[j]) {
			j++
		}
		val := ""
		if j < len(s) && s[j] == '=' {
			j++
			for j < len(s) && isSpace(s[j]) {
				j++
			}
			if j < len(s) && (s[j] == '"' || s[j] == '\'') {
				q := s[j]
				end := strings.IndexByte(s[j+1:], q)
				if end < 0 {
					val = s[j+1:]
					j = len(s)
				} else {
					val = s[j+1 : j+1+end]
					j += end + 2
				}
			} else {
				k := j
				for k < len(s) && !isSpace(s[k]) && s[k] != '>' {
					k++
				}
				val = s[j:k]
				j = k
			}
		}
		if key != "" {
			t.attrs = append(t.attrs, htmlAttr{key: key, val: html.UnescapeString(val)})
		}
	}
	return t, len(s)
}

// indexFold is a case-insensitive strings.Index for an ASCII needle.
func indexFold(s, sub string) int {
	n := len(sub)
	for i := 0; i+n <= len(s); i++ {
		if strings.EqualFold(s[i:i+n], sub) {
			return i
		}
	}
	return -1
}

// ---------------------------------------------------------------------------
// Readable text
// ---------------------------------------------------------------------------

// skipElems are dropped with everything inside them: code, chrome and widgets
// that carry no readable content.
var skipElems = map[string]bool{
	"script": true, "style": true, "noscript": true, "template": true, "svg": true,
	"math": true, "canvas": true, "iframe": true, "object": true, "embed": true,
	"nav": true, "footer": true, "aside": true, "button": true, "select": true,
	"dialog": true,
}

var voidElems = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true,
	"img": true, "input": true, "link": true, "meta": true, "source": true,
	"track": true, "wbr": true, "param": true,
}

// blockElems start on a new line.
var blockElems = map[string]bool{
	"address": true, "article": true, "blockquote": true, "dd": true, "details": true,
	"div": true, "dl": true, "dt": true, "fieldset": true, "figcaption": true,
	"figure": true, "form": true, "header": true, "hr": true, "main": true,
	"ol": true, "section": true, "summary": true, "table": true, "tr": true,
	"ul": true, "caption": true, "thead": true, "tbody": true, "label": true,
}

// pageText is the readable form of an HTML page.
type pageText struct {
	Title string
	Text  string
}

type textBuilder struct {
	b    []byte
	pre  int
	link *linkState
}

type linkState struct {
	href string
	text []byte
}

func (tb *textBuilder) out() *[]byte {
	if tb.link != nil {
		return &tb.link.text
	}
	return &tb.b
}

func (tb *textBuilder) text(s string) {
	s = strings.ReplaceAll(html.UnescapeString(s), "\u00a0", " ")
	o := tb.out()
	if tb.pre > 0 {
		*o = append(*o, s...)
		return
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isSpace(c) || c == ' ' {
			if n := len(*o); n > 0 && (*o)[n-1] != ' ' && (*o)[n-1] != '\n' {
				*o = append(*o, ' ')
			}
			continue
		}
		*o = append(*o, c)
	}
}

// newline ends the current line, leaving at least n line breaks.
func (tb *textBuilder) newline(n int) {
	tb.endLink()
	for len(tb.b) > 0 && tb.b[len(tb.b)-1] == ' ' {
		tb.b = tb.b[:len(tb.b)-1]
	}
	if len(tb.b) == 0 {
		return
	}
	have := 0
	for i := len(tb.b) - 1; i >= 0 && tb.b[i] == '\n'; i-- {
		have++
	}
	for ; have < n; have++ {
		tb.b = append(tb.b, '\n')
	}
}

func (tb *textBuilder) raw(s string) {
	o := tb.out()
	*o = append(*o, s...)
}

func (tb *textBuilder) startLink(href string) {
	tb.endLink()
	tb.link = &linkState{href: href}
}

// endLink writes the pending link as [text](url), or as plain text when it has
// no usable URL or spans several lines.
func (tb *textBuilder) endLink() {
	l := tb.link
	if l == nil {
		return
	}
	tb.link = nil
	text := strings.TrimSpace(string(l.text))
	if text == "" {
		return
	}
	if n := len(tb.b); n > 0 && tb.b[n-1] != ' ' && tb.b[n-1] != '\n' && len(l.text) > 0 && l.text[0] == ' ' {
		tb.b = append(tb.b, ' ')
	}
	if l.href == "" || strings.ContainsRune(text, '\n') || text == l.href {
		tb.b = append(tb.b, text...)
	} else {
		tb.b = append(tb.b, '[')
		tb.b = append(tb.b, text...)
		tb.b = append(tb.b, "]("...)
		tb.b = append(tb.b, l.href...)
		tb.b = append(tb.b, ')')
	}
	if last := l.text[len(l.text)-1]; last == ' ' {
		tb.b = append(tb.b, ' ')
	}
}

// htmlToText extracts a page's title and readable text: scripts, styles and
// page chrome (nav, footer, aside) are dropped, blocks become lines, headings
// and list items get Markdown markers, and links become [text](url) with URLs
// resolved against base. When the page marks its content with <main> or
// <article>, only that part is kept.
func htmlToText(src string, base *url.URL) pageText {
	z := newTokenizer(src)
	var tb textBuilder
	var title string
	var skip []string // open skipped elements
	inTitle := false
	mainDepth, mainStart, mainEnd := 0, -1, -1
	for {
		t, ok := z.next()
		if !ok {
			break
		}
		switch t.kind {
		case tokText:
			if inTitle {
				if title == "" {
					title = strings.Join(strings.Fields(html.UnescapeString(t.text)), " ")
				}
				continue
			}
			if len(skip) == 0 {
				tb.text(t.text)
			}
		case tokStart:
			if t.name == "title" && !t.selfClose {
				inTitle = true
				continue
			}
			if skipElems[t.name] && !t.selfClose && !voidElems[t.name] {
				skip = append(skip, t.name)
				continue
			}
			if len(skip) > 0 {
				continue
			}
			switch {
			case t.name == "main" || t.name == "article":
				tb.newline(1)
				if mainStart < 0 {
					mainStart = len(tb.b)
				}
				if mainEnd < 0 {
					mainDepth++
				}
			case len(t.name) == 2 && t.name[0] == 'h' && t.name[1] >= '1' && t.name[1] <= '6':
				tb.newline(2)
				tb.raw(strings.Repeat("#", int(t.name[1]-'0')) + " ")
			case t.name == "p":
				tb.newline(2)
			case t.name == "br":
				tb.newline(1)
			case t.name == "li":
				tb.newline(1)
				tb.raw("- ")
			case t.name == "td" || t.name == "th":
				if n := len(tb.b); n > 0 && tb.b[n-1] != '\n' {
					tb.raw(" | ")
				}
			case t.name == "pre":
				tb.newline(1)
				tb.pre++
			case t.name == "a":
				href := resolveHref(base, t.attr("href"))
				tb.startLink(href)
			case blockElems[t.name]:
				tb.newline(1)
			}
		case tokEnd:
			if t.name == "title" {
				inTitle = false
				continue
			}
			if len(skip) > 0 {
				// Close the innermost matching skipped element; tolerate
				// unbalanced markup by unwinding to it.
				for i := len(skip) - 1; i >= 0; i-- {
					if skip[i] == t.name {
						skip = skip[:i]
						break
					}
				}
				continue
			}
			switch {
			case t.name == "a":
				tb.endLink()
			case t.name == "main" || t.name == "article":
				if mainDepth > 0 {
					mainDepth--
					if mainDepth == 0 {
						tb.newline(1)
						mainEnd = len(tb.b)
					}
				}
			case len(t.name) == 2 && t.name[0] == 'h' && t.name[1] >= '1' && t.name[1] <= '6':
				tb.newline(2)
			case t.name == "p":
				tb.newline(2)
			case t.name == "pre":
				if tb.pre > 0 {
					tb.pre--
				}
				tb.newline(1)
			case t.name == "li" || blockElems[t.name]:
				tb.newline(1)
			}
		}
	}
	tb.endLink()
	text := string(tb.b)
	if mainStart >= 0 {
		end := mainEnd
		if end < 0 {
			end = len(tb.b)
		}
		if part := strings.TrimSpace(string(tb.b[mainStart:end])); len(part) >= 200 {
			text = part
		}
	}
	return pageText{Title: title, Text: tidyLines(text)}
}

// tidyLines trims trailing spaces and collapses runs of blank lines.
func tidyLines(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, l := range lines {
		l = strings.TrimRight(l, " \t\r")
		if strings.TrimSpace(l) == "" {
			blank++
			if blank > 1 {
				continue
			}
			l = ""
		} else {
			blank = 0
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// resolveHref returns an absolute http(s) URL for a link, or "" when it is an
// in-page anchor, a script, or unusable.
func resolveHref(base *url.URL, href string) string {
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "#") {
		return ""
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if base != nil {
		u = base.ResolveReference(u)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return u.String()
}

// plainText strips tags from an HTML fragment (search snippets).
func plainText(fragment string) string {
	z := newTokenizer(fragment)
	var b strings.Builder
	for {
		t, ok := z.next()
		if !ok {
			break
		}
		if t.kind == tokText {
			b.WriteString(t.text)
		}
	}
	return strings.Join(strings.Fields(html.UnescapeString(b.String())), " ")
}
