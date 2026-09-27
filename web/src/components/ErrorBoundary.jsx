import { Component } from 'react'
import { UiButton, UiCard, UiEmpty } from '@/components/ui'

// ErrorBoundary catches a rendering exception thrown by whatever it wraps and shows a recoverable error card
// instead of leaving the whole console shell blank (a page-level bug — e.g. a bad API shape one of them does
// not defend against, see the topology.roles regression this was added for — must not take down navigation,
// the top bar, or every other page). It only has an effect on render-time exceptions (React error boundaries
// cannot catch errors from event handlers, async code, or effects; those already go through each page's own
// try/catch and toast/UiAlert).
//
// ShellView keys this by location.pathname so navigating away from a broken page remounts it fresh, clearing
// the caught error without this component needing to know anything about routing itself.
export default class ErrorBoundary extends Component {
  state = { error: null }

  static getDerivedStateFromError(error) {
    return { error }
  }

  render() {
    const { error } = this.state
    if (!error) return this.props.children
    return (
      <UiCard variant="flat">
        <UiEmpty
          icon="alert"
          title="这个页面出错了"
          desc={error.message || '渲染时发生未知错误。'}
          actions={
            <>
              <UiButton variant="outline" size="sm" onClick={() => window.history.back()}>返回上一页</UiButton>
              <UiButton variant="primary" size="sm" onClick={() => this.setState({ error: null })}>重试</UiButton>
            </>
          }
        />
      </UiCard>
    )
  }
}
