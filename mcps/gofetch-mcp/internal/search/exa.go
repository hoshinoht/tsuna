package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const exaEndpoint = "https://api.exa.ai/search"

// exaKey resolves the Exa API key: EXA_API_KEY env first, then the same key
// file the opencode exa MCP entry uses. Empty means "no key" and the caller
// skips the Exa backend.
func exaKey() string {
	if key := strings.TrimSpace(os.Getenv("EXA_API_KEY")); key != "" {
		return key
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(home, ".config", "opencode", ".exa-api-key"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

type exaRequest struct {
	Query      string       `json:"query"`
	NumResults int          `json:"numResults"`
	Contents   *exaContents `json:"contents,omitempty"`
}

type exaContents struct {
	Highlights bool `json:"highlights"`
}

type exaResponse struct {
	Results []struct {
		Title      string   `json:"title"`
		URL        string   `json:"url"`
		Highlights []string `json:"highlights"`
	} `json:"results"`
}

func (s *Service) searchExa(ctx context.Context, key, query string, numResults int) ([]Result, error) {
	payload, err := json.Marshal(exaRequest{
		Query:      query,
		NumResults: numResults,
		Contents:   &exaContents{Highlights: true},
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, exaEndpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", key)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("exa: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, rateLimitError("exa")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("exa: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("exa: %w", err)
	}
	return parseExa(body)
}

func parseExa(body []byte) ([]Result, error) {
	var parsed exaResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("exa: %w", err)
	}
	results := make([]Result, 0, len(parsed.Results))
	for _, r := range parsed.Results {
		if r.URL == "" {
			continue
		}
		snippet := ""
		if len(r.Highlights) > 0 {
			snippet = strings.TrimSpace(r.Highlights[0])
		}
		results = append(results, Result{Title: r.Title, URL: r.URL, Snippet: snippet})
	}
	return results, nil
}
