package service

import (
	"context"
	"fmt"
	"log"

	"github.com/xcosmosbox/domain-knowledge-layer/core/dktypes"
	"github.com/xcosmosbox/domain-knowledge-layer/core/storage"
)

// ——————————————————————————————————————————————————————————————————————————————
// ListDomainsHandler — 域列表处理器
// ——————————————————————————————————————————————————————————————————————————————

// ListDomainsHandler 负责处理 "列出所有域" 的查询请求。
// 它从知识图谱的节点数据中提取域及子域的统计信息，结合覆盖率状态，
// 构造 ListDomainsResponse 返回给调用方。
//
// ListDomainsHandler is responsible for handling "list all domains" requests.
// It extracts domain and subdomain statistics from the knowledge graph's node
// data, combines them with coverage status, and constructs a ListDomainsResponse
// to return to the caller.
type ListDomainsHandler struct {
	// nodeRepo 是节点仓库，用于按域查询节点。
	// nodeRepo is the node repository, used to query nodes by domain.
	nodeRepo *storage.NodeRepo

	// coverageTracker 是覆盖率追踪器，用于获取各域的覆盖状态。
	// coverageTracker is the coverage tracker, used to get coverage status
	// for each domain.
	coverageTracker *CoverageTracker
}

// NewListDomainsHandler 创建域列表处理器实例。
// nodeRepo 是节点仓库；ct 是覆盖率追踪器，用于补充各域的覆盖状态信息。
//
// NewListDomainsHandler creates a new list-domains handler instance.
// nodeRepo is the node repository; ct is the coverage tracker used to
// supplement coverage status information for each domain.
func NewListDomainsHandler(nodeRepo *storage.NodeRepo, ct *CoverageTracker) *ListDomainsHandler {
	return &ListDomainsHandler{
		nodeRepo:        nodeRepo,
		coverageTracker: ct,
	}
}

// Handle 处理 "列出所有域" 请求，返回包含各域名称、摘要、子域数量
// 和覆盖率状态的 DomainInfo 列表。
//
// Handle processes a "list all domains" request and returns a list of
// DomainInfo containing each domain's name, summary, subdomain count,
// and coverage status.
func (h *ListDomainsHandler) Handle(ctx context.Context) (*dktypes.ListDomainsResponse, error) {
	// 获取覆盖率映射
	// Get coverage map
	coverageMap, err := h.coverageTracker.GetCoverageMap(ctx)
	if err != nil {
		log.Printf("[list-domains] failed to get coverage map: %v", err)
		// 覆盖率查询失败不阻断整体流程，降级为全部标记为 CoverageNone
		// Coverage query failure does not block the overall flow;
		// degrade by marking all as CoverageNone
		coverageMap = make(map[string]dktypes.Coverage)
	}

	// 获取所有节点并按域分组统计
	// Get all nodes and group by domain for statistics
	nodes, err := h.nodeRepo.ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: list all nodes failed: %v", ErrKGNotReady.Wrap(err), err)
	}

	// 聚合每个域的统计信息
	// Aggregate statistics per domain
	type domainAgg struct {
		name           string
		summary        string
		subdomainCount map[string]bool
	}

	aggMap := make(map[string]*domainAgg)
	for _, node := range nodes {
		if node == nil {
			continue
		}
		if node.Domain == "" {
			continue
		}

		da, exists := aggMap[node.Domain]
		if !exists {
			da = &domainAgg{
				name:           node.Domain,
				summary:        "", // 域摘要需要从其他来源获取
				subdomainCount: make(map[string]bool),
			}
			aggMap[node.Domain] = da
		}
		if node.Subdomain != "" {
			da.subdomainCount[node.Subdomain] = true
		}
		// 使用第一个非空摘要作为域的摘要
		// Use the first non-empty summary as the domain summary
		if da.summary == "" && node.Summary != "" {
			da.summary = node.Summary
		}
	}

	// 构建响应
	// Build response
	domains := make([]dktypes.DomainInfo, 0, len(aggMap))
	for _, da := range aggMap {
		coverage := coverageMap[da.name]
		if coverage == "" {
			coverage = dktypes.CoverageFull // 有节点数据的域默认视为已覆盖
		}

		di := dktypes.DomainInfo{
			Name:           da.name,
			Summary:        da.summary,
			SubdomainCount: len(da.subdomainCount),
			Coverage:       coverage,
		}
		domains = append(domains, di)
	}

	log.Printf("[list-domains] returning %d domains", len(domains))

	return &dktypes.ListDomainsResponse{
		Domains: domains,
	}, nil
}
