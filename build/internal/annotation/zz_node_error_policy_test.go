package annotation

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"testing"
)

// Node recovery must share the existing policy, including classifications already
// wrapped by the semantic gate, instead of consuming recoveries on permanent config.
func TestNodePermanentErrorPolicyReusesAnnotationClassification(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"wrapped permanent", fmt.Errorf("gate: %w", permanentError(errors.New("configuration"))), true},
		{"preserve unavailable", fmt.Errorf("gate: %w", unavailableError(errors.New("openai_compatible: status 401: body"))), false},
		{"provider 401", errors.New("openai_compatible: status 401: rejected"), true},
		{"provider 503 misleading body", errors.New("openai_compatible: status 503: missing API key"), false},
		{"provider 429", errors.New("anthropic: status 429: rate limited"), false},
		{"local protocol", &url.Error{Op: "Post", URL: "bad://provider", Err: errors.New(`unsupported protocol scheme "bad"`)}, true},
		{"provider local timeout", &url.Error{Op: "Post", URL: "https://provider", Err: context.DeadlineExceeded}, false},
		{"caller cancellation separate", context.Canceled, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsPermanentModelError(tc.err); got != tc.want {
				t.Fatalf("permanent=%v, want %v", got, tc.want)
			}
		})
	}
}
