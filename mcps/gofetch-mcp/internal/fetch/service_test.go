package fetch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// serve returns a server answering path -> (content type, body) and a
// counter of requests per path.
func serve(t *testing.T, routes map[string][2]string) (*httptest.Server, *sync.Map) {
	t.Helper()
	var hits sync.Map
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := hits.LoadOrStore(r.URL.Path, new(atomic.Int32))
		n.(*atomic.Int32).Add(1)
		route, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if route[0] != "" {
			w.Header().Set("Content-Type", route[0])
		}
		w.Write([]byte(route[1]))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func hitCount(hits *sync.Map, path string) int32 {
	n, ok := hits.Load(path)
	if !ok {
		return 0
	}
	return n.(*atomic.Int32).Load()
}

func mustFetch(t *testing.T, s *Service, req Request) *Result {
	t.Helper()
	res, toolErr := s.Fetch(context.Background(), req)
	if toolErr != nil {
		t.Fatalf("Fetch(%s): %+v", req.URL, toolErr)
	}
	return res
}

func TestFetchQualityWalls(t *testing.T) {
	html := "text/html; charset=utf-8"
	srv, _ := serve(t, map[string][2]string{
		"/challenge": {html, string(fixture(t, "challenge_cloudflare.html"))},
		"/login":     {html, string(fixture(t, "login_wall.html"))},
		"/consent":   {html, string(fixture(t, "consent_wall.html"))},
		"/spa":       {html, string(fixture(t, "spa_shell.html"))},
		"/stub":      {html, "<html><body><article><p>Moved. See the new page.</p></article></body></html>"},
		"/blank":     {html, "<html><body></body></html>"},
		"/good":      {html, string(fixture(t, "incidental_article.html"))},
	})
	cases := []struct {
		path, quality, reason string
	}{
		{"/challenge", QualityBlocked, "challenge"},
		{"/login", QualityBlocked, "login_required"},
		{"/consent", QualityBlocked, "consent_wall"},
		{"/spa", QualityEmpty, "javascript_required"},
		{"/stub", QualityThin, "short_content"},
		{"/blank", QualityEmpty, "no_text"},
		{"/good", QualityUsable, ""},
	}
	s := NewService()
	for _, c := range cases {
		res := mustFetch(t, s, Request{URL: srv.URL + c.path})
		if res.Quality != c.quality || res.QualityReason != c.reason {
			t.Errorf("%s: quality = %s/%s, want %s/%s (warnings %v)", c.path, res.Quality, res.QualityReason, c.quality, c.reason, res.Warnings)
		}
		if res.ContentOK != (c.quality == QualityUsable) {
			t.Errorf("%s: content_ok = %v with quality %s", c.path, res.ContentOK, res.Quality)
		}
		if c.quality != QualityUsable && len(res.Warnings) == 0 {
			t.Errorf("%s: non-usable result carries no warning", c.path)
		}
	}
}

func TestFetchCfMitigatedHeaderIsChallenge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("cf-mitigated", "challenge")
		fmt.Fprint(w, "<html><body><main>"+strings.Repeat("<p>Plenty of text that would otherwise look usable.</p>", 100)+"</main></body></html>")
	}))
	defer srv.Close()
	res := mustFetch(t, NewService(), Request{URL: srv.URL})
	if res.Quality != QualityBlocked || res.QualityReason != "challenge" {
		t.Errorf("cf-mitigated: quality = %s/%s", res.Quality, res.QualityReason)
	}
}

func TestLongPageWithBotScriptIsUsable(t *testing.T) {
	body := `<html><head><title>Docs</title><script src="/cdn-cgi/challenge-platform/scripts/jsd/main.js"></script></head>
<body><main>` + strings.Repeat("<p>Real documentation paragraph with enough words to matter for the reader.</p>", 60) +
		`<div class="g-recaptcha"></div></main></body></html>`
	srv, _ := serve(t, map[string][2]string{"/": {"text/html", body}})
	res := mustFetch(t, NewService(), Request{URL: srv.URL + "/"})
	if res.Quality != QualityUsable {
		t.Errorf("long page with bot-management script: quality = %s/%s", res.Quality, res.QualityReason)
	}
}

func TestFetchContainerSelection(t *testing.T) {
	srv, _ := serve(t, map[string][2]string{
		"/empty":      {"text/html", string(fixture(t, "empty_article.html"))},
		"/incidental": {"text/html", string(fixture(t, "incidental_article.html"))},
		"/docs/api/":  {"text/html", string(fixture(t, "docs_page.html"))},
	})
	s := NewService()

	res := mustFetch(t, s, Request{URL: srv.URL + "/empty"})
	if !res.ContentOK || !strings.Contains(res.Content, "streaming responses") || !strings.Contains(res.Content, "# Release notes 2.4") {
		t.Errorf("empty <article> should fall back to the body: %+v", res)
	}
	if strings.Contains(res.Content, "Docs") && strings.Contains(res.Content, "/docs)") {
		t.Errorf("nav leaked into content: %q", res.Content)
	}

	res = mustFetch(t, s, Request{URL: srv.URL + "/incidental"})
	if !strings.Contains(res.Content, "scheduler.toml") {
		t.Errorf("incidental <article> card won over the real content: %q", res.Content)
	}

	res = mustFetch(t, s, Request{URL: srv.URL + "/docs/api/"})
	c := res.Content
	for _, want := range []string{
		"# Client API",                    // page h1 outside <main> is kept
		"## Installation",                 // headings
		"```",                             // fenced code
		"go get example.com/client/trace", // code body across a blank line
		"| Free",                          // table
		srv.URL + "/docs/errors/#codes",   // page-relative link resolution
		"limits apply per API key",        // <aside> inside <main> is content
	} {
		if !strings.Contains(c, want) {
			t.Errorf("docs page missing %q:\n%s", want, c)
		}
	}
	for _, unwanted := range []string{"Docs home", "Sidebar link one", "© Example"} {
		if strings.Contains(c, unwanted) {
			t.Errorf("docs page kept chrome %q:\n%s", unwanted, c)
		}
	}
}

func TestFetchContentTypes(t *testing.T) {
	png := "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01"
	md := "# Changelog\n\n## 1.2.0\n\n- Added `--json` output.\n- Fixed a crash on empty input.\n"
	srv, _ := serve(t, map[string][2]string{
		"/notes.txt":  {"text/plain; charset=utf-8", "line one\r\nline two\r\n"},
		"/CHANGES.md": {"text/plain", md},
		"/doc":        {"text/markdown", md},
		"/image.png":  {"image/png", png},
		"/fake.html":  {"text/html", png},
		"/archive":    {"application/zip", "PK\x03\x04\x14\x00\x00\x00\x08\x00"},
	})
	s := NewService()

	res := mustFetch(t, s, Request{URL: srv.URL + "/notes.txt"})
	if res.Kind != "text" || res.Content != "line one\nline two" {
		t.Errorf("text/plain: kind=%s content=%q", res.Kind, res.Content)
	}
	for _, p := range []string{"/CHANGES.md", "/doc"} {
		res = mustFetch(t, s, Request{URL: srv.URL + p})
		if res.Kind != "markdown" || res.Title != "Changelog" || !strings.Contains(res.Content, "- Added `--json` output.") {
			t.Errorf("%s: kind=%s title=%q content=%q", p, res.Kind, res.Title, res.Content)
		}
	}
	for _, p := range []string{"/image.png", "/fake.html", "/archive"} {
		_, toolErr := s.Fetch(context.Background(), Request{URL: srv.URL + p})
		if toolErr == nil || toolErr.Code != "unsupported_format" {
			t.Errorf("%s: want unsupported_format, got %+v", p, toolErr)
		}
	}
}

func TestFetchUnicode(t *testing.T) {
	jp := strings.Repeat("日本語のテキストです。", 30) // 11 runes x 30 = 330 runes, 990 bytes
	srv, _ := serve(t, map[string][2]string{
		"/latin1": {"text/html", string(fixture(t, "latin1.html"))},
		"/jp":     {"text/plain; charset=utf-8", jp},
	})
	s := NewService()

	res := mustFetch(t, s, Request{URL: srv.URL + "/latin1"})
	if res.Title != "Café crème" || !strings.Contains(res.Content, "Crème fraîche") || strings.Contains(res.Content, "\uFFFD") {
		t.Errorf("latin-1 page not decoded: title=%q content=%q", res.Title, res.Content)
	}

	res = mustFetch(t, s, Request{URL: srv.URL + "/jp", MaxChars: 100})
	if res.TotalChars != 330 || res.NextOffset != 100 || len([]rune(res.Content)) != 100 {
		t.Errorf("rune pagination wrong: total=%d next=%d len=%d", res.TotalChars, res.NextOffset, len([]rune(res.Content)))
	}
	if !res.ContentOK {
		t.Errorf("330-rune text should be usable (thresholds count runes, not bytes): %s", res.Quality)
	}
}

func TestFetchCacheReusesExtraction(t *testing.T) {
	body := "<html><body><main>" + strings.Repeat("<p>Paragraph of cached content for pagination.</p>", 50) + "</main></body></html>"
	srv, hits := serve(t, map[string][2]string{"/doc": {"text/html", body}})
	s := NewService()

	first := mustFetch(t, s, Request{URL: srv.URL + "/doc", MaxChars: 500})
	second := mustFetch(t, s, Request{URL: srv.URL + "/doc#frag", MaxChars: 500, Offset: first.NextOffset, DocumentID: first.DocumentID})
	if hitCount(hits, "/doc") != 1 {
		t.Errorf("second page re-downloaded: %d requests", hitCount(hits, "/doc"))
	}
	if first.Cached || !second.Cached || second.DocumentID != first.DocumentID {
		t.Errorf("cache flags/ids wrong: first=%v/%s second=%v/%s", first.Cached, first.DocumentID, second.Cached, second.DocumentID)
	}
	if first.DocumentID == "" || first.FetchedAt == "" {
		t.Error("document_id and fetched_at must be set")
	}
}

func TestFetchChangedDocument(t *testing.T) {
	var version atomic.Int32
	version.Store(1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, "<html><body><main>%s</main></body></html>",
			strings.Repeat(fmt.Sprintf("<p>Version %d of a page that changes between fetches.</p>", version.Load()), 40))
	}))
	defer srv.Close()

	now := time.Unix(1_700_000_000, 0)
	s := NewServiceWithOptions(Options{CacheTTL: time.Minute})
	s.now = func() time.Time { return now }

	v1 := mustFetch(t, s, Request{URL: srv.URL, MaxChars: 300})

	// Same version after the cache expired: verified and served.
	now = now.Add(2 * time.Minute)
	again := mustFetch(t, s, Request{URL: srv.URL, MaxChars: 300, Offset: v1.NextOffset, DocumentID: v1.DocumentID})
	if again.DocumentID != v1.DocumentID || again.Cached || !strings.Contains(strings.Join(again.Warnings, " "), "verified unchanged") {
		t.Errorf("expired-but-unchanged: %+v", again)
	}

	// The page changes and the cache expires: continuing v1 must fail loudly.
	version.Store(2)
	now = now.Add(2 * time.Minute)
	_, toolErr := s.Fetch(context.Background(), Request{URL: srv.URL, MaxChars: 300, Offset: v1.NextOffset, DocumentID: v1.DocumentID})
	if toolErr == nil || toolErr.Code != "document_changed" || toolErr.DocumentID == "" || toolErr.DocumentID == v1.DocumentID {
		t.Fatalf("want document_changed with the new id, got %+v", toolErr)
	}

	// While both versions are cached, each document_id keeps serving its own.
	v2 := mustFetch(t, s, Request{URL: srv.URL, MaxChars: 300, DocumentID: toolErr.DocumentID})
	if !strings.Contains(v2.Content, "Version 2") {
		t.Errorf("new document id should serve version 2: %q", v2.Content)
	}

	// A document_id that is gone and cannot be re-fetched is reported as expired.
	srv.Close()
	now = now.Add(2 * time.Minute)
	_, toolErr = s.Fetch(context.Background(), Request{URL: srv.URL, DocumentID: v2.DocumentID})
	if toolErr == nil || toolErr.Code != "document_expired" {
		t.Errorf("want document_expired, got %+v", toolErr)
	}
}

func TestFetchDeduplicatesConcurrentRequests(t *testing.T) {
	release := make(chan struct{})
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		<-release
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html><body><main>"+strings.Repeat("<p>Shared body for concurrent callers.</p>", 20)+"</main></body></html>")
	}))
	defer srv.Close()
	s := NewService()

	// One caller gives up early; the others must still get the document.
	cancelCtx, cancel := context.WithCancel(context.Background())
	canceled := make(chan *ToolError, 1)
	go func() {
		_, toolErr := s.Fetch(cancelCtx, Request{URL: srv.URL})
		canceled <- toolErr
	}()
	var wg sync.WaitGroup
	results := make([]*ToolError, 4)
	ids := make([]string, 4)
	for i := range 4 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, toolErr := s.Fetch(context.Background(), Request{URL: srv.URL})
			results[i] = toolErr
			if res != nil {
				ids[i] = res.DocumentID
			}
		}(i)
	}
	waitFor(t, func() bool { return requests.Load() >= 1 })
	time.Sleep(20 * time.Millisecond) // let every caller join the flight
	cancel()
	if toolErr := <-canceled; toolErr == nil || toolErr.Code != "canceled" {
		t.Errorf("canceled caller: %+v", toolErr)
	}
	close(release)
	wg.Wait()

	if requests.Load() != 1 {
		t.Errorf("concurrent fetches made %d requests, want 1", requests.Load())
	}
	for i := range 4 {
		if results[i] != nil || ids[i] == "" || ids[i] != ids[0] {
			t.Errorf("caller %d: err=%+v id=%q", i, results[i], ids[i])
		}
	}
}

func TestFetchCancellationAbortsDownload(t *testing.T) {
	aborted := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		close(aborted)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	_, toolErr := NewService().Fetch(ctx, Request{URL: srv.URL})
	if toolErr == nil || toolErr.Code != "canceled" {
		t.Fatalf("want canceled, got %+v", toolErr)
	}
	select {
	case <-aborted:
	case <-time.After(2 * time.Second):
		t.Error("upstream request kept running after the only caller canceled")
	}
}

func TestFetchHTTPErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/429":
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(http.StatusTooManyRequests)
		case "/500":
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	s := NewService()
	if _, e := s.Fetch(context.Background(), Request{URL: srv.URL + "/429"}); e == nil || e.Code != "blocked" {
		t.Errorf("429: %+v", e)
	}
	if _, e := s.Fetch(context.Background(), Request{URL: srv.URL + "/500"}); e == nil || e.Code != "upstream_error" {
		t.Errorf("500: %+v", e)
	}
	if _, e := s.Fetch(context.Background(), Request{URL: "ftp://example.com/x"}); e == nil || e.Code != "invalid_input" {
		t.Errorf("ftp: %+v", e)
	}
}

func TestFetchFocusReporting(t *testing.T) {
	srv, _ := serve(t, map[string][2]string{"/docs/api/": {"text/html", string(fixture(t, "docs_page.html"))}})
	s := NewService()

	res := mustFetch(t, s, Request{URL: srv.URL + "/docs/api/", Focus: "rate limits per key"})
	if res.FocusStatus != "matched" || !res.Focused || !strings.Contains(res.Content, "## Limits") {
		t.Errorf("focus should keep the Limits section with its heading: %+v", res)
	}

	res = mustFetch(t, s, Request{URL: srv.URL + "/docs/api/", Focus: "kubernetes"})
	if res.FocusStatus != "no_match" || res.Focused || !strings.Contains(strings.Join(res.Warnings, " "), "no focus terms matched") {
		t.Errorf("no-match focus must be reported: %+v", res)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(time.Millisecond)
	}
}
