package service

import "fmt"

// ——————————————————————————————————————————————————————————————————————————————
// ServiceError — 服务层统一错误类型
// ——————————————————————————————————————————————————————————————————————————————

// ServiceError 是服务层所有错误的统一载体，包含错误码（Code）、面向用户的消息
// （Message）、面向开发者的详细信息（Detail）以及可选的底层错误（cause）。
//
// ServiceError is the unified error carrier for the entire service layer,
// containing an error code (Code), a user-facing message (Message),
// developer-facing details (Detail), and an optional underlying error (cause).
type ServiceError struct {
	// Code 是机器可读的错误码，便于客户端分类处理（如 "INVALID_QUERY"）。
	// Code is a machine-readable error code for client-side classification
	// (e.g., "INVALID_QUERY").
	Code string

	// Message 是面向用户的可读错误消息。
	// Message is a human-readable error message for end users.
	Message string

	// Detail 是面向开发者的详细错误上下文信息。
	// Detail is detailed error context information for developers.
	Detail string

	// cause 保存底层原始错误（如果有），不直接暴露给外部调用方。
	// cause holds the underlying original error (if any), not directly exposed
	// to external callers.
	cause error
}

// Error 实现 error 接口，返回格式化的错误信息：
// "[CODE] Message: Detail" 或（无 Detail 时）"[CODE] Message"。
//
// Error implements the error interface, returning a formatted error message
// in the form "[CODE] Message: Detail" or "[CODE] Message" when no Detail is set.
func (e *ServiceError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("[%s] %s: %s", e.Code, e.Message, e.Detail)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

// Unwrap 实现 errors.Unwrap 接口，使调用方可以通过 errors.Is / errors.As
// 检查底层错误。
//
// Unwrap implements the errors.Unwrap interface, enabling callers to inspect
// the underlying error via errors.Is / errors.As.
func (e *ServiceError) Unwrap() error {
	return e.cause
}

// WithDetail 创建一个新的 ServiceError，复制当前错误的 Code 和 Message，
// 但用给定的 detail 覆盖 Detail 字段。常用于在调用链上游补充更多上下文。
//
// WithDetail creates a new ServiceError by copying the Code and Message of
// the current error, but overwriting the Detail field with the given value.
// This is commonly used to add more context as the error propagates upstream.
func (e *ServiceError) WithDetail(detail string) *ServiceError {
	return &ServiceError{
		Code:    e.Code,
		Message: e.Message,
		Detail:  detail,
		cause:   e,
	}
}

// Wrap 创建一个新的 ServiceError，复制当前错误的 Code 和 Message，
// 并将给定的 cause 错误包装为底层错误。
//
// Wrap creates a new ServiceError by copying the Code and Message of the
// current error and wrapping the given cause as the underlying error.
func (e *ServiceError) Wrap(cause error) *ServiceError {
	return &ServiceError{
		Code:    e.Code,
		Message: e.Message,
		Detail:  cause.Error(),
		cause:   cause,
	}
}

// ——————————————————————————————————————————————————————————————————————————————
// 预定义的服务层错误实例
// ——————————————————————————————————————————————————————————————————————————————

var (
	// ErrInvalidQuery 表示用户提供的查询字符串为空或不合法。
	// ErrInvalidQuery indicates the user-provided query string is empty or invalid.
	ErrInvalidQuery = &ServiceError{
		Code:    "INVALID_QUERY",
		Message: "查询字符串不能为空",
	}

	// ErrInvalidDepth 表示查询请求中的 depth 参数不合法（非合法枚举值）。
	// ErrInvalidDepth indicates the depth parameter in the query request is
	// not a valid enum value.
	ErrInvalidDepth = &ServiceError{
		Code:    "INVALID_DEPTH",
		Message: "depth 参数不合法",
	}

	// ErrInvalidMinConfidence 表示 min_confidence 不在 0.0 到 1.0 的合法范围内。
	// ErrInvalidMinConfidence indicates min_confidence is outside the valid
	// range of 0.0 to 1.0.
	ErrInvalidMinConfidence = &ServiceError{
		Code:    "INVALID_CONFIDENCE",
		Message: "min_confidence 必须在 0.0-1.0 之间",
	}

	// ErrDomainNotFound 表示查询中限制的 domain 在知识库中不存在。
	// ErrDomainNotFound indicates the domain specified in the query scope
	// does not exist in the knowledge base.
	ErrDomainNotFound = &ServiceError{
		Code:    "DOMAIN_NOT_FOUND",
		Message: "指定的 domain 不存在",
	}

	// ErrKGNotReady 表示知识库尚未初始化或数据未加载，无法响应查询。
	// ErrKGNotReady indicates the knowledge base has not been initialized
	// or data has not been loaded yet, so queries cannot be served.
	ErrKGNotReady = &ServiceError{
		Code:    "KG_NOT_READY",
		Message: "知识库尚未初始化，请先执行数据入库",
	}

	// ErrInternal 表示服务层内部发生不可预期的错误。
	// ErrInternal indicates an unexpected internal error occurred in the service layer.
	ErrInternal = &ServiceError{
		Code:    "INTERNAL_ERROR",
		Message: "内部服务错误",
	}
)
