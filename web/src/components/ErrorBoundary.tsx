import { Component } from 'react'
import type { ErrorInfo, ReactNode } from 'react'
// `t` only: this is a class component, so it cannot subscribe to the language
// with a hook. It renders once, after a crash, and a language change while an
// error screen is on top of the app is not a case worth a second mechanism for.
import { t } from '../i18n'

/**
 * Keeps one broken component from taking the whole console with it.
 *
 * Without a boundary anywhere, React unmounts the entire tree when a render
 * throws, and the page becomes an empty root div: no message, no controls,
 * nothing in the DOM to explain it. That happened for real. A project
 * directory with no files in it made the server send `"entries": null`, the
 * file list read `entries.length`, and the panel — terminals, sessions, the
 * lot — went white. The one-line cause was in a panel nobody was looking at.
 *
 * So this is not decoration. The terminal and the session list are the
 * product; a file tree that cannot render should cost the file tree.
 *
 * `label` names what failed, because "something went wrong" in a panel with
 * eight of them is not information.
 */
export class ErrorBoundary extends Component<
  { label: string; children: ReactNode },
  { error: Error | null }
> {
  state: { error: Error | null } = { error: null }

  static getDerivedStateFromError(error: Error) {
    return { error }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // Kept out of the UI but not out of the console: whoever is debugging
    // needs the stack, and the boundary is what stops it reaching the usual
    // uncaught-error path.
    console.error(`vibepanel: ${this.props.label} failed`, error, info.componentStack)
  }

  render() {
    if (!this.state.error) return this.props.children
    return (
      <div className="flex h-full w-full flex-col items-center justify-center gap-2 p-4 text-center">
        <p className="text-vp-md text-ink">{this.props.label} could not be displayed</p>
        {/* Wrapped, not truncated. This is the only thing anyone can paste
            into a bug report, and the first version cut it off mid-word at
            "Cannot read properties of null (reading 'le…" — which names
            neither the property nor the place. */}
        <p className="max-w-full text-vp-sm break-words text-ink-2">{this.state.error.message}</p>
        <button
          type="button"
          onClick={() => this.setState({ error: null })}
          className="vp-press rounded-vp border border-hairline px-2 py-1 text-vp-sm text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
        >
          {t('err.tryAgain')}
        </button>
      </div>
    )
  }
}
