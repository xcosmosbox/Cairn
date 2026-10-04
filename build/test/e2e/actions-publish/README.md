# 真实构建产物的 Actions 发布验收

这个驱动使用生产 `Runner.Validate`、`Publisher.EnsureCandidate`、`HTTPForge` 和
`kbbundle.PullRemoteContext`。本地已经完成真实 LLM 构建；Actions 只验证并发布其产物，
不会调用 LLM。`GITHUB_TOKEN` 仅由此测试入口读取，不写入配置或结果文件。
生产 `cairnd` 继续使用 GitHub App ID / RSA PEM；这个测试不覆盖 App JWT 签发和刷新。

## 输入

- 已审查且干净的 Source checkout，HEAD 为实际源 PR 合并提交；origin 必须对应声明的源仓库。
- 本地真实构建的 `knowledge.db`、`build-report.json`，以及可选 `evolution/evolution.db`。
- 冻结的 JSON specification；fingerprint 必须来自该本地构建的 `frozen-builder-fingerprint.json`，不能使用
  Actions 编译此驱动的版本来冒充原始生成者。
- 同次真实构建写出的原始 `frozen-builder-fingerprint.json`；发布前逐字段对照 specification，
  缺失、无效或不一致时在任何远端请求之前拒绝发布。
- 当前 job 的 `GITHUB_TOKEN`，发布 job 需要目标仓库 `contents: write`；消费 job 只需读权限。

Specification 的完整结构如下。`fingerprint` 使用 `publisher.Fingerprint` 的实际 JSON，
摘要与版本值均由真实构建保存，示例占位符不可用于发布。

```json
{
  "repo": {
    "id": "nicedata-e2e-20261003",
    "url": "https://github.com/xcosmosbox/kd_manifeat.git",
    "owner": "xcosmosbox",
    "name": "kd_manifeat",
    "branch": "source/nicedata-e2e-20261003",
    "kg_group": "nicedata-e2e-20261003",
    "min_confidence": 0.7
  },
  "catalog": {
    "repo": "xcosmosbox/kd_manifeat",
    "branch": "main",
    "manifest_dir": "knowledge-bases",
    "release_tag_template": "kb-{group}-{source_sha}",
    "release_prerelease": true
  },
  "source_commit": "<实际源 PR 合并 SHA>",
  "created_at": "<本地构建冻结的 RFC3339 UTC 时间>",
  "config_digest": "sha256:<真实配置摘要>",
  "fingerprint": {
    "builder_version": "<本地实际版本>",
    "builder_commit": "<本地实际编译源码身份>",
    "controller_schema_version": 3,
    "db_schema_version": 5,
    "prompt_set_version": "sha256:<实际提示词源码摘要>",
    "identity_algorithm_version": "sha256:<实际 UUID 算法源码摘要>",
    "discovery_rules_digest": "sha256:<实际发现规则源码摘要>",
    "model": "deepseek-flash",
    "provider": "openai_compatible",
    "annotation_schema_version": 1,
    "config_digest": "sha256:<同一个真实配置摘要>"
  }
}
```

KG 中的 repository identity 必须与源仓库 URL 的规范身份一致。例如这个示例要求
`git:github.com/xcosmosbox/kd_manifeat`。驱动会校验全部来源账本、Markdown 和 sidecar，
并在发布前从真实远端再次核对源分支 SHA。

## 发布与重放

在固定 Cairn 提交的 checkout 根目录运行：

```bash
go run ./build/test/e2e/actions-publish \
  --phase publish --spec publication-spec.json \
  --source-dir ../source --kg-db ../prebuilt/knowledge.db \
  --build-report ../prebuilt/build-report.json \
  --builder-record ../prebuilt/frozen-builder-fingerprint.json \
  --evolution-db ../prebuilt/evolution/evolution.db \
  --output-dir ../publication --receipt ../publication/publish.json
```

驱动两次调用真正的 `EnsureCandidate`。第二次仍查询真实 Release，确认 Release ID、tag
和完整 manifest 不变；不依赖 fake Forge 或伪造成功回执。

`publication/candidate.json` 是待审查的完整 Catalog manifest。将它通过真实 Catalog PR
合并为 `knowledge-bases/<kg>/stable.json` 后，它才成为消费端的稳定事实来源。
驱动不创建或合并 PR，不覆盖既有知识库，也不声称控制器自动推进到了 `Stable`。

## 远端消费与重复安装

Catalog PR 合并后，用后续真实 Actions job 运行：

```bash
go run ./build/test/e2e/actions-publish \
  --phase consume --spec publication-spec.json \
  --expected-receipt ../publication/publish.json \
  --install-dir ../installed --receipt ../publication/consume.json
```

消费前先核对回执中的来源分支、源提交和 manifest 可表达的全部 builder 字段与 spec 一致。
这会从 Catalog 读取指针、从指定 Release 下载 `bundle.tar.gz`、校验所有 provenance 和
checksums，并实际安装。随后重复调用正式 `Install`，确认同一个不可变版本目录及 DB
内容保持不变。`consume.json` 提供绝对 `installed_db` 路径，供 CLI/MCP 使用。

## 自动回归

```bash
go test -race ./build/internal/controller/githubapp ./build/test/e2e/actions-publish
```

回归使用临时 Git 仓库、真实 SQLite 和本地 HTTP 服务；它们不会访问真实 GitHub 或调用
付费 LLM。覆盖临时 token 轮换、取消与租约失权、发布重放、Release 上传/远端拉取、
重复安装以及未审查源/错误归属/来源闭环损坏的发布阻断。
