package search

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testKey = "exa-secret-key-123456"

// providers is a fake Exa/DDG/Mojeek trio; each handler can be swapped.
type providers struct {
	exa, ddg, mojeek http.HandlerFunc
	hits             sync.Map
}

func (p *providers) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := p.hits.LoadOrStore(r.URL.Path, new(atomic.Int32))
		n.(*atomic.Int32).Add(1)
		var h http.HandlerFunc
		switch r.URL.Path {
		case "/exa":
			h = p.exa
		case "/ddg":
			h = p.ddg
		case "/mojeek":
			h = p.mojeek
		}
		if h == nil {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (p *providers) count(path string) int32 {
	n, ok := p.hits.Load(path)
	if !ok {
		return 0
	}
	return n.(*atomic.Int32).Load()
}

type sleepRecorder struct {
	mu    sync.Mutex
	waits []time.Duration
}

func (r *sleepRecorder) sleep(ctx context.Context, d time.Duration) error {
	r.mu.Lock()
	r.waits = append(r.waits, d)
	r.mu.Unlock()
	return ctx.Err()
}

func newTestService(srv *httptest.Server, key string, sleeper *sleepRecorder) *Service {
	cfg := Config{
		ExaAPIKey:      key,
		ExaEndpoint:    srv.URL + "/exa",
		DDGEndpoint:    srv.URL + "/ddg",
		MojeekEndpoint: srv.URL + "/mojeek",
		TotalTimeout:   10 * time.Second,
		AttemptTimeout: 5 * time.Second,
		Retry:          RetryPolicy{MaxTries: 3, BaseBackoff: 500 * time.Millisecond, MaxBackoff: 4 * time.Second, MaxRetryAfter: 5 * time.Second, Reserve: time.Second},
	}
	if sleeper != nil {
		cfg.Sleep = sleeper.sleep
	}
	return NewService(cfg)
}

func html(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, body)
	}
}

func status(code int, header ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i+1 < len(header); i += 2 {
			w.Header().Set(header[i], header[i+1])
		}
		w.WriteHeader(code)
	}
}

func outcomes(attempts []Attempt) string {
	var parts []string
	for _, a := range attempts {
		parts = append(parts, a.Provider+"="+a.Outcome)
	}
	return strings.Join(parts, ",")
}

func TestSearchExaSuccess(t *testing.T) {
	p := &providers{exa: func(w http.ResponseWriter, r *http.Request) {
		var req exaRequest
		json.NewDecoder(r.Body).Decode(&req)
		if r.Header.Get("x-api-key") != testKey || req.NumResults != 3 || req.Contents == nil || !req.Contents.Highlights {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		io.WriteString(w, exaFixture)
	}}
	srv := p.start(t)
	results, source, attempts, toolErr := newTestService(srv, testKey, nil).Search(context.Background(), "go mcp", 3)
	if toolErr != nil || source != "exa" || len(results) != 2 {
		t.Fatalf("got %v %q %+v %+v", results, source, attempts, toolErr)
	}
	if outcomes(attempts) != "exa=ok" || attempts[0].Results != 2 {
		t.Errorf("attempts = %+v", attempts)
	}
}

func TestSearchAllProvidersDownIsNotNoResults(t *testing.T) {
	p := &providers{
		exa: func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "invalid key "+testKey, http.StatusUnauthorized)
		},
		ddg:    html(`<html><body><div class="anomaly-modal">Unfortunately, bots use DuckDuckGo too.</div></body></html>`),
		mojeek: status(http.StatusInternalServerError),
	}
	srv := p.start(t)
	_, _, attempts, toolErr := newTestService(srv, testKey, &sleepRecorder{}).Search(context.Background(), "q", 5)
	if toolErr == nil || toolErr.Code != "search_unavailable" {
		t.Fatalf("want search_unavailable, got %+v", toolErr)
	}
	if got := outcomes(attempts); got != "exa=auth_failed,ddg=blocked,mojeek=unavailable" {
		t.Errorf("outcomes = %s", got)
	}
	if attempts[0].Status != 401 || attempts[2].Status != 500 {
		t.Errorf("statuses not reported: %+v", attempts)
	}
	blob, _ := json.Marshal(struct {
		A []Attempt
		E *ToolError
	}{attempts, toolErr})
	if strings.Contains(string(blob), testKey) {
		t.Errorf("credential leaked into diagnostics: %s", blob)
	}
}

func TestSearchDDGBlockedWith202(t *testing.T) {
	p := &providers{ddg: status(http.StatusAccepted), mojeek: html(mojeekFixture)}
	srv := p.start(t)
	_, source, attempts, toolErr := newTestService(srv, "", nil).Search(context.Background(), "q", 5)
	if toolErr != nil || source != "mojeek" || outcomes(attempts) != "ddg=blocked,mojeek=ok" {
		t.Errorf("source=%s attempts=%+v err=%+v", source, attempts, toolErr)
	}
}

func TestSearchLegitimateEmpty(t *testing.T) {
	p := &providers{
		ddg:    html(`<html><body><div id="links"><div class="no-results">No results.</div></div></body></html>`),
		mojeek: html(`<html><body><p>No pages found matching: zzzqqq</p></body></html>`),
	}
	srv := p.start(t)
	results, _, attempts, toolErr := newTestService(srv, "", nil).Search(context.Background(), "zzzqqq", 5)
	if toolErr == nil || toolErr.Code != "no_results" || results == nil || len(results) != 0 {
		t.Fatalf("want no_results with empty slice, got %v %+v", results, toolErr)
	}
	if outcomes(attempts) != "ddg=empty,mojeek=empty" {
		t.Errorf("outcomes = %s", outcomes(attempts))
	}
}

func TestSearchParserFailure(t *testing.T) {
	p := &providers{
		ddg:    html(`<html><body><div class="new-layout">Results moved</div></body></html>`),
		mojeek: html(`<html><body><section>redesigned</section></body></html>`),
	}
	srv := p.start(t)
	_, _, attempts, toolErr := newTestService(srv, "", nil).Search(context.Background(), "q", 5)
	if toolErr == nil || toolErr.Code != "parse_error" || outcomes(attempts) != "ddg=parse_error,mojeek=parse_error" {
		t.Errorf("err=%+v attempts=%+v", toolErr, attempts)
	}
}

func TestSearchHonoursRetryAfter(t *testing.T) {
	var calls atomic.Int32
	p := &providers{ddg: func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			status(http.StatusTooManyRequests, "Retry-After", "2")(w, r)
			return
		}
		html(ddgFixture)(w, r)
	}}
	srv := p.start(t)
	sleeper := &sleepRecorder{}
	_, source, attempts, toolErr := newTestService(srv, "", sleeper).Search(context.Background(), "q", 5)
	if toolErr != nil || source != "ddg" {
		t.Fatalf("source=%s err=%+v", source, toolErr)
	}
	if len(sleeper.waits) != 1 || sleeper.waits[0] != 2*time.Second {
		t.Errorf("waits = %v, want [2s] from Retry-After", sleeper.waits)
	}
	if attempts[0].Tries != 2 || attempts[0].Outcome != OutcomeOK {
		t.Errorf("attempt = %+v", attempts[0])
	}
}

func TestSearchBoundedBackoffThenReroute(t *testing.T) {
	p := &providers{
		ddg:    status(http.StatusTooManyRequests),
		mojeek: status(http.StatusTooManyRequests, "Retry-After", "120"),
	}
	srv := p.start(t)
	sleeper := &sleepRecorder{}
	_, _, attempts, toolErr := newTestService(srv, "", sleeper).Search(context.Background(), "q", 5)
	if toolErr == nil || toolErr.Code != "rate_limited" {
		t.Fatalf("want rate_limited, got %+v", toolErr)
	}
	// ddg: 3 tries with exponential backoff; mojeek: Retry-After 120s is
	// beyond the policy, so it reroutes without waiting.
	if len(sleeper.waits) != 2 || sleeper.waits[0] != 500*time.Millisecond || sleeper.waits[1] != time.Second {
		t.Errorf("waits = %v", sleeper.waits)
	}
	if attempts[0].Tries != 3 || attempts[1].Tries != 1 || attempts[1].RetryAfterS != 120 {
		t.Errorf("attempts = %+v", attempts)
	}
	if p.count("/mojeek") != 1 {
		t.Errorf("mojeek retried despite a 120s Retry-After")
	}
}

func TestSearchRetryAfterRespectsTotalDeadline(t *testing.T) {
	p := &providers{ddg: status(http.StatusTooManyRequests, "Retry-After", "3"), mojeek: html(mojeekFixture)}
	srv := p.start(t)
	s := newTestService(srv, "", &sleepRecorder{})
	s.cfg.TotalTimeout = 3 * time.Second // 3s wait + 1s reserve does not fit
	_, source, attempts, _ := s.Search(context.Background(), "q", 5)
	if source != "mojeek" || attempts[0].Tries != 1 {
		t.Errorf("should reroute instead of waiting past the deadline: %+v", attempts)
	}
}

func TestSearchOversizedResponse(t *testing.T) {
	p := &providers{
		ddg:    html("<html><body>" + strings.Repeat("x", maxBodyBytes+10) + "</body></html>"),
		mojeek: html(mojeekFixture),
	}
	srv := p.start(t)
	_, source, attempts, _ := newTestService(srv, "", nil).Search(context.Background(), "q", 5)
	if source != "mojeek" || attempts[0].Outcome != OutcomeTooLarge {
		t.Errorf("attempts = %+v", attempts)
	}
}

func TestSearchCancellationStopsFallback(t *testing.T) {
	p := &providers{
		ddg:    func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() },
		mojeek: html(mojeekFixture),
	}
	srv := p.start(t)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	_, _, attempts, toolErr := newTestService(srv, "", nil).Search(ctx, "q", 5)
	if time.Since(start) > 2*time.Second {
		t.Errorf("cancellation took %v", time.Since(start))
	}
	if toolErr == nil || toolErr.Code != "canceled" || outcomes(attempts) != "ddg=canceled,mojeek=skipped" {
		t.Errorf("err=%+v attempts=%+v", toolErr, attempts)
	}
	if p.count("/mojeek") != 0 {
		t.Error("fallback provider was queried after cancellation")
	}
}

func TestSearchCancellationDuringBackoff(t *testing.T) {
	p := &providers{ddg: status(http.StatusTooManyRequests, "Retry-After", "4"), mojeek: html(mojeekFixture)}
	srv := p.start(t)
	s := newTestService(srv, "", nil) // real sleep
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	_, _, attempts, toolErr := s.Search(ctx, "q", 5)
	if time.Since(start) > 2*time.Second || toolErr == nil || toolErr.Code != "canceled" {
		t.Errorf("took %v, err=%+v attempts=%+v", time.Since(start), toolErr, attempts)
	}
}

func TestSearchProviderTimeoutFallsBack(t *testing.T) {
	p := &providers{
		ddg:    func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() },
		mojeek: html(mojeekFixture),
	}
	srv := p.start(t)
	s := newTestService(srv, "", nil)
	s.cfg.AttemptTimeout = 100 * time.Millisecond
	_, source, attempts, toolErr := s.Search(context.Background(), "q", 5)
	if toolErr != nil || source != "mojeek" || outcomes(attempts) != "ddg=timeout,mojeek=ok" {
		t.Errorf("source=%s attempts=%+v err=%+v", source, attempts, toolErr)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := map[string]time.Duration{
		"":                              0,
		"7":                             7 * time.Second,
		"-1":                            0,
		"soon":                          0,
		"Thu, 01 Jan 2026 00:00:30 GMT": 30 * time.Second,
		"Wed, 31 Dec 2025 23:00:00 GMT": 0,
	}
	for in, want := range cases {
		if got := parseRetryAfter(in, now); got != want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", in, got, want)
		}
	}
}
