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
INSERT INTO "edges" VALUES(1,'skill::public-synthetic-queue','domain::并发阻塞队列与背压管理','provides','skill public-synthetic-queue 贡献了领域 并发阻塞队列与背压管理',NULL,'llm_inferred',1.0,'public-synthetic-queue',0,NULL,'2026-10-24 13:42:18.73034862 +0000 UTC');
INSERT INTO "edges" VALUES(2,'domain::并发阻塞队列与背压管理','subdomain::并发阻塞队列与背压管理::阻塞队列核心语义','composes','领域包含子域',NULL,'llm_inferred',1.0,NULL,0,NULL,'2026-10-24 13:42:18.73034862 +0000 UTC');
INSERT INTO "edges" VALUES(3,'subdomain::并发阻塞队列与背压管理::阻塞队列核心语义','bb69eb4c-5110-5e11-9b55-12ece9f73dd8','composes','子域包含实体',NULL,'llm_inferred',1.0,NULL,0,NULL,'2026-10-24 13:42:18.73034862 +0000 UTC');
INSERT INTO "edges" VALUES(4,'subdomain::并发阻塞队列与背压管理::阻塞队列核心语义','bbcab434-24ad-5c47-8e2a-494af4e77272','composes','子域包含概念',NULL,'llm_inferred',1.0,NULL,0,NULL,'2026-10-24 13:42:18.73034862 +0000 UTC');
INSERT INTO "edges" VALUES(5,'bb69eb4c-5110-5e11-9b55-12ece9f73dd8','bbcab434-24ad-5c47-8e2a-494af4e77272','depends_on','阻塞队列的可用性与线程协作方式依赖其阻塞行为规则。',NULL,'llm_inferred',0.95,NULL,0,NULL,'2026-10-24 13:42:18.73034862 +0000 UTC');
INSERT INTO "edges" VALUES(6,'bb69eb4c-5110-5e11-9b55-12ece9f73dd8','bbcab434-24ad-5c47-8e2a-494af4e77272','references','阻塞队列的定义直接引用阻塞行为：容量满时生产者等待、队列空时消费者等待，该行为构成阻塞队列的基本语义。',NULL,'llm_inferred',0.88,NULL,0,NULL,'2026-10-24 13:42:18.73034862 +0000 UTC');
INSERT INTO "edges" VALUES(7,'domain::并发阻塞队列与背压管理','subdomain::并发阻塞队列与背压管理::背压与重试策略','composes','领域包含子域',NULL,'llm_inferred',1.0,NULL,0,NULL,'2026-10-24 13:42:18.73034862 +0000 UTC');
INSERT INTO "edges" VALUES(8,'subdomain::并发阻塞队列与背压管理::背压与重试策略','c3123633-9fad-5391-8d39-ba4d7b393783','composes','子域包含概念',NULL,'llm_inferred',1.0,NULL,0,NULL,'2026-10-24 13:42:18.73034862 +0000 UTC');
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
INSERT INTO "kb_version" VALUES('2026-10-24T13:42:18.743759595Z','2026-10-24T13:42:18.743759595Z');
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
INSERT INTO "node_sources" VALUES('bbcab434-24ad-5c47-8e2a-494af4e77272','public-synthetic-queue-concept-阻塞行为','public-synthetic-queue','references/queue.md',NULL,NULL);
INSERT INTO "node_sources" VALUES('c3123633-9fad-5391-8d39-ba4d7b393783','public-synthetic-queue-concept-重试预算','public-synthetic-queue','references/queue.md',NULL,NULL);
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
INSERT INTO "nodes" VALUES('skill::public-synthetic-queue','Skill','public-synthetic-queue','合成队列示例 / Synthetic queue example',NULL,'public-synthetic-queue','',NULL,NULL,NULL,NULL,0.0,'llm_inferred',NULL,'2026-10-24 13:42:18.73034862 +0000 UTC','2026-10-24 13:42:18.73034862 +0000 UTC',NULL);
INSERT INTO "nodes" VALUES('domain::并发阻塞队列与背压管理','Domain','并发阻塞队列与背压管理','围绕有界阻塞队列的阻塞语义、生产者等待策略与重试预算进行建模，覆盖并发协作与背压控制。',NULL,'并发阻塞队列与背压管理','','复用性理由 / Reusability: 有界阻塞队列、阻塞等待规则与重试预算是跨语言、跨框架通用的并发编程原语，可复用于生产者-消费者、异步任务管道、限流背压与失败重试等场景，因此值得作为独立领域沉淀。',NULL,NULL,NULL,0.0,'llm_inferred','public-synthetic-queue','2026-10-24 13:42:18.73034862 +0000 UTC','2026-10-24 13:42:18.73034862 +0000 UTC',NULL);
INSERT INTO "nodes" VALUES('subdomain::并发阻塞队列与背压管理::阻塞队列核心语义','Subdomain','阻塞队列核心语义','定义有界阻塞队列及其在满/空条件下的阻塞等待规则。',NULL,'并发阻塞队列与背压管理','阻塞队列核心语义',NULL,NULL,NULL,NULL,0.0,'llm_inferred',NULL,'2026-10-24 13:42:18.73034862 +0000 UTC','2026-10-24 13:42:18.73034862 +0000 UTC',NULL);
INSERT INTO "nodes" VALUES('bb69eb4c-5110-5e11-9b55-12ece9f73dd8','Entity','阻塞队列','一种容量有限、在队列满时阻塞生产者、在队列空时阻塞消费者的并发队列。',NULL,'并发阻塞队列与背压管理','阻塞队列核心语义','阻塞队列是一种有界并发队列，本虚构文档中其容量为 8。队列达到容量上限（满）时，生产者需要等待；队列无元素（空）时，消费者需要等待。也就是说，有界队列在满或空时都会发生阻塞，这解释了阻塞队列的基本含义。',NULL,NULL,NULL,0.96,'llm_inferred','public-synthetic-queue','2026-10-24 13:42:18.73034862 +0000 UTC','2026-10-24 13:42:18.73034862 +0000 UTC','blocking-queue');
INSERT INTO "nodes" VALUES('bbcab434-24ad-5c47-8e2a-494af4e77272','Concept','阻塞行为','队列在容量满或为空时使生产者或消费者等待的规则语义。',NULL,'并发阻塞队列与背压管理','阻塞队列核心语义','阻塞行为指有界队列在达到容量上限（满）时让生产者等待、在无元素（空）时让消费者等待的规则语义，即 Bounded queue blocks when full or empty。该行为是阻塞队列的基本语义。',NULL,NULL,NULL,0.95,'llm_inferred','public-synthetic-queue','2026-10-24 13:42:18.73034862 +0000 UTC','2026-10-24 13:42:18.73034862 +0000 UTC','blocking-behavior');
INSERT INTO "nodes" VALUES('subdomain::并发阻塞队列与背压管理::背压与重试策略','Subdomain','背压与重试策略','当生产者在阻塞队列上不能无限等待时，由调用方采用的重试预算与背压控制。',NULL,'并发阻塞队列与背压管理','背压与重试策略',NULL,NULL,NULL,NULL,0.0,'llm_inferred',NULL,'2026-10-24 13:42:18.73034862 +0000 UTC','2026-10-24 13:42:18.73034862 +0000 UTC',NULL);
INSERT INTO "nodes" VALUES('c3123633-9fad-5391-8d39-ba4d7b393783','Concept','重试预算','生产者在无法无限等待时由调用方使用的有限重试额度，用于在阻塞行为之上进行背压控制。',NULL,'并发阻塞队列与背压管理','背压与重试策略','当生产者不能在阻塞队列上无限等待时，调用方使用重试预算（retry budget）。跨文档意图表明：重试预算依赖队列的阻塞行为，而不是与队列无关的独立概念；它是在阻塞行为之上由调用方采用的有限重试额度，用于进行背压控制。该说明属于合成输入中的语义，不是已观测的图谱输出。',NULL,NULL,NULL,0.9,'llm_inferred','public-synthetic-queue','2026-10-24 13:42:18.73034862 +0000 UTC','2026-10-24 13:42:18.73034862 +0000 UTC','retry-budget');
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
INSERT INTO "nodes_fts"(rowid,"name","summary","synonyms","tags","description","domain","subdomain") VALUES(2,'并发阻塞队列与背压管理','围绕有界阻塞队列的阻塞语义、生产者等待策略与重试预算进行建模，覆盖并发协作与背压控制。',NULL,NULL,'复用性理由 / Reusability: 有界阻塞队列、阻塞等待规则与重试预算是跨语言、跨框架通用的并发编程原语，可复用于生产者-消费者、异步任务管道、限流背压与失败重试等场景，因此值得作为独立领域沉淀。','并发阻塞队列与背压管理','');
INSERT INTO "nodes_fts"(rowid,"name","summary","synonyms","tags","description","domain","subdomain") VALUES(3,'阻塞队列核心语义','定义有界阻塞队列及其在满/空条件下的阻塞等待规则。',NULL,NULL,NULL,'并发阻塞队列与背压管理','阻塞队列核心语义');
INSERT INTO "nodes_fts"(rowid,"name","summary","synonyms","tags","description","domain","subdomain") VALUES(4,'阻塞队列','一种容量有限、在队列满时阻塞生产者、在队列空时阻塞消费者的并发队列。',NULL,NULL,'阻塞队列是一种有界并发队列，本虚构文档中其容量为 8。队列达到容量上限（满）时，生产者需要等待；队列无元素（空）时，消费者需要等待。也就是说，有界队列在满或空时都会发生阻塞，这解释了阻塞队列的基本含义。','并发阻塞队列与背压管理','阻塞队列核心语义');
INSERT INTO "nodes_fts"(rowid,"name","summary","synonyms","tags","description","domain","subdomain") VALUES(5,'阻塞行为','队列在容量满或为空时使生产者或消费者等待的规则语义。',NULL,NULL,'阻塞行为指有界队列在达到容量上限（满）时让生产者等待、在无元素（空）时让消费者等待的规则语义，即 Bounded queue blocks when full or empty。该行为是阻塞队列的基本语义。','并发阻塞队列与背压管理','阻塞队列核心语义');
INSERT INTO "nodes_fts"(rowid,"name","summary","synonyms","tags","description","domain","subdomain") VALUES(6,'背压与重试策略','当生产者在阻塞队列上不能无限等待时，由调用方采用的重试预算与背压控制。',NULL,NULL,NULL,'并发阻塞队列与背压管理','背压与重试策略');
INSERT INTO "nodes_fts"(rowid,"name","summary","synonyms","tags","description","domain","subdomain") VALUES(7,'重试预算','生产者在无法无限等待时由调用方使用的有限重试额度，用于在阻塞行为之上进行背压控制。',NULL,NULL,'当生产者不能在阻塞队列上无限等待时，调用方使用重试预算（retry budget）。跨文档意图表明：重试预算依赖队列的阻塞行为，而不是与队列无关的独立概念；它是在阻塞行为之上由调用方采用的有限重试额度，用于进行背压控制。该说明属于合成输入中的语义，不是已观测的图谱输出。','并发阻塞队列与背压管理','背压与重试策略');
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
