package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Search endpoints. Tests point them at fakes.
const (
	duckDuckGoURL = "https://html.duckduckgo.com/html/"
	braveURL      = "https://api.search.brave.com/res/v1/web/search"
)

// SearchResult is one web search hit.
type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

type searchTool struct {
	cfg      SearchConfig
	client   *http.Client
	ddgURL   string
	braveURL string
}

// newSearchTool builds search_web. Search goes to a fixed or configured
// endpoint (never a URL the model picks), so it uses an ordinary client and
// honors proxy settings; a SearXNG on the local network is fine.
func newSearchTool(cfg SearchConfig, client *http.Client) *searchTool {
	if client == nil {
		client = &http.Client{
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				ForceAttemptHTTP2:     true,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
				MaxIdleConns:          8,
				IdleConnTimeout:       60 * time.Second,
			},
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= maxRedirects {
					return fmt.Errorf("stopped after %d redirects", maxRedirects)
				}
				return nil
			},
		}
	}
	return &searchTool{cfg: cfg, client: client, ddgURL: duckDuckGoURL, braveURL: braveURL}
}

func (t *searchTool) Name() string { return ToolSearchWeb }

func (t *searchTool) Description() string {
	return "Search the web. Returns titles, URLs and short snippets; open the most relevant results with fetch_url " +
		"before answering, and cite the URLs you used."
}

func (t *searchTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{` +
		`"query":{"type":"string","description":"What to search for: a few specific keywords."},` +
		`"count":{"type":"integer","description":"How many results to return (default ` + strconv.Itoa(t.cfg.MaxResults) + `, at most ` + strconv.Itoa(t.cfg.MaxResults) + `)."}` +
		`},"required":["query"]}`)
}

func (t *searchTool) Run(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Query string  `json:"query"`
		Count flexInt `json:"count"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return "", err
	}
	q := strings.Join(strings.Fields(a.Query), " ")
	if q == "" {
		return "", errors.New("query is required")
	}
	if len(q) > 500 {
		q = truncateUTF8(q, 500)
	}
	n := int(a.Count)
	if n <= 0 || n > t.cfg.MaxResults {
		n = t.cfg.MaxResults
	}
	results, err := t.Search(ctx, q, n)
	if err != nil {
		return "", err
	}
	return formatResults(q, t.cfg.Backend, results), nil
}

// Search runs one query on the configured backend.
func (t *searchTool) Search(ctx context.Context, q string, n int) ([]SearchResult, error) {
	var (
		res []SearchResult
		err error
	)
	switch t.cfg.Backend {
	case SearchSearXNG:
		res, err = t.searxng(ctx, q)
	case SearchBrave:
		res, err = t.brave(ctx, q, n)
	default:
		res, err = t.duckduckgo(ctx, q)
	}
	if err != nil {
		return nil, err
	}
	if len(res) > n {
		res = res[:n]
	}
	return res, nil
}

func formatResults(q, backend string, res []SearchResult) string {
	if len(res) == 0 {
		return fmt.Sprintf("No results for %q.", q)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Search results for %q (%s):\n", q, backend)
	for i, r := range res {
		fmt.Fprintf(&b, "\n%d. %s\n   %s\n", i+1, r.Title, r.URL)
		if r.Snippet != "" {
			fmt.Fprintf(&b, "   %s\n", r.Snippet)
		}
	}
	return b.String()
}

func (t *searchTool) do(req *http.Request, what string) ([]byte, int, error) {
	req.Header.Set("User-Agent", userAgent)
	resp, err := t.client.Do(req)
	if err != nil {
		if ue, ok := err.(*url.Error); ok {
			err = ue.Err
		}
		return nil, 0, fmt.Errorf("%s search failed: %v", what, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("%s search failed: %v", what, err)
	}
	return body, resp.StatusCode, nil
}

// duckduckgo uses DuckDuckGo's no-JavaScript HTML results page, which needs no
// key. It answers bot checks with HTTP 202 or a challenge page when queried
// too often.
func (t *searchTool) duckduckgo(ctx context.Context, q string) ([]SearchResult, error) {
	form := url.Values{"q": {q}, "b": {""}, "kl": {""}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.ddgURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Referer", "https://html.duckduckgo.com/")
	body, status, err := t.do(req, "DuckDuckGo")
	if err != nil {
		return nil, err
	}
	page := string(body)
	if status == http.StatusAccepted || strings.Contains(page, "anomaly-modal") || strings.Contains(page, "challenge-form") {
		return nil, errors.New("DuckDuckGo refused the search with a bot check; wait a minute and retry, or ask the operator to set HARNESS_SEARXNG_URL to a SearXNG instance")
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("DuckDuckGo search failed: HTTP %d", status)
	}
	return parseDuckDuckGo(page), nil
}

// parseDuckDuckGo reads the results from a DuckDuckGo HTML results page: each
// a.result__a holds a title and link (direct, or a /l/?uddg= redirect), and
// the following .result__snippet its snippet. Ads are skipped.
func parseDuckDuckGo(page string) []SearchResult {
	z := newTokenizer(page)
	var out []SearchResult
	cur := -1 // index of the result being read
	var collect *strings.Builder
	collectTag, depth := "", 0
	for {
		t, ok := z.next()
		if !ok {
			break
		}
		switch t.kind {
		case tokText:
			if collect != nil {
				collect.WriteString(t.text)
			}
		case tokStart:
			if collect != nil {
				if t.name == collectTag && !t.selfClose {
					depth++
				}
				continue
			}
			switch {
			case t.name == "a" && t.hasClass("result__a"):
				link := ddgLink(t.attr("href"))
				if link == "" {
					cur = -1
					continue
				}
				out = append(out, SearchResult{URL: link})
				cur = len(out) - 1
				collect, collectTag, depth = &strings.Builder{}, "a", 1
			case t.hasClass("result__snippet") && cur >= 0 && !t.selfClose:
				collect, collectTag, depth = &strings.Builder{}, t.name, 1
			}
		case tokEnd:
			if collect == nil || t.name != collectTag {
				continue
			}
			depth--
			if depth > 0 {
				continue
			}
			text := plainText(collect.String())
			collect = nil
			if cur < 0 {
				continue
			}
			if out[cur].Title == "" {
				out[cur].Title = text
			} else if out[cur].Snippet == "" {
				out[cur].Snippet = text
			}
		}
	}
	// Drop entries without a title (broken markup).
	kept := out[:0]
	for _, r := range out {
		if r.Title != "" {
			kept = append(kept, r)
		}
	}
	return kept
}

// ddgLink returns the destination of a result link, or "" for ads and
// DuckDuckGo's own pages.
func ddgLink(href string) string {
	href = strings.TrimSpace(href)
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if strings.HasSuffix(u.Hostname(), "duckduckgo.com") || (u.Host == "" && strings.HasPrefix(u.Path, "/l/")) {
		if u.Path == "/l/" || strings.HasPrefix(u.Path, "/l/") {
			if dest := u.Query().Get("uddg"); dest != "" {
				return ddgLink(dest)
			}
		}
		return "" // y.js ads and other DuckDuckGo pages
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return u.String()
}

// searxng queries a SearXNG instance's JSON API (search.formats must include
// json in its settings.yml).
func (t *searchTool) searxng(ctx context.Context, q string) ([]SearchResult, error) {
	base := strings.TrimRight(t.cfg.SearXNGURL, "/")
	u := base + "/search?" + url.Values{"q": {q}, "format": {"json"}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	body, status, err := t.do(req, "SearXNG")
	if err != nil {
		return nil, err
	}
	switch {
	case status == http.StatusForbidden:
		return nil, errors.New("SearXNG refused JSON output: add json to search.formats in its settings.yml")
	case status == http.StatusTooManyRequests:
		return nil, errors.New("SearXNG is rate-limiting the harness (HTTP 429); turn off its limiter for this client")
	case status != http.StatusOK:
		return nil, fmt.Errorf("SearXNG search failed: HTTP %d", status)
	}
	var r struct {
		Results []struct {
			URL     string `json:"url"`
			Title   string `json:"title"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("SearXNG returned something other than JSON results: %v", err)
	}
	out := make([]SearchResult, 0, len(r.Results))
	for _, x := range r.Results {
		out = append(out, SearchResult{Title: plainText(x.Title), URL: x.URL, Snippet: plainText(x.Content)})
	}
	return out, nil
}

// brave queries the Brave Search API with the key from HARNESS_BRAVE_API_KEY.
func (t *searchTool) brave(ctx context.Context, q string, n int) ([]SearchResult, error) {
	if t.cfg.APIKey == "" {
		return nil, errors.New("Brave search needs an API key: run the container with -e HARNESS_BRAVE_API_KEY=...")
	}
	u := t.braveURL + "?" + url.Values{"q": {q}, "count": {strconv.Itoa(n)}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", t.cfg.APIKey)
	body, status, err := t.do(req, "Brave")
	if err != nil {
		return nil, err
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return nil, errors.New("Brave rejected the API key")
	case status == http.StatusTooManyRequests:
		return nil, errors.New("Brave search rate limit reached; try again later")
	case status != http.StatusOK:
		return nil, fmt.Errorf("Brave search failed: HTTP %d", status)
	}
	var r struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("Brave returned something other than JSON results: %v", err)
	}
	out := make([]SearchResult, 0, len(r.Web.Results))
	for _, x := range r.Web.Results {
		out = append(out, SearchResult{Title: plainText(x.Title), URL: x.URL, Snippet: plainText(x.Description)})
	}
	return out, nil
}
