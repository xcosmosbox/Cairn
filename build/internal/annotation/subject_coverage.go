package annotation

import (
	"encoding/json"
	"fmt"
	"strings"
)

// This is evidence returned in the existing whole-document assessment call.
// Anchoring and typed-result validation do not prove the model's semantic claims.
const subjectCoverageSystemPrompt = `
本次由调用方指定为整篇标注核验，即使 items 只有一项也不是单节点核验。整篇正判的返回合同必须额外含 subject_coverage，不得只返回通用 supported=true。仍在本次核验中完成，不增加调用。
subject_coverage 为数组，每行对应原文对一个可指称主体的直接断言：{"source_quote":"原文逐字存在的断言及必要指代/条件上下文","subject_referent":"该直接断言的主体所指","checks":[{"item_index":0,"item_referent":"该候选单元实际指称的对象或命题","same_referent":true,"preserves_assertion":true,"reason":"简短核验结果依据"}]}。
覆盖原文全部直接主体断言；一句含多主体、同一主体含多断言可以分别列行，相同引文不表示重复。checks 只列该行相关候选，不生成所有主体乘全部 items 的矩阵；没有相关候选可以为空。same_referent 核对所指身份，不按名称含词或相等猜测；完整命题P含主体X的名称不等于以X为所指。preserves_assertion 核对该行主体的直接断言及全部限定，不要求无关上下文事实也进入该单元。reason 只给简短结果依据，不输出隐含思维过程。
只有 same_referent=true 且 preserves_assertion=true 的 check 才承载该主体断言。只有命题P或另一端时可如实 same_referent=false，即使它保存了整句；它们仍可合法，不因此列为 unsupported 或删除。没有承载项时 supported=false，并在 omissions 写明应补主体、原文断言及限定，同时保留原有合法单元。same=true 但未保留断言也属于承载遗漏。
没有可明确指称的直接主体断言（如仅标题）时可 subject_coverage=[]，同时必须提供非空 no_direct_subjects_reason 说明真实原因；非空 subject_coverage 不带该理由。来源含混或仅讨论命题仍按原文真实范围核对，不为填写表格猜主体。每个索引须在 items 内，布尔值须显式给出，所有所指、引文及 reason 非空；引文须逐字存在于原文。
完成核验的正例结构：{"supported":true,"reason":"全部三项必要条件满足","unsupported_items":[],"omissions":[],"subject_coverage":[{"source_quote":"原文断言","subject_referent":"原文主体","checks":[{"item_index":0,"item_referent":"该主体","same_referent":true,"preserves_assertion":true,"reason":"同指且保留断言限定"}]}]}。上述引文和所指仅示结构，不能照抄。
已有明确否定或非空 omissions 可以直接返回具体反馈；若提供 subject_coverage，仍必须完整符合该结构。无法核验仍只返回 {"status":"unknown","reason":"具体原因"} 两字段，不夹带证据或正确性字段。`

func coverageString(fields map[string]json.RawMessage, key string) (string, error) {
	var value string
	if err := json.Unmarshal(fields[key], &value); err != nil || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("missing/empty %s", key)
	}
	return value, nil
}

func coverageArray(raw json.RawMessage, field string) ([]json.RawMessage, error) {
	if len(strings.TrimSpace(string(raw))) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return nil, fmt.Errorf("missing/null %s", field)
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("invalid %s array: %w", field, err)
	}
	return rows, nil
}

func coverageBool(fields map[string]json.RawMessage, key string) (bool, error) {
	raw, ok := fields[key]
	if !ok || strings.TrimSpace(string(raw)) == "null" {
		return false, fmt.Errorf("missing/null %s", key)
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, fmt.Errorf("invalid %s boolean", key)
	}
	return value, nil
}

func validateSubjectCoverage(v *semanticVerdict, original string, itemCount int) ([]string, error) {
	if len(v.subjectCoverage) == 0 {
		if len(v.noSubjectsReason) != 0 {
			return nil, fmt.Errorf("no_direct_subjects_reason without subject_coverage")
		}
		// Preserve known semantic defects; missing positive evidence cannot wash
		// an explicit rejection/omission into an unknown assessment.
		if v.explicitReject || len(v.Omissions) > 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("whole-document positive requires subject_coverage")
	}
	rows, err := coverageArray(v.subjectCoverage, "subject_coverage")
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		var reason string
		if err := json.Unmarshal(v.noSubjectsReason, &reason); err != nil || strings.TrimSpace(reason) == "" {
			return nil, fmt.Errorf("empty subject_coverage requires no_direct_subjects_reason")
		}
		return nil, nil
	}
	if len(v.noSubjectsReason) != 0 {
		return nil, fmt.Errorf("nonempty subject_coverage conflicts with no_direct_subjects_reason")
	}
	var omissions []string
	for rowIndex, raw := range rows {
		fields, err := decodeUniqueObject(raw)
		if err != nil {
			return nil, fmt.Errorf("row %d: %w", rowIndex, err)
		}
		if len(fields) != 3 {
			return nil, fmt.Errorf("row %d: expected source_quote/subject_referent/checks", rowIndex)
		}
		quote, err := coverageString(fields, "source_quote")
		if err != nil {
			return nil, err
		}
		if !strings.Contains(original, quote) {
			return nil, fmt.Errorf("row %d quote not in original", rowIndex)
		}
		subject, err := coverageString(fields, "subject_referent")
		if err != nil {
			return nil, err
		}
		checks, err := coverageArray(fields["checks"], "checks")
		if err != nil {
			return nil, err
		}
		carrier := false
		seen := make(map[int]bool)
		var compared []string
		for checkIndex, check := range checks {
			f, err := decodeUniqueObject(check)
			if err != nil {
				return nil, fmt.Errorf("row %d check %d: %w", rowIndex, checkIndex, err)
			}
			if len(f) != 5 {
				return nil, fmt.Errorf("row %d check %d: expected five result fields", rowIndex, checkIndex)
			}
			var index *int
			if err := json.Unmarshal(f["item_index"], &index); err != nil || index == nil || *index < 0 || *index >= itemCount {
				return nil, fmt.Errorf("row %d invalid item_index", rowIndex)
			}
			if seen[*index] {
				return nil, fmt.Errorf("row %d duplicate item_index %d", rowIndex, *index)
			}
			seen[*index] = true
			item, err := coverageString(f, "item_referent")
			if err != nil {
				return nil, err
			}
			reason, err := coverageString(f, "reason")
			if err != nil {
				return nil, err
			}
			same, err := coverageBool(f, "same_referent")
			if err != nil {
				return nil, err
			}
			preserves, err := coverageBool(f, "preserves_assertion")
			if err != nil {
				return nil, err
			}
			carrier = carrier || same && preserves
			compared = append(compared, fmt.Sprintf("item[%d]所指=%q same=%t preserves=%t：%s", *index, item, same, preserves, reason))
		}
		if !carrier {
			omissions = append(omissions, fmt.Sprintf("主体 %q 的原文断言 %q 及其限定缺少同指单元承载；相关候选结果=[%s]。补回该主体的事实，同时保留已有合法命题和其他参与者单元。", subject, quote, strings.Join(compared, "; ")))
		}
	}
	return omissions, nil
}
