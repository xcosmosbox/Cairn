package annotation

import "errors"

// IsPermanentModelError 让节点 helper 复用标注阶段的永久错误判定，避免重试策略分叉。
// Existing classified errors retain their kind; callers handle their own context cancellation.
func IsPermanentModelError(err error) bool {
	if err == nil {
		return false
	}
	var classified *processingError
	if errors.As(err, &classified) {
		return classified.kind == "permanent"
	}
	return errors.As(modelError(err), &classified) && classified.kind == "permanent"
}
