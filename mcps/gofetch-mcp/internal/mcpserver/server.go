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

type FetchInput struct {
	URL      string `json:"url" jsonschema:"absolute http(s) URL of the page or PDF to fetch"`
	Focus    string `json:"focus,omitempty" jsonschema:"optional topic or keywords; narrows the returned markdown to matching sections"`
	MaxChars int    `json:"max_chars,omitempty" jsonschema:"max markdown characters to return (default 40000, max 150000)"`
	Offset   int    `json:"offset,omitempty" jsonschema:"character offset for pagination (use next_offset from the previous call)"`
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
	Results []search.Result   `json:"results"`
	Source  string            `json:"source,omitempty"`
	Error   *search.ToolError `json:"error,omitempty"`
}

func New(version string) *Server {
	s := &Server{
		fetcher:  fetch.NewService(),
		searcher: search.NewService(),
	}
	s.sdkServer = mcp.NewServer(&mcp.Implementation{Name: "gofetch", Version: version}, nil)

	mcp.AddTool(s.sdkServer, &mcp.Tool{
		Name:        "fetch",
		Description: "Fetch a URL (HTML page or PDF) as readable markdown. Check content_ok before trusting content. Use focus for targeted extraction; paginate with max_chars/offset.",
	}, s.fetch)
	mcp.AddTool(s.sdkServer, &mcp.Tool{
		Name:        "web_search",
		Description: "Web search: Exa API when a key is configured, keyless DuckDuckGo/Mojeek fallback; rate-limited backends are retried once then rerouted. Returns title, url, snippet, and which backend answered (source).",
	}, s.webSearch)

	return s
}

func (s *Server) Run(ctx context.Context) error {
	return s.sdkServer.Run(ctx, &mcp.StdioTransport{})
}

func (s *Server) fetch(ctx context.Context, _ *mcp.CallToolRequest, input FetchInput) (*mcp.CallToolResult, FetchResponse, error) {
	if strings.TrimSpace(input.URL) == "" {
		return nil, FetchResponse{Error: &fetch.ToolError{Code: "invalid_input", Message: "url is required"}}, nil
	}
	result, toolErr := s.fetcher.Fetch(ctx, input.URL, input.Focus, input.MaxChars, input.Offset)
	if toolErr != nil {
		return nil, FetchResponse{Error: toolErr}, nil
	}
	return nil, FetchResponse{Result: result}, nil
}

func (s *Server) webSearch(ctx context.Context, _ *mcp.CallToolRequest, input SearchInput) (*mcp.CallToolResult, SearchResponse, error) {
	if strings.TrimSpace(input.Query) == "" {
		return nil, SearchResponse{Results: []search.Result{}, Error: &search.ToolError{Code: "invalid_input", Message: "query is required"}}, nil
	}
	results, source, toolErr := s.searcher.Search(ctx, input.Query, input.NumResults)
	if toolErr != nil {
		return nil, SearchResponse{Results: []search.Result{}, Error: toolErr}, nil
	}
	return nil, SearchResponse{Results: results, Source: source}, nil
}
