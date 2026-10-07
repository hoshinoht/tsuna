package fetch

import (
	"regexp"
	"strings"
	"testing"
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
	got, ok := focusContent(md, "cats")
	if !ok {
		t.Fatal("expected a focus match")
	}
	if !strings.Contains(got, "about cats here") || !strings.Contains(got, "after cats") || !strings.Contains(got, "intro block") {
		t.Errorf("context window wrong: %q", got)
	}
	if strings.Contains(got, "unrelated three") {
		t.Errorf("distant block should be elided: %q", got)
	}
	if !strings.Contains(got, "[…]") {
		t.Errorf("gap marker missing: %q", got)
	}

	if _, ok := focusContent(md, "zebras"); ok {
		t.Error("no-match focus should report ok=false")
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
	if got := sniffKind([]byte("%PDF-1.7 ..."), "text/html"); got != "pdf" {
		t.Errorf("magic bytes: got %q", got)
	}
	if got := sniffKind([]byte("<html>"), "application/pdf"); got != "pdf" {
		t.Errorf("content type: got %q", got)
	}
	if got := sniffKind([]byte("<html>"), "text/html; charset=utf-8"); got != "html" {
		t.Errorf("html: got %q", got)
	}
}
