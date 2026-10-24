"""仅做匹配检索、原始来源绑定与完整可见预算，不重实现图算法。

Matched, read-only original-FTS / actual-production-graph adapters.

No QA loader, model client, graph algorithm or source backfill lives here.
"""
from __future__ import annotations

import copy
import hashlib
import json
from pathlib import Path
import re
import subprocess
import sys

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))
import benchmark as baseline

ARMS = ("raw_source_fts_sqlite", "production_graph")
CONTEXT_CHARS = 10_000
TOP_K = 10
STRUCTURAL_LABELS = frozenset(("Skill", "Domain", "Subdomain"))


class SourceBindingError(ValueError):
    """An original binding is invalid; halt admission instead of using evidence."""


def canonical(value):
    return baseline.canonical(value)


def digest(value):
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


def match_expression(question):
    if not isinstance(question, str) or not question.strip():
        raise ValueError("nonempty original question required")
    return " OR ".join('"' + atom.replace('"', '""') + '"' for atom in question.split())


def construction_gate(audit, corpus_ids, graph_sha):
    """Count distinct (original document, accepted04 member) incidences.

    Shared member IDs in separate documents remain separate denominator units.
    Explanations are not materialized members. The receipt is independently
    inspected against actual adapter source rows, not trusted as quote proof.
    """
    if audit.get("pass") is not True or audit.get("errors", "missing") not in (None, []):
        raise SourceBindingError("construction audit failed or has unresolved errors")
    if audit.get("db_sha256") != graph_sha:
        raise SourceBindingError("construction graph hash mismatch")
    docs = audit.get("documents", [])
    if len(docs) != 609 or {d.get("doc_id") for d in docs} != set(corpus_ids):
        raise SourceBindingError("all609 actual original documents required")
    accepted, retained = set(), set()
    paths = {doc.get('path'): doc['doc_id'] for doc in docs}
    if None in paths or len(paths) != len(docs):
        raise SourceBindingError('unique actual original document paths required')
    observed, tuples = set(), set()
    for row in audit.get('source_bindings', []):
        identity = paths.get(row.get('file_path'))
        key = row.get('node_uuid'), row.get('member_id'), row.get('file_path')
        if identity is None or not key[0] or not key[1] or key in tuples:
            raise SourceBindingError('invalid/duplicate actual source ownership tuple')
        tuples.add(key)
        observed.add((identity, key[1]))
    for doc in docs:
        members = set(doc.get("members", []))
        actual = set(doc.get("materialized_members", []))
        if not members or not actual or not actual <= members:
            raise SourceBindingError("document missing accepted/materialized membership")
        accepted.update((doc["doc_id"], member) for member in members)
        retained.update((doc["doc_id"], member) for member in actual)
    if retained != observed:
        raise SourceBindingError("materialized incidence list differs from actual source tuples")
    rate = len(retained) / len(accepted)
    if rate < .95:
        raise SourceBindingError("materialized incidence fraction below0.95")
    return {"documents": len(docs), "accepted_incidences": len(accepted),
            "materialized_incidences": len(retained), "fraction": rate}


class OriginalBindings:
    def __init__(self, corpus, construction=None):
        documents = baseline.validate_corpus(corpus)
        self.documents = {baseline.document_id(url): row for url, row in documents.items()}
        self.by_path = {"references/" + identity + ".md": identity for identity in self.documents}
        self.construction = construction
        self.members = {}
        self.tuples = set()
        if construction is not None:
            for doc in construction["documents"]:
                self.members[doc["doc_id"]] = set(doc["members"])
            for row in construction.get("source_bindings", []):
                self.tuples.add((row["node_uuid"], row["member_id"], row["file_path"]))

    def original(self, identity):
        try:
            row = self.documents[identity]
        except KeyError:
            raise SourceBindingError("unknown original document") from None
        return {"doc_id": identity, "url": row["url"], "body_sha256": digest(row["body"])}

    def graph_source(self, source, node_id):
        path, member = source.get("path"), source.get("member_id")
        identity = self.by_path.get(path)
        if identity is None or self.construction is None:
            raise SourceBindingError("graph original/member proof unavailable")
        if member not in self.members.get(identity, set()):
            raise SourceBindingError("graph source member absent from accepted04 document")
        if (node_id, member, path) not in self.tuples:
            raise SourceBindingError("graph source UUID/member/document tuple unbound")
        # The hint is optional and unverified. It is not a rewritten position,
        # original quote, or permission to show original body as graph evidence.
        return {**self.original(identity), "path": path, "member_id": member,
                "skill": source.get("skill"),
                "original_hint": {"start_line": source.get("start_line"),
                                  "end_line": source.get("end_line"),
                                  "verification": "unknown_or_unverified"}}

    def verify_raw(self, hit):
        identity = hit["doc_id"]
        original = self.original(identity)
        body = self.documents[identity]["body"]
        start, end = hit["start"], hit["end"]
        if type(start) is not int or type(end) is not int or not 0 <= start < end <= len(body):
            raise SourceBindingError("invalid original raw interval")
        if body[start:end] != hit["text"]:
            raise SourceBindingError("raw text is not exact original interval")
        return {"id": hit["chunk_id"], "text": hit["text"],
                "metadata": hit["metadata"], "sources": [{**original, "start": start, "end": end}],
                "relations": [], "representation": "original_text"}

    def graph_block(self, block):
        sources = [self.graph_source(source, block["id"]) for source in block.get("sources", [])]
        if not sources and block.get("label") not in STRUCTURAL_LABELS:
            raise SourceBindingError("graph leaf lacks original source ownership")
        return {"id": block["id"], "text": block["body"],
                **({"binding_status": "structural_unbound"} if not sources else {}),
                "metadata": {key: block[key] for key in ("name", "label", "domain", "subdomain", "confidence", "provenance", "source_refs") if key in block},
                "sources": sources, "relations": copy.deepcopy(block.get("edges", [])),
                "representation": "generated_graph"}


def pack_context(blocks, max_chars=CONTEXT_CHARS):
    """All shown metadata is counted; no independent source/relation count cap.

    Prefix clipping only changes body text. Original raw intervals shrink with
    that prefix. Generated graph text is never assigned original offsets.
    """
    selected, truncated = [], len(blocks) > TOP_K
    for original in blocks[:TOP_K]:
        block = copy.deepcopy(original)
        representation = block.pop("representation")
        if len(canonical({"evidence": selected + [block], "truncated": truncated})) <= max_chars:
            selected.append(block)
            continue
        truncated = True
        left, right, best = 1, len(block["text"]), None
        while left <= right:
            length = (left + right) // 2
            candidate = {**block, "text": block["text"][:length], "text_truncated": True}
            if representation == "original_text":
                if len(block["sources"]) != 1:
                    raise SourceBindingError("raw clipping needs one exact original interval")
                source = block["sources"][0]
                candidate["sources"] = [{**source, "end": source["start"] + length}]
            if len(canonical({"evidence": selected + [candidate], "truncated": True})) <= max_chars:
                best, left = candidate, length + 1
            else:
                right = length - 1
        if best is not None:
            selected.append(best)
        break
    value = {"evidence": selected, "truncated": truncated}
    serialized = canonical(value)
    if len(serialized) > max_chars:
        raise ValueError("context budget cannot hold empty envelope")
    return {**value, "serialized": serialized, "visible_chars": len(serialized),
            "context_sha256": digest(serialized)}


class MatchedAdapters:
    def __init__(self, raw_db, raw_receipt, graph_db, graph_binary, bindings, graph_sha):
        self.raw_db, self.graph_db, self.graph_binary = map(Path, (raw_db, graph_db, graph_binary))
        self.raw_receipt, self.bindings, self.graph_sha = raw_receipt, bindings, graph_sha

    def retrieve(self, question, arm):
        expression = match_expression(question)
        if arm == ARMS[0]:
            retriever = baseline.Retriever(self.raw_db, self.raw_receipt)
            try:
                hits = retriever.search({"qid": baseline.query_id(question), "query": question}, arm, TOP_K)
            finally:
                retriever.close()
            blocks = [self.bindings.verify_raw(hit) for hit in hits]
        elif arm == ARMS[1]:
            if baseline.file_hash(self.graph_db) != self.graph_sha:
                raise SourceBindingError("graph hash changed")
            command = [str(self.graph_binary), "--db", str(self.graph_db), "--mode", "graph",
                       "--query", expression, "--query-syntax", "fts5", "--limit", str(TOP_K)]
            process = subprocess.run(command, capture_output=True, text=True, timeout=30)
            if process.returncode:
                raise RuntimeError("production graph adapter failed")
            value = json.loads(process.stdout)
            if value.get("error") or value.get("diagnostics", {}).get("match_expression") != expression:
                raise RuntimeError("production graph query contract mismatch")
            if baseline.file_hash(self.graph_db) != self.graph_sha:
                raise SourceBindingError("graph changed during read-only retrieval")
            blocks = [self.bindings.graph_block(block) for block in value["evidence"]]
        else:
            raise ValueError("unfrozen retrieval arm")
        return {**pack_context(blocks), "match_expression": expression, "top_k": TOP_K,
                "retrieval_scope": "adapted retrieval; bypasses ResponseBuilder/token packing/MCP"}


def check_answer(value, context, bindings):
    if not isinstance(value, dict) or set(value) != {"answer", "abstain", "citations", "quotes"}:
        raise ValueError("answer schema mismatch")
    if not isinstance(value["answer"], str) or type(value["abstain"]) is not bool:
        raise ValueError("invalid answer/abstention")
    if value["abstain"] and (value["answer"] != "" or value["citations"] != [] or value["quotes"] != []):
        raise ValueError("explicit abstention requires empty answer/citations/quotes")
    if not value["abstain"] and not value["answer"].strip():
        raise ValueError("empty substantive answer")
    visible = {block["id"]: block for block in context["evidence"]}
    if not isinstance(value["citations"], list) or any(type(ref) is not str or ref not in visible or not visible[ref]["sources"] for ref in value["citations"]):
        raise SourceBindingError("citation ID was not visible with original-source ownership")
    if not value["abstain"] and not value["citations"]:
        raise SourceBindingError("substantive answer lacks citations")
    if not isinstance(value["quotes"], list):
        raise ValueError("quotes must be an array")
    for quote in value["quotes"]:
        validate_quote(quote, value["answer"], value["citations"], context, bindings)
    return value


def validate_quote(quote, answer, citations, context, bindings):
    visible = {block["id"]: block for block in context["evidence"]}
    if not isinstance(quote, dict) or set(quote) != {"evidence_id", "doc_id", "start", "end", "text"}:
        raise SourceBindingError("exact quote schema mismatch")
    identity, start, end, text = quote["doc_id"], quote["start"], quote["end"], quote["text"]
    block = visible.get(quote["evidence_id"])
    if block is None or quote["evidence_id"] not in citations or identity not in {s["doc_id"] for s in block["sources"]}:
        raise SourceBindingError("quote citation/document unbound")
    body = bindings.documents.get(identity, {}).get("body")
    if not isinstance(body, str) or type(start) is not int or type(end) is not int or not 0 <= start < end <= len(body):
        raise SourceBindingError("quote original offsets invalid")
    if not isinstance(text, str) or body[start:end] != text or text not in block["text"] or text not in answer:
        raise SourceBindingError("quote is not exact original and actually visible answer text")


def reported_usage(events):
    """只汇总供应商实际报告值，未知项不按零费用解释。

    Keep incomplete/failed attempts explicit; infer no currency charge.
    """
    requests = [row for row in events if row.get('event') == 'request']
    responses = [row for row in events if row.get('event') == 'response']
    result = {'admitted_attempts': len(requests), 'response_events': len(responses),
              'attempts_without_outcome': len({row.get('http_attempt', row.get('request_id')) for row in requests} - {row.get('http_attempt', row.get('request_id')) for row in responses}),
              'reported_prompt_tokens': 0, 'reported_completion_tokens': 0,
              'responses_without_complete_usage': 0, 'currency_cost': 'not_inferred'}
    for row in responses:
        usage = row.get('usage')
        usage = usage if isinstance(usage, dict) else {}
        known = True
        for field, target in (('prompt_tokens', 'reported_prompt_tokens'), ('completion_tokens', 'reported_completion_tokens')):
            value = usage.get(field)
            if type(value) is int and value >= 0:
                result[target] += value
            else:
                known = False
        if not known:
            result['responses_without_complete_usage'] += 1
    result['usage_complete'] = result['attempts_without_outcome'] == 0 and result['responses_without_complete_usage'] == 0
    return result
