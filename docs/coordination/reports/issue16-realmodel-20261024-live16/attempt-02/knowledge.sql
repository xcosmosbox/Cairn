PRAGMA foreign_keys=OFF;
BEGIN TRANSACTION;
CREATE TABLE conflict_reports (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    entity_name         TEXT NOT NULL,
    conflicting_sources TEXT NOT NULL,
    resolution_status   TEXT NOT NULL DEFAULT 'unresolved' CHECK(resolution_status IN ('resolved', 'unresolved')),
    resolution_note     TEXT,
    resolved_by         TEXT,
    created_at          TEXT DEFAULT (datetime('now'))
);
CREATE TABLE "cross_references" (
		    id                INTEGER PRIMARY KEY AUTOINCREMENT,
		    source_entity     TEXT NOT NULL,
		    target_entity     TEXT NOT NULL,
		    target_node_id    TEXT,
		    kind              TEXT NOT NULL,
		    reason            TEXT,
		    resolution_status TEXT NOT NULL DEFAULT 'resolved' CHECK(resolution_status IN ('resolved', 'unresolved', 'external_kg')),
		    created_at        TEXT DEFAULT (datetime('now'))
		);
CREATE TABLE "edges" (
		    id            INTEGER PRIMARY KEY AUTOINCREMENT,
		    source_id     TEXT NOT NULL,
		    target_id     TEXT NOT NULL,
		    kind          TEXT NOT NULL CHECK(kind IN ('triggers', 'depends_on', 'references', 'generalizes', 'composes', 'contradicts', 'provides')),
		    description   TEXT,
		    properties    TEXT,
		    provenance    TEXT NOT NULL DEFAULT 'llm_inferred' CHECK(provenance IN ('llm_inferred', 'human_curated', 'extraction')),
		    confidence    REAL NOT NULL DEFAULT 1.0 CHECK(confidence >= 0.0 AND confidence <= 1.0),
		    source_refs   TEXT,
		    bidirectional INTEGER DEFAULT 0 CHECK(bidirectional IN (0, 1)),
		    cardinality   TEXT,
		    created_at    TEXT DEFAULT (datetime('now')),
		    FOREIGN KEY (source_id) REFERENCES nodes(id),
		    FOREIGN KEY (target_id) REFERENCES nodes(id)
		);
INSERT INTO "edges" VALUES(1,'skill::public-synthetic-queue','domain::并发队列与背压控制','provides','skill public-synthetic-queue 贡献了领域 并发队列与背压控制',NULL,'llm_inferred',1.0,'public-synthetic-queue',0,NULL,'2026-10-24 13:46:16.70177104 +0000 UTC');
INSERT INTO "edges" VALUES(2,'domain::并发队列与背压控制','subdomain::并发队列与背压控制::阻塞队列模型','composes','领域包含子域',NULL,'llm_inferred',1.0,NULL,0,NULL,'2026-10-24 13:46:16.70177104 +0000 UTC');
INSERT INTO "edges" VALUES(3,'subdomain::并发队列与背压控制::阻塞队列模型','bb69eb4c-5110-5e11-9b55-12ece9f73dd8','composes','子域包含实体',NULL,'llm_inferred',1.0,NULL,0,NULL,'2026-10-24 13:46:16.70177104 +0000 UTC');
INSERT INTO "edges" VALUES(4,'subdomain::并发队列与背压控制::阻塞队列模型','fcdf07c7-0e9b-542c-93ba-b951817908c3','composes','子域包含概念',NULL,'llm_inferred',1.0,NULL,0,NULL,'2026-10-24 13:46:16.70177104 +0000 UTC');
INSERT INTO "edges" VALUES(5,'bb69eb4c-5110-5e11-9b55-12ece9f73dd8','fcdf07c7-0e9b-542c-93ba-b951817908c3','depends_on','阻塞队列的等待行为由阻塞语义定义。',NULL,'llm_inferred',0.9,NULL,0,NULL,'2026-10-24 13:46:16.70177104 +0000 UTC');
INSERT INTO "edges" VALUES(6,'domain::并发队列与背压控制','subdomain::并发队列与背压控制::生产者重试与预算','composes','领域包含子域',NULL,'llm_inferred',1.0,NULL,0,NULL,'2026-10-24 13:46:16.70177104 +0000 UTC');
INSERT INTO "edges" VALUES(7,'subdomain::并发队列与背压控制::生产者重试与预算','5a9ab1d8-f65d-504b-a743-33138f36c8b6','composes','子域包含实体',NULL,'llm_inferred',1.0,NULL,0,NULL,'2026-10-24 13:46:16.70177104 +0000 UTC');
INSERT INTO "edges" VALUES(8,'subdomain::并发队列与背压控制::生产者重试与预算','c3123633-9fad-5391-8d39-ba4d7b393783','composes','子域包含概念',NULL,'llm_inferred',1.0,NULL,0,NULL,'2026-10-24 13:46:16.70177104 +0000 UTC');
INSERT INTO "edges" VALUES(9,'subdomain::并发队列与背压控制::生产者重试与预算','38d52b47-62d0-5c36-970d-0a11fce666da','composes','子域包含概念',NULL,'llm_inferred',1.0,NULL,0,NULL,'2026-10-24 13:46:16.70177104 +0000 UTC');
INSERT INTO "edges" VALUES(10,'5a9ab1d8-f65d-504b-a743-33138f36c8b6','c3123633-9fad-5391-8d39-ba4d7b393783','depends_on','生产者等待与重试受重试预算约束。',NULL,'llm_inferred',0.9,NULL,0,NULL,'2026-10-24 13:46:16.70177104 +0000 UTC');
INSERT INTO "edges" VALUES(11,'38d52b47-62d0-5c36-970d-0a11fce666da','c3123633-9fad-5391-8d39-ba4d7b393783','references','该关系概念引用重试预算。',NULL,'llm_inferred',0.9,NULL,0,NULL,'2026-10-24 13:46:16.70177104 +0000 UTC');
INSERT INTO "edges" VALUES(12,'c3123633-9fad-5391-8d39-ba4d7b393783','5a9ab1d8-f65d-504b-a743-33138f36c8b6','references','重试预算的描述指向生产者的等待与重试行为，并规定其最多等待 50 毫秒、最多重试 2 次。',NULL,'llm_inferred',0.85,NULL,0,NULL,'2026-10-24 13:46:16.70177104 +0000 UTC');
INSERT INTO "edges" VALUES(13,'domain::并发队列与背压控制','subdomain::并发队列与背压控制::运行环境约束','composes','领域包含子域',NULL,'llm_inferred',1.0,NULL,0,NULL,'2026-10-24 13:46:16.70177104 +0000 UTC');
INSERT INTO "edges" VALUES(14,'subdomain::并发队列与背压控制::运行环境约束','6890d8d0-de21-5dc7-a629-64c351fa706f','composes','子域包含实体',NULL,'llm_inferred',1.0,NULL,0,NULL,'2026-10-24 13:46:16.70177104 +0000 UTC');
CREATE TABLE "file_states" (
		    repo_url     TEXT NOT NULL DEFAULT '',
		    file_path    TEXT NOT NULL,
		    content_hash TEXT NOT NULL,
		    indexed_at   TEXT DEFAULT (datetime('now')),
		    PRIMARY KEY (repo_url, file_path)
		);
CREATE TABLE kb_version (
    version    TEXT PRIMARY KEY,
    updated_at TEXT DEFAULT (datetime('now'))
);
INSERT INTO "kb_version" VALUES('2026-10-24T13:46:16.733293517Z','2026-10-24T13:46:16.733293517Z');
CREATE TABLE kg_manifest (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL  -- JSON string
);
INSERT INTO "kg_manifest" VALUES('repository_identity','{"version":1,"identity":"cairn-public-real-two-doc-v1"}');
CREATE TABLE node_sources (
    node_uuid   TEXT NOT NULL,
    member_id   TEXT NOT NULL,
    skill       TEXT NOT NULL,
    file_path   TEXT NOT NULL,
    start_line  INTEGER,
    end_line    INTEGER,
    PRIMARY KEY (node_uuid, member_id, file_path)
);
INSERT INTO "node_sources" VALUES('bb69eb4c-5110-5e11-9b55-12ece9f73dd8','public-synthetic-queue-entity-阻塞队列','public-synthetic-queue','references/queue.md',NULL,NULL);
INSERT INTO "node_sources" VALUES('fcdf07c7-0e9b-542c-93ba-b951817908c3','public-synthetic-queue-concept-阻塞队列的阻塞语义','public-synthetic-queue','references/queue.md',NULL,NULL);
INSERT INTO "node_sources" VALUES('5a9ab1d8-f65d-504b-a743-33138f36c8b6','public-synthetic-queue-entity-生产者','public-synthetic-queue','references/timeout.md',NULL,NULL);
INSERT INTO "node_sources" VALUES('c3123633-9fad-5391-8d39-ba4d7b393783','public-synthetic-queue-concept-重试预算','public-synthetic-queue','references/queue.md',NULL,NULL);
INSERT INTO "node_sources" VALUES('c3123633-9fad-5391-8d39-ba4d7b393783','public-synthetic-queue-concept-重试预算','public-synthetic-queue','references/timeout.md',NULL,NULL);
INSERT INTO "node_sources" VALUES('38d52b47-62d0-5c36-970d-0a11fce666da','public-synthetic-queue-concept-重试预算与阻塞队列的依赖关系','public-synthetic-queue','references/timeout.md',NULL,NULL);
INSERT INTO "node_sources" VALUES('6890d8d0-de21-5dc7-a629-64c351fa706f','public-synthetic-queue-entity-免费-fixed-fake','public-synthetic-queue','references/timeout.md',NULL,NULL);
CREATE TABLE "nodes" (
		    id               TEXT PRIMARY KEY,
		    label            TEXT NOT NULL CHECK(label IN ('Entity', 'Concept', 'Skill', 'Domain', 'Subdomain')),
		    name             TEXT NOT NULL,
		    summary          TEXT NOT NULL,
		    synonyms         TEXT,
		    domain           TEXT NOT NULL,
		    subdomain        TEXT NOT NULL,
		    description      TEXT,
		    properties       TEXT,
		    tags             TEXT,
		    related_entities TEXT,
		    confidence       REAL NOT NULL DEFAULT 1.0 CHECK(confidence >= 0.0 AND confidence <= 1.0),
		    provenance       TEXT NOT NULL DEFAULT 'llm_inferred' CHECK(provenance IN ('llm_inferred', 'human_curated', 'extraction')),
		    source_refs      TEXT,
		    created_at       TEXT DEFAULT (datetime('now')),
		    updated_at       TEXT DEFAULT (datetime('now'))
		, file_slug TEXT);
INSERT INTO "nodes" VALUES('skill::public-synthetic-queue','Skill','public-synthetic-queue','合成队列示例 / Synthetic queue example',NULL,'public-synthetic-queue','',NULL,NULL,NULL,NULL,0.0,'llm_inferred',NULL,'2026-10-24 13:46:16.70177104 +0000 UTC','2026-10-24 13:46:16.70177104 +0000 UTC',NULL);
INSERT INTO "nodes" VALUES('domain::并发队列与背压控制','Domain','并发队列与背压控制','围绕有界阻塞队列、阻塞语义、生产者等待与重试预算，以及运行环境约束的跨 skill 复用知识。',NULL,'并发队列与背压控制','','复用性理由 / Reusability: 阻塞队列、背压/重试预算、生产者等待模式是消息中间件、并发编程、限流降级和测试环境等场景的通用主题，可跨 skill 复用。',NULL,NULL,NULL,0.0,'llm_inferred','public-synthetic-queue','2026-10-24 13:46:16.70177104 +0000 UTC','2026-10-24 13:46:16.70177104 +0000 UTC',NULL);
INSERT INTO "nodes" VALUES('subdomain::并发队列与背压控制::阻塞队列模型','Subdomain','阻塞队列模型','有界阻塞队列实体及其阻塞行为语义。',NULL,'并发队列与背压控制','阻塞队列模型',NULL,NULL,NULL,NULL,0.0,'llm_inferred',NULL,'2026-10-24 13:46:16.70177104 +0000 UTC','2026-10-24 13:46:16.70177104 +0000 UTC',NULL);
INSERT INTO "nodes" VALUES('bb69eb4c-5110-5e11-9b55-12ece9f73dd8','Entity','阻塞队列','容量为 8 的虚构有界队列，满时生产者等待、空时消费者等待。',NULL,'并发队列与背压控制','阻塞队列模型','阻塞队列（Blocking queue）是本文档描述的虚构队列实例，其容量为 8。它具有有界队列的基本特性：当队列已满时，生产者线程需要等待；当队列为空时，消费者线程需要等待。该文档为合成输入，不参与固定 fake runner 的消费，因此该队列是说明性示例而非实际运行组件。',NULL,NULL,NULL,0.95,'llm_inferred','public-synthetic-queue','2026-10-24 13:46:16.70177104 +0000 UTC','2026-10-24 13:46:16.70177104 +0000 UTC','blocking-queue');
INSERT INTO "nodes" VALUES('fcdf07c7-0e9b-542c-93ba-b951817908c3','Concept','阻塞语义','有界队列在满或空时阻塞生产者与消费者的行为规则。',NULL,'并发队列与背压控制','阻塞队列模型','阻塞语义指有界阻塞队列在边界状态下的处理规则：队列满时生产者不能继续入队而必须等待，队列空时消费者不能取元素而必须等待；正是这种在满或空时阻塞的行为解释了“阻塞队列”的基本含义，也是其与普通有界队列的核心区别。',NULL,NULL,NULL,0.95,'llm_inferred','public-synthetic-queue','2026-10-24 13:46:16.70177104 +0000 UTC','2026-10-24 13:46:16.70177104 +0000 UTC','blocking-semantics');
INSERT INTO "nodes" VALUES('subdomain::并发队列与背压控制::生产者重试与预算','Subdomain','生产者重试与预算','生产者角色、重试预算，以及重试预算对阻塞队列的依赖关系。',NULL,'并发队列与背压控制','生产者重试与预算',NULL,NULL,NULL,NULL,0.0,'llm_inferred',NULL,'2026-10-24 13:46:16.70177104 +0000 UTC','2026-10-24 13:46:16.70177104 +0000 UTC',NULL);
INSERT INTO "nodes" VALUES('5a9ab1d8-f65d-504b-a743-33138f36c8b6','Entity','生产者','执行等待与重试操作、受重试预算约束的生产者角色。',NULL,'并发队列与背压控制','生产者重试与预算','生产者是文档中描述的、受重试预算约束的执行角色：它最多等待 50 毫秒，最多重试 2 次；当重试预算用尽后不再重试，并返回可见错误。生产者的等待与重试行为依赖阻塞队列的容量与等待行为。',NULL,NULL,NULL,0.95,'llm_inferred','public-synthetic-queue','2026-10-24 13:46:16.70177104 +0000 UTC','2026-10-24 13:46:16.70177104 +0000 UTC','producer');
INSERT INTO "nodes" VALUES('c3123633-9fad-5391-8d39-ba4d7b393783','Concept','重试预算','生产者不能无限等待时调用的预算机制，其语义依赖队列的阻塞行为。',NULL,'并发队列与背压控制','生产者重试与预算','重试预算（retry budget）是调用方在生产者无法无限等待的场景下所采用的机制。跨文档意图表明，重试预算依赖队列的阻塞行为，而不是与队列无关的独立概念；其语义需要放在队列满时生产者等待这一阻塞特性的上下文中理解。该说明属于合成输入中的语义描述，并非已观测的图谱输出。在本虚构示例中，重试预算描述生产者等待与重试行为的额度约束：生产者最多等待 50 毫秒，最多重试 2 次；当重试预算用尽后，系统返回可见错误。文档强调这些数字仅供输入结构演示，不是调优建议。重试预算依赖阻塞队列的容量与等待行为。在免费 fixed fake 中不存在重试预算，查询它应明确得到零结果。',NULL,NULL,NULL,0.95,'llm_inferred','public-synthetic-queue','2026-10-24 13:46:16.70177104 +0000 UTC','2026-10-24 13:46:16.70177104 +0000 UTC','retry-budget');
INSERT INTO "nodes" VALUES('38d52b47-62d0-5c36-970d-0a11fce666da','Concept','重试预算与阻塞队列的依赖关系','重试预算依赖阻塞队列容量与等待行为的跨文档关系。',NULL,'并发队列与背压控制','生产者重试与预算','重试预算与阻塞队列的依赖关系说明：重试预算并非独立生效，而是依赖阻塞队列的容量与等待行为。文档提到，若未来用真实 LLM 构建这些材料，可以检验是否保留这条跨文档关系；本次没有运行该抽取。',NULL,NULL,NULL,0.9,'llm_inferred','public-synthetic-queue','2026-10-24 13:46:16.70177104 +0000 UTC','2026-10-24 13:46:16.70177104 +0000 UTC','retry-budget-blocking-queue-deps');
INSERT INTO "nodes" VALUES('subdomain::并发队列与背压控制::运行环境约束','Subdomain','运行环境约束','测试或假运行环境对重试预算存在性的约束。',NULL,'并发队列与背压控制','运行环境约束',NULL,NULL,NULL,NULL,0.0,'llm_inferred',NULL,'2026-10-24 13:46:16.70177104 +0000 UTC','2026-10-24 13:46:16.70177104 +0000 UTC',NULL);
INSERT INTO "nodes" VALUES('6890d8d0-de21-5dc7-a629-64c351fa706f','Entity','免费固定假运行环境','文档中的免费固定假运行环境，其中不存在重试预算。',NULL,'并发队列与背压控制','运行环境约束','免费 fixed fake 是文档所描述的固定假运行环境。在该环境中不存在重试预算，因此查询重试预算时应明确得到零结果。文档还说明，本材料属于合成输入，不会被固定假运行器消费。',NULL,NULL,NULL,0.9,'llm_inferred','public-synthetic-queue','2026-10-24 13:46:16.70177104 +0000 UTC','2026-10-24 13:46:16.70177104 +0000 UTC','fake-runtime-environment');
CREATE TABLE uuid_lineage (
    new_uuid   TEXT NOT NULL,
    old_uuid   TEXT NOT NULL,
    reason     TEXT NOT NULL CHECK(reason IN ('merged','split')),
    created_at TEXT DEFAULT (datetime('now')),
    PRIMARY KEY (new_uuid, old_uuid)
);
CREATE VIRTUAL TABLE nodes_fts USING fts5(
    name, summary, synonyms, tags, description, domain, subdomain,
    prefix='2 3'
);
INSERT INTO "nodes_fts"(rowid,"name","summary","synonyms","tags","description","domain","subdomain") VALUES(1,'public-synthetic-queue','合成队列示例 / Synthetic queue example',NULL,NULL,NULL,'public-synthetic-queue','');
INSERT INTO "nodes_fts"(rowid,"name","summary","synonyms","tags","description","domain","subdomain") VALUES(2,'并发队列与背压控制','围绕有界阻塞队列、阻塞语义、生产者等待与重试预算，以及运行环境约束的跨 skill 复用知识。',NULL,NULL,'复用性理由 / Reusability: 阻塞队列、背压/重试预算、生产者等待模式是消息中间件、并发编程、限流降级和测试环境等场景的通用主题，可跨 skill 复用。','并发队列与背压控制','');
INSERT INTO "nodes_fts"(rowid,"name","summary","synonyms","tags","description","domain","subdomain") VALUES(3,'阻塞队列模型','有界阻塞队列实体及其阻塞行为语义。',NULL,NULL,NULL,'并发队列与背压控制','阻塞队列模型');
INSERT INTO "nodes_fts"(rowid,"name","summary","synonyms","tags","description","domain","subdomain") VALUES(4,'阻塞队列','容量为 8 的虚构有界队列，满时生产者等待、空时消费者等待。',NULL,NULL,'阻塞队列（Blocking queue）是本文档描述的虚构队列实例，其容量为 8。它具有有界队列的基本特性：当队列已满时，生产者线程需要等待；当队列为空时，消费者线程需要等待。该文档为合成输入，不参与固定 fake runner 的消费，因此该队列是说明性示例而非实际运行组件。','并发队列与背压控制','阻塞队列模型');
INSERT INTO "nodes_fts"(rowid,"name","summary","synonyms","tags","description","domain","subdomain") VALUES(5,'阻塞语义','有界队列在满或空时阻塞生产者与消费者的行为规则。',NULL,NULL,'阻塞语义指有界阻塞队列在边界状态下的处理规则：队列满时生产者不能继续入队而必须等待，队列空时消费者不能取元素而必须等待；正是这种在满或空时阻塞的行为解释了“阻塞队列”的基本含义，也是其与普通有界队列的核心区别。','并发队列与背压控制','阻塞队列模型');
INSERT INTO "nodes_fts"(rowid,"name","summary","synonyms","tags","description","domain","subdomain") VALUES(6,'生产者重试与预算','生产者角色、重试预算，以及重试预算对阻塞队列的依赖关系。',NULL,NULL,NULL,'并发队列与背压控制','生产者重试与预算');
INSERT INTO "nodes_fts"(rowid,"name","summary","synonyms","tags","description","domain","subdomain") VALUES(7,'生产者','执行等待与重试操作、受重试预算约束的生产者角色。',NULL,NULL,'生产者是文档中描述的、受重试预算约束的执行角色：它最多等待 50 毫秒，最多重试 2 次；当重试预算用尽后不再重试，并返回可见错误。生产者的等待与重试行为依赖阻塞队列的容量与等待行为。','并发队列与背压控制','生产者重试与预算');
INSERT INTO "nodes_fts"(rowid,"name","summary","synonyms","tags","description","domain","subdomain") VALUES(8,'重试预算','生产者不能无限等待时调用的预算机制，其语义依赖队列的阻塞行为。',NULL,NULL,'重试预算（retry budget）是调用方在生产者无法无限等待的场景下所采用的机制。跨文档意图表明，重试预算依赖队列的阻塞行为，而不是与队列无关的独立概念；其语义需要放在队列满时生产者等待这一阻塞特性的上下文中理解。该说明属于合成输入中的语义描述，并非已观测的图谱输出。在本虚构示例中，重试预算描述生产者等待与重试行为的额度约束：生产者最多等待 50 毫秒，最多重试 2 次；当重试预算用尽后，系统返回可见错误。文档强调这些数字仅供输入结构演示，不是调优建议。重试预算依赖阻塞队列的容量与等待行为。在免费 fixed fake 中不存在重试预算，查询它应明确得到零结果。','并发队列与背压控制','生产者重试与预算');
INSERT INTO "nodes_fts"(rowid,"name","summary","synonyms","tags","description","domain","subdomain") VALUES(9,'重试预算与阻塞队列的依赖关系','重试预算依赖阻塞队列容量与等待行为的跨文档关系。',NULL,NULL,'重试预算与阻塞队列的依赖关系说明：重试预算并非独立生效，而是依赖阻塞队列的容量与等待行为。文档提到，若未来用真实 LLM 构建这些材料，可以检验是否保留这条跨文档关系；本次没有运行该抽取。','并发队列与背压控制','生产者重试与预算');
INSERT INTO "nodes_fts"(rowid,"name","summary","synonyms","tags","description","domain","subdomain") VALUES(10,'运行环境约束','测试或假运行环境对重试预算存在性的约束。',NULL,NULL,NULL,'并发队列与背压控制','运行环境约束');
INSERT INTO "nodes_fts"(rowid,"name","summary","synonyms","tags","description","domain","subdomain") VALUES(11,'免费固定假运行环境','文档中的免费固定假运行环境，其中不存在重试预算。',NULL,NULL,'免费 fixed fake 是文档所描述的固定假运行环境。在该环境中不存在重试预算，因此查询重试预算时应明确得到零结果。文档还说明，本材料属于合成输入，不会被固定假运行器消费。','并发队列与背压控制','运行环境约束');
CREATE INDEX idx_edges_kind ON edges(kind);
CREATE INDEX idx_edges_provenance ON edges(provenance);
CREATE INDEX idx_edges_source ON edges(source_id);
CREATE INDEX idx_edges_target ON edges(target_id);
CREATE INDEX idx_node_sources_file ON node_sources(file_path);
CREATE INDEX idx_node_sources_uuid ON node_sources(node_uuid);
CREATE INDEX idx_nodes_domain ON nodes(domain);
CREATE INDEX idx_nodes_label ON nodes(label);
CREATE INDEX idx_nodes_subdomain ON nodes(subdomain);
PRAGMA user_version=5;
COMMIT;
PRAGMA foreign_keys=ON;
