// Package buildmeta 提供编译时构建语义身份，避免提示词或算法已变却继续复用旧产物。
package buildmeta

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"runtime/debug"
	"sort"
	"strings"
)

// 提示词目前是 Go 字符串常量。嵌入其实际源码而非手填版本，确保修改会触发重建。
// Embedded source fingerprints track the actual compiled prompts and builder semantics.
//
//go:embed annotation/*.go extract/*.go discovery/*.go incremental/*.go ingest/*.go pipeline/*.go llm/*.go writeback/*.go rebalance/community/*.go rebalance/patch/*.go controller/runner/*.go controller/reconcile/*.go controller/publisher/*.go
var sources embed.FS

func SourceDigest(prefixes ...string) string {
	var names []string
	fs.WalkDir(sources, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.Contains(path, "/zz_") {
			return nil
		}
		for _, prefix := range prefixes {
			if strings.HasPrefix(path, prefix) {
				names = append(names, path)
				break
			}
		}
		return nil
	})
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		data, err := sources.ReadFile(name)
		if err != nil {
			panic(err)
		}
		fmt.Fprintf(h, "%s\n", name)
		h.Write(data)
		h.Write([]byte("\n"))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func BuilderVersion() (version, commit string) {
	version = "devel"
	if info, ok := debug.ReadBuildInfo(); ok {
		if info.Main.Version != "" && info.Main.Version != "(devel)" {
			version = info.Main.Version
		}
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" {
				commit = s.Value
			}
		}
	}
	commit = sourceIdentity(commit, SourceDigest(""))
	return
}

// sourceIdentity binds compiled source even when VCS metadata describes a dirty tree.
func sourceIdentity(revision, sourceDigest string) string {
	if revision == "" {
		return sourceDigest
	}
	return revision + "+" + sourceDigest
}
