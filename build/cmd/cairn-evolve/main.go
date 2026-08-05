// Command cairn-evolve 是演化观测套件的轻量本地静态后端（E-4，R-ev-8）。
//
// 职责刻意收窄——hard-code 读取演化产物仓库（--dir 指向含 evolution.db 与
// snapshots/ 的目录），纯 Go net/http、零第三方框架、零连接池：
//
//	GET /api/manifest        读 evolution.db → {changesets:[...], metrics:[...]}（时间轴+曲线一次拉齐）
//	GET /snapshots/<file>    直接 serve KG 快照 db 文件（前端 sql.js fetch ArrayBuffer 加载）
//	GET /evolution.db        直接 serve 演化库文件
//
// 快照是不可变归档 → 长缓存；manifest/evolution.db 每次运行会变 → no-store。
// CORS 放开本地前端；默认仅监听 127.0.0.1。
//
// Command cairn-evolve is the lightweight localhost backend of the evolution
// suite: it serves the evolution workspace (manifest API + immutable snapshot
// files + evolution.db) to the graph-viewer frontend.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"path/filepath"
	"strconv"

	"github.com/xcosmosbox/cairn/core/evolve"
)

func main() {
	dir := flag.String("dir", "", "演化产物目录（含 evolution.db 与 snapshots/）/ evolution workspace dir")
	addr := flag.String("addr", "127.0.0.1:7801", "监听地址（默认仅 localhost）/ listen address")
	flag.Parse()

	if *dir == "" {
		log.Fatal("用法: cairn-evolve --dir <evolution-dir> [--addr 127.0.0.1:7801]")
	}
	absDir, err := filepath.Abs(*dir)
	if err != nil {
		log.Fatalf("解析 --dir 失败: %v", err)
	}
	log.Printf("[cairn-evolve] serving %s on http://%s", absDir, *addr)
	log.Fatal(http.ListenAndServe(*addr, newHandler(absDir)))
}

// newHandler 装配全部路由并包 CORS。
func newHandler(dir string) http.Handler {
	mux := http.NewServeMux()

	// 快照文件（不可变归档 → immutable 长缓存；http.FileServer 内部 path.Clean，
	// ".." 无法逃逸根目录）。
	snapFS := http.FileServer(http.Dir(filepath.Join(dir, "snapshots")))
	mux.Handle("/snapshots/", http.StripPrefix("/snapshots/",
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			snapFS.ServeHTTP(w, r)
		})))

	// 演化库文件（每次运行会变 → no-store）。
	mux.HandleFunc("/evolution.db", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeFile(w, r, filepath.Join(dir, "evolution.db"))
	})

	// 元数据 API：时间轴 + 曲线数据一次拉齐（前端无需自己开 SQLite 读演化库）。
	mux.HandleFunc("/api/manifest", func(w http.ResponseWriter, r *http.Request) {
		m, err := evolve.ReadManifest(r.Context(), dir)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				http.Error(w, `{"error":"evolution.db 不存在（尚未记录任何 changeset）"}`, http.StatusNotFound)
				return
			}
			http.Error(w, `{"error":`+strconv.Quote(err.Error())+`}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(m)
	})

	return withCORS(mux)
}

// withCORS 放开本地前端的跨域读取（graph-viewer dev server 与后端不同端口）。
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
