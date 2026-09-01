import { Component, type ErrorInfo, type ReactNode } from 'react'

type Props = { children: ReactNode }
type State = { hasError: boolean }

export class ErrorBoundary extends Component<Props, State> {
  state: State = { hasError: false }

  static getDerivedStateFromError(): State {
    return { hasError: true }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('Vertex Pro UI error', error, info)
  }

  render() {
    if (this.state.hasError) {
      return <main className="error-state"><h1>页面暂时不可用</h1><p>请刷新页面后重试。</p></main>
    }
    return this.props.children
  }
}
