/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** Stable Bundle 的 knowledge.db URL（dkctl bundle pull 安装后由 HTTP 服务提供）。
   *  设置后前端启动自动加载该 stable KG 作为初始数据库。
   *  例: VITE_STABLE_BUNDLE_URL=http://127.0.0.1:7801/stable/my-skills/knowledge.db
   */
  readonly VITE_STABLE_BUNDLE_URL?: string
  /** Stable Bundle 的显示名（默认 "stable"） */
  readonly VITE_STABLE_BUNDLE_NAME?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}
