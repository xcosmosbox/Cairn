"""Offline, pinned official V4.1 request rendering/tokenization; no network fallback.

Runtime preparation is separate from model execution. A reviewed runtime receipt
pins the Python executable, official wheel, complete installed package inventory,
and matching tokenizer. Missing/unverified inputs fail without printing requests.
"""
import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import sys

RECIPE = "935817c66f5d6e64838f419692545f06787e485f"
TOKENIZER = "81f64d1248a68ce3663e07ab3ee48b851e5df0e32d27cb98e4c9a268151e8d99"


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def require(value):
    if not value:
        raise ValueError("unverified token runtime")


def inventory(root):
    result = {}
    for path in sorted(root.rglob("*")):
        require(not path.is_symlink())
        if path.is_file() and "__pycache__" not in path.parts and path.suffix != ".pyc":
            result[str(path.relative_to(root))] = digest(path)
    return result


def count(raw, receipt_path, receipt_sha):
    require(digest(receipt_path) == receipt_sha)
    receipt = json.loads(Path(receipt_path).read_text())
    require(receipt["recipe_commit"] == RECIPE)
    require(digest(sys.executable) == receipt["python_sha256"])
    require(digest(receipt["wheel_path"]) == receipt["wheel_sha256"])
    tokenizer_path = Path(receipt["tokenizer_path"])
    require(digest(tokenizer_path) == TOKENIZER == receipt["tokenizer_sha256"])
    root = Path(receipt["package_root"]).resolve()
    spec = importlib.util.find_spec("deepseek_recipe")
    require(spec is not None and Path(spec.origin).resolve().parent == root)
    require(inventory(root) == receipt["package_files"])
    from deepseek_recipe import (
        ChatCompletionRequest, ConversionOptions, DeepseekV41Encoding,
        ModelVersion, Tokenizer,
    )
    request = json.loads(raw)
    require(request["model"] == "deepseek-flash")
    options = ConversionOptions().with_model_alias("deepseek-flash", ModelVersion.DeepseekV41Flash)
    converted = ChatCompletionRequest(raw).convert(options)
    tokenizer = Tokenizer.from_file(str(tokenizer_path))
    encoding = DeepseekV41Encoding().with_tokenizer(tokenizer)
    tokens = len(encoding.encode(converted.conversation))
    reserved = converted.resolve_max_tokens(tokens, 1_000_000)
    require(reserved == request["max_tokens"])
    return {"input_tokens": tokens, "reserved_output_tokens": reserved,
            "context_limit": 1_000_000,
            "counter_id": "deepseek-recipe@" + RECIPE + "/v41",
            "runtime_receipt_sha256": receipt_sha}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--runtime-receipt", required=True)
    parser.add_argument("--runtime-receipt-sha256", required=True)
    args = parser.parse_args()
    try:
        print(json.dumps(count(sys.stdin.buffer.read(), args.runtime_receipt,
                               args.runtime_receipt_sha256), separators=(",", ":")))
    except Exception:
        print("Official token runtime unavailable or request does not fit", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
