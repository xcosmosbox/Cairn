// cairn-eval-index-copy changes only nodes_fts in a new physical copy of the
// frozen evaluation DB. No build, migration, LLM, or network operation occurs.
package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/xcosmosbox/cairn/core/storage"
)

const frozenSourceSHA256 = "6d2d54c533a68317a03a61e6e5291c235a656db71f4408a26fbaf28cf547c828"

var ftsColumns = []string{"name", "summary", "synonyms", "tags", "description", "domain", "subdomain"}

// Exclude exactly the known FTS virtual table and its shadow tables, not all
// names sharing its prefix. An unrelated application table must be compared.
var ftsTables = map[string]bool{
	"nodes_fts": true, "nodes_fts_data": true, "nodes_fts_idx": true,
	"nodes_fts_content": true, "nodes_fts_docsize": true, "nodes_fts_config": true,
}

type tableIdentity struct {
	Schema   string   `json:"schema"`
	Columns  []string `json:"columns"`
	RowCount int      `json:"row_count"`
	SHA256   string   `json:"sha256"`
}

type schemaObject struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Table string `json:"table"`
	SQL   string `json:"sql"`
}

type manifest struct {
	Format               string                   `json:"format"`
	Profile              string                   `json:"profile"`
	IndexProfile         string                   `json:"index_profile"`
	TextProfile          string                   `json:"text_profile"`
	SourceDBSHA256       string                   `json:"source_db_sha256"`
	SourceDBBeforeSHA256 string                   `json:"source_db_before_sha256"`
	SourceDBAfterSHA256  string                   `json:"source_db_after_sha256"`
	OutputDBSHA256       string                   `json:"output_db_sha256"`
	SourceBytes          int64                    `json:"source_bytes"`
	OutputBytes          int64                    `json:"output_bytes"`
	Before               map[string]tableIdentity `json:"non_fts_tables_before"`
	After                map[string]tableIdentity `json:"non_fts_tables_after"`
	SchemaBefore         []schemaObject           `json:"non_fts_schema_before"`
	SchemaAfter          []schemaObject           `json:"non_fts_schema_after"`
	Unchanged            bool                     `json:"all_non_fts_tables_unchanged"`
	IdentityEncoding     string                   `json:"identity_encoding"`
	GoVersion            string                   `json:"go_version"`
	UnicodeVersion       string                   `json:"unicode_version"`
	SQLiteVersion        string                   `json:"sqlite_version"`
	IndexSchema          string                   `json:"index_schema"`
	IndexColumns         []string                 `json:"index_columns"`
	IndexRows            int                      `json:"index_rows"`
	MutationScope        string                   `json:"mutation_scope"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, out, errOut io.Writer) error {
	flags := flag.NewFlagSet("cairn-eval-index-copy", flag.ContinueOnError)
	flags.SetOutput(errOut)
	source := flags.String("source", "", "existing frozen DB; SHA-256 must equal the frozen evaluation identity")
	output := flags.String("output", "", "new DB file; existing files are never overwritten")
	profile := flags.String("profile", "", "literal, trigram, or han-v1")
	receipt := flags.String("manifest", "", "new external JSON manifest; existing files are never overwritten")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || *source == "" || *output == "" || *receipt == "" {
		return errors.New("--source, --output, --profile, and --manifest are required; no positional arguments")
	}
	m, err := copyIndex(*source, *output, *receipt, *profile, frozenSourceSHA256)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(m)
}

func shaFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}

func quoted(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

func openImmutable(path string) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(absolute)}
	q := url.Values{"mode": {"ro"}, "immutable": {"1"}, "_pragma": {"query_only(1)"}}
	db, err := sql.Open("sqlite", u.String()+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func absent(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("refusing to overwrite existing path: %s", path)
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

func cleanSource(path string) error {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("source must be an existing regular DB: %v", err)
	}
	for _, suffix := range []string{"-wal", "-journal"} {
		if side, err := os.Stat(path + suffix); err == nil && side.Size() > 0 {
			return fmt.Errorf("source has a nonempty %s; refuse a physical copy of an active/uncheckpointed DB", suffix)
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func copyIndex(source, output, receipt, profile, expectedSHA string) (m manifest, err error) {
	// Inspect the real database's sidecars as well as its bytes. An alias must
	// not hide an active WAL beside the underlying source file.
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return m, err
	}
	source, err = filepath.Abs(source)
	if err != nil {
		return m, err
	}
	textProfile, tokenizer := storage.FTSTextLiteral, "unicode61"
	switch profile {
	case "literal":
	case "trigram":
		tokenizer = "trigram"
	case "han-v1":
		textProfile = storage.FTSTextHanV1
	default:
		return m, fmt.Errorf("unknown index profile %q", profile)
	}
	for _, path := range []string{output, receipt} {
		if err = absent(path); err != nil {
			return m, err
		}
	}
	outAbs, _ := filepath.Abs(output)
	receiptAbs, _ := filepath.Abs(receipt)
	if outAbs == receiptAbs {
		return m, errors.New("output DB and manifest must be different files")
	}
	if err = cleanSource(source); err != nil {
		return m, err
	}
	m.SourceDBSHA256, m.SourceBytes, err = shaFile(source)
	if err != nil {
		return m, err
	}
	if m.SourceDBSHA256 != expectedSHA {
		return m, fmt.Errorf("source DB SHA-256 mismatch: got %s; require %s", m.SourceDBSHA256, expectedSHA)
	}
	m.SourceDBBeforeSHA256 = m.SourceDBSHA256
	srcDB, err := openImmutable(source)
	if err != nil {
		return m, err
	}
	m.Before, m.SchemaBefore, err = databaseIdentity(srcDB)
	if closeErr := srcDB.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return m, fmt.Errorf("source identity: %w", err)
	}

	// O_EXCL owns this output; cleanup never removes a pre-existing file.
	dst, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return m, err
	}
	defer func() {
		if err != nil {
			for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
				_ = os.Remove(output + suffix)
			}
		}
	}()
	src, err := os.Open(source)
	if err != nil {
		dst.Close()
		return m, err
	}
	_, err = io.Copy(dst, src)
	src.Close()
	if syncErr := dst.Sync(); err == nil {
		err = syncErr
	}
	if closeErr := dst.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return m, err
	}
	if copiedHash, _, hashErr := shaFile(output); hashErr != nil || copiedHash != expectedSHA {
		return m, fmt.Errorf("physical copy identity mismatch: %s: %v", copiedHash, hashErr)
	}
	db, err := storage.OpenSQLite(output, storage.SQLiteOptions{ExistingOnly: true, JournalMode: "DELETE"})
	if err != nil {
		return m, err
	}
	defer db.Close()
	m.IndexSchema = "CREATE VIRTUAL TABLE nodes_fts USING fts5(" + strings.Join(ftsColumns, ", ") + ", prefix='2 3', tokenize='" + tokenizer + "')"
	if err = rebuildIndex(db, m.IndexSchema, textProfile); err != nil {
		return m, err
	}
	if err = db.QueryRow("SELECT sqlite_version()").Scan(&m.SQLiteVersion); err != nil {
		return m, err
	}
	if err = db.QueryRow("SELECT count(*) FROM nodes_fts").Scan(&m.IndexRows); err != nil {
		return m, err
	}
	var integrity string
	if err = db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		return m, fmt.Errorf("copy integrity_check: %s: %v", integrity, err)
	}
	m.After, m.SchemaAfter, err = databaseIdentity(db)
	if err != nil {
		return m, err
	}
	if !reflect.DeepEqual(m.Before, m.After) || !reflect.DeepEqual(m.SchemaBefore, m.SchemaAfter) {
		return m, errors.New("copy changed non-FTS data or schema; discarding output")
	}
	if err = db.Close(); err != nil {
		return m, err
	}
	if err = cleanSource(source); err != nil {
		return m, err
	}
	m.SourceDBAfterSHA256, _, err = shaFile(source)
	if err != nil || m.SourceDBAfterSHA256 != m.SourceDBSHA256 {
		return m, fmt.Errorf("source DB changed while indexing: %v", err)
	}
	m.OutputDBSHA256, m.OutputBytes, err = shaFile(output)
	if err != nil {
		return m, err
	}
	m.Format, m.Profile, m.IndexProfile, m.TextProfile = "cairn-fts-index-copy/v1", profile, profile, string(textProfile)
	m.Unchanged, m.GoVersion, m.UnicodeVersion = true, runtime.Version(), unicode.Version
	m.IndexColumns = ftsColumns
	m.IdentityEncoding = "sorted JSON rows of tagged SQLite values; N=null I=int-decimal F=float64-IEEE-bits S=text B=base64-blob; explicit rowid included unless WITHOUT ROWID; SHA256 over each row plus LF"
	m.MutationScope = "physical copy, then drop/recreate/insert/integrity-check nodes_fts only; no node, edge, source, metadata, or application-schema writes"
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return m, err
	}
	f, err := os.OpenFile(receipt, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return m, err
	}
	_, err = f.Write(append(body, '\n'))
	if syncErr := f.Sync(); err == nil {
		err = syncErr
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(receipt)
	}
	return m, err
}

func rebuildIndex(db *sql.DB, schema string, profile storage.FTSTextProfile) error {
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DROP TABLE nodes_fts"); err != nil {
		return err
	}
	if _, err = tx.Exec(schema); err != nil {
		return err
	}
	rows, err := tx.Query("SELECT rowid," + strings.Join(ftsColumns, ",") + " FROM nodes ORDER BY rowid")
	if err != nil {
		return err
	}
	var prepared [][]any
	for rows.Next() {
		values := make([]any, len(ftsColumns)+1)
		pointers := make([]any, len(values))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err = rows.Scan(pointers...); err != nil {
			rows.Close()
			return err
		}
		for i := 1; i < len(values); i++ {
			if values[i] == nil {
				continue
			}
			value, ok := values[i].(string)
			if !ok {
				rows.Close()
				return fmt.Errorf("node column %s is not TEXT or NULL", ftsColumns[i-1])
			}
			values[i], err = storage.NormalizeFTSText(profile, value)
			if err != nil {
				rows.Close()
				return err
			}
		}
		prepared = append(prepared, values)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare("INSERT INTO nodes_fts(rowid," + strings.Join(ftsColumns, ",") + ") VALUES(?,?,?,?,?,?,?,?)")
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, values := range prepared {
		if _, err = stmt.Exec(values...); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("INSERT INTO nodes_fts(nodes_fts) VALUES('integrity-check')"); err != nil {
		return err
	}
	return tx.Commit()
}

func databaseIdentity(db *sql.DB) (map[string]tableIdentity, []schemaObject, error) {
	rows, err := db.Query("SELECT type,name,tbl_name,coalesce(sql,'') FROM sqlite_master ORDER BY type,name")
	if err != nil {
		return nil, nil, err
	}
	objects := []schemaObject{}
	tables := map[string]tableIdentity{}
	for rows.Next() {
		var s schemaObject
		if err := rows.Scan(&s.Type, &s.Name, &s.Table, &s.SQL); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if ftsTables[s.Name] || ftsTables[s.Table] {
			continue
		}
		objects = append(objects, s)
		if s.Type == "table" {
			tables[s.Name] = tableIdentity{Schema: s.SQL}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	for name, identity := range tables {
		selectExpr := "rowid,*"
		if strings.Contains(strings.ToUpper(identity.Schema), "WITHOUT ROWID") {
			selectExpr = "*"
		}
		data, err := db.Query("SELECT " + selectExpr + " FROM " + quoted(name))
		if err != nil {
			return nil, nil, err
		}
		identity.Columns, err = data.Columns()
		if err != nil {
			data.Close()
			return nil, nil, err
		}
		encoded := []string{}
		for data.Next() {
			values := make([]any, len(identity.Columns))
			pointers := make([]any, len(values))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err = data.Scan(pointers...); err != nil {
				data.Close()
				return nil, nil, err
			}
			cells := make([][2]string, len(values))
			for i, value := range values {
				switch v := value.(type) {
				case nil:
					cells[i] = [2]string{"N", ""}
				case int64:
					cells[i] = [2]string{"I", strconv.FormatInt(v, 10)}
				case float64:
					cells[i] = [2]string{"F", fmt.Sprintf("%016x", math.Float64bits(v))}
				case string:
					cells[i] = [2]string{"S", v}
				case []byte:
					cells[i] = [2]string{"B", base64.StdEncoding.EncodeToString(v)}
				default:
					data.Close()
					return nil, nil, fmt.Errorf("unsupported SQLite value type %T in table %s", value, name)
				}
			}
			body, _ := json.Marshal(cells)
			encoded = append(encoded, string(body))
		}
		err = data.Err()
		data.Close()
		if err != nil {
			return nil, nil, err
		}
		sort.Strings(encoded)
		h := sha256.New()
		for _, row := range encoded {
			fmt.Fprintln(h, row)
		}
		identity.RowCount, identity.SHA256 = len(encoded), hex.EncodeToString(h.Sum(nil))
		tables[name] = identity
	}
	return tables, objects, nil
}
