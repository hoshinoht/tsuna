// Package fetch retrieves a URL (HTML, PDF, plain text or Markdown) and
// converts it to readable markdown with an explicit extraction-quality
// assessment, optional focus filtering, and character-offset pagination over
// a cached, content-addressed extraction.
package fetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxBodyBytes   = 20 << 20 // 20 MiB
	requestTimeout = 30 * time.Second
	userAgent      = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
)

type ToolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
	// DocumentID is the identifier of the current extraction when Code is
	// document_changed, so callers can restart pagination against it.
	DocumentID string `json:"document_id,omitempty"`
}

// Result is one page of an extracted document. All character counts and
// offsets are Unicode code points (runes), never bytes.
type Result struct {
	URL      string `json:"url"`
	FinalURL string `json:"final_url,omitempty"`
	Title    string `json:"title,omitempty"`
	Kind     string `json:"kind"` // "html", "pdf", "text" or "markdown"
	Content  string `json:"content"`
	// ContentOK is an extraction-quality signal: true only when Quality is
	// "usable". It does not verify that the content is accurate or current.
	ContentOK bool `json:"content_ok"`
	// Quality is "usable", "thin", "blocked" or "empty"; QualityReason
	// narrows a non-usable verdict (e.g. "challenge", "login_required").
	Quality       string   `json:"quality"`
	QualityReason string   `json:"quality_reason,omitempty"`
	Warnings      []string `json:"warnings,omitempty"`

	Focused           bool     `json:"focused,omitempty"`
	FocusStatus       string   `json:"focus_status,omitempty"` // "matched" or "no_match"
	FocusMatchedTerms []string `json:"focus_matched_terms,omitempty"`

	TotalChars int  `json:"total_chars"`
	Offset     int  `json:"offset"`
	Truncated  bool `json:"truncated"`
	NextOffset int  `json:"next_offset,omitempty"`

	// DocumentID identifies this exact extraction (a content hash). Pass it
	// back with the next offset so every page comes from the same version.
	DocumentID string `json:"document_id"`
	Cached     bool   `json:"cached,omitempty"`
	FetchedAt  string `json:"fetched_at"`
}

// Request is one fetch call.
type Request struct {
	URL        string
	Focus      string
	MaxChars   int
	Offset     int
	DocumentID string
}

// Options tunes a Service; zero values pick the defaults.
type Options struct {
	Client          *http.Client
	CacheTTL        time.Duration
	CacheMaxBytes   int64
	CacheMaxEntries int
}

type Service struct {
	client *http.Client
	cache  *docCache
	flight *flightGroup
	now    func() time.Time
}

func NewService() *Service { return NewServiceWithOptions(Options{}) }

func NewServiceWithOptions(opts Options) *Service {
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}
	s := &Service{client: client, flight: newFlightGroup(), now: time.Now}
	s.cache = newDocCache(opts.CacheTTL, opts.CacheMaxBytes, opts.CacheMaxEntries, func() time.Time { return s.now() })
	return s
}

// document is one extraction, shared between pages and concurrent callers.
// It is immutable once built.
type document struct {
	id        string
	key       string
	finalURL  string
	title     string
	kind      string
	markdown  string
	quality   assessment
	fetchedAt time.Time
}

// Fetch returns one page of the document at req.URL. The extraction is
// cached briefly so later pages reuse it; req.DocumentID pins pagination to
// one version and turns a silent version mix into document_changed.
func (s *Service) Fetch(ctx context.Context, req Request) (*Result, *ToolError) {
	parsed, err := url.Parse(strings.TrimSpace(req.URL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, &ToolError{Code: "invalid_input", Message: fmt.Sprintf("url must be absolute http(s), got %q", req.URL)}
	}
	parsed.Fragment, parsed.RawFragment = "", ""
	key := parsed.String()

	doc, cached, warnings, toolErr := s.resolve(ctx, key, strings.TrimSpace(req.DocumentID))
	if toolErr != nil {
		return nil, toolErr
	}

	markdown := doc.markdown
	res := &Result{
		URL:           req.URL,
		FinalURL:      doc.finalURL,
		Title:         doc.title,
		Kind:          doc.kind,
		ContentOK:     doc.quality.status == QualityUsable,
		Quality:       doc.quality.status,
		QualityReason: doc.quality.reason,
		DocumentID:    doc.id,
		Cached:        cached,
		FetchedAt:     doc.fetchedAt.UTC().Format(time.RFC3339),
	}
	res.Warnings = append(append([]string{}, doc.quality.warnings...), warnings...)

	if strings.TrimSpace(req.Focus) != "" {
		f := focusDocument(markdown, req.Focus)
		if f.matched {
			markdown = f.text
			res.Focused = true
			res.FocusStatus = "matched"
			res.FocusMatchedTerms = f.terms
		} else {
			res.FocusStatus = "no_match"
			res.Warnings = append(res.Warnings, "no focus terms matched; returning the full document")
		}
	}

	page := paginate(markdown, req.MaxChars, req.Offset)
	res.Content = page.content
	res.TotalChars = page.total
	res.Offset = page.offset
	res.Truncated = page.truncated
	res.NextOffset = page.nextOffset
	if len(res.Warnings) == 0 {
		res.Warnings = nil
	}
	return res, nil
}

// resolve finds the extraction to serve: by document ID, then the cached
// extraction for the URL, then a (deduplicated) fresh download.
func (s *Service) resolve(ctx context.Context, key, wantID string) (doc *document, cached bool, warnings []string, toolErr *ToolError) {
	if wantID != "" {
		if d := s.cache.getByID(wantID); d != nil && d.key == key {
			return d, true, nil, nil
		}
	}
	if d := s.cache.getByKey(key); d != nil {
		if wantID != "" && d.id != wantID {
			return nil, false, nil, changedError(wantID, d.id)
		}
		return d, true, nil, nil
	}

	d, toolErr := s.load(ctx, key)
	if toolErr != nil {
		if wantID != "" && toolErr.Code != "canceled" {
			toolErr = &ToolError{
				Code:    "document_expired",
				Message: fmt.Sprintf("document %s is no longer cached and re-fetching failed: %s", wantID, toolErr.Message),
				Hint:    "Retry later, or fetch again from offset 0 without document_id.",
			}
		}
		return nil, false, nil, toolErr
	}
	if wantID != "" {
		if d.id != wantID {
			return nil, false, nil, changedError(wantID, d.id)
		}
		warnings = append(warnings, "cached extraction had expired; re-fetched and verified unchanged")
	}
	return d, false, warnings, nil
}

func changedError(oldID, newID string) *ToolError {
	return &ToolError{
		Code:       "document_changed",
		Message:    fmt.Sprintf("the document changed since document_id %s was issued (current: %s); pages from different versions would not line up", oldID, newID),
		Hint:       "Restart pagination at offset 0 with the new document_id.",
		DocumentID: newID,
	}
}

// load downloads and extracts key once even when several callers ask at
// the same time, and caches usable results.
func (s *Service) load(ctx context.Context, key string) (*document, *ToolError) {
	return s.flight.do(ctx, key, func(ctx context.Context) (*document, *ToolError) {
		d, toolErr := s.extract(ctx, key)
		if toolErr == nil && cacheable(d) {
			s.cache.put(d)
		}
		return d, toolErr
	})
}

// cacheable keeps walls and empty extractions out of the cache so a retry
// goes back to the network.
func cacheable(d *document) bool {
	return d.quality.status == QualityUsable || d.quality.status == QualityThin
}

func (s *Service) extract(ctx context.Context, rawURL string) (*document, *ToolError) {
	body, finalURL, header, toolErr := s.download(ctx, rawURL)
	if toolErr != nil {
		return nil, toolErr
	}
	contentType := header.Get("Content-Type")

	kind, ok := sniffKind(body, contentType, finalURL)
	if !ok {
		return nil, &ToolError{
			Code:    "unsupported_format",
			Message: fmt.Sprintf("%s served %s, which fetch cannot extract", hostOf(rawURL), describeType(body, contentType)),
			Hint:    "fetch handles HTML, PDF, plain text and Markdown.",
		}
	}

	var (
		markdown, title string
		q               assessment
		err             error
	)
	switch kind {
	case "pdf":
		markdown, err = convertPDF(body)
		title = titleFromURL(finalURL)
		if err == nil {
			q = assessPlain(markdown, "pdf")
		}
	case "markdown", "text":
		markdown = convertText(body, contentType)
		title = titleFromURL(finalURL)
		if kind == "markdown" {
			if h := firstMarkdownHeading(markdown); h != "" {
				title = h
			}
		}
		q = assessPlain(markdown, kind)
	default:
		var ex *htmlExtraction
		ex, err = extractHTML(body, contentType, finalURL)
		if err == nil {
			markdown, title = ex.markdown, ex.title
			q = assessHTML(ex, header, finalURL)
		}
	}
	if err != nil {
		return nil, &ToolError{Code: "extraction_failed", Message: fmt.Sprintf("could not extract %s content: %v", kind, err)}
	}
	if !sameHost(rawURL, finalURL) {
		q.warnings = append(q.warnings, "redirected to "+hostOf(finalURL))
	}

	return &document{
		id:        documentID(kind, finalURL, markdown),
		key:       rawURL,
		finalURL:  finalURL,
		title:     title,
		kind:      kind,
		markdown:  markdown,
		quality:   q,
		fetchedAt: s.now(),
	}, nil
}

func documentID(kind, finalURL, markdown string) string {
	h := sha256.New()
	io.WriteString(h, kind)
	h.Write([]byte{0})
	io.WriteString(h, finalURL)
	h.Write([]byte{0})
	io.WriteString(h, markdown)
	return hex.EncodeToString(h.Sum(nil)[:8])
}

func (s *Service) download(ctx context.Context, rawURL string) (body []byte, finalURL string, header http.Header, toolErr *ToolError) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", nil, &ToolError{Code: "invalid_input", Message: err.Error()}
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/pdf,text/markdown,text/plain;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, "", nil, transportError(ctx, rawURL, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests, http.StatusServiceUnavailable:
		hint := "The site is blocking automated fetches; try the site's canonical/mirror URL or a cached copy."
		if resp.StatusCode == http.StatusUnauthorized {
			hint = "The page requires authentication."
		}
		return nil, "", nil, &ToolError{
			Code:    "blocked",
			Message: fmt.Sprintf("%s returned status %d", hostOf(rawURL), resp.StatusCode),
			Hint:    hint,
		}
	default:
		return nil, "", nil, &ToolError{Code: "upstream_error", Message: fmt.Sprintf("%s returned status %d", hostOf(rawURL), resp.StatusCode)}
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, "", nil, transportError(ctx, rawURL, err)
	}
	if len(data) > maxBodyBytes {
		return nil, "", nil, &ToolError{Code: "too_large", Message: fmt.Sprintf("document at %s exceeds the %d MiB limit", hostOf(rawURL), maxBodyBytes>>20)}
	}

	final := rawURL
	if resp.Request != nil && resp.Request.URL != nil {
		final = resp.Request.URL.String()
	}
	return data, final, resp.Header, nil
}

// transportError classifies a failed request without echoing the raw
// error, which can carry full URLs.
func transportError(ctx context.Context, rawURL string, err error) *ToolError {
	host := hostOf(rawURL)
	if errors.Is(ctx.Err(), context.Canceled) {
		return &ToolError{Code: "canceled", Message: "fetch was canceled"}
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return &ToolError{Code: "timeout", Message: fmt.Sprintf("request to %s timed out", host)}
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	return &ToolError{Code: "upstream_error", Message: fmt.Sprintf("request to %s failed: %v", host, err)}
}

func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}
	return u.Host
}

func sameHost(a, b string) bool {
	return strings.EqualFold(strings.TrimPrefix(hostOf(a), "www."), strings.TrimPrefix(hostOf(b), "www."))
}

func titleFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	return segs[len(segs)-1]
}

// runeCount is the single definition of "characters" used for thresholds,
// totals and offsets.
func runeCount(s string) int { return utf8.RuneCountInString(s) }
