// Package mcpserver wires the fetch and search services into an MCP stdio
// server exposing exactly two tools: fetch and web_search.
package mcpserver

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hoshinoht/gofetch-mcp/internal/fetch"
	"github.com/hoshinoht/gofetch-mcp/internal/search"
)

type Server struct {
	sdkServer *mcp.Server
	fetcher   *fetch.Service
	searcher  *search.Service
}

// Options configures the underlying services; zero values use defaults.
type Options struct {
	Fetch  fetch.Options
	Search search.Config
}

type FetchInput struct {
	URL        string `json:"url" jsonschema:"absolute http(s) URL of the page, PDF, plain-text or Markdown document to fetch"`
	Focus      string `json:"focus,omitempty" jsonschema:"optional topic or keywords (quote exact phrases); narrows the returned markdown to matching sections"`
	MaxChars   int    `json:"max_chars,omitempty" jsonschema:"max characters (Unicode code points) to return (default 40000, max 150000)"`
	Offset     int    `json:"offset,omitempty" jsonschema:"character offset for pagination (use next_offset from the previous call)"`
	DocumentID string `json:"document_id,omitempty" jsonschema:"document_id from the previous call; pins pagination to that exact extraction and reports document_changed instead of mixing versions"`
}

type FetchResponse struct {
	Result *fetch.Result    `json:"result,omitempty"`
	Error  *fetch.ToolError `json:"error,omitempty"`
}

type SearchInput struct {
	Query      string `json:"query" jsonschema:"web search query"`
	NumResults int    `json:"num_results,omitempty" jsonschema:"number of results (default 5, max 10)"`
}

type SearchResponse struct {
	Results  []search.Result   `json:"results"`
	Source   string            `json:"source,omitempty"`
	Attempts []search.Attempt  `json:"attempts,omitempty"`
	Error    *search.ToolError `json:"error,omitempty"`
}

func New(version string, opts Options) *Server {
	s := &Server{
		fetcher:  fetch.NewServiceWithOptions(opts.Fetch),
		searcher: search.NewService(opts.Search),
	}
	s.sdkServer = mcp.NewServer(&mcp.Implementation{Name: "gofetch", Version: version}, nil)

	mcp.AddTool(s.sdkServer, &mcp.Tool{
		Name: "fetch",
		Description: "Fetch a URL (HTML, PDF, plain text or Markdown) as readable markdown. " +
			"content_ok/quality report extraction quality (usable, thin, blocked by a challenge/login/consent wall, empty), not factual accuracy; read warnings. " +
			"Use focus for targeted extraction; paginate with max_chars/offset and pass document_id back so all pages come from the same version.",
	}, s.fetch)
	mcp.AddTool(s.sdkServer, &mcp.Tool{
		Name: "web_search",
		Description: "Web search: Exa API when a key is configured, keyless DuckDuckGo/Mojeek fallback. " +
			"Rate-limited providers are retried within a deadline, then rerouted. Returns title, url, snippet, the provider that answered (source) and per-provider attempts; " +
			"error.code no_results means a provider answered with nothing, other codes mean providers failed.",
	}, s.webSearch)

	return s
}

func (s *Server) Run(ctx context.Context) error {
	return s.sdkServer.Run(ctx, &mcp.StdioTransport{})
}

// toolError marks a failed call with isError while the typed output still
// fills structuredContent (and its JSON text) with the error details.
func toolError() *mcp.CallToolResult { return &mcp.CallToolResult{IsError: true} }

func (s *Server) fetch(ctx context.Context, _ *mcp.CallToolRequest, input FetchInput) (*mcp.CallToolResult, FetchResponse, error) {
	if strings.TrimSpace(input.URL) == "" {
		return toolError(), FetchResponse{Error: &fetch.ToolError{Code: "invalid_input", Message: "url is required"}}, nil
	}
	result, toolErr := s.fetcher.Fetch(ctx, fetch.Request{
		URL:        input.URL,
		Focus:      input.Focus,
		MaxChars:   input.MaxChars,
		Offset:     input.Offset,
		DocumentID: input.DocumentID,
	})
	if toolErr != nil {
		return toolError(), FetchResponse{Error: toolErr}, nil
	}
	return nil, FetchResponse{Result: result}, nil
}

func (s *Server) webSearch(ctx context.Context, _ *mcp.CallToolRequest, input SearchInput) (*mcp.CallToolResult, SearchResponse, error) {
	if strings.TrimSpace(input.Query) == "" {
		return toolError(), SearchResponse{Results: []search.Result{}, Error: &search.ToolError{Code: "invalid_input", Message: "query is required"}}, nil
	}
	results, source, attempts, toolErr := s.searcher.Search(ctx, input.Query, input.NumResults)
	if toolErr != nil {
		return toolError(), SearchResponse{Results: []search.Result{}, Attempts: attempts, Error: toolErr}, nil
	}
	return nil, SearchResponse{Results: results, Source: source, Attempts: attempts}, nil
}
