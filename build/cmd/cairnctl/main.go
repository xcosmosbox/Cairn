// Command cairnctl 是 DK Controller 的运维与 Bundle 安装 CLI（产品化 Prompt §13）。
//
// 用法：
//   cairnctl status --state-db <path>
//   cairnctl repos --state-db <path>
//   cairnctl runs --repo payment --state-db <path>
//   cairnctl retry <run-id> --state-db <path>
//   cairnctl unblock <run-id> --state-db <path>
//   cairnctl bundle list --kg payment --install-dir ~/.cairn/kbs
//   cairnctl bundle pull --kg payment --install-dir ~/.cairn/kbs [--src-dir <bundle-dir>]
//
// cairnctl 直接读取 controller.db（不依赖 daemon 在线），能在 daemon 重启后查看状态。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xcosmosbox/cairn/core/kbbundle"
	"github.com/xcosmosbox/cairn/build/internal/controller/model"
	"github.com/xcosmosbox/cairn/build/internal/controller/store"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	sub := os.Args[1]

	switch sub {
	case "status":
		stateDB := flag.String("state-db", "", "controller.db 路径")
		flag.CommandLine.Parse(os.Args[2:])
		requireFlag(*stateDB, "state-db")
		cmdStatus(*stateDB)
	case "repos":
		stateDB := flag.String("state-db", "", "controller.db 路径")
		flag.CommandLine.Parse(os.Args[2:])
		requireFlag(*stateDB, "state-db")
		cmdRepos(*stateDB)
	case "runs":
		stateDB := flag.String("state-db", "", "controller.db 路径")
		repoID := flag.String("repo", "", "repo id 过滤")
		flag.CommandLine.Parse(os.Args[2:])
		requireFlag(*stateDB, "state-db")
		cmdRuns(*stateDB, *repoID)
	case "retry", "unblock":
		stateDB := flag.String("state-db", "", "controller.db 路径")
		flag.CommandLine.Parse(os.Args[2:])
		args := flag.Args()
		if len(args) < 1 {
			fmt.Fprintln(os.Stderr, "错误：需要 <run-id>")
			os.Exit(1)
		}
		requireFlag(*stateDB, "state-db")
		cmdRunAction(*stateDB, args[0], sub)
	case "bundle":
		cmdBundle(os.Args[2:])
	default:
		usage()
	}
}

func cmdStatus(stateDB string) {
	st, err := store.Open(stateDB)
	mustNoErr(err)
	defer st.Close()
	ctx := context.Background()
	repos, _ := st.ListRepos(ctx)
	runs, _ := st.ActiveRuns(ctx)
	fmt.Printf("DK Controller 状态\n")
	fmt.Printf("  受管仓库: %d\n", len(repos))
	fmt.Printf("  活跃 run: %d\n", len(runs))
	for _, r := range repos {
		fmt.Printf("  - %s (%s/%s): last_seen=%s stable=%s\n",
			r.ID, r.GitHubOwner, r.GitHubName, short(r.LastSeenSourceSHA), short(r.LastStableBundleDigest))
	}
	for _, r := range runs {
		fmt.Printf("  run %s: %s (attempt %d, reason: %s)\n", r.RunID, r.State, r.Attempt, r.Reason)
	}
}

func cmdRepos(stateDB string) {
	st, err := store.Open(stateDB)
	mustNoErr(err)
	defer st.Close()
	repos, _ := st.ListRepos(context.Background())
	for _, r := range repos {
		fmt.Printf("%s\t%s/%s\t%s\tstable=%s\n", r.ID, r.GitHubOwner, r.GitHubName, r.Branch, short(r.LastStableBundleDigest))
	}
}

func cmdRuns(stateDB, repoID string) {
	st, err := store.Open(stateDB)
	mustNoErr(err)
	defer st.Close()
	runs, _ := st.ListRuns(context.Background(), repoID, 50)
	for _, r := range runs {
		fmt.Printf("%s\t%s\t%s\tattempt=%d\t%s\n", r.RunID, r.RepoID, r.State, r.Attempt, r.Reason)
		if r.ErrorCode != "" {
			fmt.Printf("  error: %s: %s\n", r.ErrorCode, r.ErrorMessage)
		}
	}
}

func cmdRunAction(stateDB, runID, action string) {
	st, err := store.Open(stateDB)
	mustNoErr(err)
	defer st.Close()
	ctx := context.Background()
	switch action {
	case "retry":
		// 从终态回到 FailedRetryable → Idle（经 FailedRetryable 中转，因 Blocked→Idle 可能非法）。
		st.TransitionRun(ctx, runID, model.StateFailedRetryable, "manual retry")
		st.TransitionRun(ctx, runID, model.StateIdle, "manual retry")
		fmt.Printf("run %s 已标记重试\n", runID)
	case "unblock":
		st.TransitionRun(ctx, runID, model.StateIdle, "manual unblock")
		fmt.Printf("run %s 已解除阻塞\n", runID)
	}
}

func cmdBundle(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "用法: cairnctl bundle <list|pull> [flags]")
		os.Exit(1)
	}
	sub := args[0]
	fs := flag.NewFlagSet("bundle "+sub, flag.ExitOnError)
	kg := fs.String("kg", "", "知识库 group")
	installDir := fs.String("install-dir", "", "安装目录")
	srcDir := fs.String("src-dir", "", "本地 Bundle 源目录（本地模式）")
	// 远程下载模式 flags。
	catalog := fs.String("catalog", "", "Catalog Repo（owner/name，远程下载模式）")
	tokenEnv := fs.String("token-env", "GH_TOKEN", "GitHub token 环境变量名（远程下载模式）")
	apiBase := fs.String("api-base", "https://api.github.com", "GitHub API base URL")
	manifestDir := fs.String("manifest-dir", "knowledge-bases", "Catalog Repo 中 manifest 目录")
	branch := fs.String("branch", "main", "Catalog Repo 分支")
	fs.Parse(args[1:])
	requireFlag(*kg, "kg")
	requireFlag(*installDir, "install-dir")
	switch sub {
	case "list":
		cmdBundleList(*installDir, *kg)
	case "pull":
		if *catalog != "" {
			// 远程下载模式。
			cmdBundlePullRemote(*catalog, *kg, *installDir, *tokenEnv, *apiBase, *manifestDir, *branch)
		} else {
			// 本地安装模式。
			requireFlag(*srcDir, "src-dir（或用 --catalog 远程下载）")
			cmdBundlePull(*installDir, *srcDir, *kg)
		}
	default:
		fmt.Fprintf(os.Stderr, "未知 bundle 子命令: %s\n", sub)
		os.Exit(1)
	}
}

func cmdBundleList(installDir, kg string) {
	kgDir := filepath.Join(installDir, kg)
	entries, err := os.ReadDir(kgDir)
	if err != nil {
		fmt.Printf("(无已安装的 %s Bundle)\n", kg)
		return
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() && e.Name() != "current" {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Strings(dirs)
	cur, _ := os.Readlink(filepath.Join(kgDir, "current"))
	curName := ""
	if cur != "" {
		curName = filepath.Base(cur)
	}
	fmt.Printf("已安装 %s Bundle:\n", kg)
	for _, d := range dirs {
		marker := ""
		if d == curName {
			marker = " ← current"
		}
		// 读 manifest。
		mPath := filepath.Join(kgDir, d, "kb-manifest.json")
		if data, err := os.ReadFile(mPath); err == nil {
			var m kbbundle.Manifest
			if json.Unmarshal(data, &m) == nil {
				fmt.Printf("  %s\t%s\tsource=%s%s\n", d, short(m.BundleDigest), short(m.SourceCommit), marker)
				continue
			}
		}
		fmt.Printf("  %s%s\n", d, marker)
	}
}

func cmdBundlePull(installDir, srcDir, kg string) {
	kgDir := filepath.Join(installDir, kg)
	res, err := kbbundle.Install(srcDir, kgDir)
	mustNoErr(err)
	fmt.Printf("Bundle 已安装: %s\n  digest: %s\n  current: %s\n", kgDir, short(res.Digest), res.Current)
	if res.Previous != "" {
		fmt.Printf("  上一版: %s（可用于回滚）\n", res.Previous)
	}
}

// cmdBundlePullRemote 从 Catalog Repo 的 GitHub Release 远程下载 stable Bundle 并安装。
func cmdBundlePullRemote(catalog, kg, installDir, tokenEnv, apiBase, manifestDir, branch string) {
	// 解析 catalog owner/name。
	parts := strings.SplitN(catalog, "/", 2)
	if len(parts) != 2 {
		fmt.Fprintf(os.Stderr, "错误：--catalog 格式应为 owner/name，得到 %q\n", catalog)
		os.Exit(1)
	}
	owner, repo := parts[0], parts[1]
	// 读取 token。
	token := os.Getenv(tokenEnv)
	if token == "" {
		fmt.Fprintf(os.Stderr, "错误：环境变量 %s 未设置（GitHub PAT 或 installation token）\n", tokenEnv)
		os.Exit(1)
	}
	kgDir := filepath.Join(installDir, kg)
	fmt.Printf("从 %s/%s 拉取 %s stable Bundle...\n", owner, repo, kg)
	res, err := kbbundle.PullRemote(kbbundle.PullRemoteSpec{
		CatalogOwner: owner, CatalogRepo: repo, CatalogBranch: branch,
		ManifestDir: manifestDir, KG: kg, Token: token,
		APIBaseURL: apiBase, InstallDir: kgDir,
	})
	mustNoErr(err)
	defer os.RemoveAll(res.TempDir)
	fmt.Printf("Bundle 已安装: %s\n", kgDir)
	fmt.Printf("  digest: %s\n", short(res.Manifest.BundleDigest))
	fmt.Printf("  source_commit: %s\n", short(res.Manifest.SourceCommit))
	fmt.Printf("  current: %s\n", res.Install.Current)
	if res.Install.Previous != "" {
		fmt.Printf("  上一版: %s（可用于回滚）\n", res.Install.Previous)
	}
}

func short(s string) string {
	if len(s) > 16 {
		return s[:16] + "…"
	}
	if s == "" {
		return "-"
	}
	return s
}

func requireFlag(val, name string) {
	if val == "" {
		fmt.Fprintf(os.Stderr, "错误：--%s 必填\n", name)
		os.Exit(1)
	}
}

func mustNoErr(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "cairnctl: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `用法: cairnctl <子命令> [flags]

子命令:
  status              控制器总览
  repos               列出受管仓库
  runs --repo <id>    列出 run
  retry <run-id>      重试 run
  unblock <run-id>    解除阻塞
  bundle list --kg <g> --install-dir <dir>
  bundle pull --kg <g> --install-dir <dir> --src-dir <bundle-dir>
  bundle pull --kg <g> --install-dir <dir> --catalog <owner/name> [--token-env GH_TOKEN]

远程下载示例:
  export GH_TOKEN=ghp_xxx
  cairnctl bundle pull --kg payment --install-dir ~/.cairn/kbs --catalog acme/knowledge-catalog

通用 flags:
  --state-db <path>   controller.db 路径`)
	os.Exit(2)
}

var _ = time.Now
