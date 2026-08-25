import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './styles.css'
import { App } from './App'
import { AuthGate } from './components/AuthGate'
import { ErrorBoundary } from './components/ErrorBoundary'

const root = document.getElementById('root')
if (!root) throw new Error('missing #root')

createRoot(root).render(
  <StrictMode>
    <ErrorBoundary label="The panel">
      <AuthGate>{(auth, signOut) => <App auth={auth} onSignOut={signOut} />}</AuthGate>
    </ErrorBoundary>
  </StrictMode>,
)
