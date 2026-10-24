import io
import json
from pathlib import Path
import sys
import tempfile
import threading
import copy
import unittest
import urllib.error
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
import run as evaluator
from adapter import OriginalBindings, digest, pack_context
from test_adapter import document


class RunTests(unittest.TestCase):
    def fixture(self, arm):
        binding = OriginalBindings([document()])
        identity = next(iter(binding.documents))
        block = {'id': 'e', 'text': 'alpha beta gamma', 'metadata': {}, 'sources': [binding.original(identity)],
                 'relations': [], 'representation': 'generated_graph'}
        question = 'Synthetic alpha?'
        return binding, {'question_id': evaluator.baseline.query_id(question), 'question': question,
                         'question_sha256': digest(question), 'arm': arm, 'context': pack_context([block]), 'retrieval_status': 'ok'}

    def test_only_one_identical_json_answer_request_and_no_tools(self):
        binding, raw = self.fixture(evaluator.ARMS[0]); _, graph = self.fixture(evaluator.ARMS[1])
        config = evaluator.EvalConfig(max_retries=0)
        self.assertEqual(evaluator.payload_for(raw, config), evaluator.payload_for(graph, config))
        payload = evaluator.payload_for(raw, config)
        self.assertNotIn('tools', payload)
        self.assertEqual(payload['max_tokens'], 4096)
        self.assertEqual(payload['thinking'], {'type': 'disabled'})
        self.assertEqual(len(payload['messages']), 2)

    def test_solver_projection_rejects_gold_and_preserves_hash(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / 'questions.json'
            question = 'Synthetic alpha?'
            value = {'question_id': evaluator.baseline.query_id(question), 'question': question}
            path.write_text(json.dumps([value]))
            self.assertEqual(evaluator.solver_questions(path), [value])
            path.write_text(json.dumps([{**value, 'answer': 'gold forbidden'}]))
            with self.assertRaises(ValueError): evaluator.solver_questions(path)

    def test_401_stops_admission_but_every_paired_job_gets_failure_result(self):
        binding, raw = self.fixture(evaluator.ARMS[0]); _, graph = self.fixture(evaluator.ARMS[1])
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            requests, results = evaluator.JSONLedger(root / 'requests.jsonl'), evaluator.JSONLedger(root / 'results.jsonl')
            config = evaluator.EvalConfig(max_retries=0)
            client = evaluator.NativeDeepSeekClient(config, 'synthetic-secret', evaluator.CallBudget(2), requests, results)
            failure = urllib.error.HTTPError('https://api.deepseek.com/chat/completions', 401, 'unauthorized', {}, io.BytesIO(b'private provider body'))
            with mock.patch('agent_eval_native.urlopen', side_effect=failure) as send:
                values = evaluator.solve_jobs([raw, graph], client, config, binding, workers=1)
            self.assertEqual(send.call_count, 1)
            self.assertEqual([value['status'] for value in values], ['error', 'error'])
            self.assertEqual([len(value['model_calls']) for value in values], [1, 0])
            self.assertEqual(len(results.path.read_text().splitlines()), 2)
            requests.close(); results.close()

    def test_duplicate_json_key_is_not_repaired(self):
        with self.assertRaises(ValueError): evaluator.strict_json('{"answer":"a","answer":"b"}')

    def concurrent_fixture(self):
        binding, raw = self.fixture(evaluator.ARMS[0])
        jobs = []
        for i in range(5):
            for arm in evaluator.ARMS:
                row = copy.deepcopy(raw)
                row.update(question='Synthetic alpha%d?' % i, arm=arm)
                row['question_id'] = evaluator.baseline.query_id(row['question'])
                row['question_sha256'] = digest(row['question'])
                jobs.append(row)
        return binding, jobs

    def test_concurrent_401_drains_two_admitted_calls_and_blocks_queue(self):
        self.concurrent_failure_case('authorization')

    def test_concurrent_ledger_failure_drains_usage_and_every_case_result(self):
        self.concurrent_failure_case('ledger')

    def concurrent_failure_case(self, failure_kind):
        binding, jobs = self.concurrent_fixture()
        class Response(io.BytesIO):
            status = 200
            def __enter__(self): return self
            def __exit__(self, *args): self.close()
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            requests, results = evaluator.JSONLedger(root / 'requests.jsonl'), evaluator.JSONLedger(root / 'results.jsonl')
            client = evaluator.NativeDeepSeekClient(evaluator.EvalConfig(max_retries=0), 'synthetic-secret', evaluator.CallBudget(len(jobs)), requests, results)
            both, failed = threading.Event(), threading.Event()
            lock, calls = threading.Lock(), []
            old_stop, old_fail, old_append = client.durability.stop_authorization, client.durability.fail, requests.append
            def stop(status):
                old_stop(status); failed.set()
            def fail(error):
                old_fail(error); failed.set()
            client.durability.stop_authorization, client.durability.fail = stop, fail
            first_response = [True]
            def append(row):
                if failure_kind == 'ledger' and row.get('event') == 'response' and first_response[0]:
                    first_response[0] = False
                    raise OSError('injected response persistence failure')
                return old_append(row)
            answer = json.dumps({'answer': 'alpha', 'abstain': False, 'citations': ['e'], 'quotes': []})
            payload = json.dumps({'model': 'deepseek-flash', 'usage': {'prompt_tokens': 3, 'completion_tokens': 4},
                                  'choices': [{'finish_reason': 'stop', 'message': {'content': answer}}]}).encode()
            def send(request, timeout):
                with lock:
                    index = len(calls); calls.append(index)
                    if len(calls) == 2: both.set()
                if index == 0:
                    self.assertTrue(both.wait(5), 'second call was not admitted')
                    if failure_kind == 'authorization':
                        raise urllib.error.HTTPError('https://api.deepseek.com/chat/completions', 401, 'denied', {}, io.BytesIO(b''))
                    return Response(payload)
                if index == 1:
                    self.assertTrue(failed.wait(5), 'run-level stop latch did not trip')
                    return Response(payload)
                self.fail('queued request sent after global failure latch')
            with mock.patch('agent_eval_native.urlopen', side_effect=send), mock.patch.object(requests, 'append', side_effect=append):
                if failure_kind == 'ledger':
                    with self.assertRaises(RuntimeError):
                        evaluator.solve_jobs(jobs, client, client.config, binding, workers=2)
                else:
                    evaluator.solve_jobs(jobs, client, client.config, binding, workers=2)
            records = [json.loads(line) for line in results.path.read_text().splitlines()]
            self.assertEqual(len(calls), 2)
            self.assertEqual(len(records), len(jobs))
            self.assertEqual(len({(row['question_id'], row['arm']) for row in records}), len(jobs))
            self.assertEqual(sum(len(row['model_calls']) for row in records), 2)
            self.assertTrue(any(row['status'] == 'ok' for row in records), 'already admitted success was discarded')
            self.assertEqual(sum(bool(row['model_calls']) for row in records), 2)
            requests.close(); results.close()

    def test_progress_failure_is_nonfatal_and_every_concurrent_result_persists(self):
        binding, jobs = self.concurrent_fixture()
        class Response(io.BytesIO):
            status = 200
            def __enter__(self): return self
            def __exit__(self, *args): self.close()
        answer = json.dumps({'answer': 'alpha', 'abstain': False, 'citations': ['e'], 'quotes': []})
        payload = json.dumps({'model': 'deepseek-flash', 'usage': {'prompt_tokens': 1, 'completion_tokens': 2},
                              'choices': [{'finish_reason': 'stop', 'message': {'content': answer}}]}).encode()
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            requests, results = evaluator.JSONLedger(root / 'requests.jsonl'), evaluator.JSONLedger(root / 'results.jsonl')
            client = evaluator.NativeDeepSeekClient(evaluator.EvalConfig(max_retries=0), 'synthetic-secret', evaluator.CallBudget(len(jobs)), requests, results)
            with mock.patch('agent_eval_native.urlopen', side_effect=lambda request, timeout: Response(payload)):
                values = evaluator.solve_jobs(jobs, client, client.config, binding, workers=3,
                                              progress=lambda row: (_ for _ in ()).throw(BrokenPipeError('stdout closed')))
            self.assertEqual(len(values), len(jobs))
            self.assertEqual(len(results.path.read_text().splitlines()), len(jobs))
            self.assertEqual(len(client.progress_errors), len(jobs))
            self.assertEqual(len({(row['question_id'], row['arm']) for row in values}), len(jobs))
            requests.close(); results.close()

    def test_real_retained_response_rejects_edited_scoring_artifacts(self):
        from prepare_judge import validate_solver_artifacts
        import dataclasses
        binding, job = self.fixture(evaluator.ARMS[0])
        class Response(io.BytesIO):
            status = 200
            def __enter__(self): return self
            def __exit__(self, *args): self.close()
        answer = {'answer': 'alpha', 'abstain': False, 'citations': ['e'], 'quotes': []}
        payload = json.dumps({'model': 'deepseek-flash', 'usage': {'prompt_tokens': 1, 'completion_tokens': 2},
                              'choices': [{'finish_reason': 'stop', 'message': {'content': json.dumps(answer)}}]}).encode()
        with tempfile.TemporaryDirectory() as root:
            ledger = evaluator.JSONLedger(Path(root) / 'requests.jsonl')
            config = evaluator.EvalConfig(max_retries=0, max_searches=1, max_model_turns=1, total_context_chars=10000)
            client = evaluator.NativeDeepSeekClient(config, 'synthetic-secret', evaluator.CallBudget(1), ledger)
            with mock.patch('agent_eval_native.urlopen', return_value=Response(payload)):
                actual = evaluator.solve_job(job, client, config, binding)
            receipt_bytes = ledger.path.read_bytes()
            run_config = {'config': dataclasses.asdict(config), 'system_prompt_sha256': digest(evaluator.SYSTEM)}
            validate_solver_artifacts([copy.deepcopy(actual)], [job], run_config)
            mutations = [
                lambda row: row.update(answer='edited answer'),
                lambda row: row['candidate'].update(answer='edited candidate'),
                lambda row: row['candidate'].update(abstain=0),
                lambda row: row.update(abstain=0),
                lambda row: row.update(question='edited question'),
                lambda row: row.update(question_sha256='edited'),
                lambda row: row['model_calls'][0].update(request_sha256='edited'),
            ]
            for mutate in mutations:
                edited = copy.deepcopy(actual); mutate(edited)
                with self.assertRaises(ValueError): validate_solver_artifacts([edited], [job], run_config)
            wrong_snapshot = copy.deepcopy(job); wrong_snapshot['question'] = 'Other literal question'
            with self.assertRaises(ValueError): validate_solver_artifacts([copy.deepcopy(actual)], [wrong_snapshot], run_config)
            self.assertEqual(ledger.path.read_bytes(), receipt_bytes, 'artifact validation rewrote actual HTTP receipts')
            ledger.close()
