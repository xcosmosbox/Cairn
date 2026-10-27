package extract

import "encoding/json"

// endpointScope 用完整归属限定临时别名，避免跨子域重名被 map 的最后一次写入覆盖。
// endpointScope qualifies temporary aliases without changing persisted identities.
type endpointScope struct{ domain, subdomain string }

type endpointRef struct {
	scope endpointScope
	node  *Node
}

type endpointIndex struct {
	local  map[endpointScope]map[string][]endpointRef
	global map[string][]endpointRef
}

// indexEndpoints 在改写任何 ID 之前建完整索引；局部唯一优先，只有局部缺失才查全局唯一。
// Local ambiguity must not fall through to a convenient global match.
func indexEndpoints(domains []Domain) endpointIndex {
	x := endpointIndex{local: make(map[endpointScope]map[string][]endpointRef), global: make(map[string][]endpointRef)}
	for di := range domains {
		for si := range domains[di].Subdomains {
			sd := &domains[di].Subdomains[si]
			scope := endpointScope{domains[di].Slug, sd.Slug}
			if x.local[scope] == nil {
				x.local[scope] = make(map[string][]endpointRef)
			}
			for _, nodes := range [][]Node{sd.Entities, sd.Concepts} {
				for ni := range nodes {
					n := &nodes[ni]
					if n.ID == "" {
						continue
					}
					r := endpointRef{scope, n}
					x.local[scope][n.ID] = append(x.local[scope][n.ID], r)
					x.global[n.ID] = append(x.global[n.ID], r)
				}
			}
		}
	}
	return x
}

func (x endpointIndex) resolve(scope endpointScope, alias string) (endpointRef, string) {
	matches := x.local[scope][alias]
	// 带归属别名只用于代码生成的跨域端点，不改写原文 ID。
	// Resolve an exact framed scope only when no ordinary identity already matches.
	if len(matches) == 0 && len(x.global[alias]) == 0 {
		var framed []string
		if json.Unmarshal([]byte(alias), &framed) == nil && len(framed) == 3 {
			matches = x.local[endpointScope{framed[0], framed[1]}][framed[2]]
		}
	}
	if len(matches) == 0 {
		matches = x.global[alias]
	}
	switch len(matches) {
	case 0:
		return endpointRef{}, "missing"
	case 1:
		return matches[0], "resolved"
	default:
		return endpointRef{}, "ambiguous"
	}
}

func (x endpointIndex) relation(scope endpointScope, r Relation) (endpointRef, endpointRef, string, string) {
	source, ss := x.resolve(scope, r.Source)
	target, ts := x.resolve(scope, r.Target)
	return source, target, ss, ts
}

// qualifiedRelationAlias 使用无歧义数组编码保留跨域重名端点的归属。
// Framed aliases are translated by the existing endpoint/UUID boundary.
func qualifiedRelationAlias(ref endpointRef) string {
	b, _ := json.Marshal([]string{ref.scope.domain, ref.scope.subdomain, ref.node.ID})
	return string(b)
}
