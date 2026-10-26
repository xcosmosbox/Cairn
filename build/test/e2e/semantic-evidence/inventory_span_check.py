#!/usr/bin/env python3
"""Verify restricted inventory byte provenance and public aggregate correspondence.

Usage: inventory_span_check.py --inventory PRIVATE/inventory.json
       --sources PRIVATE/sources --summary testdata/v011/inventory_summary.json

Only sanitized counts are printed. This deterministic check proves span accounting,
not semantic coverage in a constructed graph or correctness of attributed claims.
"""
import argparse
from collections import Counter
import hashlib
import json
from pathlib import Path


def require(condition, message):
    if not condition:
        raise ValueError(message)


def public_exclusion_reason(reason):
    """Return only generic categories; private source labels must not escape."""
    if reason.startswith('template wrapper'):
        return 'template_wrapper'
    if reason.startswith(('attachment navigation', 'navigation section')):
        return 'navigation'
    if reason.startswith(('newsletter self-promotion', 'subscription signup')):
        return 'editorial_promotion'
    if reason.startswith(('newsletter thanks', 'editorial transition', 'future-review', 'navigation transition')):
        return 'editorial_transition'
    raise ValueError('unmapped private exclusion category')


def validate(inventory, source_dir, summary=None, inventory_bytes=None):
    require(inventory.get('schema_version') == 'v011-evidence-1', 'inventory schema')
    sources = inventory.get('sources', [])
    require([s.get('source_id') for s in sources] == [f'S{i:02}' for i in range(1,7)], 'six ordered source IDs required')
    seen = set()
    expected = []
    for src in sources:
        sid = src['source_id']
        raw = (Path(source_dir) / f'{sid}.md').read_bytes()
        text = raw.decode('utf-8')
        require(hashlib.sha256(raw).hexdigest() == src['original_sha256'], f'{sid}: original hash mismatch')
        require(len(raw) == src['bytes'] and len(text.splitlines()) == src['lines'], f'{sid}: source size mismatch')
        paragraphs = {p['paragraph_id']:p for p in src['paragraphs']}
        require(len(paragraphs) == len(src['paragraphs']), f'{sid}: duplicate paragraph ID')
        coverage = bytearray(len(raw))
        line_offsets = [0]
        for line in raw.splitlines(keepends=True):
            line_offsets.append(line_offsets[-1] + len(line))
        for p in paragraphs.values():
            a,b = p['byte_start'],p['byte_end']
            require(0 <= a < b <= len(raw), f'{sid}: invalid paragraph span')
            require(raw[a:b].decode('utf-8') == p['text'], f'{sid}: paragraph text mismatch')
            require(a == line_offsets[p['line']-1], f'{sid}: paragraph line mismatch')
            require(bool(p.get('preservation_facets')) and p.get('publication_anchor') == src['metadata']['published_at'], f'{sid}: missing facets or time anchor')
        substantive, exclusions = [], []
        for unit in src['units']:
            uid = unit['unit_id']
            require(uid.startswith(sid+'-U') and uid not in seen, f'{sid}: duplicate/invalid unit ID')
            seen.add(uid)
            a,b = unit['byte_start'],unit['byte_end']
            require(0 <= a < b <= len(raw), f'{sid}: invalid unit span')
            require(raw[a:b].decode('utf-8') == unit['source_quote'], f'{sid}: source quote mismatch')
            p = paragraphs[unit['paragraph_id']]
            require(p['byte_start'] <= a < b <= p['byte_end'], f'{sid}: unit outside context')
            require(unit['line_start'] == unit['line_end'] == p['line'], f'{sid}: unit line mismatch')
            require(not any(coverage[a:b]), f'{sid}: overlapping unit spans')
            coverage[a:b] = b'\x01' * (b-a)
            if unit['disposition'] == 'substantive':
                require(unit['expected_claim'] == unit['source_quote'], f'{sid}: expected claim drift')
                require(unit['preservation_facets'] == p['preservation_facets'], f'{sid}: detached facets')
                require(bool(unit.get('assertion_scope')) and bool(unit.get('acceptance')), f'{sid}: missing interpretation requirements')
                require(not unit.get('exclusion_reason'), f'{sid}: conflicting substantive disposition')
                substantive.append(unit)
            else:
                require(unit['disposition'] == 'excluded' and bool(unit.get('exclusion_reason')), f'{sid}: unexplained exclusion')
                require(unit.get('expected_claim') is None and unit.get('preservation_facets') is None, f'{sid}: conflicting excluded disposition')
                exclusions.append(unit)
            for highlights in unit['qualifiers'].values():
                for h in highlights:
                    require(a <= h['byte_start'] < h['byte_end'] <= b, f'{sid}: qualifier outside unit')
                    require(raw[h['byte_start']:h['byte_end']].decode('utf-8') == h['text'], f'{sid}: qualifier drift')
        char_offset = 0
        for char in text:
            end = char_offset + len(char.encode())
            require(char.isspace() or all(coverage[char_offset:end]), f'{sid}: uncovered non-whitespace source bytes')
            char_offset = end
        for relation in src['explicit_relations']:
            rid = relation['relation_id']
            require(rid.startswith(sid+'-R') and rid not in seen, f'{sid}: duplicate/invalid relation ID')
            seen.add(rid)
            p = paragraphs[relation['paragraph_id']]
            require(relation['evidence_byte_start'] == p['byte_start'] and relation['evidence_byte_end'] == p['byte_end'], f'{sid}: detached relation evidence')
            require(relation['evidence_quote'] == p['text'] and relation['preservation_facets'] == p['preservation_facets'], f'{sid}: relation evidence drift')
            require(all(relation.get(k) for k in ['subject','predicate','object','assertion_status','required_representation']), f'{sid}: relation semantics missing')
            require(relation['direction'] == 'subject -> object', f'{sid}: relation direction missing')
            require(any(u['paragraph_id'] == p['paragraph_id'] for u in substantive), f'{sid}: relation only binds excluded context')
        expected.append(dict(source_id=sid, original_bytes=len(raw), original_lines=len(text.splitlines()),
            substantive_units=len(substantive), excluded_units=len(exclusions), covered_units=len(substantive),
            explicit_relations=len(src['explicit_relations']),
            classifications=dict(Counter(k for u in substantive for k in u['classifications'])),
            exclusion_reasons=dict(Counter(public_exclusion_reason(u['exclusion_reason']) for u in exclusions))))
    totals={k:sum(s[k] for s in expected) for k in ['original_bytes','original_lines','substantive_units','excluded_units','covered_units','explicit_relations']}
    if summary is not None:
        require(summary.get('schema_version') == inventory['schema_version'], 'summary schema')
        require(len(summary['sources']) == 6, 'summary source count')
        for actual,want in zip(summary['sources'],expected):
            require(all(actual.get(k) == v for k,v in want.items()), f"{want['source_id']}: public aggregate mismatch")
            require(bool(actual.get('expected_inventory_ref')), 'missing public expected inventory reference')
        require(summary['totals'] == totals, 'summary totals mismatch')
        if inventory_bytes is not None:
            require(summary['private_evidence']['inventory_sha256'] == hashlib.sha256(inventory_bytes).hexdigest(), 'inventory digest mismatch')
    return totals


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--inventory', required=True, type=Path)
    parser.add_argument('--sources', required=True, type=Path)
    parser.add_argument('--summary', type=Path)
    args=parser.parse_args()
    raw=args.inventory.read_bytes()
    totals=validate(json.loads(raw),args.sources,json.loads(args.summary.read_text()) if args.summary else None,raw)
    print(json.dumps(dict(structural_check='pass',semantic_acceptance='requires separate source/meaning review',**totals),sort_keys=True))


if __name__ == '__main__': main()
