package llm

import (
	"context"
	"sync"
)

// MockClient 是用于测试的 LLM Client mock 实现。
// 线程安全，记录所有请求供测试断言。
type MockClient struct {
	mu       sync.Mutex
	Requests []CompleteRequest
	Response *CompleteResponse
	Err      error
}

func NewMockClient() *MockClient {
	return &MockClient{Requests: make([]CompleteRequest, 0)}
}

func (m *MockClient) ProviderName() string { return "mock" }

func (m *MockClient) Complete(ctx context.Context, req CompleteRequest) (*CompleteResponse, error) {
	m.mu.Lock()
	m.Requests = append(m.Requests, req)
	m.mu.Unlock()
	if m.Err != nil { return nil, m.Err }
	if m.Response != nil { return m.Response, nil }
	return &CompleteResponse{Text: "mock response", FinishReason: "stop"}, nil
}
