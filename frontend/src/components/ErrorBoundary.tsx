import { Component, type ReactNode } from 'react';

// ErrorBoundary keeps one page's crash, or a page that failed to download, from blanking the whole
// dashboard: the header and navigation stay, and only the page area says what happened. Keyed by
// the path, so going to another page starts afresh.
export class ErrorBoundary extends Component<{ children: ReactNode }, { error?: Error }> {
  state: { error?: Error } = {};

  // React itself logs the error to the console; this only swaps in the message.
  static getDerivedStateFromError(error: Error) {
    return { error };
  }

  render() {
    if (!this.state.error) return this.props.children;
    return (
      <main>
        <div className="empty" role="alert">
          <strong>Something went wrong on this page</strong>
          {navigator.onLine ? 'Reloading usually fixes it, or try another page.' : 'This device seems to be offline. Reload once it is back.'}
          <div>
            <button type="button" className="btn" onClick={() => window.location.reload()}>
              Reload
            </button>
          </div>
        </div>
      </main>
    );
  }
}
