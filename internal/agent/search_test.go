package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ddgPage mirrors DuckDuckGo's HTML results page: an ad, a result with a
// direct link (the current markup) and one with a /l/?uddg= redirect (older
// markup), plus entities and <b> highlights.
const ddgPage = `<!DOCTYPE html><html><head><title>q at DuckDuckGo</title></head><body>
<div class="result results_links result--ad">
  <h2 class="result__title"><a rel="nofollow" class="result__a" href="https://duckduckgo.com/y.js?ad_domain=x&amp;u3=1">Buy stuff</a></h2>
  <a class="result__snippet" href="https://duckduckgo.com/y.js?x">Ad text</a>
</div>
<div class="result results_links results_links_deep web-result ">
  <div class="links_main links_deep result__body">
    <h2 class="result__title">
      <a rel="nofollow" class="result__a" href="https://github.com/ggml-org/llama.cpp">GitHub - ggml-org/<b>llama.cpp</b>: LLM inference in C/C++</a>
    </h2>
    <div class="result__extras"><div class="result__extras__url"><a class="result__url" href="https://github.com/ggml-org/llama.cpp">github.com/ggml-org/llama.cpp</a></div></div>
    <a class="result__snippet" href="https://github.com/ggml-org/llama.cpp">LLM inference in C/C++. Contribute to <b>llama.cpp</b> &amp; more.</a>
  </div>
</div>
<div class="result results_links results_links_deep web-result ">
  <h2 class="result__title"><a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.org%2Fpage%3Fa%3D1&amp;rut=abc">Example &quot;page&quot;</a></h2>
  <a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.org%2Fpage">Second snippet</a>
</div>
</body></html>`

func TestParseDuckDuckGo(t *testing.T) {
	res := parseDuckDuckGo(ddgPage)
	if len(res) != 2 {
		t.Fatalf("results = %+v", res)
	}
	if res[0].URL != "https://github.com/ggml-org/llama.cpp" || res[0].Title != "GitHub - ggml-org/llama.cpp: LLM inference in C/C++" ||
		res[0].Snippet != "LLM inference in C/C++. Contribute to llama.cpp & more." {
		t.Errorf("first = %+v", res[0])
	}
	if res[1].URL != "https://example.org/page?a=1" || res[1].Title != `Example "page"` || res[1].Snippet != "Second snippet" {
		t.Errorf("second = %+v", res[1])
	}
}

func newTestSearch(cfg SearchConfig, srv *httptest.Server) *searchTool {
	if cfg.MaxResults == 0 {
		cfg.MaxResults = 5
	}
	st := newSearchTool(cfg, srv.Client())
	st.ddgURL = srv.URL + "/html/"
	st.braveURL = srv.URL + "/brave"
	return st
}

func TestDuckDuckGoSearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.FormValue("q") != "llama cpp" {
			t.Errorf("request %s q=%q", r.Method, r.FormValue("q"))
		}
		if ua := r.UserAgent(); !strings.Contains(ua, "llm-factory-harness") {
			t.Errorf("user agent %q", ua)
		}
		io.WriteString(w, ddgPage)
	}))
	defer srv.Close()
	st := newTestSearch(SearchConfig{Backend: SearchDuckDuckGo}, srv)
	out, err := st.Run(context.Background(), json.RawMessage(`{"query":"  llama   cpp ","count":"1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1. GitHub - ggml-org/llama.cpp") || strings.Contains(out, "example.org") || strings.Contains(out, "Buy stuff") {
		t.Errorf("output = %s", out)
	}
	if _, err := st.Run(context.Background(), json.RawMessage(`{"query":""}`)); err == nil {
		t.Error("empty query accepted")
	}
}

func TestDuckDuckGoBotCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `<div class="anomaly-modal">bots</div>`)
	}))
	defer srv.Close()
	_, err := newTestSearch(SearchConfig{}, srv).Run(context.Background(), json.RawMessage(`{"query":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "SearXNG") {
		t.Errorf("err = %v", err)
	}
}

func TestSearXNG(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/forbidden/search" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.URL.Path != "/sx/search" || r.URL.Query().Get("format") != "json" || r.URL.Query().Get("q") != "go generics" {
			t.Errorf("request %s", r.URL)
		}
		io.WriteString(w, `{"results":[{"url":"https://go.dev/doc","title":"Go <b>docs</b>","content":"All about Go."},{"url":"https://b","title":"B","content":""}]}`)
	}))
	defer srv.Close()
	st := newTestSearch(SearchConfig{Backend: SearchSearXNG, SearXNGURL: srv.URL + "/sx/"}, srv)
	out, err := st.Run(context.Background(), json.RawMessage(`{"query":"go generics"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1. Go docs\n   https://go.dev/doc\n   All about Go.") || !strings.Contains(out, "(searxng)") {
		t.Errorf("output = %q", out)
	}
	st.cfg.SearXNGURL = srv.URL + "/forbidden"
	if _, err := st.Run(context.Background(), json.RawMessage(`{"query":"x"}`)); err == nil || !strings.Contains(err.Error(), "settings.yml") {
		t.Errorf("403 err = %v", err)
	}
}

func TestBrave(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Subscription-Token") != "key123" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("count") != "2" {
			t.Errorf("count = %s", r.URL.Query().Get("count"))
		}
		io.WriteString(w, `{"web":{"results":[{"title":"T","url":"https://t","description":"<strong>D</strong>"}]}}`)
	}))
	defer srv.Close()
	st := newTestSearch(SearchConfig{Backend: SearchBrave, APIKey: "key123"}, srv)
	out, err := st.Run(context.Background(), json.RawMessage(`{"query":"x","count":2}`))
	if err != nil || !strings.Contains(out, "1. T\n   https://t\n   D") {
		t.Errorf("out = %q err = %v", out, err)
	}
	st.cfg.APIKey = "wrong"
	if _, err := st.Run(context.Background(), json.RawMessage(`{"query":"x"}`)); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Errorf("bad key err = %v", err)
	}
	st.cfg.APIKey = ""
	if _, err := st.Run(context.Background(), json.RawMessage(`{"query":"x"}`)); err == nil {
		t.Error("no key accepted")
	}
}

func TestDDGLink(t *testing.T) {
	cases := map[string]string{
		"https://a.com/x": "https://a.com/x",
		"//duckduckgo.com/l/?uddg=https%3A%2F%2Fb.com": "https://b.com",
		"/l/?uddg=https%3A%2F%2Fc.com":                 "https://c.com",
		"https://duckduckgo.com/y.js?ad=1":             "",
		"javascript:alert(1)":                          "",
	}
	for in, want := range cases {
		if got := ddgLink(in); got != want {
			t.Errorf("ddgLink(%q) = %q, want %q", in, got, want)
		}
	}
}
