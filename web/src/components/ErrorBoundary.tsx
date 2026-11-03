import { Component } from 'react'
import type { ErrorInfo, ReactNode } from 'react'

// Keeps a render error inside the page that raised it: the nav rail and header
// stay usable, and the page offers a way back instead of going blank.
export class ErrorBoundary extends Component<{ children: ReactNode }, { error: Error | null }> {
  state = { error: null as Error | null }

  static getDerivedStateFromError(error: Error) {
    return { error }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('[ui] render error', error, info.componentStack)
  }

  render() {
    if (!this.state.error) return this.props.children
    return (
      <div style={{ padding: 32, display: 'flex', flexDirection: 'column', gap: 12, alignItems: 'flex-start' }}>
        <strong>This page failed to load.</strong>
        <code style={{ fontSize: 12, opacity: 0.8 }}>{this.state.error.message}</code>
        <button className="btn btn--secondary" onClick={() => { window.location.href = '/' }}>
          Go to start
        </button>
      </div>
    )
  }
}
