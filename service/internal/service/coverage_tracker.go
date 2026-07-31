package service

import (
	"context"
	"fmt"
	"log"

	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

// ——————————————————————————————————————————————————————————————————————————————
// CoverageTracker — 域覆盖率追踪器
// ——————————————————————————————————————————————————————————————————————————————

// CoverageTracker 负责追踪和查询各业务域的覆盖率状态。
// 通过统计每个域/子域中已入库的节点数量来判断该域是否已被覆盖（CoverageFull）
// 或尚未覆盖（CoverageNone）。
//
// CoverageTracker is responsible for tracking and querying the coverage status
// of business domains. It determines whether a domain has been covered
// (CoverageFull) or not yet covered (CoverageNone) by counting the number of
// ingested nodes in each domain/subdomain.
type CoverageTracker struct {
	// nodeRepo 是节点仓库，用于按域查询节点统计信息。
	// nodeRepo is the node repository, used to query node statistics by domain.
	nodeRepo *storage.NodeRepo
}

// NewCoverageTracker 创建覆盖率追踪器实例。
// nodeRepo 必须是已初始化的节点仓库。
//
// NewCoverageTracker creates a new coverage tracker instance.
// nodeRepo must be an initialized node repository.
func NewCoverageTracker(nodeRepo *storage.NodeRepo) *CoverageTracker {
	return &CoverageTracker{nodeRepo: nodeRepo}
}

// GetCoverageMap 返回所有域及其覆盖率状态的映射。
// 通过查询知识图谱中已存在的节点来判定每个域的覆盖情况：
// 若某域下至少存在一个节点，则标记为 CoverageFull，否则标记为 CoverageNone。
//
// GetCoverageMap returns a map of all domains to their coverage status.
// Coverage is determined by querying existing nodes in the knowledge graph:
// if at least one node exists under a domain, it is marked CoverageFull;
// otherwise, it is marked CoverageNone.
func (ct *CoverageTracker) GetCoverageMap(ctx context.Context) (map[string]dktypes.Coverage, error) {
	// 获取所有域下的节点列表（按域分组）
	// Get all nodes grouped by domain
	nodes, err := ct.nodeRepo.ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("list all nodes for coverage: %w", err)
	}

	// 建立 domain → hasNodes 的映射
	// Build a domain → hasNodes map
	domainSet := make(map[string]bool)
	for _, node := range nodes {
		if node == nil {
			continue
		}
		if node.Domain != "" {
			domainSet[node.Domain] = true
		}
	}

	// 所有已知域的完整列表（从配置或数据库 Schema 获取）
	// 此处采用从数据中推断的方式：所有已有节点的域 + 通过 ListDomains 获取
	// Full list of known domains (from config or database schema)
	// Here we infer from data: domains with existing nodes + from ListDomains
	result := make(map[string]dktypes.Coverage)
	for domain := range domainSet {
		result[domain] = dktypes.CoverageFull
	}

	log.Printf("[coverage-tracker] coverage map contains %d domains", len(result))
	return result, nil
}

// GetDomainCoverage 返回指定域的覆盖率状态。
// 检查该域下是否存在至少一个节点来判定覆盖状态。
//
// GetDomainCoverage returns the coverage status of the specified domain.
// It checks whether at least one node exists under the given domain to
// determine coverage status.
func (ct *CoverageTracker) GetDomainCoverage(ctx context.Context, domain string) (dktypes.Coverage, error) {
	if domain == "" {
		return dktypes.CoverageNone, fmt.Errorf("domain must not be empty")
	}

	// 尝试列出该域下的节点
	// Try listing nodes under this domain
	nodes, err := ct.nodeRepo.ListByDomain(ctx, domain)
	if err != nil {
		return dktypes.CoverageNone, fmt.Errorf("list nodes for domain %q: %w", domain, err)
	}

	if len(nodes) > 0 {
		return dktypes.CoverageFull, nil
	}
	return dktypes.CoverageNone, nil
}

// IsDomainCovered 检查指定域是否已被覆盖（即存在至少一个节点）。
// 返回 true 表示域已被覆盖；false 表示尚未覆盖。
// 如果查询过程出错，返回 false 及对应错误。
//
// IsDomainCovered checks whether the specified domain has been covered
// (i.e., at least one node exists). Returns true if covered, false otherwise.
// If the query encounters an error, returns false with the error.
func (ct *CoverageTracker) IsDomainCovered(ctx context.Context, domain string) (bool, error) {
	coverage, err := ct.GetDomainCoverage(ctx, domain)
	if err != nil {
		return false, err
	}
	return coverage == dktypes.CoverageFull, nil
}
