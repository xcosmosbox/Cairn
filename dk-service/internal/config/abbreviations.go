package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// LoadAbbreviations 加载缩写映射表（abbreviations.yaml）。
// 返回 map[abbreviation]expansion，例如: "OM" → "OrderManagement"。
// 查询改写阶段用它把用户输入的缩写展开为全称，提升 FTS5 召回。
//
// 注意：只有 ASCII 字母数字构成的缩写会被实际应用（见
// service.QueryRewriter.ExpandAbbreviations），因为只有它们能可靠地
// 判定词边界；其他形态的键会被安全跳过。
//
// LoadAbbreviations loads the abbreviation map (abbreviations.yaml), returning
// map[abbreviation]expansion. Only ASCII-alphanumeric keys are actually applied
// during query rewriting, since only those have reliable word boundaries.
func LoadAbbreviations(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read abbreviations file %s: %w", path, err)
	}
	var result map[string]string
	if err := yaml.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("parse abbreviations: %w", err)
	}
	return result, nil
}
