package llm

import (
	"context"
	"errors"
	"testing"
)

// A declared single-attempt diagnostic must not silently send a second paid
// request after a transient error. Zero retains the ordinary client's default.
func TestNegativeMaxRetriesMakesOneAttempt(t *testing.T) {
	calls := 0
	failure := &RateLimitError{}
	_, err := retryComplete(context.Background(), "test", -1, func(context.Context) (*CompleteResponse, error) { calls++; return nil, failure })
	if calls != 1 || !errors.Is(err, failure) {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
