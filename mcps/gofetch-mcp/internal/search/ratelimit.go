package search

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// errRateLimited marks a backend response as a rate limit (HTTP 429) so the
// search loop can retry once and then reroute to the next backend.
var errRateLimited = errors.New("rate limited")

// rateLimitBackoff is how long to wait before the single retry of a
// rate-limited backend.
const rateLimitBackoff = 700 * time.Millisecond

func rateLimitError(backend string) error {
	return fmt.Errorf("%s: %w", backend, errRateLimited)
}

func isRateLimited(err error) bool {
	return errors.Is(err, errRateLimited)
}

// withRateLimitRetry runs fn; on a rate-limit error it backs off once and
// retries, then gives up so the caller reroutes to the next backend. Any
// other error is returned immediately.
func withRateLimitRetry(ctx context.Context, fn func() ([]Result, error)) ([]Result, error) {
	results, err := fn()
	if !isRateLimited(err) {
		return results, err
	}
	select {
	case <-time.After(rateLimitBackoff):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return fn()
}
