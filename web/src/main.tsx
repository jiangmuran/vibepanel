import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './styles.css'
import { App } from './App'
import { AuthGate } from './components/AuthGate'
import { Dashboard } from './components/Dashboard'
import { ErrorBoundary } from './components/ErrorBoundary'
import { watchSystemTheme } from './components/theme'

const root = document.getElementById('root')
if (!root) throw new Error('missing #root')

// Before the first render: the meta tag ships one fixed colour, and on a
// home-screen PWA that colour is the chrome around the whole app.
watchSystemTheme()

// The service worker is what makes this installable and what shows a
// notification on Android, where `new Notification()` is refused outright. It
// caches nothing -- see public/sw.js for why that is deliberate rather than
// unfinished.
//
// Registered after load so it never competes with the first paint, and failing
// silently: a panel that will not start because a service worker would not
// register has traded the whole product for a nicety.
if ('serviceWorker' in navigator && window.isSecureContext) {
  window.addEventListener('load', () => {
    void navigator.serviceWorker.register('/sw.js').catch(() => {})
  })
}

/**
 * The read-only dashboard's capability, read out of the address bar.
 *
 * `/share/<token>` reaches the single-page fallback like any other unknown
 * path, so the token arrives here and nowhere else — it is never put in
 * storage, never sent to another endpoint, and never rendered.
 *
 * The character class is base64url, which is what auth.NewToken emits. The
 * length floor is there so that `/share/` with something short after it — a
 * truncated paste, a link somebody typed from memory — falls through to the
 * panel instead of being sent to the server as a credential and recorded as a
 * rejected one.
 */
function shareToken(pathname: string): string | null {
  const m = /^\/share\/([A-Za-z0-9_-]{20,})\/?$/.exec(pathname)
  return m ? m[1] : null
}

const token = shareToken(location.pathname)

// Two roots, and only one of them is ever built.
//
// The dashboard is deliberately not the panel with pieces hidden: AuthGate is
// what asks who you are and then hands the whole console to whoever answers,
// and a read-only page must not be one `if` away from that. It is also why the
// dashboard component reaches exactly one endpoint — there is no socket and no
// state fetch anywhere below this line.
createRoot(root).render(
  <StrictMode>
    {token ? (
      <ErrorBoundary label="The dashboard">
        <Dashboard token={token} />
      </ErrorBoundary>
    ) : (
      <ErrorBoundary label="The panel">
        <AuthGate>{(auth, signOut) => <App auth={auth} onSignOut={signOut} />}</AuthGate>
      </ErrorBoundary>
    )}
  </StrictMode>,
)
