package service

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/xcosmosbox/cairn/core/dktypes"
)

// ——————————————————————————————————————————————————————————————————————————————
// QueryDomainKnowledgeHandler — 领域知识查询处理器
// ——————————————————————————————————————————————————————————————————————————————

// QueryDomainKnowledgeHandler 负责处理 "查询领域知识" 的请求，
// 它是整个服务层对外的主要入口。处理流程包括：
//   1. 请求校验（validateRequest）
//   2. 应用默认值（applyDefaults）
//   3. 执行检索管线（SearchPipeline.Execute）
//   4. 构建查询响应（ResponseBuilder.Build）
//
// QueryDomainKnowledgeHandler is responsible for handling "query domain knowledge"
// requests. It serves as the main external entry point for the entire service layer.
// The processing flow includes:
//   1. Request validation (validateRequest)
//   2. Apply defaults (applyDefaults)
//   3. Execute retrieval pipeline (SearchPipeline.Execute)
//   4. Build query response (ResponseBuilder.Build)
type QueryDomainKnowledgeHandler struct {
	// pipeline 是检索管线，负责查询改写、FTS5 搜索、BFS 遍历和打分排序。
	// pipeline is the retrieval pipeline, responsible for query rewriting,
	// FTS5 search, BFS traversal, and scoring/ranking.
	pipeline *SearchPipeline

	// builder 是响应构建器，负责将检索结果按不同深度组装为 QueryResponse。
	// builder is the response builder, responsible for assembling retrieval
	// results into QueryResponse at different depth levels.
	builder *ResponseBuilder
}

// NewQueryDomainKnowledgeHandler 创建领域知识查询处理器实例。
// pipeline 是已初始化的检索管线；builder 是已初始化的响应构建器。
//
// NewQueryDomainKnowledgeHandler creates a new query-domain-knowledge handler instance.
// pipeline is an initialized search pipeline; builder is an initialized response builder.
func NewQueryDomainKnowledgeHandler(
	pipeline *SearchPipeline,
	builder *ResponseBuilder,
) *QueryDomainKnowledgeHandler {
	return &QueryDomainKnowledgeHandler{
		pipeline: pipeline,
		builder:  builder,
	}
}

// Handle 处理领域知识查询请求，执行完整的校验-搜索-构建链路。
// 返回统一的 QueryResponse，其中 Results 字段的具体类型取决于 depth 参数。
//
// Handle processes a domain knowledge query request, executing the complete
// validate-search-build chain. Returns a unified QueryResponse whose Results
// field type depends on the depth parameter.
func (h *QueryDomainKnowledgeHandler) Handle(
	ctx context.Context,
	req *dktypes.QueryRequest,
) (*dktypes.QueryResponse, error) {
	// 1. 请求校验
	// 1. Validate request
	if err := h.validateRequest(req); err != nil {
		return nil, err
	}

	// 2. 应用默认值
	// 2. Apply defaults
	h.applyDefaults(req)

	log.Printf("[query-handler] processing query: %q (depth=%s, tokens=%d, confidence=%.2f)",
		req.Query, req.Depth, req.MaxTokens, req.MinConfidence)

	// 3. 执行检索管线
	// 3. Execute retrieval pipeline
	sc, err := h.pipeline.Execute(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("search pipeline execution failed: %w", err)
	}

	// 4. 构建查询响应
	// 4. Build query response
	resp, err := h.builder.Build(sc, req.Depth, req.MaxTokens)
	if err != nil {
		return nil, fmt.Errorf("response building failed: %w", err)
	}

	log.Printf("[query-handler] query complete: %d matches in %dms (tokens=%d)",
		resp.Meta.TotalMatches, resp.Meta.QueryMs, resp.Meta.TokensUsed)

	return resp, nil
}

// ——————————————————————————————————————————————————————————————————————————————
// validateRequest — 请求校验
// ——————————————————————————————————————————————————————————————————————————————

// validateRequest 校验 QueryRequest 中各项参数的合法性。
// 校验规则：
//   - Query 不能为空字符串或全空白字符
//   - Depth 如果指定则必须是合法枚举值
//   - MinConfidence 必须在 [0.0, 1.0] 范围内
//   - MaxTokens 如果为负数则视为非法（0 表示无限制，合法）
//
// validateRequest validates the legality of parameters in the QueryRequest.
// Validation rules:
//   - Query must not be an empty string or all whitespace
//   - Depth, if specified, must be a valid enum value
//   - MinConfidence must be within [0.0, 1.0]
//   - MaxTokens, if negative, is considered invalid (0 means unlimited, valid)
func (h *QueryDomainKnowledgeHandler) validateRequest(req *dktypes.QueryRequest) error {
	if req == nil {
		return ErrInvalidQuery.WithDetail("request must not be nil")
	}

	// 校验查询字符串
	// Validate query string
	if strings.TrimSpace(req.Query) == "" {
		return ErrInvalidQuery.WithDetail("query string is empty or contains only whitespace")
	}

	// 校验 depth（如果指定了值）
	// Validate depth (if a value was specified)
	if req.Depth != "" && !req.Depth.IsValid() {
		return ErrInvalidDepth.WithDetail(
			fmt.Sprintf("depth %q is not a valid depth level (expected: summary, entity, neighborhood, subgraph)", req.Depth),
		)
	}

	// 校验置信度范围
	// Validate confidence range
	if req.MinConfidence < 0.0 || req.MinConfidence > 1.0 {
		return ErrInvalidMinConfidence.WithDetail(
			fmt.Sprintf("min_confidence %.2f is outside valid range [0.0, 1.0]", req.MinConfidence),
		)
	}

	// 校验 MaxTokens
	// Validate MaxTokens
	if req.MaxTokens < 0 {
		return &ServiceError{
			Code:    "INVALID_MAX_TOKENS",
			Message: "max_tokens 不能为负数",
			Detail:  fmt.Sprintf("max_tokens=%d is negative", req.MaxTokens),
		}
	}

	return nil
}

// ——————————————————————————————————————————————————————————————————————————————
// applyDefaults — 应用默认值
// ——————————————————————————————————————————————————————————————————————————————

// applyDefaults 为未显式设置的请求参数填充默认值。
// 默认值来源为管线中绑定的 ServiceConfig：
//   - Depth 默认为 ServiceConfig.DefaultDepth
//   - MaxTokens 默认为 ServiceConfig.DefaultMaxTokens（仅在未设置时）
//   - MinConfidence 默认为 ServiceConfig.DefaultMinConfidence（仅在为 0 时）
//   - Scope 如果非空，则保持用户指定值
//
// applyDefaults fills in default values for request parameters that were not
// explicitly set. Defaults are sourced from the ServiceConfig bound to the pipeline:
//   - Depth defaults to ServiceConfig.DefaultDepth
//   - MaxTokens defaults to ServiceConfig.DefaultMaxTokens (only if not set)
//   - MinConfidence defaults to ServiceConfig.DefaultMinConfidence (only if 0)
//   - Scope is kept as-is if non-empty
func (h *QueryDomainKnowledgeHandler) applyDefaults(req *dktypes.QueryRequest) {
	cfg := h.pipeline.config

	// 应用默认深度
	// Apply default depth
	if req.Depth == "" {
		req.Depth = cfg.DefaultDepth
	}

	// 应用默认最大 token 数
	// Apply default max tokens
	if req.MaxTokens == 0 {
		req.MaxTokens = cfg.DefaultMaxTokens
	}

	// 应用默认最低置信度（如果未设置或为 0）
	// Apply default minimum confidence (if not set or zero)
	if req.MinConfidence == 0.0 {
		req.MinConfidence = cfg.DefaultMinConfidence
	}
}
