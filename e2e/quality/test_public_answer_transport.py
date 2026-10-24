"""The new payload seam preserves historical native HTTP bytes and safety."""
import hashlib
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

from agent_eval_native import NativeDeepSeekClient, EvalConfig, CallBudget, JSONLedger, TOOLS
from retrieval import json_text


class Response(io.BytesIO):
    status = 200
    def __enter__(self):
        return self
    def __exit__(self, *args):
        self.close()


class PublicPayloadTransportTests(unittest.TestCase):
    def test_existing_native_request_bytes_and_metadata_are_unchanged(self):
        with tempfile.TemporaryDirectory() as root:
            ledger = JSONLedger(Path(root) / 'requests.jsonl')
            config = EvalConfig(max_retries=0)
            client = NativeDeepSeekClient(config, 'synthetic-secret', CallBudget(1), ledger)
            messages = [{'role': 'system', 'content': 'Synthetic JSON'}, {'role': 'user', 'content': 'Synthetic question'}]
            choice = {'type': 'function', 'function': {'name': 'search'}}
            expected = json_text({'model': config.model, 'messages': messages, 'max_tokens': config.max_tokens,
                                  'tools': TOOLS, 'tool_choice': choice, 'thinking': {'type': 'disabled'}}).encode()
            body = {'model': 'deepseek-flash', 'id': 'synthetic-response', 'usage': {'prompt_tokens': 1, 'completion_tokens': 2},
                    'choices': [{'finish_reason': 'tool_calls', 'message': {'content': None, 'tool_calls': [], 'reasoning_content': 'never retained'}}]}
            def send(request, timeout):
                self.assertEqual(request.data, expected)
                return Response(json.dumps(body).encode())
            with mock.patch('agent_eval_native.urlopen', side_effect=send):
                _, attempts = client.complete(messages, {'question_id': 'synthetic', 'arm': 'graph', 'turn': 1}, choice)
            self.assertEqual(attempts[0]['request_sha256'], hashlib.sha256(expected).hexdigest())
            self.assertEqual(attempts[0]['protocol'], 'native_tool_calls/v3-batches')
            self.assertEqual(attempts[0]['tool_choice'], choice)
            self.assertNotIn('reasoning_content', attempts[0]['assistant_message'])
            ledger.close()

    def test_one_shot_json_payload_reuses_same_durable_transaction(self):
        with tempfile.TemporaryDirectory() as root:
            ledger = JSONLedger(Path(root) / 'requests.jsonl')
            config = EvalConfig(max_retries=0)
            client = NativeDeepSeekClient(config, 'synthetic-secret', CallBudget(1), ledger)
            payload = {'model': config.model, 'messages': [{'role': 'user', 'content': 'JSON'}],
                       'max_tokens': 4096, 'thinking': {'type': 'disabled'}, 'response_format': {'type': 'json_object'}}
            response = {'model': config.model, 'choices': [{'finish_reason': 'stop', 'message': {'content': '{}'}}]}
            with mock.patch('agent_eval_native.urlopen', return_value=Response(json.dumps(response).encode())):
                message, attempts = client.complete_payload(payload, {'question_id': 'q', 'arm': 'raw', 'turn': 1}, protocol='public-one-shot/v1')
            self.assertEqual(message['content'], '{}')
            self.assertEqual(len(attempts), 1)
            rows = [json.loads(line) for line in ledger.path.read_text().splitlines()]
            self.assertEqual([row['event'] for row in rows], ['request', 'response'])
            ledger.close()
