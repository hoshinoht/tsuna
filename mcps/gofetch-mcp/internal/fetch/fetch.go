// Package fetch retrieves a URL (HTML or PDF) and converts it to readable
// markdown with optional focus filtering and character-offset pagination.
package fetch

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maxBodyBytes    = 20 << 20 // 20 MiB
	requestTimeout  = 30 * time.Second
	userAgent       = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
	minContentChars = 200 // below this, content_ok is false
)

type ToolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

type Result struct {
	URL         string `json:"url"`
	FinalURL    string `json:"final_url,omitempty"`
	Title       string `json:"title,omitempty"`
	Kind        string `json:"kind"` // "html" or "pdf"
	Content     string `json:"content"`
	ContentOK   bool   `json:"content_ok"`
	Focused     bool   `json:"focused,omitempty"`
	TotalChars  int    `json:"total_chars"`
	Offset      int    `json:"offset"`
	Truncated   bool   `json:"truncated"`
	NextOffset  int    `json:"next_offset,omitempty"`
}

type Service struct {
	client *http.Client
}

func NewService() *Service {
	return &Service{client: &http.Client{Timeout: requestTimeout}}
}

var pdfMagic = []byte("%PDF-")

// Fetch downloads rawURL, extracts readable markdown (HTML via
// main-content selection, PDF via text extraction), optionally narrows to
// blocks matching focus, and paginates by max_chars/offset.
func (s *Service) Fetch(ctx context.Context, rawURL, focus string, maxChars, offset int) (*Result, *ToolError) {
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, &ToolError{Code: "invalid_input", Message: fmt.Sprintf("url must be absolute http(s), got %q", rawURL)}
	}

	body, finalURL, contentType, toolErr := s.download(ctx, rawURL)
	if toolErr != nil {
		return nil, toolErr
	}

	kind := sniffKind(body, contentType)
	var markdown, title string
	switch kind {
	case "pdf":
		markdown, err = convertPDF(body)
		title = pdfTitleFromURL(finalURL)
	default:
		markdown, title, err = convertHTML(body, finalURL)
	}
	if err != nil {
		return nil, &ToolError{Code: "extraction_failed", Message: fmt.Sprintf("could not extract %s content: %v", kind, err)}
	}

	focused := false
	if strings.TrimSpace(focus) != "" {
		if narrowed, ok := focusContent(markdown, focus); ok {
			markdown = narrowed
			focused = true
		}
	}

	page := paginate(markdown, maxChars, offset)
	return &Result{
		URL:        rawURL,
		FinalURL:   finalURL,
		Title:      title,
		Kind:       kind,
		Content:    page.content,
		ContentOK:  len(strings.TrimSpace(markdown)) >= minContentChars,
		Focused:    focused,
		TotalChars: page.total,
		Offset:     page.offset,
		Truncated:  page.truncated,
		NextOffset: page.nextOffset,
	}, nil
}

func (s *Service) download(ctx context.Context, rawURL string) (body []byte, finalURL, contentType string, toolErr *ToolError) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", "", &ToolError{Code: "invalid_input", Message: err.Error()}
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/pdf,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, "", "", &ToolError{Code: "upstream_error", Message: fmt.Sprintf("request to %s failed: %v", hostOf(rawURL), err)}
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden, http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return nil, "", "", &ToolError{
			Code:    "blocked",
			Message: fmt.Sprintf("%s returned status %d", hostOf(rawURL), resp.StatusCode),
			Hint:    "The site is blocking automated fetches; try the site's canonical/mirror URL or a cached copy.",
		}
	default:
		return nil, "", "", &ToolError{Code: "upstream_error", Message: fmt.Sprintf("%s returned status %d", hostOf(rawURL), resp.StatusCode)}
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, "", "", &ToolError{Code: "upstream_error", Message: fmt.Sprintf("reading body from %s failed: %v", hostOf(rawURL), err)}
	}
	if len(data) > maxBodyBytes {
		return nil, "", "", &ToolError{Code: "too_large", Message: fmt.Sprintf("document at %s exceeds the %d MiB limit", hostOf(rawURL), maxBodyBytes>>20)}
	}

	final := rawURL
	if resp.Request != nil && resp.Request.URL != nil {
		final = resp.Request.URL.String()
	}
	return data, final, resp.Header.Get("Content-Type"), nil
}

func sniffKind(body []byte, contentType string) string {
	if bytes.HasPrefix(bytes.TrimLeft(body, "\r\n\t "), pdfMagic) {
		return "pdf"
	}
	if strings.Contains(strings.ToLower(contentType), "pdf") {
		return "pdf"
	}
	return "html"
}

func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}
	return u.Host
}

func pdfTitleFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) == 0 {
		return ""
	}
	return segs[len(segs)-1]
}
