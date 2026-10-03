# 正确性修复与验收 / Correctness fixes

本轮针对审查基线 `fa1b8eac47afad7e56977a5b1ef15208c73c99ea` 的 F01–F18。
目标是阻止错误归属、失败被误报成功、并发请求失效、未闭环发布及不安全重放。
本轮不做性能优化或架构约束放宽。

| 问题 | 修复后的行为 | 主要回归位置 |
| --- | --- | --- |
| F01 | KG 绑定稳定仓库及扫描根身份；错误仓库、无证明旧库、越界或 symlink 路径在变更前停止 | `build/internal/incremental/zz_repository_identity_test.go`、`core/repoidentity/zz_identity_test.go` |
| F02 | 转义 `file:` URI，真正的只读权限；不创建或迁移查询库；writer WAL 与锁等待生效 | `core/storage/zz_connection_test.go`、`core/evolve/zz_readonly_test.go` |
| F03 | attempt、恢复阶段、错误与退避持久化；重启后仍遵守重试上限 | `build/internal/controller/store/zz_recovery_test.go` |
| F04 | 状态写入错误向上传递；等待 PR 时的网络错误可从原阶段恢复 | `build/internal/controller/reconcile/zz_recovery_test.go` |
| F05 | 分支读取失败或空 SHA 进入可重试失败，不误判 Stale | `build/internal/controller/reconcile/zz_recovery_test.go` |
| F06 | 请求持有数据库代引用，旧代在最后一个读者结束后才关闭；重装和回滚不覆盖已有代 | `service/cmd/cairn-mcp/zz_lifecycle_test.go`、`core/kbbundle/zz_snapshot_test.go` |
| F07 | 替换前验证实际数据库、查询列与 FTS；无效代保留健康 current 和服务 | `core/kbbundle/zz_snapshot_test.go`、`service/cmd/cairn-mcp/zz_lifecycle_test.go` |
| F08 | 合并源 SHA 统一持久化；人工调整后的新增写回必须再次审查，不能发布脏工作树 | `build/internal/controller/reconcile/zz_publication_test.go` |
| F09 | 冻结候选输入、时间、摘要和产物；同一输入重放复用同一 Release 身份 | `build/internal/controller/publisher/zz_replay_test.go` |
| F10 | MCP 初始化协商实际支持的日期版本，错误响应有机器可识别标记 | `service/cmd/cairn-mcp/zz_lifecycle_test.go`、`zz_transport_test.go` |
| F11 | 全局 `--db-path` 可放在命令前或后；只显式哨兵记录允许写观测历史 | `service/cmd/cairn/zz_global_flags_test.go` |
| F12 | 文档中的 `--once --fake` 可运行；本地 Source/Catalog、模拟合并与 Bundle 隔离且完整 | `build/cmd/cairnd/zz_fake_test.go` |
| F13 | 冒烟使用可移植临时文件名，验证协议与成功载荷；错误响应不能假通过 | `scripts/dev/zz_smoke_mcp_test.py` |
| F14 | fingerprint 取真实 schema、构建源码、提示词和配置；daemon 和 once 均识别同 SHA 配置漂移 | `build/internal/controller/reconcile/zz_publication_test.go`、`scheduler/zz_recovery_test.go`、`build/cmd/cairnd/zz_once_test.go` |
| F15 | clone/fetch/push 每次使用当前凭据，token 不进入命令参数或持久 Git 配置；受管 Git 禁用继承的 hooks | `build/internal/controller/workspace/zz_authentication_test.go`、`zz_hooks_test.go` |
| F16 | MD/sidecar 以持久 journal 维护文件对；只读校验要求完整来源、共享 primary 及逐字段一致，部分写回阻止发布 | `build/internal/writeback/zz_pair_test.go`、`pipeline/zz_writeback_test.go`、`controller/runner/zz_validation_test.go`、`zz_review_materialization_test.go` |
| F17 | 执行租约续期、同步动作检查、事务内状态保护与外部动作收据接通；丢回执可重放原身份 | `controller/store/zz_recovery_test.go`、`controller/githubapp/zz_httpforge_replay_test.go`、`controller/publisher/zz_replay_test.go`（均位于 `build/internal`） |
| F18 | WASM 使用构建资产 URL，部署到 `/cairn/` 子路径仍能加载 SQLite | `graph-viewer/scripts/test-subpath.mjs` |

交叉复核还补齐了真实 schema 4（实际缺少 `file_slug`）的只读兼容、未 checkpoint WAL
的完整快照、演化记录并发与失败恢复、哨兵历史的跨进程原子追加，以及异常查询和关闭中的更新请求。

## 本地验收

```sh
go test -race -count=1 ./...
make verify
bash -n scripts/dev/smoke-mcp.sh
python3 scripts/dev/zz_smoke_mcp_test.py
cd graph-viewer
npm ci
npm run test:subpath
```

GitHub Actions 自动执行 Go 1.22 与当前稳定版的竞态测试、构建、vet、架构门禁，
以及冒烟脚本失败场景和查看器子路径加载。

测试使用临时 SQLite、本地 Git bare 仓库和真实 HTTP 测试服务；包括远端成功后 TCP
回执丢失、PR 已关闭或合并、分页、失租、取消、无效 Bundle 与人工修改的 Catalog。
真实 GitHub App 权限配置、网络条件和真实 LLM 抽取质量仍需对应环境验收。

## 兼容性与操作变化

- 无归属旧 KG 必须完整证明后显式 `--adopt-legacy`，或者全量重建；见
  [仓库身份说明](REPOSITORY_IDENTITY.md)。真实 schema 4 可只读查询，不会被自动迁移。
- 显式 `sentinel --record=true` 只写观测历史，保持 schema 和 journal mode；普通查询只读。
- 声明 schema 4 而实际为 5、缺失查询契约、摘要或 Catalog 不一致的 Bundle 会被拒绝。
  无法证明 Catalog 身份的旧裸 DB Release 需要重新发布完整 Bundle。
- 全量入库先关闭、checkpoint 临时库再原子替换。目标仍有 WAL/SHM 时拒绝替换，
  需要先关闭使用该目标的连接。
- MCP stdio 与 legacy SSE 支持 `2024-11-05`；初始化返回实际支持版本。
- 自动重试达到上限后停止。管理员显式恢复沿用失败阶段；已关闭提案的新恢复采用新 run 身份。
- 租约保护协作执行及状态写入；外部服务不参与 SQLite 事务，外部成功但本地回执未落盘的窗口
  通过稳定请求身份与远端 Ensure 操作恢复，不能把它当作跨服务原子事务。
