// Package dktypes 定义领域知识层（Cairn）中所有跨包共享的核心类型、
// 枚举、数据结构及其校验方法。
//
// 本文件包含领域和子域的对外公开表示类型。
package dktypes

// ——————————————————————————————————————————————————————————————————————————————
// Domain — 业务域
// ——————————————————————————————————————————————————————————————————————————————

// Domain 表示一个业务域，是对外公开的域级别信息摘要。
// Domain represents a business domain, providing a public-facing summary
// of domain-level information.
type Domain struct {
	// Name 是业务域的名称（唯一标识）。
	// Name is the unique name of the business domain.
	Name string `json:"name" yaml:"name"`
	// Summary 是业务域的简要描述。
	// Summary is a brief summary of the business domain.
	Summary string `json:"summary" yaml:"summary"`
	// Description 是业务域的详细描述（可选）。
	// Description is a detailed description of the business domain (optional).
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	// Coverage 表示该业务域的覆盖率状态。
	// Coverage indicates the coverage status of this business domain.
	Coverage Coverage `json:"coverage" yaml:"coverage"`
}

// ——————————————————————————————————————————————————————————————————————————————
// Subdomain — 业务子域
// ——————————————————————————————————————————————————————————————————————————————

// Subdomain 表示一个业务子域，是对外公开的子域级别信息摘要。
// Subdomain represents a business subdomain, providing a public-facing summary
// of subdomain-level information.
type Subdomain struct {
	// Name 是子域的名称。
	// Name is the name of the subdomain.
	Name string `json:"name" yaml:"name"`
	// Summary 是子域的简要描述。
	// Summary is a brief summary of the subdomain.
	Summary string `json:"summary" yaml:"summary"`
	// Description 是子域的详细描述（可选）。
	// Description is a detailed description of the subdomain (optional).
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	// DomainName 是子域所属的父域名称。
	// DomainName is the name of the parent domain to which this subdomain belongs.
	DomainName string `json:"domain_name" yaml:"domain_name"`
}
