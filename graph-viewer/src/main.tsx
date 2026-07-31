import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import App from './App'
import './index.css'

// ============================================================
// 应用入口 / Application Entry Point
//
// 将 React 组件树挂载到 #root DOM 节点。
// StrictMode 在开发模式下启用额外检查（副作用检测、弃用警告）。
// ============================================================

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
