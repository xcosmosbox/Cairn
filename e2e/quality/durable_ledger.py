"""Short-lived append descriptors and independent result/HTTP ledger checks.

The original baseline ledger anomaly has an unknown cause. These checks defend
against observable replacement/truncation; they do not assert filesystem history.
An integrity error is terminal for this ledger and blocks subsequent requests.
The affected record is saved separately for audit, never presented as a verified
pre-request ledger event.
"""
from __future__ import annotations

import json
import os
import tempfile
import threading
from pathlib import Path

from retrieval import json_text, sha256_file


class LedgerIntegrityError(RuntimeError):
    pass


class JSONLedger:
    def __init__(self, path: Path):
        self.path = Path(path)
        self.lock = threading.Lock()
        self.closed = False
        self.failure = None
        with self.path.open("xb") as stream:
            stream.flush()
            os.fsync(stream.fileno())
            stat = os.fstat(stream.fileno())
            self.identity = (stat.st_dev, stat.st_ino)
        self.size = 0
        self._sync_directory()

    def _sync_directory(self):
        descriptor = os.open(self.path.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(descriptor)
        finally:
            os.close(descriptor)

    def _check(self, stat, expected_size):
        if (stat.st_dev, stat.st_ino) != self.identity:
            raise LedgerIntegrityError("ledger path identity changed")
        if stat.st_size != expected_size:
            raise LedgerIntegrityError("ledger file size changed unexpectedly")

    def _preserve_failure(self, error, record):
        self.failure = str(error)
        receipt = {"format": "cairn-ledger-integrity-failure/v1", "ledger": self.path.name,
                   "error": self.failure, "expected_bytes_before_append": self.size,
                   "pre_request_persistence_verified": False,
                   "record": record,
                   "note": "Separate recovery record; the primary append may be absent or partial. A failed request append prevents its network call."}
        descriptor, filename = tempfile.mkstemp(prefix=self.path.stem + "-ledger-failure-", suffix=".json", dir=self.path.parent)
        with os.fdopen(descriptor, "w", encoding="utf-8") as stream:
            stream.write(json_text(receipt) + "\n")
            stream.flush()
            os.fsync(stream.fileno())
        self._sync_directory()
        raise LedgerIntegrityError(f"{self.failure}; audit record: {Path(filename).name}")

    def append(self, record: dict):
        data = (json_text(record) + "\n").encode("utf-8")
        with self.lock:
            if self.closed:
                raise LedgerIntegrityError("ledger is closed")
            descriptor = None
            try:
                if self.failure:
                    raise LedgerIntegrityError(self.failure)
                # No retained descriptor, no O_CREAT after initialization, and
                # no symlink following: a missing/replaced path fails closed.
                descriptor = os.open(self.path, os.O_WRONLY | os.O_APPEND | os.O_NOFOLLOW)
                self._check(os.fstat(descriptor), self.size)
                self._check(self.path.stat(follow_symlinks=False), self.size)
                remaining = memoryview(data)
                while remaining:
                    written = os.write(descriptor, remaining)
                    if written <= 0:
                        raise OSError("ledger append made no progress")
                    remaining = remaining[written:]
                os.fsync(descriptor)
                expected_size = self.size + len(data)
                self._check(os.fstat(descriptor), expected_size)
                self._check(self.path.stat(follow_symlinks=False), expected_size)
                self.size = expected_size
            except (OSError, LedgerIntegrityError) as error:
                self._preserve_failure(error, record)
            finally:
                if descriptor is not None:
                    os.close(descriptor)

    def verify(self):
        with self.lock:
            try:
                if self.failure:
                    raise LedgerIntegrityError(self.failure)
                self._check(self.path.stat(follow_symlinks=False), self.size)
            except (OSError, LedgerIntegrityError) as error:
                self._preserve_failure(error, None)

    def close(self):
        # Appends close/fsync their descriptor immediately; close is idempotent.
        with self.lock:
            self.closed = True


REQUEST_FIELDS = ("question_id", "arm", "turn", "http_attempt", "attempt_for_turn", "started_at",
                  "request_sha256", "request_bytes", "request_max_tokens", "model", "thinking",
                  "tool_choice", "protocol", "retry_of_http_attempt")


def read_jsonl(path: Path) -> list[dict]:
    data = path.read_bytes()
    if data and not data.endswith(b"\n"):
        raise LedgerIntegrityError(f"incomplete trailing ledger row: {path.name}")
    rows = [json.loads(line) for line in data.splitlines()]
    if any(not isinstance(row, dict) for row in rows):
        raise LedgerIntegrityError(f"non-object ledger row: {path.name}")
    return rows


def verify_run_ledgers(output_dir: Path, expected_attempts: int, expected_jobs: list[dict]) -> dict:
    """Require the on-disk events and completed-result responses to agree.

    The check does not repair or reconstruct anything. Every request must occur
    before its response in the observed event sequence, and every model_calls
    response must be exactly present once in the HTTP ledger.
    """
    events = read_jsonl(output_dir / "requests.jsonl")
    results = read_jsonl(output_dir / "results.jsonl")
    requests, responses, calls, jobs = {}, {}, {}, set()
    for row in events:
        kind, attempt = row.get("event"), row.get("http_attempt")
        if type(attempt) is not int or attempt < 1 or kind not in ("request", "response"):
            raise LedgerIntegrityError("invalid HTTP ledger event identity")
        destination = requests if kind == "request" else responses
        if attempt in destination:
            raise LedgerIntegrityError("duplicate HTTP ledger event")
        if kind == "response" and attempt not in requests:
            raise LedgerIntegrityError("response has no earlier persisted request event")
        destination[attempt] = row
    expected_ids = set(range(1, expected_attempts + 1))
    if set(requests) != expected_ids or set(responses) != expected_ids:
        raise LedgerIntegrityError("request/response ledger attempt coverage mismatch")
    for row in results:
        identity = (row["question_id"], row["arm"])
        if identity in jobs:
            raise LedgerIntegrityError("duplicate result job")
        jobs.add(identity)
        for call in row.get("model_calls", []):
            attempt = call.get("http_attempt")
            if type(attempt) is not int or attempt < 1 or attempt in calls or (call.get("question_id"), call.get("arm")) != identity:
                raise LedgerIntegrityError("duplicate or misattributed result model call")
            calls[attempt] = call
    scheduled = {(row["question_id"], row["arm"]) for row in expected_jobs}
    if len(scheduled) != len(expected_jobs) or jobs != scheduled or len(results) != len(expected_jobs):
        raise LedgerIntegrityError("completed result jobs do not match scheduled jobs")
    if set(calls) != expected_ids:
        raise LedgerIntegrityError("result model-call attempt coverage mismatch")
    for attempt in sorted(expected_ids):
        if json_text(calls[attempt]) != json_text(responses[attempt]):
            raise LedgerIntegrityError("response ledger disagrees with result model call")
        for field in REQUEST_FIELDS:
            if json_text(requests[attempt].get(field)) != json_text(responses[attempt].get(field)):
                raise LedgerIntegrityError(f"request/response metadata disagrees: {field}")
    return {"format": "cairn-ledger-integrity/v1", "status": "verified", "reconstructed_events": 0,
            "http_attempts": expected_attempts, "request_events": len(requests),
            "response_events": len(responses), "result_model_calls": len(calls),
            "completed_jobs": len(jobs), "all_responses_have_prior_request_event": True,
            "response_records_match_results_exactly": True,
            "source_sha256": {name: sha256_file(output_dir / name)
                              for name in ("requests.jsonl", "results.jsonl", "run-config.json")}}
