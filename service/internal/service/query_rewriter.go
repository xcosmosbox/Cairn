package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// ——————————————————————————————————————————————————————————————————————————————
// QueryRewriter — 查询改写器
// ——————————————————————————————————————————————————————————————————————————————

// QueryRewriter 对用户输入的原始查询做检索前改写，当前只做一件事：
// 把英文缩写替换为全称（如 "OM" → "OrderManagement"），然后按空白切分。
//
// 为什么不做中文分词——这是本项目一个重要的、容易被误改的设计约束：
//
//	nodes_fts 建表时未指定 tokenize，使用 FTS5 默认的 unicode61；
//	而索引侧是把 nodes 表字段原文直接 INSERT 进 nodes_fts（见
//	storage.FTSIndex.RebuildAll），没有任何分词预处理。
//	unicode61 会把「连续汉字」整段视为单个 token，因此索引里存在的
//	term 形如 "订单聚合根"、"负责订单退款流程的领域对象" 这样的整串。
//
//	在这种索引形态下，若查询侧擅自分词，切出的碎片（"订单"、"退款"）
//	在索引中根本不存在，FTS5 的多 token 查询又是 AND 语义，结果是
//	命中数从「整串精确匹配可命中」直接跌到零。也就是说：查询侧分词
//	在索引侧不分词的前提下是净负收益。
//
// 因此中文检索当前的有效路径是「整串精确匹配 + BFS 图扩展」：
// 命中一个入口节点后，由图遍历把周边子图带出来。节点的同义词召回
// 走的是另一条路——LLM 在提取阶段写入 node.synonyms，随 nodes_fts
// 的 synonyms 列进索引（索引侧扩展），无需查询侧维护全局同义词表。
//
// 中文片段实验必须同时修改索引与查询。可在独立副本上比较 trigram，或
// storage.FTSTextHanV1 的字符短语方案；后者必须匹配同样预处理过的索引。
// 此改写器仍不分词。生产默认索引和 TextProfile 都保持 literal。
//
// QueryRewriter rewrites the raw user query before retrieval. It currently
// performs abbreviation expansion only, then splits on whitespace.
//
// Deliberately no Chinese segmentation: nodes_fts uses FTS5's default
// unicode61 tokenizer and the index side inserts raw field text without any
// segmentation, so a run of Chinese characters becomes a single token.
// Segmenting on the query side would produce fragments absent from the index,
// and FTS5 multi-token queries are AND semantics — turning working exact
// matches into zero hits. Synonym recall is handled index-side instead, via
// the LLM-populated node.synonyms column.
type QueryRewriter struct {
	// abbreviations 是缩写到全称的映射表（如 "OM" → "OrderManagement"）。
	// abbreviations maps abbreviated forms to their full expansions.
	abbreviations map[string]string

	// abbrOrder 是按长度降序预排好的缩写键，保证最长匹配优先。
	// 在构造期算一次，避免每次查询都重排。
	// abbrOrder holds abbreviation keys pre-sorted by length descending so that
	// longest-match wins; computed once at construction time.
	abbrOrder []string
}

// NewQueryRewriter 创建查询改写器。
// abbr 为缩写映射表，传 nil 或空表示不做缩写扩展（改写退化为按空白切分）。
//
// NewQueryRewriter creates a query rewriter. Passing a nil or empty abbr map
// disables abbreviation expansion, reducing rewriting to whitespace splitting.
func NewQueryRewriter(abbr map[string]string) *QueryRewriter {
	qr := &QueryRewriter{abbreviations: abbr}
	if len(abbr) > 0 {
		qr.abbrOrder = make([]string, 0, len(abbr))
		for k := range abbr {
			qr.abbrOrder = append(qr.abbrOrder, k)
		}
		// 长度降序；等长时按字典序，保证改写结果稳定可复现。
		// Longest first; ties broken lexicographically for reproducibility.
		sort.Slice(qr.abbrOrder, func(i, j int) bool {
			if len(qr.abbrOrder[i]) != len(qr.abbrOrder[j]) {
				return len(qr.abbrOrder[i]) > len(qr.abbrOrder[j])
			}
			return qr.abbrOrder[i] < qr.abbrOrder[j]
		})
	}
	return qr
}

// Rewrite 执行改写：缩写扩展 → 按空白切分 → 以空格重新拼接。
// 返回普通文本；调用方必须通过 PrepareSearchQuery 编译为安全 MATCH。
//
// Rewrite expands abbreviations, then normalizes whitespace. The result is
// plain text, not an FTS5 MATCH expression. Use PrepareSearchQuery for MATCH.
func (qr *QueryRewriter) Rewrite(ctx context.Context, query string) (string, error) {
	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("query must not be empty")
	}

	// 提前检查 context，避免在调用方已取消后仍做无用功。
	// Check context first to avoid wasted work after cancellation.
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}

	expanded := qr.ExpandAbbreviations(query)
	return strings.Join(strings.Fields(expanded), " "), nil
}

// ExpandAbbreviations 将查询中的缩写替换为全称，按长度降序保证最长匹配优先。
//
// 关键：替换要求词边界匹配。早期实现用 strings.ReplaceAll 直接替换，会
// 命中单词内部——例如映射 "PM"→"Payment" 会把查询 "PMS" 误改成
// "PaymentS"，凭空制造一个索引里不存在的 token，使该查询永远零命中。
// 现在只在「左右两侧都不是字母数字」时替换，且缩写自身必须是纯 ASCII
// 字母数字（缩写表的既定形态），中文查询因此完全不受影响。
//
// ExpandAbbreviations replaces abbreviations with their full forms, longest
// match first. Replacement is word-boundary aware: a naive ReplaceAll would
// rewrite "PMS" into "PaymentS" under a "PM"→"Payment" mapping, fabricating a
// token absent from the index and guaranteeing zero hits.
func (qr *QueryRewriter) ExpandAbbreviations(query string) string {
	if len(qr.abbreviations) == 0 {
		return query
	}

	result := query
	for _, abbr := range qr.abbrOrder {
		if abbr == "" || !isASCIIAlnum(abbr) {
			// 非 ASCII 字母数字的键无法定义可靠的词边界，跳过而不是冒险替换。
			// Keys outside ASCII alphanumerics have no reliable word boundary.
			continue
		}
		result = replaceWholeWord(result, abbr, qr.abbreviations[abbr])
	}
	return result
}

// Tokenize 按空白切分查询。保留此方法是因为调用方（含 CLI 的调试输出）
// 依赖它观察改写结果；它不做中文分词，理由见 QueryRewriter 的文档注释。
//
// Tokenize splits the query on whitespace. It performs no Chinese
// segmentation — see the QueryRewriter doc comment for why.
func (qr *QueryRewriter) Tokenize(query string) []string {
	return strings.Fields(query)
}

// ——————————————————————————————————————————————————————————————————————————————
// 内部辅助 / Internal helpers
// ——————————————————————————————————————————————————————————————————————————————

// replaceWholeWord 把 s 中所有「独立成词」的 old 替换为 new。
// 独立成词的判定：出现位置的前一个与后一个字节均不是 ASCII 字母或数字。
//
// replaceWholeWord replaces every whole-word occurrence of old in s with new.
func replaceWholeWord(s, old, new string) string {
	if old == "" {
		return s
	}

	var b strings.Builder
	for i := 0; i < len(s); {
		// 从 i 起找下一处候选 / find next candidate at or after i
		idx := strings.Index(s[i:], old)
		if idx < 0 {
			b.WriteString(s[i:])
			break
		}
		start := i + idx
		end := start + len(old)

		// 左右边界检查：紧邻字符不得是 ASCII 字母数字。
		// Boundary check: adjacent bytes must not be ASCII alphanumerics.
		leftOK := start == 0 || !isASCIIAlnumByte(s[start-1])
		rightOK := end == len(s) || !isASCIIAlnumByte(s[end])

		b.WriteString(s[i:start])
		if leftOK && rightOK {
			b.WriteString(new)
		} else {
			b.WriteString(old)
		}
		i = end
	}
	return b.String()
}

// isASCIIAlnum 判断字符串是否全为 ASCII 字母或数字。
// isASCIIAlnum reports whether s consists solely of ASCII alphanumerics.
func isASCIIAlnum(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isASCIIAlnumByte(s[i]) {
			return false
		}
	}
	return true
}

// isASCIIAlnumByte 判断单字节是否为 ASCII 字母或数字。
// isASCIIAlnumByte reports whether b is an ASCII letter or digit.
func isASCIIAlnumByte(b byte) bool {
	return b < unicode.MaxASCII &&
		(b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z')
}
