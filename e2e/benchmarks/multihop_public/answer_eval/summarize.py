"""按冻结的全局支持组抽样计算配对下界，不丢弃失败。

Frozen paired/type-balanced estimators; no QA or provider access.

PCG64 is an explicit analysis-runtime requirement. There is no alternate RNG,
Wilson substitution, pseudo-count, dropped failure or winning-route selection.
"""
from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import sys

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
sys.path.insert(0, str(HERE.parents[2] / 'quality'))
from adapter import ARMS, baseline, digest, reported_usage
from prepare_judge import POSITIVE_TYPES, validate_adapted_verdict
import grade
from durable_ledger import read_jsonl, verify_run_ledgers

REPLICATES = 20_000
POSITIVE_SEED = 20261024
NULL_SEED = 20261025


def score_pairs(private, blind, grades):
    cases = {case['blind_id']: case for case in blind['cases']}
    latest = {}
    for record in grades:
        identity = record['blind_id']
        if identity not in cases:
            raise ValueError('judge result outside frozen blind cases')
        latest[identity] = record
    output = []
    for pair in private['pairs']:
        mechanical = pair['mechanical']
        explicit_abstention = bool(mechanical['solver_ok'] and mechanical['abstain'] and mechanical['answer'] == ''
                                   and mechanical['citations_valid'] and mechanical['quotes_valid'])
        value = {key: pair[key] for key in ('question_id', 'arm', 'question_type', 'group_hash', 'answerable')}
        value.update(grounded_correct=0, visible_supported_fact_recall=0.0,
                     attempt=int(not explicit_abstention), null_success=int(not pair['answerable'] and explicit_abstention),
                     judge_resolved=True, emitted_citations=mechanical.get('emitted_citations', 0),
                     invalid_citations=mechanical.get('invalid_citations', 0), emitted_quotes=mechanical.get('emitted_quotes', 0),
                     invalid_quotes=mechanical.get('invalid_quotes', 0))
        for kind in ('answer', 'evidence'):
            identity = pair[kind + '_blind_id']
            if identity is None:
                continue  # Known mechanical failure/abstention/empty evidence =>0.
            record = latest.get(identity)
            if record is None or record.get('status') != 'ok':
                value['judge_resolved'] = False
                continue
            verdict, case = record['verdict'], cases[identity]
            try:
                grade.validate_verdict(case, verdict)
                validate_adapted_verdict(case, verdict, private['mapping'][identity])
            except (ValueError, KeyError, TypeError):
                value['judge_resolved'] = False
                continue
            if verdict['overall']['gold_ambiguity'].strip():
                value['judge_resolved'] = False
                continue
            if kind == 'evidence':
                n = len(pair['fact_unit_ids'])
                if n < 1 or len(verdict['fact_verdicts']) != n:
                    raise ValueError('fact denominator mismatch')
                value['visible_supported_fact_recall'] = sum(item['status'] == 'met' for item in verdict['fact_verdicts']) / n
            else:
                claims = [item for item in verdict['claim_verdicts'] if item['support'] != 'not_checkable']
                value['grounded_correct'] = int(mechanical['solver_ok'] and not explicit_abstention
                    and verdict['fact_verdicts'][0]['status'] == 'met'
                    and verdict['overall']['rating'] == 'correct' and bool(claims)
                    and all(item['support'] == 'supported' and item['retrieval_support'] == 'supported' for item in claims)
                    and not any(item['present'] for item in verdict['forbidden_claim_verdicts'])
                    and mechanical['citations_valid'] and mechanical['quotes_valid'])
        # Any unresolved semantic adjudication makes this case provisional0.
        if not value['judge_resolved']:
            value['grounded_correct'] = 0
            value['visible_supported_fact_recall'] = 0.0
        output.append(value)
    return output


def paired_rows(scores):
    pairs = {}
    for row in scores:
        if row['arm'] not in ARMS:
            raise ValueError('unfrozen arm')
        identity = row['question_id']
        group = pairs.setdefault(identity, {})
        if row['arm'] in group:
            raise ValueError('duplicate question/arm score')
        group[row['arm']] = row
    result = []
    for identity in sorted(pairs):
        pair = pairs[identity]
        if set(pair) != set(ARMS):
            raise ValueError('missing paired arm; no denominator deletion allowed')
        left, right = pair[ARMS[0]], pair[ARMS[1]]
        if any(left[key] != right[key] for key in ('question_type', 'group_hash', 'answerable')):
            raise ValueError('paired type/group binding mismatch')
        result.append({'question_id': identity, 'question_type': left['question_type'],
                       'group_hash': left['group_hash'], 'answerable': left['answerable'],
                       'raw': left, 'graph': right})
    return result


def _numpy():
    try:
        import numpy as np
    except ImportError:
        raise RuntimeError('frozen NumPy PCG64 runtime required; no fallback RNG') from None
    return np


def bootstrap_weights(positive, null, replicates=REPLICATES, max_draws=None):
    np = _numpy()
    groups = sorted({row['group_hash'] for row in positive})
    if not groups or not null or {row['question_type'] for row in positive} != set(POSITIVE_TYPES):
        raise ValueError('missing positive groups/types or null denominator')
    group_index = {group: i for i, group in enumerate(groups)}
    indices = np.asarray([group_index[row['group_hash']] for row in positive], dtype=np.int64)
    type_indices = [np.asarray([i for i, row in enumerate(positive) if row['question_type'] == kind], dtype=np.int64) for kind in POSITIVE_TYPES]
    rng = np.random.Generator(np.random.PCG64(POSITIVE_SEED))
    accepted, rejected, draws = [], 0, 0
    maximum = max_draws if max_draws is not None else replicates * 100
    while len(accepted) < replicates:
        draws += 1
        if draws > maximum:
            raise ValueError('finite bootstrap guard reached; no formal estimate/pass')
        weights = np.bincount(rng.integers(0, len(groups), size=len(groups)), minlength=len(groups))
        question_weights = weights[indices]
        if any(question_weights[items].sum() == 0 for items in type_indices):
            rejected += 1
            continue
        accepted.append(weights)
    positive_weights = np.asarray(accepted, dtype=np.int64)
    # Generate/store all accepted positive vectors first. The null stream is
    # independent, so adding null summaries cannot perturb the primary RNG.
    null_rng = np.random.Generator(np.random.PCG64(NULL_SEED))
    null_weights = np.asarray([np.bincount(null_rng.integers(0, len(null), size=len(null)), minlength=len(null))
                               for _ in range(replicates)], dtype=np.int64)
    return {'groups': groups, 'positive': positive_weights, 'null': null_weights,
            'indices': indices, 'type_indices': type_indices, 'rejected': rejected,
            'positive_draws': draws, 'positive_questions': [{key: row[key] for key in ('question_id', 'question_type', 'group_hash')} for row in positive],
            'null_question_ids': [row['question_id'] for row in null], 'numpy_version': np.__version__, 'pcg64_module_sha256': baseline.file_hash(Path(sys.modules[np.random.PCG64.__module__].__file__))}


def _bound(values):
    np = _numpy()
    return {'lower_one_sided_95': float(np.quantile(values, .05, method='linear')),
            'degenerate': bool(np.max(values) == np.min(values)),
            'interval_type': 'nominal percentile cluster-bootstrap; not exact finite-sample coverage'}


def analyze(scores, weights=None, replicates=REPLICATES):
    np = _numpy()
    paired = paired_rows(scores)
    positive = [row for row in paired if row['answerable']]
    null = [row for row in paired if not row['answerable']]
    if len({row['group_hash'] for row in null}) != len(null):
        raise ValueError('null singleton group identities must be unique')
    weights = weights or bootstrap_weights(positive, null, replicates)
    qw = weights['positive'][:, weights['indices']]
    nw = weights['null']
    def balanced(values, multiplicity):
        values = np.asarray(values, dtype=float)
        return sum((multiplicity[:, indices] * values[indices]).sum(axis=1) / multiplicity[:, indices].sum(axis=1)
                   for indices in weights['type_indices']) / 3
    graph_gc = np.asarray([row['graph']['grounded_correct'] for row in positive], dtype=float)
    raw_gc = np.asarray([row['raw']['grounded_correct'] for row in positive], dtype=float)
    one = np.ones((1, len(positive)), dtype=np.int64)
    difference = balanced(graph_gc - raw_gc, qw)
    result = {'format': 'cairn-public-answer-comparison/v1', 'positive_questions': len(positive), 'null_questions': len(null),
              'bootstrap': {'replicates': len(qw), 'positive_seed': POSITIVE_SEED, 'null_seed': NULL_SEED,
                            'rng': 'NumPy PCG64', 'numpy_version': weights['numpy_version'],
                            'positive_rejected_empty_type_draws': weights['rejected'], 'positive_draws': weights['positive_draws'],
                            'pcg64_module_sha256': weights['pcg64_module_sha256'], 'group_count': len(weights['groups']), 'aggregation': 'equal mean of3positive-type question means',
                            'quantile': .05, 'quantile_method': 'linear'},
              'difference': {'point': float(balanced(graph_gc - raw_gc, one)[0]), **_bound(difference)}, 'arms': {}}
    for arm in ('raw', 'graph'):
        pgc = np.asarray([row[arm]['grounded_correct'] for row in positive], dtype=float)
        recall = np.asarray([row[arm]['visible_supported_fact_recall'] for row in positive], dtype=float)
        pa = np.asarray([row[arm]['attempt'] for row in positive], dtype=float)
        na = np.asarray([row[arm]['attempt'] for row in null], dtype=float)
        ns = np.asarray([row[arm]['null_success'] for row in null], dtype=float)
        success = (qw * pgc).sum(axis=1)
        attempts = (qw * pa).sum(axis=1) + (nw * na).sum(axis=1)
        precision = np.divide(success, attempts, out=np.zeros_like(success), where=attempts > 0)
        point_attempts = float(pa.sum() + na.sum())
        result['arms'][arm] = {'grounded_correct': {'point': float(balanced(pgc, one)[0]), **_bound(balanced(pgc, qw))},
            'visible_supported_fact_recall': {'point': float(balanced(recall, one)[0]), **_bound(balanced(recall, qw))},
            'attempt_precision': {'point': float(pgc.sum() / point_attempts) if point_attempts else None,
                                  'attempts': int(point_attempts), 'undefined_replicates_assigned_zero': int((attempts == 0).sum()), **_bound(precision)},
            'null_success': {'point': float(ns.mean()), **_bound((nw * ns).sum(axis=1) / len(null))},
            'emitted_citations': sum(row[arm].get('emitted_citations', 0) for row in paired),
            'invalid_citations': sum(row[arm]['invalid_citations'] for row in paired),
            'emitted_quotes': sum(row[arm].get('emitted_quotes', 0) for row in paired),
            'invalid_quotes': sum(row[arm]['invalid_quotes'] for row in paired),
            'unresolved_judge_cases': sum(not row[arm]['judge_resolved'] for row in paired)}
    for metrics in result['arms'].values():
        for noun in ('citations', 'quotes'):
            n = metrics['emitted_' + noun]
            metrics[noun + '_no_observations'] = n == 0
            metrics[noun + '_valid_fraction'] = (n - metrics['invalid_' + noun]) / n if n else None
    formal_counts = len(positive) == 192 and len(null) == 64 and all(sum(row['question_type'] == kind for row in positive) == 64 for kind in POSITIVE_TYPES)
    evidence = result['arms']['graph']
    resolved = all(row[arm]['judge_resolved'] for row in paired for arm in ('raw', 'graph'))
    clean_citations = evidence['invalid_citations'] == 0 and evidence['invalid_quotes'] == 0
    formal = formal_counts and len(qw) == REPLICATES and resolved
    result['gates'] = {'formal_denominators': formal_counts, 'all_judges_resolved': resolved,
        'noninferiority': result['difference']['lower_one_sided_95'] > -.05,
        'intrinsic_evidence_floor': evidence['visible_supported_fact_recall']['lower_one_sided_95'] >= .30,
        'observed_citations_and_exact_quotes_100percent': clean_citations,
        'answer_demo': evidence['grounded_correct']['lower_one_sided_95'] >= .50
            and evidence['attempt_precision']['point'] is not None and evidence['attempt_precision']['point'] >= .95
            and evidence['attempt_precision']['lower_one_sided_95'] >= .90
            and evidence['null_success']['point'] >= .90 and evidence['null_success']['lower_one_sided_95'] >= .80 and clean_citations,
        'formal_statistics_ready': formal}
    result['interpretation'] = 'Infrastructure NI/evidence floor and cited-answer demo are separate. No superiority, hosted answer SLA, old exact-span39.76% comparison, or stable-release claim.'
    return result, weights


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ('private-map', 'blind', 'grades', 'usage', 'construction-audit', 'run-config', 'corpus', 'out', 'weights-out'):
        parser.add_argument('--' + name, type=Path, required=True)
    parser.add_argument('--private-map-sha256', required=True)
    parser.add_argument('--blind-sha256', required=True)
    parser.add_argument('--construction-audit-sha256', required=True)
    parser.add_argument('--run-config-sha256', required=True)
    args = parser.parse_args()
    for path, expected in ((args.private_map, args.private_map_sha256), (args.blind, args.blind_sha256), (args.construction_audit, args.construction_audit_sha256), (args.run_config, args.run_config_sha256)):
        if baseline.file_hash(path) != expected:
            raise ValueError('frozen scoring input identity mismatch')
    private, blind = json.loads(args.private_map.read_text()), json.loads(args.blind.read_text())
    if private['blind_input_sha256'] != args.blind_sha256:
        raise ValueError('blind/private map binding mismatch')
    grades, events = read_jsonl(args.grades), read_jsonl(args.usage)
    configs = {row['judge_config_sha256'] for row in grades + events if 'judge_config_sha256' in row}
    if not grades and not events:
        requests, outcomes = {}, {}  # No admission, including legitimate mechanical-only negative runs.
    else:
        if len(configs) != 1:
            raise ValueError('one frozen judge config required for nonempty judge ledgers')
        requests, outcomes = grade.validate_judge_recovery(grades, events, blind['cases'], args.blind_sha256, next(iter(configs)))
    judge_ledger_complete = set(requests) == set(outcomes)
    scores = score_pairs(private, blind, grades)
    summary, weights = analyze(scores)
    summary['judge_ledger_complete'] = judge_ledger_complete
    summary['unmatched_judge_requests'] = sorted(set(requests) - set(outcomes))
    summary['gates']['formal_statistics_ready'] = summary['gates']['formal_statistics_ready'] and judge_ledger_complete
    # Runtime materialization proof is explicit and independently tied to the
    # graph/source freeze. Never count explanations as retained incidences.
    from adapter import construction_gate
    audit = json.loads(args.construction_audit.read_text())
    run_config = json.loads(args.run_config.read_text())
    if run_config.get('construction_audit_sha256') != args.construction_audit_sha256:
        raise ValueError('construction proof differs from actual solver admission')
    if private['solver_run_config_sha256'] != args.run_config_sha256 or private['projection_receipt_sha256'] != run_config['projection_receipt_sha256']:
        raise ValueError('scoring does not bind actual solver question projection')
    solver_events = read_jsonl(args.run_config.parent / 'requests.jsonl')
    verify_run_ledgers(args.run_config.parent, sum(row.get('event') == 'request' for row in solver_events), run_config['job_order'])
    if args.corpus.name != 'corpus.json':
        raise ValueError('exact frozen original corpus required')
    from adapter import OriginalBindings
    corpus = baseline.read_source(args.corpus.parent, args.corpus.name)
    construction = construction_gate(audit, OriginalBindings(corpus).documents, run_config['kg_sha256'])
    summary['construction'] = construction
    summary['gates']['infrastructure'] = summary['gates']['formal_statistics_ready'] and summary['gates']['noninferiority'] and summary['gates']['intrinsic_evidence_floor'] and summary['gates']['observed_citations_and_exact_quotes_100percent']
    summary['gates']['answer_integration'] = summary['gates']['infrastructure'] and summary['gates']['answer_demo']
    # Store exact accepted vectors before the summary can claim its bound.
    summary['usage'] = {'solver': reported_usage(solver_events), 'judge': reported_usage(events)}
    weight_value = {'groups': weights['groups'], 'positive_questions': weights['positive_questions'], 'null_question_ids': weights['null_question_ids'], 'positive': weights['positive'].tolist(), 'null': weights['null'].tolist(),
                    'positive_seed': POSITIVE_SEED, 'null_seed': NULL_SEED, 'rejected_empty_type_draws': weights['rejected']}
    baseline.write_new(args.weights_out, weight_value)
    summary['weights_sha256'] = baseline.file_hash(args.weights_out)
    summary['phase_inputs'] = {
        'solver': {'run_config_sha256': args.run_config_sha256,
                   'request_ledger_sha256': baseline.file_hash(args.run_config.parent / 'requests.jsonl'),
                   'result_ledger_sha256': baseline.file_hash(args.run_config.parent / 'results.jsonl'),
                   'contexts_sha256': run_config['contexts_sha256'], 'projection_receipt_sha256': run_config['projection_receipt_sha256'],
                   'construction_audit_sha256': args.construction_audit_sha256, 'graph_db_sha256': run_config['kg_sha256'],
                   'raw_index_sha256': run_config['raw_index_sha256']},
        'judge': {'blind_input_sha256': args.blind_sha256, 'private_map_sha256': args.private_map_sha256,
                  'result_ledger_sha256': baseline.file_hash(args.grades), 'usage_ledger_sha256': baseline.file_hash(args.usage),
                  'judge_config_sha256': next(iter(configs)) if configs else None},
        'analysis': {'protocol_sha256': baseline.file_hash(HERE / 'protocol.json'),
                     'estimator_source_sha256': baseline.file_hash(Path(__file__)), 'weights_sha256': summary['weights_sha256']}}
    baseline.write_new(args.out, summary)


if __name__ == '__main__':
    main()
