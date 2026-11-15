package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// userAgent identifies the harness to the sites it reads and searches. It
// carries no user or host details.
const userAgent = "Mozilla/5.0 (compatible; llm-factory-harness/" + Version + ")"

const maxRedirects = 5

type fetchTool struct {
	client   *http.Client
	maxBytes int
	maxOut   int
}

// newFetchTool builds fetch_url. Its HTTP client ignores proxy settings (a
// proxy would make the connection the guard can't see) and dials only through
// the guard.
func newFetchTool(cfg FetchConfig, maxOut int, g *Guard) *fetchTool {
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           g.DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConns:          16,
		IdleConnTimeout:       30 * time.Second,
	}
	client := &http.Client{
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("stopped after %d redirects", maxRedirects)
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("refused a redirect to a %s: URL", req.URL.Scheme)
			}
			return CheckHost(req.URL.Hostname())
		},
	}
	return &fetchTool{client: client, maxBytes: cfg.MaxBytes, maxOut: maxOut}
}

func (t *fetchTool) Name() string { return ToolFetchURL }

func (t *fetchTool) Description() string {
	return "Read a web page and return its text (title, headings, paragraphs, lists and links as Markdown). " +
		"Use it to open search results or a URL the user gave. Long pages come back in parts: " +
		"call again with the offset it tells you to read more. Only public http(s) sites can be read."
}

func (t *fetchTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{` +
		`"url":{"type":"string","description":"The http or https URL to read."},` +
		`"offset":{"type":"integer","description":"Where to continue in the page text, from a previous call. Default 0."}` +
		`},"required":["url"]}`)
}

func (t *fetchTool) Run(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		URL    string  `json:"url"`
		Offset flexInt `json:"offset"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return "", err
	}
	raw := strings.TrimSpace(a.URL)
	if raw == "" {
		return "", errors.New("url is required")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("%q is not a URL", a.URL)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("only http and https URLs can be read, not %s:", u.Scheme)
	}
	if err := CheckHost(u.Hostname()); err != nil {
		return "", err
	}
	u.Fragment = ""

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9,application/json;q=0.8,*/*;q=0.5")
	req.Header.Set("Accept-Language", "en;q=0.9,*;q=0.5")
	resp, err := t.client.Do(req)
	if err != nil {
		var be *BlockedError
		if errors.As(err, &be) {
			return "", be
		}
		if ue, ok := err.(*url.Error); ok {
			err = ue.Err
		}
		return "", fmt.Errorf("can't fetch %s: %v", u.Redacted(), err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(t.maxBytes)+1))
	if err != nil && len(body) == 0 {
		return "", fmt.Errorf("reading %s: %v", u.Redacted(), err)
	}
	cut := len(body) > t.maxBytes
	if cut {
		body = body[:t.maxBytes]
	}
	final := resp.Request.URL
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("%s answered HTTP %d %s", final.Redacted(), resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	ctype := resp.Header.Get("Content-Type")
	if ctype == "" {
		ctype = http.DetectContentType(body)
	}
	mt, params, _ := mime.ParseMediaType(ctype)
	text := decodeCharset(body, params["charset"])
	var title string
	switch {
	case mt == "text/html" || mt == "application/xhtml+xml":
		p := htmlToText(text, final)
		title, text = p.Title, p.Text
	case isTextType(mt):
	default:
		return "", fmt.Errorf("fetch_url reads text pages; %s is %s (%d bytes)", final.Redacted(), mt, len(body))
	}
	return pageWindow(final.String(), title, text, int(a.Offset), t.maxOut, cut), nil
}

func isTextType(mt string) bool {
	switch {
	case strings.HasPrefix(mt, "text/"):
		return true
	case mt == "application/json", mt == "application/xml", mt == "application/javascript",
		mt == "application/x-yaml", mt == "application/yaml", mt == "application/toml",
		strings.HasSuffix(mt, "+json"), strings.HasSuffix(mt, "+xml"):
		return true
	}
	return false
}

// decodeCharset returns body as UTF-8 text: Latin-1 and Windows-1252 pages are
// converted, anything else invalid is replaced.
func decodeCharset(body []byte, charset string) string {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "iso-8859-1", "latin1", "latin-1", "windows-1252", "cp1252", "us-ascii":
		if !utf8.Valid(body) {
			r := make([]rune, len(body))
			for i, b := range body {
				r[i] = rune(b)
			}
			return string(r)
		}
	}
	return strings.ToValidUTF8(string(body), "�")
}

// pageWindow renders one part of a page's text that fits in maxOut bytes,
// with a note saying where the next part starts.
func pageWindow(u, title, text string, offset, maxOut int, downloadCut bool) string {
	var head strings.Builder
	head.WriteString("URL: " + u + "\n")
	if title != "" {
		head.WriteString("Title: " + title + "\n")
	}
	head.WriteString("\n")
	if offset < 0 {
		offset = 0
	}
	if offset > len(text) {
		return head.String() + fmt.Sprintf("[offset %d is past the end of the page text (%d bytes)]", offset, len(text))
	}
	for offset > 0 && offset < len(text) && !utf8.RuneStart(text[offset]) {
		offset++
	}
	budget := maxOut - head.Len() - 200
	if budget < 256 {
		budget = 256
	}
	part := truncateUTF8(text[offset:], budget)
	end := offset + len(part)
	var b strings.Builder
	b.WriteString(head.String())
	if strings.TrimSpace(part) == "" && offset == 0 {
		b.WriteString("[the page has no readable text]")
	} else {
		b.WriteString(part)
	}
	switch {
	case end < len(text):
		fmt.Fprintf(&b, "\n\n[Showing bytes %d-%d of %d of the page text. Call fetch_url with offset=%s to read more.]", offset, end, len(text), strconv.Itoa(end))
	case offset > 0:
		fmt.Fprintf(&b, "\n\n[End of the page text (bytes %d-%d of %d).]", offset, end, len(text))
	}
	if downloadCut && end >= len(text) {
		b.WriteString("\n[The page was larger than the download limit; the rest wasn't read.]")
	}
	return b.String()
}
