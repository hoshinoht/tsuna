package fetch

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestConvertHTMLPicksMainContent(t *testing.T) {
	html := `<html><head><title>Example Domain</title></head><body>
<nav>Site nav that should vanish</nav>
<article><h1>Heading</h1><p>Body text with a <a href="/rel">relative link</a>.</p></article>
<footer>Footer junk</footer></body></html>`

	md, title, err := convertHTML([]byte(html), "https://example.com/page")
	if err != nil {
		t.Fatalf("convertHTML: %v", err)
	}
	if title != "Example Domain" {
		t.Errorf("title = %q, want Example Domain", title)
	}
	if !strings.Contains(md, "# Heading") {
		t.Errorf("markdown missing heading: %q", md)
	}
	if strings.Contains(md, "Site nav") || strings.Contains(md, "Footer junk") {
		t.Errorf("chrome not stripped: %q", md)
	}
	if !strings.Contains(md, "https://example.com/rel") {
		t.Errorf("relative link not absolutized: %q", md)
	}
}

func TestConvertHTMLTables(t *testing.T) {
	html := `<html><body><article>
<table><thead><tr><th>Agent</th><th>Model</th></tr></thead>
<tbody><tr><td>plan</td><td>sol</td></tr><tr><td>explore</td><td>luna</td></tr></tbody></table>
</article></body></html>`

	md, _, err := convertHTML([]byte(html), "https://example.com/docs/")
	if err != nil {
		t.Fatalf("convertHTML: %v", err)
	}
	normalized := regexp.MustCompile(` +`).ReplaceAllString(md, " ")
	if !strings.Contains(normalized, "| Agent | Model |") {
		t.Errorf("table header not converted to pipe table: %q", md)
	}
	if !strings.Contains(normalized, "| plan | sol |") {
		t.Errorf("table row not converted: %q", md)
	}
}

func TestConvertHTMLResolvesLinksAgainstPagePath(t *testing.T) {
	html := `<html><body><article>
<p><a href="#install">Install section</a></p>
<p><a href="../sibling">Sibling page</a></p>
<p><img src="diagram.png" alt="diagram"></p>
</article></body></html>`

	md, _, err := convertHTML([]byte(html), "https://example.com/docs/page/")
	if err != nil {
		t.Fatalf("convertHTML: %v", err)
	}
	if !strings.Contains(md, "https://example.com/docs/page/#install") {
		t.Errorf("fragment link lost page path: %q", md)
	}
	if !strings.Contains(md, "https://example.com/docs/sibling") {
		t.Errorf("../ path not resolved: %q", md)
	}
	if !strings.Contains(md, "https://example.com/docs/page/diagram.png") {
		t.Errorf("relative img src not resolved: %q", md)
	}
}

func TestFocusContent(t *testing.T) {
	md := "intro block\n\nabout cats here\n\nafter cats\n\nunrelated one\n\nunrelated two\n\nunrelated three"
	f := focusDocument(md, "cats")
	if !f.matched {
		t.Fatal("expected a focus match")
	}
	got := f.text
	if !strings.Contains(got, "about cats here") || !strings.Contains(got, "after cats") || !strings.Contains(got, "intro block") {
		t.Errorf("context window wrong: %q", got)
	}
	if strings.Contains(got, "unrelated three") {
		t.Errorf("distant block should be elided: %q", got)
	}
	if !strings.Contains(got, "[…]") {
		t.Errorf("gap marker missing: %q", got)
	}

	if f := focusDocument(md, "zebras"); f.matched || f.text != md {
		t.Error("no-match focus should report matched=false and return the document unchanged")
	}
}

func TestPaginate(t *testing.T) {
	md := strings.Repeat("é", 100)

	p := paginate(md, 40, 0)
	if p.total != 100 || !p.truncated || p.nextOffset != 40 || len([]rune(p.content)) != 40 {
		t.Errorf("first page wrong: %+v", p)
	}

	p = paginate(md, 40, 80)
	if p.truncated || p.nextOffset != 0 || len([]rune(p.content)) != 20 {
		t.Errorf("last page wrong: %+v", p)
	}

	p = paginate(md, 40, 500)
	if p.content != "" || p.truncated {
		t.Errorf("past-end page wrong: %+v", p)
	}
}

func TestSniffKind(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01")
	cases := []struct {
		name, body, contentType, url, want string
		ok                                 bool
	}{
		{"pdf magic beats header", "%PDF-1.7 ...", "text/html", "https://x/a", "pdf", true},
		{"pdf header", "<html>", "application/pdf", "https://x/a", "pdf", true},
		{"html", "<html>", "text/html; charset=utf-8", "https://x/a", "html", true},
		{"xhtml", "<html>", "application/xhtml+xml", "https://x/a", "html", true},
		{"markdown type", "# Title", "text/markdown; charset=utf-8", "https://x/a", "markdown", true},
		{"plain .md", "# Title", "text/plain", "https://x/README.md", "markdown", true},
		{"plain", "hello", "text/plain", "https://x/notes.txt", "text", true},
		{"json", `{"a":1}`, "application/json", "https://x/a", "text", true},
		{"missing type sniffs html", "<!DOCTYPE html><html><body>x</body></html>", "", "https://x/a", "html", true},
		{"octet-stream text", "just words", "application/octet-stream", "https://x/a", "text", true},
		{"image", string(png), "image/png", "https://x/a.png", "", false},
		{"binary behind text/html", string(png), "text/html", "https://x/a", "", false},
		{"zip", "PK\x03\x04\x14\x00\x00\x00", "application/zip", "https://x/a.zip", "", false},
		{"docx", "PK\x03\x04", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "https://x/a.docx", "", false},
	}
	for _, c := range cases {
		got, ok := sniffKind([]byte(c.body), c.contentType, c.url)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: sniffKind = (%q, %v), want (%q, %v)", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestPaginateEndsOnBlockBoundary(t *testing.T) {
	para := strings.Repeat("word ", 30) // 150 runes
	code := "```go\nfunc main() {\n\n\tprintln(\"hi\")\n}\n```"
	md := para + "\n\n" + para + "\n\n" + code + "\n\n" + para
	p := paginate(md, 330, 0)
	if !p.truncated || !strings.HasSuffix(p.content, "\n\n") {
		t.Fatalf("page should end at a block boundary: %q", p.content)
	}
	if strings.Count(p.content, "```")%2 != 0 {
		t.Errorf("page splits a code fence: %q", p.content)
	}
	next := paginate(md, 330, p.nextOffset)
	if p.content+next.content != md[:len(p.content)+len(next.content)] {
		t.Error("consecutive pages must tile the document without gaps or overlap")
	}

	// No boundary in the lookback window: cut exactly at max_chars.
	solid := strings.Repeat("x", 1000)
	if p := paginate(solid, 400, 0); p.nextOffset != 400 {
		t.Errorf("hard cut: next=%d", p.nextOffset)
	}
}

func TestSplitBlocksKeepsFencesWhole(t *testing.T) {
	blocks := splitBlocks("intro\n\n```\na\n\nb\n```\n\noutro")
	if len(blocks) != 3 || !strings.Contains(blocks[1], "a\n\nb") {
		t.Errorf("blocks = %q", blocks)
	}
}

func TestDocCacheBoundsMemory(t *testing.T) {
	now := time.Unix(0, 0)
	c := newDocCache(time.Minute, 10_000, 100, func() time.Time { return now })
	mk := func(id string, size int) *document {
		return &document{id: id, key: "https://x/" + id, markdown: strings.Repeat("a", size)}
	}
	if c.put(mk("huge", 5000)) {
		t.Error("a document over a quarter of the budget must not be cached")
	}
	for i := range 10 {
		c.put(mk(fmt.Sprint(i), 1500))
	}
	entries, bytes := c.stats()
	if bytes > 10_000 || entries >= 10 {
		t.Errorf("cache over budget: %d entries, %d bytes", entries, bytes)
	}
	if c.getByID("9") == nil || c.getByID("0") != nil {
		t.Error("eviction should drop least recently used first")
	}
	now = now.Add(2 * time.Minute)
	if c.getByID("9") != nil || c.getByKey("https://x/9") != nil {
		t.Error("expired entries must not be served")
	}
}

func TestFocusMatching(t *testing.T) {
	if !containsTerm("cats are here", "cat", false) || containsTerm("a good day", "go", false) || !containsTerm("use go here", "go", false) || !containsTerm("rate limiting", "limit", false) {
		t.Error("word-boundary matching wrong")
	}
	if !containsTerm("これは東京の話", "東京", false) {
		t.Error("CJK terms match as substrings")
	}

	md := "# Guide\n\n## Install\n\nRun the installer.\n\n## Rate limiting\n\nRequests are throttled.\n\nBursts are queued.\n\n## Other\n\nThe rate of change is slow.\n\nUnrelated."
	f := focusDocument(md, `"rate limiting"`)
	if !f.matched || !strings.Contains(f.text, "Bursts are queued.") || strings.Contains(f.text, "Run the installer") {
		t.Errorf("phrase match should keep its section: %q", f.text)
	}
	if len(f.terms) != 1 || f.terms[0] != "rate limiting" {
		t.Errorf("matched terms = %q", f.terms)
	}

	// Stopwords are ignored; blocks matching more terms win.
	f = focusDocument(md, "how are bursts queued")
	if !f.matched || !strings.Contains(f.text, "## Rate limiting") || strings.Contains(f.text, "rate of change") {
		t.Errorf("multi-term focus: %q", f.text)
	}

	// Quoted phrases match exactly; unquoted words still allow inflections.
	doc := "# API\n\n## Throttling\n\nRate limiting applies per key."
	if f := focusDocument(doc, `"rate limit"`); f.matched {
		t.Errorf("quoted phrase must not match an inflected form: %+v", f)
	}
	if f := focusDocument(doc, "rate limit"); !f.matched {
		t.Error("unquoted words should match inflected forms")
	}
	if f := focusDocument(doc+"\n\nThe rate limit is 10/s.", `"rate limit"`); !f.matched || !strings.Contains(f.text, "10/s") {
		t.Errorf("exact quoted phrase should match: %+v", f)
	}
}
