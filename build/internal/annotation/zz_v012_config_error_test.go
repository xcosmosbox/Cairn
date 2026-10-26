package annotation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/llm"
)

// 生产 HTTP 客户端会在联网前拒绝这些端点；原先却重复标注直至耗尽恢复次数。
// Local endpoint rejection must stop once, preserving the actual configuration reason.
func TestV012ConfigErrorProductionEndpointStopsOnce(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		endpoint, reason string
	}{
		{"ftp://invalid.invalid/chat/completions", `unsupported protocol scheme "ftp"`},
		{"api.invalid/chat/completions", `unsupported protocol scheme ""`},
		{"http://", "http: no Host in request URL"},
	} {
		t.Run(tc.endpoint, func(t *testing.T) {
			t.Parallel()
			client, err := llm.NewOpenAICompatClient(llm.Config{Endpoint: tc.endpoint, MaxRetries: -1})
			if err != nil {
				t.Fatal(err)
			}
			result := NewDocumentProcessor(client, 0, 2, 0).Process(context.Background(), "s", "p", "one fact")
			if result.Outcome != DocumentUnavailable || result.Exhausted || len(result.Attempts) != 1 {
				t.Fatalf("known local configuration error retried: %+v", result)
			}
			attempt := result.Attempts[0]
			if attempt.Number != 1 || attempt.Step != "annotate" || attempt.Outcome != "permanent" || attempt.CandidateID != "" || !strings.Contains(attempt.Reason, tc.reason) || result.Reason != attempt.Reason {
				t.Fatalf("configuration attempt lost its classification or reason: %+v", result)
			}
		})
	}
}

// 只有明确的本地请求错误形状可终止；URL 包装、正文中的配置字样都不足以判为永久错误。
// Restrict the classifier to known local causes, retaining the original error chain.
func TestV012ConfigErrorClassificationIsNarrow(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, want string
		err        error
	}{
		{"wrapped unsupported scheme", "permanent", fmt.Errorf("openai_compatible: http do: %w", &url.Error{Op: "Post", URL: "ftp://fixture.invalid", Err: errors.New(`unsupported protocol scheme "ftp"`)})},
		{"wrapped missing host", "permanent", fmt.Errorf("anthropic: %w", &url.Error{Op: "Post", URL: "http://", Err: errors.New("http: no Host in request URL")})},
		{"transient url error", "unavailable", fmt.Errorf("openai_compatible: http do: %w", &url.Error{Op: "Post", URL: "https://fixture.invalid", Err: io.ErrUnexpectedEOF})},
		{"provider timeout", "unavailable", fmt.Errorf("openai_compatible: timeout: %w", &url.Error{Op: "Post", URL: "https://fixture.invalid", Err: context.DeadlineExceeded})},
		{"plain configuration phrase", "unavailable", errors.New(`upstream unavailable: unsupported protocol scheme "ftp"`)},
		{"503 body", "unavailable", errors.New(`openai_compatible: status 503: unsupported protocol scheme "ftp"; missing API key`)},
		{"429 body", "unavailable", errors.New("openai_compatible: status 429: http: no Host in request URL; max_tokens subsystem")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := modelError(tc.err)
			var classified *processingError
			if !errors.As(got, &classified) || classified.kind != tc.want || !errors.Is(got, tc.err) || got.Error() != tc.err.Error() {
				t.Fatalf("classification=%v, want kind=%q and original error", got, tc.want)
			}
		})
	}
}
