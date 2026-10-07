// Package search provides keyless web search: DuckDuckGo's HTML endpoint
// first, with Mojeek as fallback.
package search

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	requestTimeout    = 20 * time.Second
	maxBodyBytes      = 4 << 20
	userAgent         = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
	DefaultNumResults = 5
	MaxNumResults     = 10
)

type Result struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
}

type ToolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

type Service struct {
	client *http.Client
}

func NewService() *Service {
	return &Service{client: &http.Client{Timeout: requestTimeout}}
}

// Search prefers the Exa API when a key is available (EXA_API_KEY env or
// ~/.config/opencode/.exa-api-key), then falls back to keyless DuckDuckGo
// and Mojeek scraping. The source of the returned results is reported so
// agents can judge freshness/coverage.
func (s *Service) Search(ctx context.Context, query string, numResults int) ([]Result, string, *ToolError) {
	if numResults <= 0 {
		numResults = DefaultNumResults
	}
	if numResults > MaxNumResults {
		numResults = MaxNumResults
	}

	type backend struct {
		name string
		run  func() ([]Result, error)
	}
	backends := []backend{}
	if key := exaKey(); key != "" {
		backends = append(backends, backend{"exa", func() ([]Result, error) { return s.searchExa(ctx, key, query, numResults) }})
	}
	backends = append(backends,
		backend{"ddg", func() ([]Result, error) { return s.searchDDG(ctx, query, numResults) }},
		backend{"mojeek", func() ([]Result, error) { return s.searchMojeek(ctx, query, numResults) }},
	)

	// Each backend gets one backoff-retry on HTTP 429, then the next backend
	// takes over; any other failure reroutes immediately.
	var failures []string
	for _, b := range backends {
		results, err := withRateLimitRetry(ctx, b.run)
		if err == nil && len(results) > 0 {
			return results, b.name, nil
		}
		failures = append(failures, fmt.Sprintf("%s: %s", b.name, errString(err)))
	}

	return nil, "", &ToolError{
		Code:    "no_results",
		Message: "no results (" + strings.Join(failures, "; ") + ")",
		Hint:    "Rephrase the query, or use exa_web_search_exa if it is available.",
	}
}

func errString(err error) string {
	if err == nil {
		return "empty result set"
	}
	return err.Error()
}

func (s *Service) get(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, errRateLimited
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
}

func (s *Service) searchDDG(ctx context.Context, query string, numResults int) ([]Result, error) {
	endpoint := "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(query)
	body, err := s.get(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("ddg: %w", err)
	}
	return parseDDG(body, numResults)
}

func (s *Service) searchMojeek(ctx context.Context, query string, numResults int) ([]Result, error) {
	endpoint := "https://www.mojeek.com/search?q=" + url.QueryEscape(query)
	body, err := s.get(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("mojeek: %w", err)
	}
	return parseMojeek(body, numResults)
}

// decodeDDGHref unwraps DuckDuckGo's redirect links
// (//duckduckgo.com/l/?uddg=<escaped-url>&rut=...) to the target URL.
func decodeDDGHref(href string) string {
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil {
		return href
	}
	if uddg := u.Query().Get("uddg"); uddg != "" {
		if target, err := url.QueryUnescape(uddg); err == nil {
			return target
		}
	}
	return href
}
