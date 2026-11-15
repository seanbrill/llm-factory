package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAddrBlocked(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.255.0.9", "10.1.2.3", "172.16.0.1", "172.31.255.255", "192.168.1.1",
		"169.254.169.254", "100.100.100.200", "100.64.0.1", "0.0.0.0", "0.1.2.3", "224.0.0.1",
		"255.255.255.255", "240.0.0.1", "198.18.0.1", "192.0.0.170", "192.0.2.1",
		"::1", "::", "fe80::1", "fe80::1%eth0", "fc00::1", "fd00:ec2::254", "ff02::1",
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", "::ffff:169.254.169.254",
		"64:ff9b::7f00:1", "64:ff9b::a9fe:a9fe", "2002:7f00:1::1", "2002:c0a8:101::1",
		"2001::1", "2001:db8::1", "::127.0.0.1", "fec0::1",
	}
	for _, s := range blocked {
		if bad, why := AddrBlocked(netip.MustParseAddr(s)); !bad || why == "" {
			t.Errorf("%s is allowed", s)
		}
	}
	allowed := []string{"93.184.216.34", "8.8.8.8", "1.1.1.1", "172.32.0.1", "100.128.0.1", "2606:4700::1111", "64:ff9b::808:808", "2002:808:808::1"}
	for _, s := range allowed {
		if bad, why := AddrBlocked(netip.MustParseAddr(s)); bad {
			t.Errorf("%s is blocked (%s)", s, why)
		}
	}
	if bad, _ := AddrBlocked(netip.Addr{}); !bad {
		t.Error("the zero address is allowed")
	}
}

func TestCheckHost(t *testing.T) {
	for _, h := range []string{"localhost", "LOCALHOST.", "app.localhost", "metadata.google.internal", "metadata",
		"host.docker.internal", "host.containers.internal", "printer.local", "router.home.arpa", "intranet", ""} {
		if CheckHost(h) == nil {
			t.Errorf("%q is allowed", h)
		}
	}
	for _, h := range []string{"example.com", "docs.python.org", "93.184.216.34", "::1", "public.test"} {
		if err := CheckHost(h); err != nil {
			t.Errorf("%q refused: %v", h, err)
		}
	}
}

// fetchHarness is a fetch_url whose DNS is a table and whose "public"
// addresses all lead to one local test server.
type fetchHarness struct {
	t      *testing.T
	srv    *httptest.Server
	tool   *fetchTool
	secret atomic.Int32
}

const publicIP = "93.184.216.34"

func newFetchHarness(t *testing.T, maxBytes, maxOut int) *fetchHarness {
	h := &fetchHarness{t: t}
	mux := http.NewServeMux()
	h.srv = httptest.NewServer(mux)
	t.Cleanup(h.srv.Close)
	port := h.srv.Listener.Addr().(*net.TCPAddr).Port
	redirect := func(to string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, to, http.StatusFound) }
	}
	mux.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, `<html><head><title>Test &amp; Page</title><script>var x="hidden";</script></head>
<body><nav>Menu Home About</nav><h1>Hello</h1><p>First <a href="/next">paragraph</a>.</p></body></html>`)
	})
	mux.HandleFunc("/secret", func(w http.ResponseWriter, r *http.Request) {
		h.secret.Add(1)
		io.WriteString(w, "SECRET")
	})
	mux.HandleFunc("/redirect-public", redirect("http://other.test/page"))
	mux.HandleFunc("/redirect-loopback", redirect(fmt.Sprintf("http://127.0.0.1:%d/secret", port)))
	mux.HandleFunc("/redirect-v6", redirect(fmt.Sprintf("http://[::1]:%d/secret", port)))
	mux.HandleFunc("/redirect-internal", redirect("http://internal.test/secret"))
	mux.HandleFunc("/redirect-metadata", redirect("http://169.254.169.254/latest/meta-data/"))
	mux.HandleFunc("/redirect-localhost", redirect(fmt.Sprintf("http://localhost:%d/secret", port)))
	mux.HandleFunc("/redirect-file", redirect("file:///etc/passwd"))
	mux.HandleFunc("/loop", redirect("/loop"))
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, strings.Repeat("a", 10000))
	})
	mux.HandleFunc("/long", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		for i := 0; i < 300; i++ {
			fmt.Fprintf(w, "line %03d of the long page\n", i)
		}
	})
	mux.HandleFunc("/png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("\x89PNG\r\n\x1a\n...."))
	})
	mux.HandleFunc("/404", http.NotFound)
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})

	dns := map[string][]string{
		"public.test":   {publicIP},
		"other.test":    {publicIP},
		"internal.test": {"10.0.0.5"},
		"mixed.test":    {"127.0.0.1", publicIP},
		"meta.test":     {"169.254.169.254"},
		"mapped.test":   {"::ffff:127.0.0.1"},
	}
	g := &Guard{
		Resolve: func(ctx context.Context, host string) ([]netip.Addr, error) {
			var out []netip.Addr
			for _, s := range dns[host] {
				out = append(out, netip.MustParseAddr(s))
			}
			if out == nil {
				return nil, errors.New("no such host")
			}
			return out, nil
		},
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, _ := net.SplitHostPort(addr)
			if host != publicIP {
				t.Errorf("the guard dialed a non-public address %s", addr)
				return nil, errors.New("test: refused")
			}
			var d net.Dialer
			return d.DialContext(ctx, network, h.srv.Listener.Addr().String())
		},
	}
	h.tool = newFetchTool(FetchConfig{MaxBytes: maxBytes}, maxOut, g)
	return h
}

func (h *fetchHarness) fetch(args string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return h.tool.Run(ctx, json.RawMessage(args))
}

func TestFetchReadsPage(t *testing.T) {
	h := newFetchHarness(t, 1<<20, 8192)
	out, err := h.fetch(`{"url":"http://public.test/page#frag"}`)
	if err != nil {
		t.Fatal(err)
	}
	want := "URL: http://public.test/page\nTitle: Test & Page\n\n# Hello\n\nFirst [paragraph](http://public.test/next)."
	if out != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}
	// A redirect to another public host is followed.
	out, err = h.fetch(`{"url":"http://public.test/redirect-public"}`)
	if err != nil || !strings.HasPrefix(out, "URL: http://other.test/page") {
		t.Errorf("public redirect: %q %v", out, err)
	}
	// A host with one private and one public address uses only the public one.
	if _, err := h.fetch(`{"url":"http://mixed.test/page"}`); err != nil {
		t.Errorf("mixed answer: %v", err)
	}
	// A URL without a scheme gets https (which the plain-HTTP test server
	// can't speak, so the error names the upgraded URL).
	if _, err := h.fetch(`{"url":"public.test/page"}`); err == nil || !strings.Contains(err.Error(), "https://public.test/page") {
		t.Errorf("scheme-less: %v", err)
	}
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestFetchBlocksPrivateDestinations(t *testing.T) {
	h := newFetchHarness(t, 1<<20, 8192)
	cases := map[string]string{
		"loopback literal":     fmt.Sprintf(`{"url":"http://127.0.0.1:%d/secret"}`, h.srv.Listener.Addr().(*net.TCPAddr).Port),
		"v6 loopback literal":  `{"url":"http://[::1]/secret"}`,
		"localhost name":       `{"url":"http://localhost/secret"}`,
		"metadata ip":          `{"url":"http://169.254.169.254/latest/meta-data/"}`,
		"metadata name":        `{"url":"http://metadata.google.internal/computeMetadata/v1/"}`,
		"docker host":          `{"url":"http://host.docker.internal:8799/api/"}`,
		"resolves private":     `{"url":"http://internal.test/secret"}`,
		"resolves metadata":    `{"url":"http://meta.test/"}`,
		"resolves mapped v4":   `{"url":"http://mapped.test/"}`,
		"redirect to loopback": `{"url":"http://public.test/redirect-loopback"}`,
		"redirect to v6":       `{"url":"http://public.test/redirect-v6"}`,
		"redirect to private":  `{"url":"http://public.test/redirect-internal"}`,
		"redirect to metadata": `{"url":"http://public.test/redirect-metadata"}`,
		"redirect localhost":   `{"url":"http://public.test/redirect-localhost"}`,
		"redirect to file":     `{"url":"http://public.test/redirect-file"}`,
		"file scheme":          `{"url":"file:///etc/passwd"}`,
		"gopher scheme":        `{"url":"gopher://public.test/"}`,
		"decimal ip":           `{"url":"http://2130706433/"}`,
		"redirect loop":        `{"url":"http://public.test/loop"}`,
	}
	for name, args := range cases {
		out, err := h.fetch(args)
		if err == nil {
			t.Errorf("%s: fetched %q", name, out)
		}
	}
	if n := h.secret.Load(); n != 0 {
		t.Errorf("the secret endpoint was reached %d times", n)
	}
}

func TestFetchLimits(t *testing.T) {
	h := newFetchHarness(t, 4096, 8192)
	out, err := h.fetch(`{"url":"http://public.test/big"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, strings.Repeat("a", 4097)) || !strings.Contains(out, strings.Repeat("a", 4096)) || !strings.Contains(out, "larger than the download limit") {
		t.Errorf("download not capped: %d bytes", len(out))
	}
	if _, err := h.fetch(`{"url":"http://public.test/png"}`); err == nil || !strings.Contains(err.Error(), "image/png") {
		t.Errorf("binary: %v", err)
	}
	if _, err := h.fetch(`{"url":"http://public.test/404"}`); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("404: %v", err)
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := h.tool.Run(ctx, json.RawMessage(`{"url":"http://public.test/slow"}`)); err == nil {
		t.Error("slow page didn't time out")
	}
	if time.Since(start) > 2*time.Second {
		t.Error("the timeout wasn't honored")
	}
}

func TestFetchPaging(t *testing.T) {
	h := newFetchHarness(t, 1<<20, 1024)
	out, err := h.fetch(`{"url":"http://public.test/long"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > 1024 || !strings.Contains(out, "line 000") || !strings.Contains(out, "Call fetch_url with offset=") {
		t.Fatalf("first part (%d bytes): %s", len(out), out)
	}
	var next int
	fmt.Sscanf(out[strings.Index(out, "offset=")+len("offset="):], "%d", &next)
	out2, err := h.fetch(fmt.Sprintf(`{"url":"http://public.test/long","offset":%d}`, next))
	if err != nil || strings.Contains(out2, "line 000") || !strings.Contains(out2, fmt.Sprintf("Showing bytes %d-", next)) {
		t.Errorf("second part: %v\n%s", err, out2)
	}
	out3, _ := h.fetch(`{"url":"http://public.test/long","offset":99999999}`)
	if !strings.Contains(out3, "past the end") {
		t.Errorf("past the end: %s", out3)
	}
}

func TestGuardRealDialerChecksAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	g := &Guard{}
	ctx := context.Background()
	// The default dialer refuses a private address even when asked directly.
	if _, err := g.dial(ctx, "tcp", srv.Listener.Addr().String()); err == nil {
		t.Error("the default dialer connected to loopback")
	} else {
		var be *BlockedError
		if !errors.As(err, &be) {
			t.Errorf("err = %v, want a BlockedError", err)
		}
	}
	if _, err := g.DialContext(ctx, "tcp", srv.Listener.Addr().String()); err == nil {
		t.Error("DialContext connected to loopback")
	}
	if _, err := g.DialContext(ctx, "tcp", "localhost:80"); err == nil {
		t.Error("DialContext connected to localhost")
	}
}

func TestHTMLToText(t *testing.T) {
	src := `<!doctype html><html><head><title> My   Title </title><style>body{}</style>
<meta charset="utf-8"></head><body>
<header><a href="/">Logo</a></header>
<nav><ul><li>Nav item</li></ul></nav>
<main>
<h2>Section&nbsp;one</h2>
<p>Text with <b>bold</b> and <a href="https://x.org/a?b=1&amp;c=2">a link</a>, plus <a href="#top">an anchor</a>.</p>
<ul><li>First</li><li>Second <a href="javascript:void(0)">js</a></li></ul>
<pre>  code   keeps
    spacing</pre>
<table><tr><th>A</th><th>B</th></tr><tr><td>1</td><td>2</td></tr></table>
<p>Unclosed paragraph<p>Next one with enough text to make the main part long enough to be used as the content of the page.</p>
<!-- a comment <p>hidden</p> -->
<script>document.write("<p>no</p>")</script>
<svg><title>icon</title><path d="M0"/></svg>
</main>
<footer>Footer text</footer>
</body></html>`
	p := htmlToText(src, mustURL(t, "https://site.test/dir/page"))
	if p.Title != "My Title" {
		t.Errorf("title = %q", p.Title)
	}
	for _, want := range []string{
		"## Section one",
		"Text with bold and [a link](https://x.org/a?b=1&c=2), plus an anchor.",
		"- First\n- Second js",
		"  code   keeps\n    spacing",
		"A | B\n1 | 2",
		"Unclosed paragraph\n\nNext one",
	} {
		if !strings.Contains(p.Text, want) {
			t.Errorf("missing %q in:\n%s", want, p.Text)
		}
	}
	for _, bad := range []string{"Nav item", "Footer text", "hidden", "no<", "document.write", "icon", "body{}", "Logo"} {
		if strings.Contains(p.Text, bad) {
			t.Errorf("%q should be dropped:\n%s", bad, p.Text)
		}
	}
	// Without <main>, the whole body is used; a relative link resolves.
	p2 := htmlToText(`<body><p>Hi <a href="../up">there</a></p></body>`, mustURL(t, "https://site.test/dir/page"))
	if p2.Text != "Hi [there](https://site.test/up)" {
		t.Errorf("text = %q", p2.Text)
	}
}

func TestDecodeCharset(t *testing.T) {
	if got := decodeCharset([]byte("caf\xe9"), "ISO-8859-1"); got != "café" {
		t.Errorf("latin1 = %q", got)
	}
	if got := decodeCharset([]byte("ok \xff"), ""); got != "ok �" {
		t.Errorf("invalid utf8 = %q", got)
	}
}
