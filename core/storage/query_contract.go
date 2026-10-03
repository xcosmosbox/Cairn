package storage

import (
	"context"
	"fmt"
)

// ValidateQueryContract 验证真实查询能力，不能仅相信 user_version 或文件摘要。
// v4 只缺 file_slug，查询层以 NULL 投影兼容；缺失 FTS 等实际契约则必须拒绝。
// ValidateQueryContract validates supported KG tables, columns and an executable FTS query.
func ValidateQueryContract(ctx context.Context, db *DB) error {
	version, err := db.SchemaVersion()
	if err != nil {
		return err
	}
	if version < 4 || version > LatestSchemaVersion() {
		return fmt.Errorf("storage: unsupported query schema %d (supported 4..%d)", version, LatestSchemaVersion())
	}
	var integrity string
	if err := db.Conn().QueryRowContext(ctx, "PRAGMA quick_check").Scan(&integrity); err != nil {
		return err
	}
	if integrity != "ok" {
		return fmt.Errorf("storage: quick_check: %s", integrity)
	}
	fileSlug := "file_slug"
	if version == 4 {
		fileSlug = "NULL AS file_slug"
	}
	queries := []string{
		`SELECT id,label,name,summary,synonyms,domain,subdomain,description,properties,tags,related_entities,confidence,provenance,source_refs,` + fileSlug + `,created_at,updated_at FROM nodes LIMIT 0`,
		`SELECT id,source_id,target_id,kind,description,properties,provenance,confidence,source_refs,bidirectional,cardinality,created_at FROM edges LIMIT 0`,
		`SELECT rowid,name,summary,synonyms,tags,description,domain,subdomain,bm25(nodes_fts) FROM nodes_fts WHERE nodes_fts MATCH 'cairn_validation_probe' LIMIT 1`,
		`SELECT new_uuid,old_uuid,reason,created_at FROM uuid_lineage LIMIT 0`,
		`SELECT source_entity,target_entity,target_node_id,kind,reason,resolution_status FROM cross_references LIMIT 0`,
		`SELECT key,value FROM kg_manifest LIMIT 0`,
		`SELECT version,updated_at FROM kb_version LIMIT 0`,
		`SELECT node_uuid,member_id,skill,file_path,start_line,end_line FROM node_sources LIMIT 0`,
	}
	for _, query := range queries {
		rows, err := db.Conn().QueryContext(ctx, query)
		if err != nil {
			return fmt.Errorf("storage: KG query contract: %w", err)
		}
		for rows.Next() {
		}
		err = rows.Err()
		closeErr := rows.Close()
		if err != nil {
			return fmt.Errorf("storage: KG query contract: %w", err)
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
