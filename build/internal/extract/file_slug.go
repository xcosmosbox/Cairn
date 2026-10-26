// Deterministic filenames never require a model request.
package extract

import (
	"context"
	"fmt"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"regexp"
	"strings"
)

// GenerateFileSlugs preserves assigned paths and allocates unique deterministic
// names for new nodes. Same-name nodes must never overwrite each other's files.
func GenerateFileSlugs(_ context.Context, domains []Domain, _ llm.Client) {
	seen := map[string]bool{}
	for di := range domains {
		for si := range domains[di].Subdomains {
			sd := &domains[di].Subdomains[si]
			for _, nodes := range [][]Node{sd.Entities, sd.Concepts} {
				for i := range nodes {
					if nodes[i].FileSlug != "" {
						seen[nodes[i].FileSlug] = true
					}
				}
			}
		}
	}
	for di := range domains {
		for si := range domains[di].Subdomains {
			sd := &domains[di].Subdomains[si]
			for _, nodes := range [][]Node{sd.Entities, sd.Concepts} {
				for i := range nodes {
					if nodes[i].FileSlug != "" {
						continue
					}
					base := fallbackSlugify(nodes[i].Name)
					slug := base
					for n := 2; seen[slug]; n++ {
						slug = fmt.Sprintf("%s-%d", base, n)
					}
					seen[slug] = true
					nodes[i].FileSlug = slug
				}
			}
		}
	}
}

// generateFileSlugsFallback 无 LLM 时用确定性 slugify。
func generateFileSlugsFallback(domains []Domain) {
	for di := range domains {
		for si := range domains[di].Subdomains {
			sd := &domains[di].Subdomains[si]
			for ni := range sd.Entities {
				if sd.Entities[ni].FileSlug == "" {
					sd.Entities[ni].FileSlug = fallbackSlugify(sd.Entities[ni].Name)
				}
			}
			for ni := range sd.Concepts {
				if sd.Concepts[ni].FileSlug == "" {
					sd.Concepts[ni].FileSlug = fallbackSlugify(sd.Concepts[ni].Name)
				}
			}
		}
	}
}

// sanitizeSlug 清理 LLM 输出为合法 slug。
func sanitizeSlug(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ToLower(s)
	// 去掉非 [a-z0-9-] 字符。
	reg := regexp.MustCompile(`[^a-z0-9-]+`)
	s = reg.ReplaceAllString(s, "-")
	// 去掉开头/结尾的连字符。
	s = strings.Trim(s, "-")
	// 压缩连续连字符。
	reg2 := regexp.MustCompile(`-+`)
	s = reg2.ReplaceAllString(s, "-")
	if len(s) > 40 {
		s = s[:40]
		s = strings.Trim(s, "-")
	}
	return s
}

// fallbackSlugify 确定性 slugify（无 LLM 时用）。
// 中文直接用 ASCII 音译近似（简化版：取 name 的 hash 或直接转 ASCII）。
func fallbackSlugify(name string) string {
	// 尝试直接 sanitize（对英文名有效）。
	slug := sanitizeSlug(name)
	if slug != "" {
		return slug
	}
	// 纯中文名：用 name 的 hash 作 slug（保证确定性 + 唯一）。
	return fmt.Sprintf("node-%x", hashString(name))
}

func hashString(s string) uint32 {
	h := uint32(2166136261)
	for _, c := range s {
		h ^= uint32(c)
		h *= 16777619
	}
	return h
}
