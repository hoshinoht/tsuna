package search

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

type exaRequest struct {
	Query      string       `json:"query"`
	NumResults int          `json:"numResults"`
	Contents   *exaContents `json:"contents,omitempty"`
}

type exaContents struct {
	Highlights bool `json:"highlights"`
}

type exaResponse struct {
	Results *[]struct {
		Title      string   `json:"title"`
		URL        string   `json:"url"`
		Highlights []string `json:"highlights"`
	} `json:"results"`
}

// searchExa calls POST /search with the key in the x-api-key header; the
// key never appears in URLs, errors or diagnostics.
func (s *Service) searchExa(ctx context.Context, query string, numResults int) ([]Result, error) {
	payload, err := json.Marshal(exaRequest{
		Query:      query,
		NumResults: numResults,
		Contents:   &exaContents{Highlights: true},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.ExaEndpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", s.cfg.ExaAPIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	body, err := s.do(req, true)
	if err != nil {
		return nil, err
	}
	return parseExa(body)
}

func parseExa(body []byte) ([]Result, error) {
	var parsed exaResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, &providerError{outcome: OutcomeParseError, detail: "invalid JSON response"}
	}
	if parsed.Results == nil {
		return nil, &providerError{outcome: OutcomeParseError, detail: `response has no "results" field`}
	}
	results := make([]Result, 0, len(*parsed.Results))
	for _, r := range *parsed.Results {
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
