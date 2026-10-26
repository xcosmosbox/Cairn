"""Real urllib redirect processing against an in-memory wire; zero sockets."""
from contextlib import redirect_stdout
from email.message import Message
import io
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock
from urllib.request import BaseHandler, Request, build_opener
from urllib.response import addinfourl

import safe_http
import agent_eval_native as native
import grade
from durable_ledger import JSONLedger, read_jsonl


class MemoryWire(BaseHandler):
    handler_order = 1
    def __init__(self, target=None, status=302):
        self.target, self.status, self.sent = target, status, []
    def https_open(self, request):
        self.sent.append(request)
        headers = Message()
        if self.target is not None and len(self.sent) == 1:
            headers['Location'] = self.target
            response = addinfourl(io.BytesIO(b'synthetic redirect body'), headers, request.full_url, self.status)
            response.msg = 'Redirect'
            return response
        response = addinfourl(io.BytesIO(b'normal bytes'), headers, request.full_url, 200)
        response.msg = 'OK'
        return response
    http_open = https_open


class SafeHTTPTests(unittest.TestCase):
    def setUp(self):
        self.network = mock.patch('socket.create_connection', side_effect=AssertionError('network forbidden'))
        self.network.start(); self.addCleanup(self.network.stop)

    def opener(self, wire):
        return mock.patch('safe_http.build_opener', side_effect=lambda *handlers: build_opener(wire, *handlers))

    def test_post_same_host_cross_host_and_downgrade_redirects_never_forward(self):
        for status in (300, 301, 302, 303, 304, 307, 308):
            for target in ('https://api.deepseek.com/other', 'https://other.invalid/chat/completions', 'http://api.deepseek.com/chat/completions', 'http://[malformed'):
                with self.subTest(status=status, target=target):
                    wire = MemoryWire(target, status)
                    request = Request('https://api.deepseek.com/chat/completions', data=b'exact synthetic JSON bytes',
                                      headers={'Authorization': 'Bearer synthetic-secret', 'Content-Type': 'application/json'}, method='POST')
                    with self.opener(wire), self.assertRaises(safe_http.RedirectRefused) as caught:
                        safe_http.urlopen(request, timeout=3)
                    self.assertEqual(caught.exception.code, status)
                    self.assertNotIn('synthetic-secret', str(caught.exception))
                    self.assertEqual(len(wire.sent), 1)
                    self.assertEqual(wire.sent[0].get_method(), 'POST')
                    self.assertEqual(wire.sent[0].data, b'exact synthetic JSON bytes')

    def test_normal_request_payload_headers_and_response_bytes_unchanged(self):
        wire = MemoryWire()
        request = Request('https://api.deepseek.com/chat/completions', data=b'{"model":"deepseek-flash"}',
                          headers={'Authorization': 'Bearer synthetic-secret', 'Content-Type': 'application/json'}, method='POST')
        with self.opener(wire), safe_http.urlopen(request, timeout=3) as response:
            self.assertEqual(response.read(), b'normal bytes')
        self.assertIs(wire.sent[0], request)
        self.assertEqual(wire.sent[0].data, b'{"model":"deepseek-flash"}')
        self.assertEqual(wire.sent[0].get_header('Authorization'), 'Bearer synthetic-secret')
        self.assertEqual(wire.sent[0].get_header('Content-type'), 'application/json')

    def test_native_redirect_status_is_receipted_then_stops_new_admission(self):
        wire = MemoryWire('https://other.invalid/', 302)
        with tempfile.TemporaryDirectory() as root:
            ledger = JSONLedger(Path(root) / 'requests.jsonl')
            client = native.NativeDeepSeekClient(native.EvalConfig(max_retries=1), 'synthetic-secret', native.AttemptCounter(), ledger)
            with self.opener(wire):
                with self.assertRaises(native.AuthorizationAbort): client.complete([], {'question_id': 'q', 'arm': 'graph', 'turn': 1}, 'required')
                with self.assertRaises(native.AuthorizationAbort): client.complete([], {'question_id': 'later', 'arm': 'graph', 'turn': 1}, 'required')
            self.assertEqual(len(wire.sent), 1)
            rows = read_jsonl(ledger.path)
            self.assertEqual([row['event'] for row in rows], ['request', 'response'])
            self.assertEqual(rows[1]['http_status'], 302)
            self.assertTrue(rows[1]['redirect_refused'])
            self.assertTrue(rows[1]['usage_unknown'])
            ledger.close()

    def test_legacy_direct_judge_fences_redirect_without_injected_hook(self):
        wire = MemoryWire('http://other.invalid/', 303)
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            blind = root / 'blind.json'; blind.write_text(json.dumps({'cases': [{'blind_id': 'b0', 'answer': 'synthetic'}, {'blind_id': 'b1', 'answer': 'synthetic'}]}))
            args = SimpleNamespace(blind=blind, out=root / 'grades.jsonl', usage=root / 'usage.jsonl', extra_body=None,
                model='deepseek-flash', api_base='https://api.deepseek.com', temperature=0, max_tokens=100,
                max_calls=4, max_total_tokens=100000, max_input_chars=10000, timeout=3,
                validation_retries=1, api_key_env='SYNTHETIC_JUDGE_KEY', limit=0, concurrency=1)
            with mock.patch.dict('os.environ', {'SYNTHETIC_JUDGE_KEY': 'synthetic-secret'}), self.opener(wire), redirect_stdout(io.StringIO()):
                status = grade.judge(args)
                self.assertTrue(status['endpoint_credential_unavailable'])
                self.assertEqual(status['collected_outcomes'], 2)
            self.assertEqual(len(wire.sent), 1)
            events = read_jsonl(args.usage)
            self.assertEqual([row['event'] for row in events], ['request', 'response'])
            self.assertEqual(events[1]['http_status'], 303)
