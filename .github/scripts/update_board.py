"""Project task Issues into a hub. Never execute Issue text or change task status.

仅替换入口中的生成区；任务状态与领取记录仍由 Issue 及其评论管理。
Only a deterministic view; no worker dispatch or exclusive-lock guarantees.
"""
import json
import os
from pathlib import Path
import urllib.error
import urllib.parse
import urllib.request

BEGIN = "<!-- cairn-board:start -->"
END = "<!-- cairn-board:end -->"
STATUSES = ("backlog", "ready", "active", "review", "blocked", "done")


def cell(value):
    """Prevent titles from creating table rows or active Markdown/HTML."""
    value = " ".join(str(value).split())
    for old, new in (("&", "&amp;"), ("<", "&lt;"), (">", "&gt;"),
                     ("|", "&#124;"), ("[", "&#91;"), ("]", "&#93;"),
                     ("`", "&#96;"), ("*", "&#42;"), ("_", "&#95;"),
                     ("\\", "&#92;")):
        value = value.replace(old, new)
    return value


def names(issue):
    return {label["name"] for label in issue.get("labels", [])}


def status(issue):
    labels = names(issue)
    states = [label[7:] for label in labels if label.startswith("status:")]
    if len(states) != 1 or states[0] not in STATUSES:
        return "needs-triage"
    state = states[0]
    if issue.get("state") == "closed":
        if state == "done" and issue.get("state_reason") == "completed":
            return "done"
        return "closed-unaccepted"
    return "needs-triage" if state == "done" else state


def render(issues, task_label):
    tasks = [i for i in issues if "pull_request" not in i and task_label in names(i)]
    tasks.sort(key=lambda i: i["number"])
    groups = (*STATUSES, "needs-triage", "closed-unaccepted")
    lines = ["## Task board / 自动任务视图", "",
             "Source: task Issue status labels. See latest comments for session/lease, dependencies and evidence.",
             "本视图不表示 agent 正在后台运行；领取与过期冲突需按协议核对。", "",
             "| State | Count |", "| --- | ---: |"]
    lines += [f"| {s} | {sum(status(i) == s for i in tasks)} |" for s in groups]
    lines += ["", "| Task | State | Priority / stage | Latest task update (UTC) |",
              "| --- | --- | --- | --- |"]
    for i in tasks:
        meta = sorted(n for n in names(i) if n.startswith(("priority:", "phase:")))
        number = int(i["number"])
        # GitHub auto-links numeric Issue references in the hub.
        lines.append(f"| #{number} {cell(i['title'])} | "
                     f"{status(i)} | {cell(', '.join(meta))} | {cell(i.get('updated_at', 'unknown'))} |")
    lines += ["", "`done` requires a closed/completed Issue and status:done; other closures remain unaccepted.",
              "正文任务元数据为快照；标签冲突显示 needs-triage。验收细节以 Issue/PR 证据为准。"]
    return "\n".join(lines)


def replace_region(body, board):
    if body.count(BEGIN) != 1 or body.count(END) != 1:
        raise ValueError("Hub requires exactly one board marker pair; refusing overwrite")
    start, end = body.index(BEGIN) + len(BEGIN), body.index(END)
    if start > end:
        raise ValueError("Board markers are reversed")
    return body[:start] + "\n" + board + "\n" + body[end:]


class GitHub:
    def __init__(self, repo, token):
        if len(repo.split("/")) != 2 or any(c not in "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._/" for c in repo):
            raise ValueError("Invalid owner/repository")
        self.root = f"https://api.github.com/repos/{repo}"
        self.token = token

    def request(self, path, method="GET", data=None):
        payload = None if data is None else json.dumps(data).encode()
        req = urllib.request.Request(self.root + path, data=payload, method=method,
                                     headers={"Authorization": "Bearer " + self.token,
                                              "Accept": "application/vnd.github+json",
                                              "X-GitHub-Api-Version": "2022-11-28",
                                              "Content-Type": "application/json"})
        # Do not log request headers or credential-bearing response bodies on failure.
        with urllib.request.urlopen(req, timeout=30) as response:
            return json.load(response)

    def issues(self, task_label):
        result, page = [], 1
        while True:
            query = urllib.parse.urlencode({"state": "all", "labels": task_label,
                                            "per_page": 100, "page": page})
            batch = self.request("/issues?" + query)
            if not isinstance(batch, list):
                raise ValueError("Unexpected Issues response")
            result.extend(batch)
            if len(batch) < 100:
                return result
            page += 1


def update(api, config):
    path = f"/issues/{int(config['hub_issue'])}"
    hub = api.request(path)
    source_body = hub.get("body") or ""
    new = replace_region(source_body, render(api.issues(config["task_label"]), config["task_label"]))
    if new == source_body:
        return False
    # Detect intervening manual edits. This is not a CAS: a final race remains possible.
    fresh = api.request(path)
    if fresh.get("body") != hub.get("body") or fresh.get("updated_at") != hub.get("updated_at"):
        raise RuntimeError("Hub changed during render; rerun instead of overwriting")
    api.request(path, "PATCH", {"body": new})
    return True


def main():
    config = json.loads((Path(__file__).resolve().parents[1] / "coordination.json").read_text())
    api = GitHub(os.environ["GITHUB_REPOSITORY"], os.environ["GH_TOKEN"])
    try:
        changed = update(api, config)
    except urllib.error.HTTPError as exc:
        raise SystemExit(f"GitHub HTTP {exc.code}; board not confirmed updated") from None
    print("Board updated" if changed else "Board already current")


if __name__ == "__main__":
    main()
