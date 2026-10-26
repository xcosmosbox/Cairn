"""Mixed synthetic outcomes preserve all intended pairs; no sockets or model calls."""
import copy
import io
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock
import run as evaluator
import grade
import prepare_judge
import summarize
from adapter import ARMS, construction_gate, reported_usage
import test_run

class Response(io.BytesIO):
    status = 200
    def __enter__(self): return self
    def __exit__(self, *args): self.close()

class DiagnosticTests(unittest.TestCase):
    def test_missing_usage_bad_answer_later_success_and_pair_denominators(self):
        binding, base = test_run.RunTests().fixture(ARMS[0])
        jobs = []
        for i in range(3):
            for arm in ARMS:
                job = copy.deepcopy(base)
                job.update(question=f'Question {i}', question_id=evaluator.baseline.query_id(f'Question {i}'), arm=arm)
                job['question_sha256'] = evaluator.digest(job['question'])
                jobs.append(job)
        counter = [0]
        def send(request, timeout):
            i = counter[0]; counter[0] += 1
            content = json.dumps({'answer': 'alpha', 'abstain': False, 'citations': ['invented' if i == 2 else 'e'], 'quotes': []})
            return Response(json.dumps({'choices': [{'finish_reason': 'stop', 'message': {'content': content}}]}).encode())
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            request_ledger, result_ledger = evaluator.JSONLedger(root/'requests.jsonl'), evaluator.JSONLedger(root/'results.jsonl')
            config = evaluator.EvalConfig(max_retries=0)
            client = evaluator.NativeDeepSeekClient(config, 'synthetic-key', evaluator.AttemptCounter(), request_ledger, result_ledger)
            with mock.patch('agent_eval_native.urlopen', side_effect=send):
                values = evaluator.solve_jobs(jobs, client, config, binding, workers=1)
            self.assertEqual(len(values), 6)
            self.assertEqual(counter[0], 6)
            self.assertEqual([row['status'] for row in values], ['ok','ok','error','ok','ok','ok'])
            self.assertEqual(len(result_ledger.records), 6)
            self.assertIn('request_body', request_ledger.records[0])
            self.assertIn('response_body', request_ledger.records[1])
            usage = reported_usage(request_ledger.records)
            self.assertEqual(usage['responses_without_complete_usage'], 6)
            self.assertIsNone(usage['reported_prompt_tokens'])

    def test_bad_source_context_is_local_and_later_contexts_snapshot(self):
        class Adapters:
            def retrieve(self, question, arm):
                if question == 'bad' and arm == ARMS[1]:
                    raise evaluator.SourceBindingError('wrong original')
                return evaluator.pack_context([]) if hasattr(evaluator, 'pack_context') else __import__('adapter').pack_context([])
        with tempfile.TemporaryDirectory() as root:
            jobs = evaluator.snapshot_contexts([{'question_id':'q1','question':'bad'}, {'question_id':'q2','question':'later'}], Adapters(), root)
            self.assertEqual(len(jobs), 4)
            self.assertEqual(sum(j['retrieval_status']=='error' for j in jobs), 1)

    def test_partial_construction_and_diagnostic_roster(self):
        audit = {'pass': False, 'errors':['one failed'], 'db_sha256':'sha', 'documents':[
            {'doc_id':'a','members':['m','n'],'materialized_members':['m']},
            {'doc_id':'b','members':[],'materialized_members':[]}]}
        value = construction_gate(audit, ['a','b'], 'sha')
        self.assertEqual(value['fraction'], .5)
        self.assertEqual(value['missing_documents'], ['b'])
        frozen = json.loads((Path(__file__).parent.parent/'split-freeze.json').read_text())
        qid = frozen['selected']['dev'][0]['qid']
        self.assertEqual(prepare_judge.validate_roster({'selected':{'dev':[{'qid':qid}]}}, 'dev'), {qid})
        with self.assertRaises(ValueError): prepare_judge.validate_roster(frozen, 'holdout')

    def test_judge_unknown_usage_oversize_bad_verdict_then_later_success(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            cases = [{'blind_id':'b0'}, {'blind_id':'b1','answer':'x'*4000}, {'blind_id':'b2'}, {'blind_id':'b3'}]
            blind = root/'blind.json'; blind.write_text(json.dumps({'cases':cases}))
            args = SimpleNamespace(blind=blind,out=root/'grades.jsonl',usage=root/'usage.jsonl',extra_body=None,
                model='deepseek-flash',api_base='https://api.deepseek.com',temperature=0,max_tokens=4096,
                max_input_chars=2000,timeout=3,api_key_env='TEST_KEY',limit=0,concurrency=1)
            n = [0]
            def send(request, timeout):
                n[0] += 1
                return Response(json.dumps({'choices':[{'finish_reason':'stop','message':{'content':'{}'}}]}).encode())
            def validate(case, verdict):
                if case['blind_id']=='b2': raise ValueError('malformed evidence quote')
                return verdict
            with mock.patch.dict('os.environ',{'TEST_KEY':'synthetic-key'}), mock.patch('grade.scores',return_value={}):
                status = grade.judge(args,transport=send,verdict_validator=validate)
            rows = [json.loads(line) for line in args.out.read_text().splitlines()]
            self.assertEqual([row['status'] for row in rows], ['ok','error','error','ok'])
            self.assertEqual(status['collected_outcomes'], 4)
            self.assertEqual(n[0], 3)
            self.assertIsNone(rows[-1]['accounted_tokens'])

    def test_small_diagnostic_needs_no_formal_statistics(self):
        import test_summarize
        scores = test_summarize.fixture()[:4]
        summary, weights = summarize.analyze(scores)
        self.assertTrue(summary['diagnostic'])
        self.assertIsNone(weights)
        self.assertEqual(summary['intended_arm_slots'], 4)
