package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hoshinoht/gofetch-mcp/internal/search"
)

// connect runs the real server over in-memory transports and returns a
// client session, exercising the same tools/call path as stdio.
func connect(t *testing.T, opts Options) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	srv := New("test", opts)
	serverT, clientT := mcp.NewInMemoryTransports()
	ss, err := srv.sdkServer.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, out any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	blob, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(blob, out); err != nil {
		t.Fatalf("structuredContent does not match the response type: %v\n%s", err, blob)
	}
	if len(res.Content) != 1 {
		t.Fatalf("want one text content block, got %d", len(res.Content))
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok || !json.Valid([]byte(text.Text)) {
		t.Fatalf("content[0] should be the JSON text of the structured result: %#v", res.Content[0])
	}
	return res
}

type fetchOut struct {
	Result *struct {
		Content    string   `json:"content"`
		ContentOK  bool     `json:"content_ok"`
		Quality    string   `json:"quality"`
		Warnings   []string `json:"warnings"`
		DocumentID string   `json:"document_id"`
		NextOffset int      `json:"next_offset"`
		Truncated  bool     `json:"truncated"`
	} `json:"result"`
	Error *struct {
		Code       string `json:"code"`
		Message    string `json:"message"`
		DocumentID string `json:"document_id"`
	} `json:"error"`
}

func TestToolsKeepTheirNamesAndInputs(t *testing.T) {
	cs := connect(t, Options{})
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	props := map[string][]string{}
	for _, tool := range res.Tools {
		schema, _ := json.Marshal(tool.InputSchema)
		var s struct {
			Properties map[string]any `json:"properties"`
			Required   []string       `json:"required"`
		}
		json.Unmarshal(schema, &s)
		for p := range s.Properties {
			props[tool.Name] = append(props[tool.Name], p)
		}
		if tool.OutputSchema == nil {
			t.Errorf("%s: missing output schema", tool.Name)
		}
		if len(s.Required) != 1 {
			t.Errorf("%s: required = %v, want exactly one (url/query)", tool.Name, s.Required)
		}
	}
	if len(res.Tools) != 2 {
		t.Fatalf("want exactly 2 tools, got %d", len(res.Tools))
	}
	for tool, want := range map[string][]string{
		"fetch":      {"url", "focus", "max_chars", "offset", "document_id"},
		"web_search": {"query", "num_results"},
	} {
		for _, p := range want {
			if !strings.Contains(strings.Join(props[tool], ","), p) {
				t.Errorf("%s: input %q missing (have %v)", tool, p, props[tool])
			}
		}
	}
}

func TestFetchToolContract(t *testing.T) {
	page := "<html><body><main>" + strings.Repeat("<p>Paragraph of real article text for the contract test.</p>", 40) + "</main></body></html>"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, page)
		case "/challenge":
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, `<html><head><title>Just a moment...</title></head><body><script>window._cf_chl_opt={}</script></body></html>`)
		case "/image":
			w.Header().Set("Content-Type", "image/png")
			w.Write([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"))
		}
	}))
	defer upstream.Close()
	cs := connect(t, Options{})

	var ok fetchOut
	res := call(t, cs, "fetch", map[string]any{"url": upstream.URL + "/page", "max_chars": 500}, &ok)
	if res.IsError || ok.Result == nil || !ok.Result.ContentOK || ok.Result.DocumentID == "" || !ok.Result.Truncated {
		t.Fatalf("success contract broken: isError=%v %+v", res.IsError, ok)
	}

	// Continuing with document_id is accepted and served from the same extraction.
	var next fetchOut
	res = call(t, cs, "fetch", map[string]any{"url": upstream.URL + "/page", "offset": ok.Result.NextOffset, "document_id": ok.Result.DocumentID}, &next)
	if res.IsError || next.Result == nil || next.Result.DocumentID != ok.Result.DocumentID {
		t.Errorf("pagination with document_id: %+v", next)
	}

	// A wall is a successful call with an explicit quality verdict.
	var wall fetchOut
	res = call(t, cs, "fetch", map[string]any{"url": upstream.URL + "/challenge"}, &wall)
	if res.IsError || wall.Result == nil || wall.Result.ContentOK || wall.Result.Quality != "blocked" || len(wall.Result.Warnings) == 0 {
		t.Errorf("challenge page: isError=%v %+v", res.IsError, wall.Result)
	}

	for name, args := range map[string]map[string]any{
		"missing url": {"url": ""},
		"bad scheme":  {"url": "file:///etc/passwd"},
		"unsupported": {"url": upstream.URL + "/image"},
	} {
		var failed fetchOut
		res = call(t, cs, "fetch", args, &failed)
		if !res.IsError || failed.Error == nil || failed.Error.Code == "" || failed.Result != nil {
			t.Errorf("%s: failed calls must set isError and carry a structured error: isError=%v %+v", name, res.IsError, failed)
		}
	}
}

func TestWebSearchToolContract(t *testing.T) {
	const key = "contract-secret-key"
	providers := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/exa":
			w.WriteHeader(http.StatusUnauthorized)
		case "/ddg":
			w.Header().Set("Retry-After", "600")
			w.WriteHeader(http.StatusTooManyRequests)
		case "/mojeek":
			if r.URL.Query().Get("q") == "works" {
				io.WriteString(w, `<ul class="results-standard"><li><h2><a href="https://go.dev/">Go</a></h2><p class="s">Go.</p></li></ul>`)
				return
			}
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer providers.Close()
	cs := connect(t, Options{Search: search.Config{
		ExaAPIKey:      key,
		ExaEndpoint:    providers.URL + "/exa",
		DDGEndpoint:    providers.URL + "/ddg",
		MojeekEndpoint: providers.URL + "/mojeek",
		TotalTimeout:   5 * time.Second,
	}})

	type searchOut struct {
		Results  []search.Result  `json:"results"`
		Source   string           `json:"source"`
		Attempts []search.Attempt `json:"attempts"`
		Error    *search.ToolError
	}

	var ok searchOut
	res := call(t, cs, "web_search", map[string]any{"query": "works"}, &ok)
	if res.IsError || ok.Source != "mojeek" || len(ok.Results) != 1 || len(ok.Attempts) != 3 {
		t.Errorf("success with fallback: isError=%v %+v", res.IsError, ok)
	}

	var failed searchOut
	res = call(t, cs, "web_search", map[string]any{"query": "fails"}, &failed)
	if !res.IsError || failed.Error == nil || failed.Error.Code == "no_results" || failed.Results == nil {
		t.Errorf("outage must be isError with a non-no_results code and an empty results array: %+v", failed)
	}
	text := fmt.Sprint(res.Content[0].(*mcp.TextContent).Text, res.StructuredContent)
	if strings.Contains(text, key) {
		t.Error("API key leaked into the tool result")
	}

	var invalid searchOut
	if res = call(t, cs, "web_search", map[string]any{"query": " "}, &invalid); !res.IsError || invalid.Error.Code != "invalid_input" {
		t.Errorf("empty query: %+v", invalid)
	}
}
