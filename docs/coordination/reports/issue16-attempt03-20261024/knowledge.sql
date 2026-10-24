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
INSERT INTO "edges" VALUES(1,'skill::public-synthetic-queue','domain::并发队列与重试控制','provides','skill public-synthetic-queue 贡献了领域 并发队列与重试控制',NULL,'llm_inferred',1.0,'public-synthetic-queue',0,NULL,'2026-10-24 14:49:29.868548372 +0000 UTC');
INSERT INTO "edges" VALUES(2,'domain::并发队列与重试控制','subdomain::并发队列与重试控制::阻塞队列模型','composes','领域包含子域',NULL,'llm_inferred',1.0,NULL,0,NULL,'2026-10-24 14:49:29.868548372 +0000 UTC');
INSERT INTO "edges" VALUES(3,'subdomain::并发队列与重试控制::阻塞队列模型','87177315-8d2f-52d8-9d48-68fcf2afeb4e','composes','子域包含实体',NULL,'llm_inferred',1.0,NULL,0,NULL,'2026-10-24 14:49:29.868548372 +0000 UTC');
INSERT INTO "edges" VALUES(4,'subdomain::并发队列与重试控制::阻塞队列模型','5a9ab1d8-f65d-504b-a743-33138f36c8b6','composes','子域包含实体',NULL,'llm_inferred',1.0,NULL,0,NULL,'2026-10-24 14:49:29.868548372 +0000 UTC');
INSERT INTO "edges" VALUES(5,'subdomain::并发队列与重试控制::阻塞队列模型','3456e85c-77bb-575e-9382-89408577ddde','composes','子域包含实体',NULL,'llm_inferred',1.0,NULL,0,NULL,'2026-10-24 14:49:29.868548372 +0000 UTC');
INSERT INTO "edges" VALUES(6,'subdomain::并发队列与重试控制::阻塞队列模型','85f91f1c-05be-5486-9594-2a3fe6f3fd3b','composes','子域包含概念',NULL,'llm_inferred',1.0,NULL,0,NULL,'2026-10-24 14:49:29.868548372 +0000 UTC');
INSERT INTO "edges" VALUES(7,'domain::并发队列与重试控制','subdomain::并发队列与重试控制::超时与重试','composes','领域包含子域',NULL,'llm_inferred',1.0,NULL,0,NULL,'2026-10-24 14:49:29.868548372 +0000 UTC');
INSERT INTO "edges" VALUES(8,'subdomain::并发队列与重试控制::超时与重试','c3123633-9fad-5391-8d39-ba4d7b393783','composes','子域包含概念',NULL,'llm_inferred',1.0,NULL,0,NULL,'2026-10-24 14:49:29.868548372 +0000 UTC');
INSERT INTO "edges" VALUES(9,'subdomain::并发队列与重试控制::超时与重试','67caf821-d848-5bd1-bad2-8838ab6e602c','composes','子域包含概念',NULL,'llm_inferred',1.0,NULL,0,NULL,'2026-10-24 14:49:29.868548372 +0000 UTC');
INSERT INTO "edges" VALUES(10,'subdomain::并发队列与重试控制::超时与重试','698d2de1-6b03-5972-8a88-ceee42f5816b','composes','子域包含概念',NULL,'llm_inferred',1.0,NULL,0,NULL,'2026-10-24 14:49:29.868548372 +0000 UTC');
INSERT INTO "edges" VALUES(11,'skill::public-synthetic-queue','domain::合成文档与验证','provides','skill public-synthetic-queue 贡献了领域 合成文档与验证',NULL,'llm_inferred',1.0,'public-synthetic-queue',0,NULL,'2026-10-24 14:49:29.868548372 +0000 UTC');
INSERT INTO "edges" VALUES(12,'domain::合成文档与验证','subdomain::合成文档与验证::合成输入与假运行器','composes','领域包含子域',NULL,'llm_inferred',1.0,NULL,0,NULL,'2026-10-24 14:49:29.868548372 +0000 UTC');
INSERT INTO "edges" VALUES(13,'subdomain::合成文档与验证::合成输入与假运行器','7d779d1f-57d2-5f13-b71b-7b929e02e152','composes','子域包含实体',NULL,'llm_inferred',1.0,NULL,0,NULL,'2026-10-24 14:49:29.868548372 +0000 UTC');
INSERT INTO "edges" VALUES(14,'subdomain::合成文档与验证::合成输入与假运行器','672d23d4-1865-5d4f-8880-b56a77d84ec9','composes','子域包含实体',NULL,'llm_inferred',1.0,NULL,0,NULL,'2026-10-24 14:49:29.868548372 +0000 UTC');
INSERT INTO "edges" VALUES(15,'subdomain::合成文档与验证::合成输入与假运行器','9b12848f-fdb0-5f4e-973d-7ac29039774d','composes','子域包含概念',NULL,'llm_inferred',1.0,NULL,0,NULL,'2026-10-24 14:49:29.868548372 +0000 UTC');
INSERT INTO "edges" VALUES(16,'subdomain::合成文档与验证::合成输入与假运行器','81dc5edf-699f-55fa-81cb-5ce972b6552d','composes','子域包含概念',NULL,'llm_inferred',1.0,NULL,0,NULL,'2026-10-24 14:49:29.868548372 +0000 UTC');
INSERT INTO "edges" VALUES(17,'5a9ab1d8-f65d-504b-a743-33138f36c8b6','87177315-8d2f-52d8-9d48-68fcf2afeb4e','depends_on','生产者向阻塞队列提交元素，其行为依赖队列的容量与阻塞状态。',NULL,'llm_inferred',0.9,NULL,0,NULL,'2026-10-24 14:49:29.868548372 +0000 UTC');
INSERT INTO "edges" VALUES(18,'3456e85c-77bb-575e-9382-89408577ddde','87177315-8d2f-52d8-9d48-68fcf2afeb4e','depends_on','消费者从阻塞队列获取元素，其行为依赖队列的阻塞状态。',NULL,'llm_inferred',0.9,NULL,0,NULL,'2026-10-24 14:49:29.868548372 +0000 UTC');
INSERT INTO "edges" VALUES(19,'85f91f1c-05be-5486-9594-2a3fe6f3fd3b','87177315-8d2f-52d8-9d48-68fcf2afeb4e','references','有界队列阻塞规则描述并约束阻塞队列的阻塞行为。',NULL,'llm_inferred',0.88,NULL,0,NULL,'2026-10-24 14:49:29.868548372 +0000 UTC');
INSERT INTO "edges" VALUES(20,'5a9ab1d8-f65d-504b-a743-33138f36c8b6','85f91f1c-05be-5486-9594-2a3fe6f3fd3b','depends_on','生产者在队列满时的等待行为遵循有界队列阻塞规则。',NULL,'llm_inferred',0.86,NULL,0,NULL,'2026-10-24 14:49:29.868548372 +0000 UTC');
INSERT INTO "edges" VALUES(21,'3456e85c-77bb-575e-9382-89408577ddde','85f91f1c-05be-5486-9594-2a3fe6f3fd3b','depends_on','消费者在队列空时的等待行为遵循有界队列阻塞规则。',NULL,'llm_inferred',0.86,NULL,0,NULL,'2026-10-24 14:49:29.868548372 +0000 UTC');
INSERT INTO "edges" VALUES(22,'67caf821-d848-5bd1-bad2-8838ab6e602c','c3123633-9fad-5391-8d39-ba4d7b393783','depends_on','超时与重试机制由重试预算进行约束。',NULL,'llm_inferred',0.9,NULL,0,NULL,'2026-10-24 14:49:29.868548372 +0000 UTC');
INSERT INTO "edges" VALUES(23,'698d2de1-6b03-5972-8a88-ceee42f5816b','67caf821-d848-5bd1-bad2-8838ab6e602c','depends_on','可见错误在重试机制耗尽预算后产生。',NULL,'llm_inferred',0.85,NULL,0,NULL,'2026-10-24 14:49:29.868548372 +0000 UTC');
INSERT INTO "edges" VALUES(24,'c3123633-9fad-5391-8d39-ba4d7b393783','87177315-8d2f-52d8-9d48-68fcf2afeb4e','depends_on','重试预算所依赖的队列组件，其容量与等待行为影响预算。',NULL,'llm_inferred',0.85,NULL,0,NULL,'2026-10-24 14:49:29.868548372 +0000 UTC');
INSERT INTO "edges" VALUES(25,'c3123633-9fad-5391-8d39-ba4d7b393783','698d2de1-6b03-5972-8a88-ceee42f5816b','triggers','重试预算用尽后返回可见错误，预算耗尽是触发可见错误的直接原因。',NULL,'llm_inferred',0.92,NULL,0,NULL,'2026-10-24 14:49:29.868548372 +0000 UTC');
INSERT INTO "edges" VALUES(26,'67caf821-d848-5bd1-bad2-8838ab6e602c','698d2de1-6b03-5972-8a88-ceee42f5816b','references','超时与重试机制以可见错误作为预算耗尽后的明确反馈，机制描述中引用了该错误结果。',NULL,'llm_inferred',0.75,NULL,0,NULL,'2026-10-24 14:49:29.868548372 +0000 UTC');
INSERT INTO "edges" VALUES(27,'7d779d1f-57d2-5f13-b71b-7b929e02e152','672d23d4-1865-5d4f-8880-b56a77d84ec9','references','固定假运行器不消费合成输入，二者为引用但非消费关系。',NULL,'llm_inferred',0.85,NULL,0,NULL,'2026-10-24 14:49:29.868548372 +0000 UTC');
INSERT INTO "edges" VALUES(28,'81dc5edf-699f-55fa-81cb-5ce972b6552d','7d779d1f-57d2-5f13-b71b-7b929e02e152','depends_on','零结果规则基于固定假运行器不含重试预算而成立。',NULL,'llm_inferred',0.85,NULL,0,NULL,'2026-10-24 14:49:29.868548372 +0000 UTC');
INSERT INTO "edges" VALUES(29,'672d23d4-1865-5d4f-8880-b56a77d84ec9','7d779d1f-57d2-5f13-b71b-7b929e02e152','references','合成输入被明确说明不会被固定假运行器消费，体现两者之间的关联。',NULL,'llm_inferred',0.95,NULL,0,NULL,'2026-10-24 14:49:29.868548372 +0000 UTC');
INSERT INTO "edges" VALUES(30,'9b12848f-fdb0-5f4e-973d-7ac29039774d','672d23d4-1865-5d4f-8880-b56a77d84ec9','references','跨文档关系保留检验设想未来用真实 LLM 构建这些合成材料。',NULL,'llm_inferred',0.75,NULL,0,NULL,'2026-10-24 14:49:29.868548372 +0000 UTC');
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
INSERT INTO "kb_version" VALUES('2026-10-24T14:49:29.928541812Z','2026-10-24T14:49:29.928541812Z');
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
INSERT INTO "node_sources" VALUES('87177315-8d2f-52d8-9d48-68fcf2afeb4e','public-synthetic-queue-entity-容量为8的阻塞队列','public-synthetic-queue','references/queue.md',NULL,NULL);
INSERT INTO "node_sources" VALUES('87177315-8d2f-52d8-9d48-68fcf2afeb4e','public-synthetic-queue-entity-阻塞队列','public-synthetic-queue','references/timeout.md',NULL,NULL);
INSERT INTO "node_sources" VALUES('5a9ab1d8-f65d-504b-a743-33138f36c8b6','public-synthetic-queue-entity-生产者','public-synthetic-queue','references/queue.md',NULL,NULL);
INSERT INTO "node_sources" VALUES('5a9ab1d8-f65d-504b-a743-33138f36c8b6','public-synthetic-queue-entity-生产者','public-synthetic-queue','references/timeout.md',NULL,NULL);
INSERT INTO "node_sources" VALUES('3456e85c-77bb-575e-9382-89408577ddde','public-synthetic-queue-entity-消费者','public-synthetic-queue','references/queue.md',NULL,NULL);
INSERT INTO "node_sources" VALUES('85f91f1c-05be-5486-9594-2a3fe6f3fd3b','public-synthetic-queue-concept-有界队列阻塞规则','public-synthetic-queue','references/queue.md',NULL,NULL);
INSERT INTO "node_sources" VALUES('c3123633-9fad-5391-8d39-ba4d7b393783','public-synthetic-queue-concept-重试预算','public-synthetic-queue','references/queue.md',NULL,NULL);
INSERT INTO "node_sources" VALUES('c3123633-9fad-5391-8d39-ba4d7b393783','public-synthetic-queue-concept-重试预算','public-synthetic-queue','references/timeout.md',NULL,NULL);
INSERT INTO "node_sources" VALUES('67caf821-d848-5bd1-bad2-8838ab6e602c','public-synthetic-queue-concept-超时与重试机制','public-synthetic-queue','references/timeout.md',NULL,NULL);
INSERT INTO "node_sources" VALUES('698d2de1-6b03-5972-8a88-ceee42f5816b','public-synthetic-queue-concept-可见错误','public-synthetic-queue','references/timeout.md',NULL,NULL);
INSERT INTO "node_sources" VALUES('7d779d1f-57d2-5f13-b71b-7b929e02e152','public-synthetic-queue-entity-fixed-fake-runner','public-synthetic-queue','references/timeout.md',NULL,NULL);
INSERT INTO "node_sources" VALUES('672d23d4-1865-5d4f-8880-b56a77d84ec9','public-synthetic-queue-entity-合成输入','public-synthetic-queue','references/timeout.md',NULL,NULL);
INSERT INTO "node_sources" VALUES('9b12848f-fdb0-5f4e-973d-7ac29039774d','public-synthetic-queue-concept-跨文档关系保留检验','public-synthetic-queue','references/timeout.md',NULL,NULL);
INSERT INTO "node_sources" VALUES('81dc5edf-699f-55fa-81cb-5ce972b6552d','public-synthetic-queue-concept-重试预算零结果规则','public-synthetic-queue','references/timeout.md',NULL,NULL);
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
INSERT INTO "nodes" VALUES('skill::public-synthetic-queue','Skill','public-synthetic-queue','合成队列示例 / Synthetic queue example',NULL,'public-synthetic-queue','',NULL,NULL,NULL,NULL,0.0,'llm_inferred',NULL,'2026-10-24 14:49:29.868548372 +0000 UTC','2026-10-24 14:49:29.868548372 +0000 UTC',NULL);
INSERT INTO "nodes" VALUES('domain::并发队列与重试控制','Domain','并发队列与重试控制','围绕阻塞队列的容量、阻塞行为以及生产者/消费者的超时重试进行建模的领域。',NULL,'并发队列与重试控制','','复用性理由 / Reusability: 阻塞队列、有界阻塞规则、生产者/消费者、重试预算与超时错误是并发编程与消息中间件中高度通用的基础构件，几乎任何涉及限流、背压与失败重试的系统都可复用这些实体与概念。',NULL,NULL,NULL,0.0,'llm_inferred','public-synthetic-queue','2026-10-24 14:49:29.868548372 +0000 UTC','2026-10-24 14:49:29.868548372 +0000 UTC',NULL);
INSERT INTO "nodes" VALUES('subdomain::并发队列与重试控制::阻塞队列模型','Subdomain','阻塞队列模型','描述阻塞队列本体、其阻塞行为规则以及与之交互的生产者和消费者。',NULL,'并发队列与重试控制','阻塞队列模型',NULL,NULL,NULL,NULL,0.0,'llm_inferred',NULL,'2026-10-24 14:49:29.868548372 +0000 UTC','2026-10-24 14:49:29.868548372 +0000 UTC',NULL);
INSERT INTO "nodes" VALUES('87177315-8d2f-52d8-9d48-68fcf2afeb4e','Entity','阻塞队列','具有固定容量（如容量为8）的队列组件，满时阻塞生产者、空时阻塞消费者。',NULL,'并发队列与重试控制','阻塞队列模型','阻塞队列是文档中描述的虚构有界队列组件，容量为 8。其基本语义是：当队列已满时，生产者不能继续入队而进入等待；当队列为空时，消费者不能继续出队而进入等待。这对应英文描述 Bounded queue blocks when full or empty。该队列也是文档中重试预算所依赖的队列组件；文档指出重试预算依赖阻塞队列的容量与等待行为，但未展开阻塞队列的其他细节。',NULL,NULL,NULL,0.92,'llm_inferred','public-synthetic-queue','2026-10-24 14:49:29.868548372 +0000 UTC','2026-10-24 14:49:29.868548372 +0000 UTC','blocking-queue');
INSERT INTO "nodes" VALUES('5a9ab1d8-f65d-504b-a743-33138f36c8b6','Entity','生产者','向阻塞队列提交元素的角色，队列满时进入等待。',NULL,'并发队列与重试控制','阻塞队列模型','生产者是向阻塞队列提交元素的角色。在容量为 8 的虚构队列中，当队列已满时，生产者不能继续提交而进入等待；当生产者不能无限等待时，调用方使用重试预算。在文档的虚构示例中，生产者在超时与重试机制中最多等待 50 毫秒，最多重试 2 次，预算用尽后返回可见错误。该行为仅用于输入结构演示，不是调优建议。',NULL,NULL,NULL,0.9,'llm_inferred','public-synthetic-queue','2026-10-24 14:49:29.868548372 +0000 UTC','2026-10-24 14:49:29.868548372 +0000 UTC','producer');
INSERT INTO "nodes" VALUES('3456e85c-77bb-575e-9382-89408577ddde','Entity','消费者','从阻塞队列获取元素的角色，队列空时进入等待。',NULL,'并发队列与重试控制','阻塞队列模型','消费者是从阻塞队列获取元素的角色。在容量为 8 的虚构队列中，当队列为空时，消费者不能继续获取而进入等待。',NULL,NULL,NULL,0.9,'llm_inferred','public-synthetic-queue','2026-10-24 14:49:29.868548372 +0000 UTC','2026-10-24 14:49:29.868548372 +0000 UTC','consumer');
INSERT INTO "nodes" VALUES('85f91f1c-05be-5486-9594-2a3fe6f3fd3b','Concept','有界队列阻塞规则','有界队列在满时阻塞生产者、空时阻塞消费者的行为规则。',NULL,'并发队列与重试控制','阻塞队列模型','有界队列阻塞规则指：当有界队列达到容量上限（本文为 8）时，生产者需要等待；当队列为空时，消费者需要等待。该规则解释了阻塞队列的基本含义，英文表述为 Bounded queue blocks when full or empty。',NULL,NULL,NULL,0.9,'llm_inferred','public-synthetic-queue','2026-10-24 14:49:29.868548372 +0000 UTC','2026-10-24 14:49:29.868548372 +0000 UTC','bounded-queue-blocking-rule');
INSERT INTO "nodes" VALUES('subdomain::并发队列与重试控制::超时与重试','Subdomain','超时与重试','描述生产者在无法无限等待时的超时等待、有限次重试以及重试耗尽后的可见错误。',NULL,'并发队列与重试控制','超时与重试',NULL,NULL,NULL,NULL,0.0,'llm_inferred',NULL,'2026-10-24 14:49:29.868548372 +0000 UTC','2026-10-24 14:49:29.868548372 +0000 UTC',NULL);
INSERT INTO "nodes" VALUES('c3123633-9fad-5391-8d39-ba4d7b393783','Concept','重试预算','生产者在不能无限等待时由调用方使用的重试预算机制，约束重试次数。',NULL,'并发队列与重试控制','超时与重试','重试预算是当生产者不能无限等待时，由调用方使用的机制，用于约束重试次数。该机制依赖队列的阻塞行为，尤其是阻塞队列的容量与等待行为，而不是与队列无关的独立概念；相关语义属于合成输入中的说明，不是已观测的图谱输出。文档中的虚构示例将重试预算具体化为：生产者最多等待 50 毫秒、最多重试 2 次，预算用尽后返回可见错误；这些数字仅用于输入结构演示，不是调优建议。文档还说明，在免费 fixed fake 中不存在重试预算，查询它应明确得到零结果。',NULL,NULL,NULL,0.9,'llm_inferred','public-synthetic-queue','2026-10-24 14:49:29.868548372 +0000 UTC','2026-10-24 14:49:29.868548372 +0000 UTC','retry-budget');
INSERT INTO "nodes" VALUES('67caf821-d848-5bd1-bad2-8838ab6e602c','Concept','超时与重试机制','生产者的超时等待与有限次重试机制，由重试预算约束。',NULL,'并发队列与重试控制','超时与重试','超时与重试机制是文档的主题，描述生产者在等待或调用过程中设置超时上限并进行有限次重试的总体机制，并由重试预算约束。文档的虚构示例通过重试预算具体化该机制：生产者最多等待 50 毫秒、最多重试 2 次，预算用尽后返回可见错误；这些数字仅用于演示输入结构，不是调优建议。',NULL,NULL,NULL,0.9,'llm_inferred','public-synthetic-queue','2026-10-24 14:49:29.868548372 +0000 UTC','2026-10-24 14:49:29.868548372 +0000 UTC','timeout-and-retry-mechanism');
INSERT INTO "nodes" VALUES('698d2de1-6b03-5972-8a88-ceee42f5816b','Concept','可见错误','重试预算用尽后返回给调用方的可观测错误结果。',NULL,'并发队列与重试控制','超时与重试','可见错误是重试预算用尽后返回给调用方的错误结果，用于表明生产者已用完最多 2 次重试和最多 50 毫秒等待的预算，无法继续等待或重试。文档将其作为预算耗尽后的明确反馈。',NULL,NULL,NULL,0.88,'llm_inferred','public-synthetic-queue','2026-10-24 14:49:29.868548372 +0000 UTC','2026-10-24 14:49:29.868548372 +0000 UTC','visible-error');
INSERT INTO "nodes" VALUES('domain::合成文档与验证','Domain','合成文档与验证','围绕用于结构演示的合成输入、固定假运行器以及跨文档关系保真检验进行建模的领域。',NULL,'合成文档与验证','','复用性理由 / Reusability: 合成输入、假运行器、零结果规则与跨文档关系保留检验是任何基于合成数据做知识构建与质量验证系统的通用组件，可跨文档处理、测试替身与评估流程复用。',NULL,NULL,NULL,0.0,'llm_inferred','public-synthetic-queue','2026-10-24 14:49:29.868548372 +0000 UTC','2026-10-24 14:49:29.868548372 +0000 UTC',NULL);
INSERT INTO "nodes" VALUES('subdomain::合成文档与验证::合成输入与假运行器','Subdomain','合成输入与假运行器','描述作为结构演示的合成输入、不消费它的固定假运行器，以及相应的验证与零结果规则。',NULL,'合成文档与验证','合成输入与假运行器',NULL,NULL,NULL,NULL,0.0,'llm_inferred',NULL,'2026-10-24 14:49:29.868548372 +0000 UTC','2026-10-24 14:49:29.868548372 +0000 UTC',NULL);
INSERT INTO "nodes" VALUES('7d779d1f-57d2-5f13-b71b-7b929e02e152','Entity','固定假运行器','不消费本文合成输入的固定假运行器，其免费版本不含重试预算。',NULL,'合成文档与验证','合成输入与假运行器','固定假运行器是文档提到的固定假运行器；本文档的合成输入不会被它消费。文档还说明在免费 fixed fake 中不存在重试预算，因此查询重试预算应明确得到零结果。',NULL,NULL,NULL,0.88,'llm_inferred','public-synthetic-queue','2026-10-24 14:49:29.868548372 +0000 UTC','2026-10-24 14:49:29.868548372 +0000 UTC','fixed-fake-runner');
INSERT INTO "nodes" VALUES('672d23d4-1865-5d4f-8880-b56a77d84ec9','Entity','合成输入','作为结构演示的合成输入，不被固定假运行器消费。',NULL,'合成文档与验证','合成输入与假运行器','合成输入是本文档所标注的输入材料，文档明确声明它是合成输入，不会被 fixed fake runner 消费；其中的 50 毫秒等待和 2 次重试等数字仅用于演示输入结构，不是调优建议。',NULL,NULL,NULL,0.88,'llm_inferred','public-synthetic-queue','2026-10-24 14:49:29.868548372 +0000 UTC','2026-10-24 14:49:29.868548372 +0000 UTC','synthetic-input');
INSERT INTO "nodes" VALUES('9b12848f-fdb0-5f4e-973d-7ac29039774d','Concept','跨文档关系保留检验','未来用真实 LLM 构建材料时检验是否保留重试预算与阻塞队列跨文档关系的设想。',NULL,'合成文档与验证','合成输入与假运行器','跨文档关系保留检验是文档提出的一个未来检验设想：如果未来用真实 LLM 构建这些材料，可以检验是否保留“重试预算依赖阻塞队列容量与等待行为”这条跨文档关系；本次没有运行该抽取。它属于对抽取流程的元说明，而非当前已执行的操作。',NULL,NULL,NULL,0.85,'llm_inferred','public-synthetic-queue','2026-10-24 14:49:29.868548372 +0000 UTC','2026-10-24 14:49:29.868548372 +0000 UTC','cross-doc-relation-retention-test');
INSERT INTO "nodes" VALUES('81dc5edf-699f-55fa-81cb-5ce972b6552d','Concept','重试预算零结果规则','免费固定假运行器中不存在重试预算，查询它应明确得到零结果。',NULL,'合成文档与验证','合成输入与假运行器','重试预算零结果规则是文档给出的约束：免费 fixed fake 中不存在重试预算，因此查询重试预算时应明确得到零结果。该规则与 fixed fake runner 和重试预算的缺失状态相关。',NULL,NULL,NULL,0.87,'llm_inferred','public-synthetic-queue','2026-10-24 14:49:29.868548372 +0000 UTC','2026-10-24 14:49:29.868548372 +0000 UTC','retry-budget-zero-results-rule');
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
INSERT INTO "nodes_fts"(rowid,"name","summary","synonyms","tags","description","domain","subdomain") VALUES(2,'并发队列与重试控制','围绕阻塞队列的容量、阻塞行为以及生产者/消费者的超时重试进行建模的领域。',NULL,NULL,'复用性理由 / Reusability: 阻塞队列、有界阻塞规则、生产者/消费者、重试预算与超时错误是并发编程与消息中间件中高度通用的基础构件，几乎任何涉及限流、背压与失败重试的系统都可复用这些实体与概念。','并发队列与重试控制','');
INSERT INTO "nodes_fts"(rowid,"name","summary","synonyms","tags","description","domain","subdomain") VALUES(3,'阻塞队列模型','描述阻塞队列本体、其阻塞行为规则以及与之交互的生产者和消费者。',NULL,NULL,NULL,'并发队列与重试控制','阻塞队列模型');
INSERT INTO "nodes_fts"(rowid,"name","summary","synonyms","tags","description","domain","subdomain") VALUES(4,'阻塞队列','具有固定容量（如容量为8）的队列组件，满时阻塞生产者、空时阻塞消费者。',NULL,NULL,'阻塞队列是文档中描述的虚构有界队列组件，容量为 8。其基本语义是：当队列已满时，生产者不能继续入队而进入等待；当队列为空时，消费者不能继续出队而进入等待。这对应英文描述 Bounded queue blocks when full or empty。该队列也是文档中重试预算所依赖的队列组件；文档指出重试预算依赖阻塞队列的容量与等待行为，但未展开阻塞队列的其他细节。','并发队列与重试控制','阻塞队列模型');
INSERT INTO "nodes_fts"(rowid,"name","summary","synonyms","tags","description","domain","subdomain") VALUES(5,'生产者','向阻塞队列提交元素的角色，队列满时进入等待。',NULL,NULL,'生产者是向阻塞队列提交元素的角色。在容量为 8 的虚构队列中，当队列已满时，生产者不能继续提交而进入等待；当生产者不能无限等待时，调用方使用重试预算。在文档的虚构示例中，生产者在超时与重试机制中最多等待 50 毫秒，最多重试 2 次，预算用尽后返回可见错误。该行为仅用于输入结构演示，不是调优建议。','并发队列与重试控制','阻塞队列模型');
INSERT INTO "nodes_fts"(rowid,"name","summary","synonyms","tags","description","domain","subdomain") VALUES(6,'消费者','从阻塞队列获取元素的角色，队列空时进入等待。',NULL,NULL,'消费者是从阻塞队列获取元素的角色。在容量为 8 的虚构队列中，当队列为空时，消费者不能继续获取而进入等待。','并发队列与重试控制','阻塞队列模型');
INSERT INTO "nodes_fts"(rowid,"name","summary","synonyms","tags","description","domain","subdomain") VALUES(7,'有界队列阻塞规则','有界队列在满时阻塞生产者、空时阻塞消费者的行为规则。',NULL,NULL,'有界队列阻塞规则指：当有界队列达到容量上限（本文为 8）时，生产者需要等待；当队列为空时，消费者需要等待。该规则解释了阻塞队列的基本含义，英文表述为 Bounded queue blocks when full or empty。','并发队列与重试控制','阻塞队列模型');
INSERT INTO "nodes_fts"(rowid,"name","summary","synonyms","tags","description","domain","subdomain") VALUES(8,'超时与重试','描述生产者在无法无限等待时的超时等待、有限次重试以及重试耗尽后的可见错误。',NULL,NULL,NULL,'并发队列与重试控制','超时与重试');
INSERT INTO "nodes_fts"(rowid,"name","summary","synonyms","tags","description","domain","subdomain") VALUES(9,'重试预算','生产者在不能无限等待时由调用方使用的重试预算机制，约束重试次数。',NULL,NULL,'重试预算是当生产者不能无限等待时，由调用方使用的机制，用于约束重试次数。该机制依赖队列的阻塞行为，尤其是阻塞队列的容量与等待行为，而不是与队列无关的独立概念；相关语义属于合成输入中的说明，不是已观测的图谱输出。文档中的虚构示例将重试预算具体化为：生产者最多等待 50 毫秒、最多重试 2 次，预算用尽后返回可见错误；这些数字仅用于输入结构演示，不是调优建议。文档还说明，在免费 fixed fake 中不存在重试预算，查询它应明确得到零结果。','并发队列与重试控制','超时与重试');
INSERT INTO "nodes_fts"(rowid,"name","summary","synonyms","tags","description","domain","subdomain") VALUES(10,'超时与重试机制','生产者的超时等待与有限次重试机制，由重试预算约束。',NULL,NULL,'超时与重试机制是文档的主题，描述生产者在等待或调用过程中设置超时上限并进行有限次重试的总体机制，并由重试预算约束。文档的虚构示例通过重试预算具体化该机制：生产者最多等待 50 毫秒、最多重试 2 次，预算用尽后返回可见错误；这些数字仅用于演示输入结构，不是调优建议。','并发队列与重试控制','超时与重试');
INSERT INTO "nodes_fts"(rowid,"name","summary","synonyms","tags","description","domain","subdomain") VALUES(11,'可见错误','重试预算用尽后返回给调用方的可观测错误结果。',NULL,NULL,'可见错误是重试预算用尽后返回给调用方的错误结果，用于表明生产者已用完最多 2 次重试和最多 50 毫秒等待的预算，无法继续等待或重试。文档将其作为预算耗尽后的明确反馈。','并发队列与重试控制','超时与重试');
INSERT INTO "nodes_fts"(rowid,"name","summary","synonyms","tags","description","domain","subdomain") VALUES(12,'合成文档与验证','围绕用于结构演示的合成输入、固定假运行器以及跨文档关系保真检验进行建模的领域。',NULL,NULL,'复用性理由 / Reusability: 合成输入、假运行器、零结果规则与跨文档关系保留检验是任何基于合成数据做知识构建与质量验证系统的通用组件，可跨文档处理、测试替身与评估流程复用。','合成文档与验证','');
INSERT INTO "nodes_fts"(rowid,"name","summary","synonyms","tags","description","domain","subdomain") VALUES(13,'合成输入与假运行器','描述作为结构演示的合成输入、不消费它的固定假运行器，以及相应的验证与零结果规则。',NULL,NULL,NULL,'合成文档与验证','合成输入与假运行器');
INSERT INTO "nodes_fts"(rowid,"name","summary","synonyms","tags","description","domain","subdomain") VALUES(14,'固定假运行器','不消费本文合成输入的固定假运行器，其免费版本不含重试预算。',NULL,NULL,'固定假运行器是文档提到的固定假运行器；本文档的合成输入不会被它消费。文档还说明在免费 fixed fake 中不存在重试预算，因此查询重试预算应明确得到零结果。','合成文档与验证','合成输入与假运行器');
INSERT INTO "nodes_fts"(rowid,"name","summary","synonyms","tags","description","domain","subdomain") VALUES(15,'合成输入','作为结构演示的合成输入，不被固定假运行器消费。',NULL,NULL,'合成输入是本文档所标注的输入材料，文档明确声明它是合成输入，不会被 fixed fake runner 消费；其中的 50 毫秒等待和 2 次重试等数字仅用于演示输入结构，不是调优建议。','合成文档与验证','合成输入与假运行器');
INSERT INTO "nodes_fts"(rowid,"name","summary","synonyms","tags","description","domain","subdomain") VALUES(16,'跨文档关系保留检验','未来用真实 LLM 构建材料时检验是否保留重试预算与阻塞队列跨文档关系的设想。',NULL,NULL,'跨文档关系保留检验是文档提出的一个未来检验设想：如果未来用真实 LLM 构建这些材料，可以检验是否保留“重试预算依赖阻塞队列容量与等待行为”这条跨文档关系；本次没有运行该抽取。它属于对抽取流程的元说明，而非当前已执行的操作。','合成文档与验证','合成输入与假运行器');
INSERT INTO "nodes_fts"(rowid,"name","summary","synonyms","tags","description","domain","subdomain") VALUES(17,'重试预算零结果规则','免费固定假运行器中不存在重试预算，查询它应明确得到零结果。',NULL,NULL,'重试预算零结果规则是文档给出的约束：免费 fixed fake 中不存在重试预算，因此查询重试预算时应明确得到零结果。该规则与 fixed fake runner 和重试预算的缺失状态相关。','合成文档与验证','合成输入与假运行器');
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
