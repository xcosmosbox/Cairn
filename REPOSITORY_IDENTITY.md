# 仓库身份与旧知识库导入

每个新建 KG 在 `kg_manifest.repository_identity` 中保存稳定归属。增量与重整在
任何迁移、KG 写入或文档修复之前，以只读连接验证版本及身份；缺失或不匹配时停止。

身份来源按优先级为显式 `--repository-identity`、规范化的 Git `origin`、本地持久
标记 `.cairn-repository-id`。GitHub 的 HTTPS 与 SSH 地址均规范化为
`git:github.com/<owner>/<repo>`。扫描 Git 子目录时还包含相对扫描根的作用域，避免
把子目录误当整个仓库而删除根路径知识。身份不包含凭据或短命工作树位置。非 Git 目录的
全量构建会创建 UUID 标记；移动或复制同一个工作区时应保留该文件。控制器应传入
实际源仓库的规范化身份，不能只用可重新指向其他仓库的配置名称。

既有 Bundle 仍可通过只读查询接口使用。旧 KG 没有归属记录时，不会自动放行增量。
可以全量重建，或者在完整原始工作区中显式导入：

```sh
cairn-incremental --repo /path/to/original-repo --db /path/to/knowledge.db --adopt-legacy
```

导入要求全部 `node_sources` 的文档存在、sidecar 合法、文档路径匹配，并包含该来源
的 UUID 和 member ID。一个 UUID 相交、空目录、缺失文档或部分 sidecar 都不足以
证明归属；来源文档和 sidecar 的 symlink 也不能充当归属证明。导入验证成功后才允许创建本地标记、迁移库并持久化
身份。以后同一仓库内的正常文档删除仍按原有 C2 语义执行。

`cairn-rebalance` 也支持 `--adopt-legacy`；`--check` 始终只读，不迁移、不建立标记、
不持久化身份，因此它不能代替实际导入。

已经绑定身份的 KG 也不会盲目信任数据库中的路径。来源、当前文件状态和 shared
primary 的路径会在任何变更分派前验证；越界路径或 symlink 目录/文件一律停止，
正常缺失的来源文档仍可按 C2 删除。

SQLite 连接使用转义后的绝对 `file:` URI 和 modernc 支持的 `_pragma`。查询入口
同时启用 `mode=ro` 与 `query_only(1)`，不创建缺失文件、不执行 DDL 或迁移。构建
连接默认启用 WAL 和 5000ms 锁等待。打包与演化快照通过一致性快照输出独立 DB，
不能直接复制一个仍有未 checkpoint WAL 的主文件。

真实 schema 4 的只读查询以逻辑空 `file_slug` 兼容，不迁移旧库。显式写哨兵元数据
保留既有 journal mode。全量入库先完成、checkpoint 并关闭临时数据库，再原子替换
目标；失败保留原库。存在旧 WAL/SHM 连接的目标不会被强制替换或删除附属文件。
