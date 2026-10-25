// Package annotation 提供 reference 文档的标注与校验能力。
//
// 本文件定义「双门校验」阶段的共享回退信号。标注产出的 AnnotatedDocument 在进入
// 提取阶段前，必须依次通过两道门：
//
//	Gate A（FormatGate，纯代码）：去重 + 命名归一化 + 字段合法性校验。
//	Gate B（SemanticGate，LLM）：核验「原文 ↔ 标注出的 entity/concept」是否语义幂等。
//
// 任一门未通过，都不视为流水线失败，而是回退到标注阶段重新标注该文档（然后重新
// 依次走 Gate A → Gate B）。两道门通过统一的 ErrRollbackToAnnotate 信号表达「需要
// 回退重标注」，供上层状态机用 errors.Is 识别并执行回退。
//
// This file defines the shared rollback signal of the two-gate validation stage.
// Before entering extraction, an AnnotatedDocument must pass Gate A (FormatGate,
// pure code: dedup + name normalization + field validity) and Gate B (SemanticGate,
// LLM: original-vs-annotation semantic idempotency). A failure at either gate is
// not a pipeline failure; it rolls the document back to annotation. Both gates
// express "needs re-annotation" via ErrRollbackToAnnotate, which the state machine
// detects with errors.Is.
package annotation

import "errors"

// ErrRollbackToAnnotate 是双门校验的回退信号：校验未通过，需把该文档退回标注阶段
// 重新标注。它总是被包裹在具体的错误信息中（fmt.Errorf("...: %w", ErrRollbackToAnnotate)），
// 状态机用 errors.Is(err, ErrRollbackToAnnotate) 判定是否执行回退。
//
// ErrRollbackToAnnotate is the rollback signal of the two-gate validation: a check
// failed and the document must be re-annotated. It is always wrapped in a concrete
// error; the state machine detects it via errors.Is.
var ErrRollbackToAnnotate = errors.New("校验未通过，需回退到标注阶段重新标注")

// SemanticRejection 仅表示已成功解析的显式否定及其具体理由，不能用调用/解析错误
// 构造修订反馈。它仍保留原来的回退信号，供未使用修订的调用者识别。
// SemanticRejection carries a verified explicit negative verdict, not an operational error.
type SemanticRejection struct {
	Reason string
}

func (e *SemanticRejection) Error() string { return e.Reason }
func (e *SemanticRejection) Unwrap() error { return ErrRollbackToAnnotate }
