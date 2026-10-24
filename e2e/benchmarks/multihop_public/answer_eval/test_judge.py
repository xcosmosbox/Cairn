import copy
from pathlib import Path
import sys
import unittest

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE)); sys.path.insert(0, str(HERE.parents[2] / 'quality'))
import grade
from adapter import ARMS, OriginalBindings, SourceBindingError, canonical, digest, pack_context
from prepare_judge import blind_cases, fact_units, validate_adapted_verdict, validate_visible
from summarize import score_pairs
from test_adapter import document


class JudgeTests(unittest.TestCase):
    def fixture(self, metadata_only=False, null=False):
        body = 'Alpha uses exactly two workers.'
        corpus = [document(body=body)]
        bindings = OriginalBindings(corpus)
        identity = next(iter(bindings.documents))
        question = 'How many workers does Alpha use?' if not null else 'What is an unavailable fact?'
        qid = digest(question)
        q = {'question_id': qid, 'question': question, 'question_type': 'inference_query' if not null else 'null_query',
             'group_hash': 'synthetic-group', 'answerable': not null, 'expected_answer': 'two workers',
             'fact_units': [] if null else fact_units({'evidence_list': [{'url': corpus[0]['url'], 'fact': body}]}, bindings)}
        block = {'id': 'e', 'text': 'Unrelated material.' if metadata_only else body, 'metadata': {'title': body} if metadata_only else {},
                 'sources': [bindings.original(identity)], 'relations': [], 'representation': 'generated_graph'}
        context = pack_context([block])
        answer = {'answer': '' if null else 'Alpha uses two workers.', 'abstain': null, 'citations': [] if null else ['e'], 'quotes': []}
        results = [{'question_id': qid, 'arm': arm, 'status': 'ok', **answer, 'candidate': answer,
                    'context_sha256': context['context_sha256'], 'question_sha256': digest(question)} for arm in ARMS]
        snapshots = [{'question_id': qid, 'arm': arm, 'context': context, 'retrieval_status': 'ok'} for arm in ARMS]
        blind, private = blind_cases([q], results, snapshots, bindings)
        return bindings, q, context, results, snapshots, blind, private

    def verdict(self, case):
        quote = 'Alpha uses exactly two workers.' if case['answer'].startswith('{') else 'two workers'
        return {'fact_verdicts': [{'fact_index': 0, 'status': 'met', 'answer_quote': quote, 'evidence_refs': ['gold:1'], 'reason': 'Synthetic complete entailment.'}],
                'claim_verdicts': [{'claim': 'Alpha uses two workers.', 'answer_quote': 'two workers', 'support': 'supported', 'evidence_refs': ['gold:1'],
                                    'reason': 'Pinned original supports claim.', 'retrieval_support': 'supported', 'retrieved_refs': ['retrieved:1'],
                                    'retrieved_quotes': [{'ref': 'retrieved:1', 'quote': 'Alpha uses exactly two workers.'}], 'retrieval_reason': 'Shown substantive text supports claim.'}],
                'forbidden_claim_verdicts': [], 'abstention': {'appropriate': False, 'unnecessary': False, 'reason': 'Answerable fixture.'},
                'overall': {'rating': 'correct', 'reason': 'Synthetic correct.', 'gold_ambiguity': ''}}

    def test_separate_positive_case_targets_and_no_partial_fact_credit(self):
        _, _, context, _, _, blind, private = self.fixture()
        records = []
        for case in blind['cases']:
            meta = private['mapping'][case['blind_id']]
            if meta['case_kind'] == 'answer': self.assertEqual(case['required_facts'], ['two workers'])
            else: self.assertEqual(case['answer'], context['serialized'])
            verdict = self.verdict(case)
            grade.validate_verdict(case, verdict); validate_adapted_verdict(case, verdict, meta)
            if meta['case_kind'] == 'evidence': verdict['fact_verdicts'][0]['status'] = 'partial'
            records.append({'blind_id': case['blind_id'], 'status': 'ok', 'verdict': verdict})
        scores = score_pairs(private, blind, records)
        self.assertTrue(all(row['grounded_correct'] == 1 for row in scores))
        self.assertTrue(all(row['visible_supported_fact_recall'] == 0 for row in scores))

    def test_metadata_only_fact_and_uncited_answer_support_are_rejected(self):
        *_, blind, private = self.fixture(metadata_only=True)
        for case in blind['cases']:
            meta = private['mapping'][case['blind_id']]
            if meta['case_kind'] == 'evidence':
                with self.assertRaises(SourceBindingError): validate_adapted_verdict(case, self.verdict(case), meta)
        *_, blind, private = self.fixture()
        case = next(case for case in blind['cases'] if private['mapping'][case['blind_id']]['case_kind'] == 'answer')
        meta = copy.deepcopy(private['mapping'][case['blind_id']]); meta['allowed_retrieved_refs'] = []
        with self.assertRaises(SourceBindingError): validate_adapted_verdict(case, self.verdict(case), meta)

    def test_null_is_mechanical_without_judge_or_fabricated_fact(self):
        *_, blind, private = self.fixture(null=True)
        self.assertEqual(blind['cases'], [])
        scores = score_pairs(private, blind, [])
        self.assertTrue(all(row['null_success'] == 1 and row['attempt'] == 0 for row in scores))
        private['pairs'][0]['mechanical']['solver_ok'] = False
        scores = score_pairs(private, blind, [])
        self.assertEqual(scores[0]['attempt'], 1)
        self.assertEqual(scores[0]['null_success'], 0)

    def test_unresolved_or_ambiguous_judge_stays_zero_and_blocks_acceptance(self):
        *_, blind, private = self.fixture()
        records = [{'blind_id': case['blind_id'], 'status': 'ok', 'verdict': self.verdict(case)} for case in blind['cases']]
        records[0]['verdict']['overall']['gold_ambiguity'] = 'Unresolved synthetic ambiguity.'
        scores = score_pairs(private, blind, records)
        self.assertTrue(any(not row['judge_resolved'] and row['grounded_correct'] == 0 for row in scores))
        self.assertTrue(any(not row['judge_resolved'] for row in score_pairs(private, blind, records[:-1])))

    def test_fact_units_dedup_original_url_and_fact_and_snapshot_binding(self):
        a, b = document(), document(url='https://synthetic.test/b')
        bindings = OriginalBindings([a, b])
        units = fact_units({'evidence_list': [{'url': a['url'], 'fact': 'alpha'}, {'url': a['url'], 'fact': 'alpha'}, {'url': b['url'], 'fact': 'alpha'}]}, bindings)
        self.assertEqual(len(units), 2)
        binding, _, context, *_ = self.fixture()
        broken = copy.deepcopy(context); broken['context_sha256'] = 'wrong'
        with self.assertRaises(SourceBindingError): validate_visible(broken, binding)

    def test_zero_judge_phase_creates_empty_ledgers_without_provider_key(self):
        import json
        import tempfile
        from types import SimpleNamespace
        from unittest import mock
        from prepare_judge import execute_judge, baseline
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            blind = root / 'blind.json'; private = root / 'private.json'
            blind.write_text(json.dumps({'cases': [], 'adapter_source_sha256': baseline.file_hash(HERE / 'prepare_judge.py')}))
            private.write_text(json.dumps({'mapping': {}, 'blind_input_sha256': baseline.file_hash(blind)}))
            args = SimpleNamespace(blind=blind, private_map=private, blind_sha256=baseline.file_hash(blind),
                                   private_map_sha256=baseline.file_hash(private), out=root / 'grades.jsonl', usage=root / 'usage.jsonl')
            with mock.patch.dict('os.environ', {}, clear=True), mock.patch('grade.urlopen', side_effect=AssertionError('no network')):
                execute_judge(args)
            self.assertEqual(args.out.read_bytes(), b'')
            self.assertEqual(args.usage.read_bytes(), b'')

    def test_structural_visibility_cannot_earn_fact_or_citation_credit(self):
        bindings, q, _, results, snapshots, _, _ = self.fixture()
        block = bindings.graph_block({'id': 'e', 'body': 'Alpha uses exactly two workers.',
                                      'label': 'Subdomain', 'sources': []})
        context = pack_context([block])
        validate_visible(context, bindings)
        for row in results:
            row.update(status='error', context_sha256=context['context_sha256'])
        for row in snapshots:
            row['context'] = context
        blind, private = blind_cases([q], results, snapshots, bindings)
        self.assertEqual(len(blind['cases']), 2)
        self.assertTrue(all(row['mechanical']['invalid_citations'] == 1 for row in private['pairs']))
        for case in blind['cases']:
            meta = private['mapping'][case['blind_id']]
            self.assertEqual(meta['case_kind'], 'evidence')
            self.assertIn('structural_unbound', case['answer'])
            self.assertEqual(meta['retrieved_ref_docs'], {'retrieved:1': []})
            with self.assertRaises(SourceBindingError):
                validate_adapted_verdict(case, self.verdict(case), meta)
        scores = score_pairs(private, blind, [])
        self.assertTrue(all(row['visible_supported_fact_recall'] == 0 and row['grounded_correct'] == 0 for row in scores))
        forged = copy.deepcopy(block)
        forged['metadata']['label'] = 'Entity'
        with self.assertRaises(SourceBindingError):
            validate_visible(pack_context([forged]), bindings)
