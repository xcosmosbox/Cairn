#!/usr/bin/env bash
# ═══════════════════════════════════════════════════════════════════
# pipeline.sh — 单机数据闭环编排脚本
#
# 串起构建端三个二进制，形成「全量 → 增量 → 漂移驱动重整」闭环：
#   1. dk-ingest      首次/全量构建（db 不存在时；零删除铁律：已存在绝不重建）
#   2. dk-incremental 吸收人工编辑（局部 upsert，可反复重入）
#   3. dk-rebalance --check 哨兵检查；仅「触发: 是」时才真正重整
#      （漂移驱动，避免无谓重整；--check 本身零 LLM 零改图）
#
# 适用场景：本地实验、单机 cron、CI 里跑一轮构建。
# 若你需要「监听源仓库 → 自动开 PR → 发布 Bundle → 提升 stable」的完整
# GitOps 流程，用 dkd 守护进程（见 configs/dkd.example.yaml），而不是本脚本。
#
# 用法 / Usage:
#   scripts/pipeline.sh <repo> <db> [bin_dir]
#
# 参数 / Args:
#   repo     仓库工作区路径（语料源）
#   db       SQLite 知识库路径（不存在则首次全量构建）
#   bin_dir  二进制目录（默认 ./bin，须已 make build）
#
# 前置：export DK_LLM_API_KEY="your-key"
#
# 可重入：dk-incremental / dk-rebalance 均幂等可重入；脚本任意一步失败
# 直接退出非零（set -euo pipefail），修复后原样重跑即可。
#
# 定时运营（可选）：
#   每日增量:   17 3 * * *   cd /path/to/repo-root && scripts/pipeline.sh /path/to/skills /data/kg.db
#   每周哨兵:   23 4 * * 1   ./bin/dk sentinel --db-path /data/kg.db --record
# ═══════════════════════════════════════════════════════════════════
set -euo pipefail

REPO="${1:?usage: pipeline.sh <repo> <db> [bin_dir]}"
DB="${2:?usage: pipeline.sh <repo> <db> [bin_dir]}"
BIN_DIR="${3:-./bin}"

if [ -z "${DK_LLM_API_KEY:-}" ]; then
  echo "✗ 未设置 DK_LLM_API_KEY 环境变量（LLM Key 只走环境变量，绝不写进配置）" >&2
  exit 1
fi

echo "── pipeline: repo=${REPO} db=${DB} bin=${BIN_DIR} ──"

# Step 1: 首次/全量构建（db 已存在则跳过——零删除铁律，绝不重建）。
if [ ! -f "$DB" ]; then
  echo "── step 1: dk-ingest 首次全量构建 / initial full build ──"
  "$BIN_DIR/dk-ingest" --repo "$REPO" --db "$DB"
else
  echo "── step 1: db 已存在，跳过全量构建 / db exists, skip ingest ──"
fi

# Step 2: 增量吸收人工编辑（局部 upsert，幂等可重入）。
echo "── step 2: dk-incremental 增量吸收 / incremental update ──"
"$BIN_DIR/dk-incremental" --repo "$REPO" --db "$DB"

# Step 3: 漂移驱动重整——哨兵 --check 报「触发: 是」才真正重整。
# --check 的「触发」行输出到 stderr（dk-rebalance/main.go），故 2>&1 合并后再 grep。
echo "── step 3: dk-rebalance --check 哨兵检查 / sentinel check ──"
if "$BIN_DIR/dk-rebalance" --repo "$REPO" --db "$DB" --check 2>&1 | grep -q "触发: 是"; then
  echo "── 哨兵越阈值，执行全量重整 / breach detected, running rebalance ──"
  "$BIN_DIR/dk-rebalance" --repo "$REPO" --db "$DB"
else
  echo "── 哨兵未触发，跳过重整 / within thresholds, skip rebalance ──"
fi

echo "✅ pipeline 完成 / pipeline done"
