package search

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RetryPolicy bounds how long one provider may be retried before the next
// provider takes over.
type RetryPolicy struct {
	// MaxTries is the total number of tries per provider (1 = no retry).
	MaxTries int
	// BaseBackoff doubles per retry, capped at MaxBackoff, when the provider
	// sends no Retry-After.
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
	// MaxRetryAfter is the longest Retry-After honoured; a longer one
	// reroutes immediately instead of stalling the search.
	MaxRetryAfter time.Duration
	// Reserve is time kept back after a wait so later providers still get
	// a chance before the total deadline.
	Reserve time.Duration
}

var DefaultRetryPolicy = RetryPolicy{
	MaxTries:      3,
	BaseBackoff:   500 * time.Millisecond,
	MaxBackoff:    4 * time.Second,
	MaxRetryAfter: 10 * time.Second,
	Reserve:       5 * time.Second,
}

// wait returns how long to sleep before retry number try+1, or ok=false
// when waiting would not fit the policy or the context's deadline.
func (p RetryPolicy) wait(ctx context.Context, try int, retryAfter time.Duration) (time.Duration, bool) {
	d := retryAfter
	if d <= 0 {
		d = p.BaseBackoff << (try - 1)
		if d > p.MaxBackoff || d <= 0 {
			d = p.MaxBackoff
		}
	} else if d > p.MaxRetryAfter {
		return 0, false
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline)-d < p.Reserve {
		return 0, false
	}
	return d, true
}

// parseRetryAfter reads a Retry-After header in either form (delay seconds
// or HTTP date); 0 means absent or unparseable.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
