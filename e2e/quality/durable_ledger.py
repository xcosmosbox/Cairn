"""Raw request/outcome JSONL records with an in-memory fallback on I/O failure."""
from __future__ import annotations
import copy
import json
import threading
from pathlib import Path
from retrieval import json_text, sha256_file

class LedgerIntegrityError(RuntimeError):
    pass

class JSONLedger:
    def __init__(self, path: Path, *, resume=False):
        self.path, self.lock = Path(path), threading.Lock()
        self.closed, self.failure = False, None
        self.initial_records = read_jsonl(self.path) if resume and self.path.exists() else []
        self.records = copy.deepcopy(self.initial_records)
        try:
            with self.path.open("a" if resume else "x", encoding="utf-8"):
                pass
        except FileExistsError:
            raise
        except OSError as error:
            self.failure = type(error).__name__

    def append(self, record):
        with self.lock:
            row = copy.deepcopy(record)
            self.records.append(row)
            try:
                if self.closed:
                    raise OSError("ledger closed")
                with self.path.open("a", encoding="utf-8") as stream:
                    stream.write(json_text(row) + "\n")
            except OSError as error:
                self.failure = type(error).__name__
                row["persistence_unavailable"] = self.failure
        return row

    def close(self):
        self.closed = True


def parse_jsonl(data: bytes, name: str) -> list[dict]:
    if data and not data.endswith(b"\n"):
        raise LedgerIntegrityError(f"incomplete trailing ledger row: {name}")
    try:
        rows = [json.loads(line) for line in data.splitlines()]
    except (ValueError, UnicodeError) as exc:
        raise LedgerIntegrityError(f"invalid ledger JSON: {name}") from exc
    if any(not isinstance(row, dict) for row in rows):
        raise LedgerIntegrityError(f"non-object ledger row: {name}")
    return rows


def read_jsonl(path: Path) -> list[dict]:
    return parse_jsonl(path.read_bytes(), path.name)
