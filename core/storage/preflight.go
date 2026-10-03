package storage

// OpenPreflightReadOnly 专供“先验证、后迁移”的构建入口。它不设置 journal_mode，
// 不执行 DDL/索引/迁移，并通过 mode=ro + query_only 保证无写副作用。

func OpenPreflightReadOnly(path string) (*DB, error) {
	return OpenReadOnly(path)
}
