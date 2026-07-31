import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// ============================================================
// Vite 构建配置 / Vite Build Configuration
//
// - @vitejs/plugin-react: JSX 转换与 HMR
// - @tailwindcss/vite: Tailwind CSS v4 集成（零配置模式）
// ============================================================
export default defineConfig({
  plugins: [react(), tailwindcss()],
  // 纯静态产物，支持任意路径部署 / Pure static output, deployable at any path
  base: './',
  // OPFS 需要安全上下文（localhost 或 HTTPS）/ OPFS requires secure context
  server: {
    host: 'localhost',
    port: 5173,
  },
  // 将大型 WASM 文件标记为外部资源，避免内联 / Mark large WASM as external
  build: {
    target: 'esnext',
    assetsInlineLimit: 0,
  },
  // sql.js 通过 wasmBinary 参数加载 WASM，预构建不影响功能
  // sql.js loads WASM via wasmBinary param, pre-bundling is safe
})
