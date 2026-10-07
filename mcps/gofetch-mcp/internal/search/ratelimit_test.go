package search

import (
	"context"
	"errors"
	"testing"
)

func TestWithRateLimitRetry(t *testing.T) {
	ctx := context.Background()

	calls := 0
	results, err := withRateLimitRetry(ctx, func() ([]Result, error) {
		calls++
		if calls == 1 {
			return nil, rateLimitError("ddg")
		}
		return []Result{{Title: "ok", URL: "https://x"}}, nil
	})
	if err != nil || len(results) != 1 || calls != 2 {
		t.Errorf("retry after rate limit failed: calls=%d err=%v results=%+v", calls, err, results)
	}

	calls = 0
	_, err = withRateLimitRetry(ctx, func() ([]Result, error) {
		calls++
		return nil, rateLimitError("exa")
	})
	if !isRateLimited(err) || calls != 2 {
		t.Errorf("persistent rate limit: calls=%d err=%v (want 2 calls, rate-limited error)", calls, err)
	}

	calls = 0
	_, err = withRateLimitRetry(ctx, func() ([]Result, error) {
		calls++
		return nil, errors.New("hard failure")
	})
	if calls != 1 || err == nil || isRateLimited(err) {
		t.Errorf("non-rate-limit error should not retry: calls=%d err=%v", calls, err)
	}
}
