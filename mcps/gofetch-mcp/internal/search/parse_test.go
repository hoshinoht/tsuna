package search

import "testing"

const ddgFixture = `<html><body>
<div class="result result--ad">
  <a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fads.example%2F">Sponsored</a>
</div>
<div class="result">
  <a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2Fdoc%2F&rut=abc">Go Documentation</a>
  <a class="result__snippet">Official Go docs.</a>
</div>
<div class="result">
  <a class="result__a" href="https://pkg.go.dev/">pkg.go.dev</a>
  <div class="result__snippet">Package index.</div>
</div>
</body></html>`

func TestParseDDG(t *testing.T) {
	results, err := parseDDG([]byte(ddgFixture), 5)
	if err != nil {
		t.Fatalf("parseDDG: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2 (ad excluded): %+v", len(results), results)
	}
	if results[0].URL != "https://go.dev/doc/" {
		t.Errorf("redirect not unwrapped: %q", results[0].URL)
	}
	if results[0].Title != "Go Documentation" || results[0].Snippet != "Official Go docs." {
		t.Errorf("first result wrong: %+v", results[0])
	}
	if results[1].URL != "https://pkg.go.dev/" {
		t.Errorf("direct href wrong: %q", results[1].URL)
	}
}

const mojeekFixture = `<html><body><ul class="results-standard">
<li><h2><a href="https://go.dev/">The Go Programming Language</a></h2><p class="s">Build simple, secure software.</p></li>
<li><h2><a href="https://go.dev/doc/">Documentation</a></h2><p class="s">Go docs.</p></li>
<li><h2><a href="https://go.dev/x/">Extra</a></h2><p class="s">More.</p></li>
</ul></body></html>`

func TestParseMojeek(t *testing.T) {
	results, err := parseMojeek([]byte(mojeekFixture), 2)
	if err != nil {
		t.Fatalf("parseMojeek: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2 (numResults cap): %+v", len(results), results)
	}
	if results[0].Title != "The Go Programming Language" || results[0].URL != "https://go.dev/" {
		t.Errorf("first result wrong: %+v", results[0])
	}
}

func TestDecodeDDGHref(t *testing.T) {
	got := decodeDDGHref("//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fa%3Fb%3Dc&rut=xyz")
	if got != "https://example.com/a?b=c" {
		t.Errorf("decode wrong: %q", got)
	}
	if got := decodeDDGHref("https://plain.example/"); got != "https://plain.example/" {
		t.Errorf("plain href mangled: %q", got)
	}
}

const exaFixture = `{"results":[
 {"title":"MCP Go SDK","url":"https://github.com/modelcontextprotocol/go-sdk","highlights":["The official Go SDK for MCP servers and clients."]},
 {"title":"","url":"https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk/mcp","highlights":[]},
 {"title":"no url","url":""}
]}`

func TestParseExa(t *testing.T) {
	results, err := parseExa([]byte(exaFixture))
	if err != nil {
		t.Fatalf("parseExa: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2 (empty url skipped): %+v", len(results), results)
	}
	if results[0].Snippet != "The official Go SDK for MCP servers and clients." {
		t.Errorf("highlight not used as snippet: %+v", results[0])
	}
}
