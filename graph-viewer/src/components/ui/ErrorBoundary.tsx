import React from 'react'
import { AlertTriangle } from 'lucide-react'

// ============================================================
// ErrorBoundary — React 错误边界 / React Error Boundary
//
// 捕获子组件树中未被处理的 JavaScript 错误，
// 显示友好的错误回退 UI，防止整个应用白屏。
//
// Catches unhandled JavaScript errors in the child component
// tree, displays a friendly fallback UI instead of a white
// screen.
// ============================================================

interface Props {
  children: React.ReactNode
}

interface State {
  hasError: boolean
  error: Error | null
}

/** 错误回退 UI / Error Fallback UI */
function ErrorFallback({ error, onReset }: { error: Error | null; onReset: () => void }) {
  return (
    <div className="flex h-full items-center justify-center" style={{ background: 'var(--bg-root)' }}>
      <div
        className="max-w-md w-full mx-4 p-6 rounded-xl shadow-lg"
        style={{
          background: 'var(--bg-card)',
          border: '1px solid var(--border-default)',
        }}
      >
        <div className="flex items-center gap-3 mb-4">
          <AlertTriangle className="w-6 h-6 text-red-400 shrink-0" strokeWidth={2} />
          <h2 className="text-lg font-semibold" style={{ color: 'var(--text-primary)' }}>
            出了点问题 / Something went wrong
          </h2>
        </div>
        {error && (
          <p
            className="text-sm mb-6 break-words"
            style={{ color: 'var(--text-secondary)' }}
          >
            {error.message.length > 200
              ? error.message.slice(0, 200) + '…'
              : error.message}
          </p>
        )}
        <div className="flex gap-3">
          <button
            onClick={onReset}
            className="flex-1 px-4 py-2 rounded-lg text-sm font-medium transition-colors"
            style={{
              background: 'var(--accent)',
              color: '#fff',
            }}
          >
            重试 / Try Again
          </button>
          <button
            onClick={() => window.location.reload()}
            className="flex-1 px-4 py-2 rounded-lg text-sm font-medium transition-colors"
            style={{
              background: 'var(--bg-hover)',
              color: 'var(--text-primary)',
            }}
          >
            刷新 / Reload
          </button>
        </div>
      </div>
    </div>
  )
}

export class ErrorBoundary extends React.Component<Props, State> {
  state: State = { hasError: false, error: null }

  static getDerivedStateFromError(error: Error): State {
    return { hasError: true, error }
  }

  componentDidCatch(error: Error, info: React.ErrorInfo) {
    console.error('[GraphViewer] Error Boundary caught:', error, info.componentStack)
  }

  handleReset = () => {
    this.setState({ hasError: false, error: null })
  }

  render() {
    if (this.state.hasError) {
      return <ErrorFallback error={this.state.error} onReset={this.handleReset} />
    }
    return this.props.children
  }
}
