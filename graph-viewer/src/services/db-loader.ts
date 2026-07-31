// ============================================================
// DBLoader — SQLite 数据库加载器 / SQLite Database Loader
//
// 使用 sql.js 在浏览器内存中直接打开 SQLite .db 文件。
// sql.js 是 SQLite 的 WASM 编译版本，支持从 ArrayBuffer
// 一行构造 Database 对象，无需 VFS 配置或 COOP/COEP headers。
//
// Uses sql.js to open SQLite .db files directly in browser
// memory. sql.js is SQLite compiled to WASM — it constructs a
// Database from an ArrayBuffer in a single call, no VFS config
// or COOP/COEP headers required.
// ============================================================

import initSqlJs, { type Database } from 'sql.js'
import type { KGNode, KGEdge, GraphLoadResult, NodeLabel, SentinelSample } from '../types'

// ============================================================
// DBLoader 类 / DBLoader Class
// ============================================================

export class DBLoader {
  private db: Database | null = null

  /**
   * 从 File 对象加载数据库 / Load database from File object
   *
   * 流程 / Workflow:
   *   1. 读取文件为 ArrayBuffer / Read file as ArrayBuffer
   *   2. 初始化 sql.js WASM 模块 / Initialize sql.js WASM module
   *   3. 从 buffer 构造内存数据库 / Construct in-memory DB from buffer
   *   4. 查询 nodes / edges 表，映射为类型化数据 / Query and map
   *   5. 计算统计信息 / Compute metadata
   *
   * @param file  - 用户选择的 .db 文件
   * @param dbName - 数据库名称（仅用于元信息，不影响加载）
   * @returns 包含节点、边和元信息的加载结果
   */
  async loadFromFile(file: File, dbName: string): Promise<GraphLoadResult> {
    // 先关闭旧连接 / Close previous connection first
    this.close()

    try {
      // Step 1: 读取文件 / Read file
      const buffer = await file.arrayBuffer()
      return await this.openBuffer(buffer)
    } catch (err: any) {
      // 检测常见错误 / Detect common errors
      if (err.message?.includes('no such table')) {
        throw new Error(
          'Invalid knowledge graph database: nodes table not found. ' +
          'Ensure the .db file was produced by cairn.',
        )
      }
      throw new Error(`Failed to load database: ${err.message || err}`)
    }
  }

  /**
   * 从 URL 加载数据库（E-5 演化模式快照懒加载）。
   *
   * 与 loadFromFile 同构：fetch 拿到 ArrayBuffer 后走完全相同的
   * sql.js 构造与查询路径（queryNodes/queryEdges 等既有行为不变）。
   *
   * Load database from a URL (E-5 evolution snapshot lazy loading).
   * Identical to loadFromFile once the ArrayBuffer is obtained.
   *
   * @param url - 快照 db 的 URL（如 http://127.0.0.1:7801/snapshots/kg-000001-x.db）
   * @returns 包含节点、边和元信息的加载结果
   */
  async loadFromURL(url: string): Promise<GraphLoadResult> {
    this.close()

    try {
      const resp = await fetch(url)
      if (!resp.ok) {
        throw new Error(`HTTP ${resp.status}`)
      }
      const buffer = await resp.arrayBuffer()
      return await this.openBuffer(buffer)
    } catch (err: any) {
      if (err.message?.includes('no such table')) {
        throw new Error(
          'Invalid knowledge graph database: nodes table not found. ' +
          'Ensure the snapshot was produced by cairn.',
        )
      }
      throw new Error(`Failed to load snapshot from ${url}: ${err.message || err}`)
    }
  }

  /**
   * 从 ArrayBuffer 构造内存库并读出全部图数据（loadFromFile / loadFromURL 共用）。
   *
   * Shared path: construct the in-memory DB from a buffer and run all queries.
   */
  private async openBuffer(buffer: ArrayBuffer): Promise<GraphLoadResult> {
    // Step 2: 初始化 sql.js / Initialize sql.js
    // 预加载 WASM 二进制文件以避免 Vite 模块转换拦截。
    // Pre-fetch the WASM binary to avoid Vite module transform interception.
    const wasmResponse = await fetch('/sql-wasm.wasm')
    if (!wasmResponse.ok) {
      throw new Error('Failed to fetch SQLite WASM binary. Ensure sql-wasm.wasm is in the public/ directory.')
    }
    const wasmBinary = await wasmResponse.arrayBuffer()
    const SQL = await initSqlJs({ wasmBinary })

    // Step 3: 从 buffer 构造数据库 / Construct DB from buffer
    this.db = new SQL.Database(new Uint8Array(buffer))

    // Step 4: 查询节点并映射 / Query nodes and map
    const nodes = this.queryNodes()

    // Step 5: 查询边并映射 / Query edges and map
    const edges = this.queryEdges()

    // Step 6: 计算元信息 / Compute metadata
    const meta = this.queryMeta()

    // Step 7: 读取复杂度哨兵时序（W-B，可选；老库无此键则为空数组）
    // Read sentinel history (W-B, optional; empty for older DBs)
    const sentinelHistory = this.querySentinelHistory()

    return { nodes, edges, meta, sentinelHistory }
  }

  /**
   * 关闭数据库连接并释放 WASM 内存。
   * Close the database connection and free WASM memory.
   */
  close(): void {
    if (this.db) {
      try {
        this.db.close()
      } catch {
        /* ignore close errors */
      }
      this.db = null
    }
  }

  // ─── 私有查询方法 / Private Query Methods ──────────────────

  /**
   * 查询 nodes 表并映射为 KGNode[]。
   * Queries the nodes table and maps rows to KGNode[].
   *
   * sql.js exec() 返回 Array<{columns: string[], values: any[][]}>，
   * 其中 columns 是列名数组，values 是行数据二维数组。
   * sql.js exec() returns Array<{columns: string[], values: any[][]}>,
   * where columns is an array of column names and values is a 2D array of row data.
   */
  private queryNodes(): KGNode[] {
    if (!this.db) return []

    const results = this.db.exec('SELECT * FROM nodes')
    if (!results.length) return []

    const { columns, values } = results[0]
    const colIndex = (name: string) => columns.indexOf(name)

    return values.map((row: any[]) => ({
      id: String(row[colIndex('id')] ?? ''),
      // label 直接透传 SQL 值（Entity/Concept/Skill/Domain/Subdomain）；
      // db-loader 按列读取，新增的一等节点类型无需特殊处理即可读出。
      // Pass label through verbatim; the new first-class node types
      // (Skill/Domain/Subdomain) require no special handling here.
      label: (row[colIndex('label')] ?? 'Entity') as NodeLabel,
      name: String(row[colIndex('name')] ?? ''),
      summary: String(row[colIndex('summary')] ?? ''),
      synonyms: row[colIndex('synonyms')] ? String(row[colIndex('synonyms')]) : null,
      domain: String(row[colIndex('domain')] ?? ''),
      subdomain: String(row[colIndex('subdomain')] ?? ''),
      description: row[colIndex('description')] ? String(row[colIndex('description')]) : null,
      properties: row[colIndex('properties')] ? String(row[colIndex('properties')]) : null,
      tags: row[colIndex('tags')] ? String(row[colIndex('tags')]) : null,
      related_entities: row[colIndex('related_entities')] ? String(row[colIndex('related_entities')]) : null,
      visibility: (row[colIndex('visibility')] ?? 'public') as 'public' | 'private',
      confidence: Number(row[colIndex('confidence')] ?? 0),
      provenance: (row[colIndex('provenance')] ?? 'llm_inferred') as KGNode['provenance'],
      source_refs: row[colIndex('source_refs')] ? String(row[colIndex('source_refs')]) : null,
      created_at: String(row[colIndex('created_at')] ?? ''),
      updated_at: String(row[colIndex('updated_at')] ?? ''),
    }))
  }

  /**
   * 查询 edges 表并映射为 KGEdge[]。
   * Queries the edges table and maps rows to KGEdge[].
   */
  private queryEdges(): KGEdge[] {
    if (!this.db) return []

    const results = this.db.exec('SELECT * FROM edges')
    if (!results.length) return []

    const { columns, values } = results[0]
    const colIndex = (name: string) => columns.indexOf(name)

    return values.map((row: any[]) => ({
      id: Number(row[colIndex('id')] ?? 0),
      source_id: String(row[colIndex('source_id')] ?? ''),
      target_id: String(row[colIndex('target_id')] ?? ''),
      kind: (row[colIndex('kind')] ?? 'references') as KGEdge['kind'],
      description: row[colIndex('description')] ? String(row[colIndex('description')]) : null,
      properties: row[colIndex('properties')] ? String(row[colIndex('properties')]) : null,
      provenance: (row[colIndex('provenance')] ?? 'llm_inferred') as KGEdge['provenance'],
      confidence: Number(row[colIndex('confidence')] ?? 0),
      source_refs: row[colIndex('source_refs')] ? String(row[colIndex('source_refs')]) : null,
      bidirectional: row[colIndex('bidirectional')] === 1 || row[colIndex('bidirectional')] === '1',
      cardinality: row[colIndex('cardinality')] ? String(row[colIndex('cardinality')]) : null,
      created_at: String(row[colIndex('created_at')] ?? ''),
    }))
  }

  /**
   * 查询节点/边/领域计数。
   * Queries node, edge, and domain counts.
   */
  private queryMeta(): { domainCount: number; nodeCount: number; edgeCount: number } {
    if (!this.db) return { domainCount: 0, nodeCount: 0, edgeCount: 0 }

    const nodeCount = this.db.exec('SELECT COUNT(*) as cnt FROM nodes')
    const edgeCount = this.db.exec('SELECT COUNT(*) as cnt FROM edges')
    const domainCount = this.db.exec('SELECT COUNT(DISTINCT domain) as cnt FROM nodes')

    return {
      nodeCount: nodeCount[0]?.values[0]?.[0] as number ?? 0,
      edgeCount: edgeCount[0]?.values[0]?.[0] as number ?? 0,
      domainCount: domainCount[0]?.values[0]?.[0] as number ?? 0,
    }
  }

  /**
   * 读取复杂度哨兵时序（W-B）：kg_manifest 的 sentinel.history 键（JSON 数组）。
   * 老库无 kg_manifest 表或无该键 / JSON 损坏时返回空数组（观测数据，缺省安全）。
   * 与 service 的 SentinelSnapshot JSON 字段（ts/q/singleton_ratio/...）对应。
   *
   * Reads the sentinel history (W-B) from kg_manifest's sentinel.history key.
   * Returns [] for older DBs without the table/key or with corrupt JSON.
   */
  querySentinelHistory(): SentinelSample[] {
    if (!this.db) return []

    let rows
    try {
      rows = this.db.exec("SELECT value FROM kg_manifest WHERE key = 'sentinel.history'")
    } catch {
      return [] // 老库无 kg_manifest 表 / older DB without kg_manifest
    }
    const raw = rows[0]?.values?.[0]?.[0]
    if (!raw) return []
    try {
      const parsed = JSON.parse(String(raw))
      return Array.isArray(parsed) ? (parsed as SentinelSample[]) : []
    } catch {
      return []
    }
  }
}
