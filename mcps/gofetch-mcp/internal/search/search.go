// Package search provides web search through the Exa API (when a key is
// configured) with keyless DuckDuckGo and Mojeek scraping as fallbacks, and
// reports every provider attempt so failures are never mistaken for an empty
// result set.
package search

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maxBodyBytes      = 4 << 20
	userAgent         = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
	DefaultNumResults = 5
	MaxNumResults     = 10

	defaultTotalTimeout   = 40 * time.Second
	defaultAttemptTimeout = 15 * time.Second
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

// Attempt outcomes.
const (
	OutcomeOK            = "ok"
	OutcomeEmpty         = "empty" // the provider answered: no results
	OutcomeRateLimited   = "rate_limited"
	OutcomeAuthFailed    = "auth_failed"
	OutcomeQuotaExceeded = "quota_exceeded"
	OutcomeBlocked       = "blocked"
	OutcomeTimeout       = "timeout"
	OutcomeParseError    = "parse_error"
	OutcomeTooLarge      = "too_large"
	OutcomeUnavailable   = "unavailable" // 5xx
	OutcomeHTTPError     = "http_error"
	OutcomeNetworkError  = "network_error"
	OutcomeCanceled      = "canceled"
	OutcomeSkipped       = "skipped"
)

// Attempt is the compact diagnostic for one provider.
type Attempt struct {
	Provider    string `json:"provider"`
	Outcome     string `json:"outcome"`
	Status      int    `json:"status,omitempty"`
	Results     int    `json:"results,omitempty"`
	Tries       int    `json:"tries,omitempty"`
	RetryAfterS int    `json:"retry_after_s,omitempty"`
	ElapsedMs   int64  `json:"elapsed_ms"`
	Detail      string `json:"detail,omitempty"`
}

// Config wires the service. Endpoints, client and sleep are overridable for
// tests; zero values use the real providers.
type Config struct {
	ExaAPIKey      string
	ExaEndpoint    string
	DDGEndpoint    string
	MojeekEndpoint string
	Client         *http.Client
	TotalTimeout   time.Duration
	AttemptTimeout time.Duration
	Retry          RetryPolicy
	// Sleep waits for d or until ctx is done; tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error
}

type Service struct {
	cfg       Config
	client    *http.Client
	providers []provider
}

type provider struct {
	name   string
	search func(ctx context.Context, query string, n int) ([]Result, error)
}

func NewService(cfg Config) *Service {
	if cfg.ExaEndpoint == "" {
		cfg.ExaEndpoint = "https://api.exa.ai/search"
	}
	if cfg.DDGEndpoint == "" {
		cfg.DDGEndpoint = "https://html.duckduckgo.com/html/"
	}
	if cfg.MojeekEndpoint == "" {
		cfg.MojeekEndpoint = "https://www.mojeek.com/search"
	}
	if cfg.TotalTimeout <= 0 {
		cfg.TotalTimeout = defaultTotalTimeout
	}
	if cfg.AttemptTimeout <= 0 {
		cfg.AttemptTimeout = defaultAttemptTimeout
	}
	if cfg.Retry == (RetryPolicy{}) {
		cfg.Retry = DefaultRetryPolicy
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleepContext
	}
	client := cfg.Client
	if client == nil {
		// Per-attempt deadlines come from contexts, not a client timeout.
		client = &http.Client{}
	}
	s := &Service{cfg: cfg, client: client}
	if cfg.ExaAPIKey != "" {
		s.providers = append(s.providers, provider{"exa", s.searchExa})
	}
	s.providers = append(s.providers,
		provider{"ddg", s.searchDDG},
		provider{"mojeek", s.searchMojeek},
	)
	return s
}

// Search tries each provider in order until one returns results. Each
// provider is retried on rate limits within the total deadline; every
// attempt is reported, on success and failure alike.
func (s *Service) Search(ctx context.Context, query string, numResults int) ([]Result, string, []Attempt, *ToolError) {
	if numResults <= 0 {
		numResults = DefaultNumResults
	}
	if numResults > MaxNumResults {
		numResults = MaxNumResults
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, s.cfg.TotalTimeout)
	defer cancel()

	var attempts []Attempt
	for i, p := range s.providers {
		if ctx.Err() != nil {
			for _, rest := range s.providers[i:] {
				attempts = append(attempts, Attempt{Provider: rest.name, Outcome: OutcomeSkipped, Detail: stopReason(parent)})
			}
			break
		}
		results, a := s.attempt(ctx, parent, p, query, numResults)
		attempts = append(attempts, a)
		if a.Outcome == OutcomeOK {
			return results, p.name, attempts, nil
		}
		if a.Outcome == OutcomeCanceled {
			for _, rest := range s.providers[i+1:] {
				attempts = append(attempts, Attempt{Provider: rest.name, Outcome: OutcomeSkipped, Detail: "canceled"})
			}
			break
		}
	}
	return []Result{}, "", attempts, summarize(attempts)
}

func stopReason(parent context.Context) string {
	if parent.Err() != nil {
		return "canceled"
	}
	return "search deadline exceeded"
}

// attempt runs one provider with bounded retries.
func (s *Service) attempt(ctx, parent context.Context, p provider, query string, n int) ([]Result, Attempt) {
	a := Attempt{Provider: p.name}
	start := time.Now()
	finish := func(results []Result, a Attempt) ([]Result, Attempt) {
		a.ElapsedMs = time.Since(start).Milliseconds()
		return results, a
	}

	for try := 1; ; try++ {
		a.Tries = try
		attemptCtx, cancel := context.WithTimeout(ctx, s.cfg.AttemptTimeout)
		results, err := p.search(attemptCtx, query, n)
		cancel()

		if err == nil {
			a.Outcome, a.Results, a.Status, a.Detail = OutcomeOK, len(results), 0, ""
			if len(results) == 0 {
				a.Outcome = OutcomeEmpty
			}
			return finish(results, a)
		}

		pe := classify(parent, err)
		a.Outcome, a.Status, a.Detail = pe.outcome, pe.status, redact(pe.detail, s.cfg.ExaAPIKey)
		a.RetryAfterS = int((pe.retryAfter + time.Second - 1) / time.Second)
		if pe.outcome == OutcomeCanceled || !pe.retryable() || try >= s.cfg.Retry.MaxTries {
			return finish(nil, a)
		}
		wait, ok := s.cfg.Retry.wait(ctx, try, pe.retryAfter)
		if !ok {
			if pe.retryAfter > 0 {
				a.Detail = joinDetail(a.Detail, "Retry-After exceeds the remaining budget; rerouted")
			}
			return finish(nil, a)
		}
		if err := s.cfg.Sleep(ctx, wait); err != nil {
			if parent.Err() != nil {
				a.Outcome, a.Detail = OutcomeCanceled, "canceled while backing off"
			} else {
				a.Outcome, a.Detail = OutcomeTimeout, "search deadline reached while backing off"
			}
			return finish(nil, a)
		}
	}
}

// summarize turns a failed run into one error code: no_results only when a
// provider genuinely answered with nothing, otherwise the shared failure
// mode, otherwise search_unavailable.
func summarize(attempts []Attempt) *ToolError {
	parts := make([]string, 0, len(attempts))
	outcomes := map[string]bool{}
	for _, a := range attempts {
		part := a.Provider + ": " + a.Outcome
		if a.Status != 0 {
			part += fmt.Sprintf(" (HTTP %d)", a.Status)
		}
		parts = append(parts, part)
		if a.Outcome != OutcomeSkipped {
			outcomes[a.Outcome] = true
		}
	}
	msg := strings.Join(parts, "; ")

	switch {
	case outcomes[OutcomeCanceled]:
		return &ToolError{Code: "canceled", Message: "search canceled (" + msg + ")"}
	case outcomes[OutcomeEmpty]:
		return &ToolError{Code: "no_results", Message: "no results (" + msg + ")",
			Hint: "The query matched nothing; rephrase or broaden it."}
	case len(outcomes) == 1:
		for o := range outcomes {
			return &ToolError{Code: o, Message: "all search providers failed (" + msg + ")", Hint: hintFor(o)}
		}
	case len(outcomes) == 0:
		return &ToolError{Code: "timeout", Message: "search deadline exceeded before any provider answered"}
	}
	return &ToolError{Code: "search_unavailable", Message: "all search providers failed (" + msg + ")",
		Hint: "See attempts for each provider's failure; retry shortly."}
}

func hintFor(outcome string) string {
	switch outcome {
	case OutcomeRateLimited:
		return "Every provider is rate limiting; wait before retrying."
	case OutcomeAuthFailed, OutcomeQuotaExceeded:
		return "Check the configured Exa API key and its quota."
	case OutcomeBlocked:
		return "Providers are blocking automated queries from this network; configure an Exa API key."
	case OutcomeTimeout:
		return "Providers did not answer in time; retry shortly."
	case OutcomeParseError:
		return "A provider's result page format changed; gofetch needs an update."
	}
	return ""
}

// providerError is a classified provider failure.
type providerError struct {
	outcome    string
	status     int
	retryAfter time.Duration
	detail     string
}

func (e *providerError) Error() string {
	if e.detail == "" {
		return e.outcome
	}
	return e.outcome + ": " + e.detail
}

func (e *providerError) retryable() bool {
	return e.outcome == OutcomeRateLimited || (e.outcome == OutcomeUnavailable && e.retryAfter > 0)
}

// classify maps any provider error to a providerError, telling the
// caller's cancellation apart from a timeout.
func classify(parent context.Context, err error) *providerError {
	if parent.Err() != nil && errors.Is(parent.Err(), context.Canceled) {
		return &providerError{outcome: OutcomeCanceled, detail: "canceled by caller"}
	}
	var pe *providerError
	if errors.As(err, &pe) {
		return pe
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return &providerError{outcome: OutcomeTimeout, detail: "no response before the deadline"}
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err // drop the URL, which echoes the query
	}
	return &providerError{outcome: OutcomeNetworkError, detail: err.Error()}
}

// do sends req and returns the body of a 200 response, classifying every
// other outcome. authOn403 marks providers where 403 means a bad key rather
// than a bot block.
func (s *Service) do(req *http.Request, authOn403 bool) ([]byte, error) {
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return nil, statusError(resp, authOn403)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBodyBytes {
		return nil, &providerError{outcome: OutcomeTooLarge, status: resp.StatusCode,
			detail: fmt.Sprintf("response exceeds %d MiB", maxBodyBytes>>20)}
	}
	return body, nil
}

func statusError(resp *http.Response, authOn403 bool) *providerError {
	pe := &providerError{status: resp.StatusCode, retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())}
	switch code := resp.StatusCode; {
	case code == http.StatusTooManyRequests:
		pe.outcome = OutcomeRateLimited
	case code == http.StatusUnauthorized, code == http.StatusForbidden && authOn403:
		pe.outcome, pe.detail = OutcomeAuthFailed, "API key rejected"
	case code == http.StatusPaymentRequired:
		pe.outcome, pe.detail = OutcomeQuotaExceeded, "payment required"
	case code == http.StatusForbidden, code == http.StatusAccepted:
		// DuckDuckGo answers bot checks with 202 and a challenge page.
		pe.outcome, pe.detail = OutcomeBlocked, "bot check or block page"
	case code >= 500:
		pe.outcome = OutcomeUnavailable
	default:
		pe.outcome = OutcomeHTTPError
	}
	return pe
}

func newGet(ctx context.Context, rawURL string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	return req, nil
}

func (s *Service) searchDDG(ctx context.Context, query string, n int) ([]Result, error) {
	req, err := newGet(ctx, s.cfg.DDGEndpoint+"?q="+url.QueryEscape(query))
	if err != nil {
		return nil, err
	}
	body, err := s.do(req, false)
	if err != nil {
		return nil, err
	}
	return parseDDG(body, n)
}

func (s *Service) searchMojeek(ctx context.Context, query string, n int) ([]Result, error) {
	req, err := newGet(ctx, s.cfg.MojeekEndpoint+"?q="+url.QueryEscape(query))
	if err != nil {
		return nil, err
	}
	body, err := s.do(req, false)
	if err != nil {
		return nil, err
	}
	return parseMojeek(body, n)
}

// redact removes a credential from text bound for diagnostics.
func redact(text, secret string) string {
	if secret == "" || len(secret) < 4 {
		return text
	}
	return strings.ReplaceAll(text, secret, "[redacted]")
}

func joinDetail(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
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
