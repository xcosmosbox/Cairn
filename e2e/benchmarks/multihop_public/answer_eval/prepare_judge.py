"""分离求解器与判分数据，核验原始来源和实际可见支持。

Strict solver/judge projections and original-source bindings.

Functions are exercised with synthetic data during implementation. Real QA may
only be supplied after the separately recorded stage opening and freeze.
"""
from __future__ import annotations

import argparse
import copy
import json
from pathlib import Path
import sys

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
from adapter import OriginalBindings, SourceBindingError, STRUCTURAL_LABELS, baseline, canonical, digest


POSITIVE_TYPES = ('inference_query', 'comparison_query', 'temporal_query')


def stage_roster(freeze, stage):
    if stage == 'smoke':
        return set(freeze['smoke_qids'])
    if stage in ('dev', 'holdout'):
        return {row['qid'] for row in freeze['selected'][stage]}
    raise ValueError('unknown frozen stage')


def fact_units(question, bindings):
    """Deduplicate original(URL,fact) within one retrieval question's targets."""
    units, seen = [], set()
    for evidence in question['evidence_list']:
        url, fact = evidence['url'], evidence['fact']
        if (url, fact) in seen:
            continue
        seen.add((url, fact))
        identity = digest(url)
        source = bindings.original(identity)
        body = bindings.documents[identity]['body']
        if not isinstance(fact, str) or not fact or fact not in body:
            raise SourceBindingError('gold support fact is not in pinned original body')
        start = body.index(fact)
        units.append({'unit_id': digest(canonical([url, fact])), 'fact': fact,
                      **source, 'start': start, 'end': start + len(fact)})
    return units


def question_projections(qa_rows, corpus, freeze, stage):
    documents = baseline.validate_corpus(corpus)
    prepared = baseline.prepare_questions(qa_rows, documents)
    bindings = OriginalBindings(corpus)
    selected = stage_roster(freeze, stage)
    by_id = {question['qid']: question for question in prepared}
    if not selected <= set(by_id):
        raise ValueError('frozen question missing')
    solver, judge = [], []
    for identity in sorted(selected):
        question = by_id[identity]
        solver.append({'question_id': identity, 'question': question['query']})
        units = fact_units(question, bindings)
        judge.append({'question_id': identity, 'question': question['query'],
                      'expected_answer': question['answer'], 'question_type': question['question_type'],
                      'group_hash': question['group_hash'], 'answerable': question['question_type'] != 'null_query',
                      'fact_units': units, 'stage': stage})
    return solver, judge


def validate_visible(context, bindings):
    serialized = context['serialized']
    actual = json.loads(serialized)
    if actual != {'evidence': context['evidence'], 'truncated': context['truncated']}:
        raise SourceBindingError('visible context does not match exact snapshot')
    if context['context_sha256'] != digest(serialized) or context['visible_chars'] != len(serialized) or len(serialized) > 10000:
        raise SourceBindingError('visible context hash/size mismatch')
    for block in context['evidence']:
        structural = block.get('binding_status') == 'structural_unbound'
        if not block['sources']:
            if not structural or block.get('metadata', {}).get('label') not in STRUCTURAL_LABELS:
                raise SourceBindingError('visible source-less block is not legitimate structural evidence')
        elif structural:
            raise SourceBindingError('structural-unbound marker conflicts with original ownership')
        for source in block['sources']:
            original = bindings.original(source['doc_id'])
            if any(source.get(key) != value for key, value in original.items()):
                raise SourceBindingError('visible original binding mismatch')
            if 'start' in source or 'end' in source:
                body = bindings.documents[source['doc_id']]['body']
                start, end = source['start'], source['end']
                if type(start) is not int or type(end) is not int or body[start:end] != block['text'] or not 0 <= start < end <= len(body):
                    raise SourceBindingError('visible raw interval mismatch')
    return actual


def original_evidence(question, context, bindings):
    """Original source text is evaluator-only; never added to solver contexts."""
    gold = []
    for index, unit in enumerate(question['fact_units']):
        body = bindings.documents[unit['doc_id']]['body']
        start, end = max(0, unit['start'] - 500), min(len(body), unit['end'] + 500)
        quote = body[start:end]
        gold.append({'ref': 'gold:%d' % (index + 1), 'quote': quote,
                     **{key: unit[key] for key in ('doc_id', 'url', 'body_sha256')},
                     'start': start, 'end': end})
    identities = sorted({source['doc_id'] for block in context['evidence'] for source in block['sources']})
    originals = [{'ref': 'original:%d' % (index + 1), **bindings.original(identity),
                  'quote': bindings.documents[identity]['body']} for index, identity in enumerate(identities)]
    return gold, originals


def blind_cases(questions, results, snapshots, bindings, seed=20261024):
    """Reuse the unchanged judge schema for two distinct positive targets.

    A: actual answer, required target=the expected answer to the literal question.
    B: exact visible context as candidate, required targets=original support facts.
    B's overall/strict/claim aggregate scores are never answer-quality metrics.
    Nulls use only the frozen mechanical structured-abstention contract.
    """
    import random
    from adapter import ARMS, check_answer, validate_quote
    qmap = {row['question_id']: row for row in questions}
    rmap, cmap = {}, {}
    for row in results:
        key = row['question_id'], row['arm']
        if key in rmap:
            raise ValueError('duplicate result pair identity')
        rmap[key] = row
    for row in snapshots:
        key = row['question_id'], row['arm']
        if key in cmap:
            raise ValueError('duplicate context pair identity')
        cmap[key] = row
    expected = {(identity, arm) for identity in qmap for arm in ARMS}
    if set(rmap) != expected or set(cmap) != expected:
        raise ValueError('paired fixed result/context denominator incomplete')
    cases, mapping, pairs = [], {}, []
    pending = []
    for identity, arm in sorted(expected):
        question, result, snapshot = qmap[identity], rmap[identity, arm], cmap[identity, arm]
        context = snapshot['context']
        validate_visible(context, bindings)
        if result.get('context_sha256') != context['context_sha256'] or result.get('question_sha256') != digest(question['question']):
            raise SourceBindingError('result question/context binding mismatch')
        mechanical = {'solver_ok': result.get('status') == 'ok', 'answer': result.get('answer', ''),
                      'abstain': result.get('abstain') is True, 'citations_valid': False, 'quotes_valid': False}
        if mechanical['solver_ok']:
            try:
                check_answer({key: result[key] for key in ('answer', 'abstain', 'citations', 'quotes')}, context, bindings)
                mechanical.update(citations_valid=True, quotes_valid=True)
            except (ValueError, KeyError, TypeError):
                mechanical['solver_ok'] = False
        candidate = result.get('candidate', result)
        candidate = candidate if isinstance(candidate, dict) else {}
        citations = candidate.get('citations', [])
        citations = citations if isinstance(citations, list) else []
        quotes = candidate.get('quotes', [])
        quotes = quotes if isinstance(quotes, list) else []
        bound_ids = {block['id'] for block in context['evidence'] if block['sources']}
        mechanical['emitted_citations'] = len(citations)
        mechanical['invalid_citations'] = sum(not isinstance(ref, str) or ref not in bound_ids for ref in citations)
        mechanical['emitted_quotes'], mechanical['invalid_quotes'] = len(quotes), 0
        for quote in quotes:
            try:
                validate_quote(quote, candidate.get('answer', ''), citations, context, bindings)
            except (ValueError, KeyError, TypeError):
                mechanical['invalid_quotes'] += 1
        pair = {'question_id': identity, 'arm': arm, 'question_type': question['question_type'],
                'group_hash': question['group_hash'], 'answerable': question['answerable'],
                'fact_unit_ids': [unit['unit_id'] for unit in question['fact_units']],
                'mechanical': mechanical, 'answer_blind_id': None, 'evidence_blind_id': None}
        pairs.append(pair)
        if not question['answerable']:
            continue
        if not question['fact_units']:
            raise SourceBindingError('answerable case lacks original support facts')
        gold, originals = original_evidence(question, context, bindings)
        visible = [{'ref': 'retrieved:%d' % (index + 1), 'text': canonical(block), 'source_refs': []}
                   for index, block in enumerate(context['evidence'])]
        common = {'question': question['question'], 'answerable': True,
                  'expected_answer': question['expected_answer'], 'forbidden_claims': [],
                  'source_evidence': gold, 'verified_retrieved_originals': originals,
                  'retrieved_evidence': visible}
        if mechanical['solver_ok'] and not mechanical['abstain']:
            case = {**copy.deepcopy(common), 'answer': mechanical['answer'],
                    'required_facts': [question['expected_answer']]}
            pending.append((pair, 'answer', case))
        # Empty/error retrieval has known zero discoverable support, not an
        # unresolved semantic judge. Successful nonempty evidence gets B even
        # when solver transport failed or abstained.
        if snapshot.get('retrieval_status') == 'ok' and context['evidence']:
            case = {**copy.deepcopy(common), 'answer': context['serialized'],
                    'required_facts': [unit['fact'] for unit in question['fact_units']]}
            pending.append((pair, 'evidence', case))
    random.Random(seed).shuffle(pending)
    for index, (pair, kind, case) in enumerate(pending, 1):
        identity = 'case-%05d' % index
        case['blind_id'] = identity
        cases.append(case)
        pair[kind + '_blind_id'] = identity
        source_context = cmap[pair['question_id'], pair['arm']]['context']
        answer_result = rmap[pair['question_id'], pair['arm']]
        allowed = {'retrieved:%d' % (i + 1) for i, block in enumerate(source_context['evidence']) if block['id'] in answer_result.get('citations', [])}
        mapping[identity] = {'question_id': pair['question_id'], 'arm': pair['arm'], 'case_kind': kind,
                             'answer_sha256': digest(case['answer']), 'context_sha256': source_context['context_sha256'],
                             'allowed_retrieved_refs': sorted(allowed),
                             'unit_doc_ids': [unit['doc_id'] for unit in qmap[pair['question_id']]['fact_units']],
                             'evidence_ref_docs': {item['ref']: item['doc_id'] for item in case['source_evidence'] + case['verified_retrieved_originals']},
                             'retrieved_ref_docs': {'retrieved:%d' % (i + 1): [source['doc_id'] for source in block['sources']] for i, block in enumerate(source_context['evidence'])},
                             'substantive_fragments': {'retrieved:%d' % (i + 1): [canonical(block['text'])[1:-1]] + [canonical(relation['description'])[1:-1] for relation in block.get('relations', []) if isinstance(relation.get('description'), str)] for i, block in enumerate(source_context['evidence'])}}
    return {'format': 'cairn-blind-judge-input/v1', 'adapter_source_sha256': baseline.file_hash(Path(__file__)), 'cases': cases,
            'blinding_limitations': 'No arm/method labels; evidence wording, IDs and structure can reveal a method.'}, \
           {'format': 'cairn-public-answer-private-map/v1', 'mapping': mapping, 'pairs': pairs}


def validate_adapted_verdict(case, verdict, meta):
    """Additional local bindings around the unchanged semantic judge validator."""
    evidence_docs = meta['evidence_ref_docs']
    ref_docs = meta['retrieved_ref_docs']
    fragments = meta['substantive_fragments']
    def substantive(ref, quote):
        return isinstance(quote, str) and bool(quote.strip()) and any(quote in text for text in fragments.get(ref, []))
    if meta['case_kind'] == 'evidence':
        for fact in verdict['fact_verdicts']:
            if fact['status'] != 'met':
                continue
            identity = meta['unit_doc_ids'][fact['fact_index']]
            if not any(evidence_docs.get(ref) == identity for ref in fact.get('evidence_refs', [])):
                raise SourceBindingError('credited visible fact lacks its original URL/hash binding')
            if not any(identity in ref_docs[ref] and substantive(ref, fact['answer_quote']) for ref in ref_docs):
                raise SourceBindingError('credited fact quote is metadata-only or not bound visible substantive content')
    elif meta['case_kind'] == 'answer':
        for claim in verdict['claim_verdicts']:
            if claim['support'] == 'not_checkable' or claim['retrieval_support'] != 'supported':
                continue
            refs = claim['retrieved_refs']
            if not set(refs) <= set(meta['allowed_retrieved_refs']):
                raise SourceBindingError('answer claim uses evidence the solver did not cite')
            original_ids = {evidence_docs[ref] for ref in claim.get('evidence_refs', []) if ref in evidence_docs}
            for quote in claim['retrieved_quotes']:
                ref = quote['ref']
                if not substantive(ref, quote['quote']) or not original_ids.intersection(ref_docs.get(ref, [])):
                    raise SourceBindingError('answer claim lacks original-bound cited substantive support')
    else:
        raise ValueError('unknown private judge case kind')
    return verdict


def validate_solver_artifacts(results, snapshots, config):
    """绑定保留的真实单次响应与最终回答，禁止账本外改写答案。

    Existing ledger verification binds event identities; this adapter also binds
    the exact projected payload and parsed retained response to score inputs.
    """
    from run import EvalConfig, payload_for, strict_json, SYSTEM
    frozen = EvalConfig(**config['config'])
    if frozen.model != 'deepseek-flash' or frozen.thinking != 'disabled' or frozen.max_tokens != 4096 or frozen.max_retries != 0:
        raise ValueError('solver config is outside frozen one-shot contract')
    if config['system_prompt_sha256'] != digest(SYSTEM):
        raise ValueError('solver prompt differs from actual run freeze')
    contexts = {(row['question_id'], row['arm']): row for row in snapshots}
    for result in results:
        job = contexts[result['question_id'], result['arm']]
        if job['question_id'] != baseline.query_id(job['question']) or result.get('question') != job['question'] or result.get('question_sha256') != digest(job['question']):
            raise SourceBindingError('actual snapshot/result literal question identity mismatch')
        expected = canonical(payload_for(job, frozen))
        calls = result.get('model_calls', [])
        if len(calls) > 1:
            raise ValueError('one-shot case has more than one HTTP attempt')
        for call in calls:
            if call['request_sha256'] != digest(expected) or call['request_bytes'] != len(expected.encode('utf-8')):
                raise SourceBindingError('retained request does not bind literal question/context payload')
        if result.get('status') == 'ok':
            if len(calls) != 1 or calls[0].get('status') != 'ok' or calls[0].get('finish_reason') != 'stop':
                raise ValueError('successful answer lacks its single retained completed response')
            message = calls[0]['assistant_message']
            if message.get('tool_calls') not in (None, []):
                raise ValueError('one-shot retained response unexpectedly calls tools')
            candidate = strict_json(message['content'])
            if canonical(candidate) != canonical(result.get('candidate')) or canonical({key: result[key] for key in ('answer', 'abstain', 'citations', 'quotes')}) != canonical({key: candidate[key] for key in ('answer', 'abstain', 'citations', 'quotes')}):
                raise SourceBindingError('final answer/candidate differs from actual retained response')
        elif calls and calls[0].get('status') == 'ok' and isinstance(calls[0].get('assistant_message', {}).get('content'), str):
            try:
                candidate = strict_json(calls[0]['assistant_message']['content'])
            except ValueError:
                continue
            if 'candidate' in result and canonical(result['candidate']) != canonical(candidate):
                raise SourceBindingError('failed candidate was edited outside retained response')
            result['candidate'] = candidate
    return results


def execute_judge(args):
    """Invoke the existing judge scheduler/ledger with a scoped validator adapter.

    The wrapper adds no HTTP code. Adapter identity is bound by the blind-input
    hash; evidence guard failures receive only the existing frozen validation
    retry allowance. Ambiguity is retained for no-pass scoring, never repaired
    into agreement by this wrapper.
    """
    sys.path.insert(0, str(HERE.parents[2] / 'quality'))
    import grade
    blind = json.loads(args.blind.read_text())
    private = json.loads(args.private_map.read_text())
    if baseline.file_hash(args.blind) != args.blind_sha256 or baseline.file_hash(args.private_map) != args.private_map_sha256:
        raise ValueError('judge input/map pin mismatch')
    if blind.get('adapter_source_sha256') != baseline.file_hash(Path(__file__)):
        raise ValueError('judge adapter changed after blind freeze')
    if private.get('blind_input_sha256') != args.blind_sha256:
        raise ValueError('private arm/case mapping does not bind blind input')
    if not blind['cases']:
        # A mechanical-only negative run needs no provider key or HTTP admission.
        # Reuse the durable ledger implementation, refusing historical overwrite.
        from durable_ledger import JSONLedger
        for path in (args.out, args.usage):
            Path(path).parent.mkdir(parents=True, exist_ok=True)
        with_json = []
        try:
            for path in (args.out, args.usage):
                with_json.append(JSONLedger(Path(path)))
        finally:
            for ledger in with_json:
                ledger.close()
        return
    from safe_http import urlopen as fenced_wire
    def validate(case, verdict):
        grade.validate_verdict(case, verdict)
        return validate_adapted_verdict(case, verdict, private['mapping'][case['blind_id']])
    # Same stateless wire fence inside the existing separately metered judge
    # transaction. No nested Native admission and no global monkeypatch.
    grade.judge(args, transport=fenced_wire, verdict_validator=validate)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='command', required=True)
    project = sub.add_parser('project')
    project.add_argument('--qa', type=Path, required=True)
    project.add_argument('--corpus', type=Path, required=True)
    project.add_argument('--roster', type=Path, required=True)
    project.add_argument('--stage', choices=('smoke', 'dev', 'holdout'), required=True)
    project.add_argument('--stage-admitted', action='store_true')
    project.add_argument('--open-holdout', action='store_true')
    project.add_argument('--out', type=Path, required=True)
    blind = sub.add_parser('blind')
    for name in ('judge-questions', 'results', 'contexts', 'corpus', 'out', 'private-map'):
        blind.add_argument('--' + name, type=Path, required=True)
    blind.add_argument('--judge-questions-sha256', required=True)
    blind.add_argument('--results-sha256', required=True)
    blind.add_argument('--contexts-sha256', required=True)
    blind.add_argument('--run-dir', type=Path, required=True)
    blind.add_argument('--run-config-sha256', required=True)
    blind.add_argument('--projection-receipt', type=Path, required=True)
    blind.add_argument('--projection-receipt-sha256', required=True)
    judge = sub.add_parser('judge')
    for name in ('blind', 'private-map', 'out', 'usage', 'extra-body'):
        judge.add_argument('--' + name, type=Path, required=True)
    judge.add_argument('--blind-sha256', required=True)
    judge.add_argument('--private-map-sha256', required=True)
    judge.add_argument('--allow-paid', action='store_true')
    judge.add_argument('--max-calls', type=int, required=True)
    judge.add_argument('--max-total-tokens', type=int, required=True)
    judge.add_argument('--max-input-chars', type=int, default=500000)
    judge.add_argument('--max-tokens', type=int, default=32768)
    judge.add_argument('--timeout', type=float, default=180)
    judge.add_argument('--concurrency', type=int, default=8)
    args = parser.parse_args()
    if args.command == 'project':
        if not args.stage_admitted or (args.stage == 'holdout' and not args.open_holdout):
            parser.error('separate stage opening required; no QA opened')
        if args.qa.name != 'MultiHopRAG.json' or args.corpus.name != 'corpus.json':
            raise ValueError('exact frozen original dataset filenames required')
        if baseline.file_hash(args.roster) != baseline.file_hash(HERE.parent / 'split-freeze.json'):
            raise ValueError('unchanged frozen roster required')
        qa = baseline.read_source(args.qa.parent, args.qa.name)
        corpus = baseline.read_source(args.corpus.parent, args.corpus.name)
        solver, questions = question_projections(qa, corpus, json.loads(args.roster.read_text()), args.stage)
        args.out.mkdir(parents=True, exist_ok=False)
        baseline.write_new(args.out / 'solver-questions.json', solver)
        baseline.write_new(args.out / 'judge-questions.json', questions)
        baseline.write_new(args.out / 'projection-receipt.json', {'format': 'cairn-public-question-projection/v1', 'stage': args.stage,
            'qa_sha256': baseline.file_hash(args.qa), 'corpus_sha256': baseline.file_hash(args.corpus),
            'roster_sha256': baseline.file_hash(args.roster), 'solver_questions_sha256': baseline.file_hash(args.out / 'solver-questions.json'),
            'judge_questions_sha256': baseline.file_hash(args.out / 'judge-questions.json'), 'adapter_source_sha256': baseline.file_hash(Path(__file__))})
    elif args.command == 'blind':
        sys.path.insert(0, str(HERE.parents[2] / 'quality'))
        from durable_ledger import verify_run_ledgers, read_jsonl
        config_path = args.run_dir / 'run-config.json'
        if baseline.file_hash(config_path) != args.run_config_sha256 or baseline.file_hash(args.projection_receipt) != args.projection_receipt_sha256:
            raise ValueError('solver config/projection pin mismatch')
        config = json.loads(config_path.read_text())
        projection = json.loads(args.projection_receipt.read_text())
        if config['projection_receipt_sha256'] != args.projection_receipt_sha256 or projection['judge_questions_sha256'] != args.judge_questions_sha256:
            raise ValueError('judge questions do not bind the actual solver projection')
        if args.results.resolve() != (args.run_dir / 'results.jsonl').resolve() or args.contexts.resolve() != (args.run_dir / 'contexts.json').resolve():
            raise ValueError('judge must consume actual frozen solver run files')
        events = read_jsonl(args.run_dir / 'requests.jsonl')
        verify_run_ledgers(args.run_dir, sum(row.get('event') == 'request' for row in events), config['job_order'])
        if config['contexts_sha256'] != args.contexts_sha256:
            raise ValueError('judge contexts differ from actual solver snapshot')
        for path, expected in ((args.judge_questions, args.judge_questions_sha256), (args.results, args.results_sha256), (args.contexts, args.contexts_sha256)):
            if baseline.file_hash(path) != expected:
                raise ValueError('frozen question/result/context hash mismatch')
        if args.corpus.name != 'corpus.json':
            raise ValueError('exact frozen original corpus required')
        corpus = baseline.read_source(args.corpus.parent, args.corpus.name)
        questions = json.loads(args.judge_questions.read_text())
        results = [json.loads(line) for line in args.results.read_text().splitlines()]
        snapshots = json.loads(args.contexts.read_text())['jobs']
        validate_solver_artifacts(results, snapshots, config)
        value, private = blind_cases(questions, results, snapshots, OriginalBindings(corpus))
        baseline.write_new(args.out, value)
        private['blind_input_sha256'] = baseline.file_hash(args.out)
        private['solver_run_config_sha256'] = args.run_config_sha256
        private['projection_receipt_sha256'] = args.projection_receipt_sha256
        baseline.write_new(args.private_map, private)
    else:
        if not args.allow_paid:
            parser.error('separate judge admission required; no model request made')
        extra = json.loads(args.extra_body.read_text())
        if extra != {'thinking': {'type': 'disabled'}}:
            raise ValueError('judge must use the frozen disabled-thinking body only')
        args.model, args.api_base, args.api_key_env = 'deepseek-flash', 'https://api.deepseek.com', 'CAIRN_LLM_API_KEY'
        args.temperature, args.validation_retries, args.limit = 0, 1, 0
        execute_judge(args)


if __name__ == '__main__':
    main()
