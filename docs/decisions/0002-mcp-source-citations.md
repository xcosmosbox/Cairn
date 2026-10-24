# ADR-0002: opt-in stored source metadata for resolve_node

Status: proposed for acceptance with the implementation PR. Date: 2026-10-24.
Task: [#26](https://github.com/xcosmosbox/Cairn/issues/26).

## Context

`domain_search` returns node UUIDs and summaries; `resolve_node` follows merge/split
lineage but has not exposed the graph's existing source ledger. A client could find a
node without learning which stored document/member contributed to it. The DB already
contains `nodes.source_refs` and `node_sources` relative file paths and optional spans.
Neither field proves that the source file or content remains available to this client.

## Options and decision

An independent source tool would add another discovery/routing surface. Always extending
the existing response would affect consumers and reveal metadata they did not request.
We instead add a boolean `include_sources` to `resolve_node`, defaulting to false. No new
configuration or authorization model is introduced: the server's current KG routing and
access boundary applies. Only an explicit JSON boolean true enables the extra output.

The existing first text content item remains byte-for-byte unchanged. An opt-in call adds
one JSON text content item after it. Clients should parse that item as JSON, not concatenate
it into the legacy node summary. Non-boolean values, including null, are rejected.

```json
{"name":"resolve_node","arguments":{"uuid":"old-node","kg":"demo","include_sources":true}}
```

Illustrative second text item (not a claim that this example UUID exists):

```json
{
  "requested_uuid": "old-node",
  "scope": "resolved_node_only",
  "source_metadata": {
    "node_uuid": "live-node",
    "status": "available",
    "source_refs": ["queue-skill"],
    "locations": [{
      "member_id": "queue-member",
      "skill": "queue-skill",
      "file_path": "docs/queue.md",
      "start_line": 6,
      "end_line": 8,
      "span_status": "available"
    }]
  }
}
```

## Ownership and unknown semantics

- `requested_uuid` preserves the client's input. `source_metadata.node_uuid` is the
  exact live UUID that the existing resolver selected in the same leased KG generation.
  The service's new `GetNodeSources` reads an exact live UUID and does not itself resolve
  lineage. Sources belonging to an obsolete UUID are not carried onto its successor.
- Merge reads the selected survivor's current ledger. Split reads only the primary live
  successor; the original text continues listing other live successors. Resolve each other
  successor separately to obtain its own metadata. No sibling source lists are combined.
- `status=available` means at least one safe stored file location exists, not that a file
  was fetched or its content verified. `source_refs` are stored reference labels; labels
  alone do not establish a file location. Their absence does not invalidate a ledger row.
- No safe locations yields `status=unknown`, with `reason=no_source_locations`. An absent
  legacy `node_sources` table yields `source_table_unavailable`. An unresolved/missing node
  yields `node_not_found` and no `node_uuid`. Safe reference labels may still accompany an
  unknown location status. Real query/DB errors remain errors rather than unknown success.
- A valid span is a positive, 1-based inclusive start/end pair with end >= start. Otherwise
  `span_status=unknown` and both line numbers are omitted. No span or file contents are inferred.
- `omitted`, when present, counts unsafe stored references/locations excluded from output;
  excluded values are not echoed. With partial safe data, available locations remain usable.

## Relative metadata and access boundary

Only canonical repository-relative paths/reference identifiers are emitted. The read side
conservatively excludes empty/invalid UTF-8 identifiers, leading/trailing whitespace,
absolute paths, Windows/UNC forms, traversal/dot/repeated-separator forms, URLs, colon,
backslash, percent-encoded forms and control characters. It does not silently normalize
such values into a different asserted source. This can omit unusual otherwise legitimate
filenames; `omitted` makes the loss visible. File paths, member IDs and skill labels follow
the same rule. Existing node summary/name output is not redefined by this change.

The method reads DB metadata only. It neither opens source files nor makes network calls,
uses App credentials, invents repository URLs/commits, hydrates source text, migrates a DB,
or updates KG structure. It does not promise production source-backfill semantics.
Relative paths and labels can themselves be private: an explicit opt-in response is within
the existing KG access boundary, not a new per-user GitHub authorization check. A server
operator must continue to restrict access to private KGs under the existing deployment
model. This feature adds no general-purpose authorization or source-fetch capability.

## Compatibility, validation and rollback

No DB schema, Bundle format, config or dependency change. Omitted/false preserves existing
content and does not read the source table; unknown/legacy shapes do not trigger migrations.
The single-KG service interface gains a read-only metadata DTO/method; frontends retain
multi-KG routing. The existing request lease covers resolution and source lookup so hot
swapping cannot combine different Bundle generations.

Tests cover exact default text, false/true and invalid options, source UUID ownership for
merge/split, missing nodes/locations/table, legacy schema 4, invalid paths/spans, cross-KG
routing, cancelled reads, unchanged DB bytes/mtime and a real entry-point stdio session.
Run focused service/MCP race tests, full `go test -race -count=1 ./...` and `make verify`.
The feature needs no paid calls or Viewer changes. Another session reviews the contract
and tests before maintainer acceptance.

Rollback removes `include_sources` discovery/formatting and the new service method/DTO.
No persisted data conversion or reverse migration is necessary. Clients can omit the
option to retain the previous response contract throughout rollout.
