import { useCallback, useEffect, useMemo, useState } from 'react'
import { Moon, Plus, Sun, Terminal as TerminalIcon, Monitor, X } from 'lucide-react'

import { api } from './protocol/api'
import { PanelSocket } from './protocol/socket'
import type { SocketStatus } from './protocol/socket'
import type { PanelState, Project, Session } from './protocol/wire'
import { TerminalView } from './components/Terminal'
import { StateDot } from './components/StateDot'
import { applyTheme, loadTheme } from './components/theme'
import type { ThemeChoice } from './components/theme'

/** How often the panel re-reads the session list. */
const STATE_POLL_MS = 2000

/**
 * A name to show for a session.
 *
 * Never the tmux name: that is a hex id, and showing it means the automatic
 * naming failed in a way the user has to decode. The command, or failing that
 * the word "session", is at least readable.
 */
function sessionLabel(s: Session): string {
  return s.title || s.command || 'session'
}

const SELECTED_KEY = 'vibepanel.selected'

/**
 * The session the user was last looking at.
 *
 * Without this a reload drops you on whichever session sorts first, and the
 * sort is by recent output — so a session that prints constantly (a monitor, a
 * build) steals your place every time you refresh. The page is meant to be
 * something you can close and reopen without losing anything.
 */
function loadSelected(): string | null {
  try {
    return localStorage.getItem(SELECTED_KEY)
  } catch {
    return null
  }
}

function saveSelected(id: string | null) {
  try {
    if (id) localStorage.setItem(SELECTED_KEY, id)
    else localStorage.removeItem(SELECTED_KEY)
  } catch {
    /* private mode: the choice simply does not persist */
  }
}

export function App() {
  const socket = useMemo(() => new PanelSocket(), [])
  const [status, setStatus] = useState<SocketStatus>('closed')
  const [state, setState] = useState<PanelState>({ projects: [], sessions: [], live: [] })
  const [selected, setSelected] = useState<string | null>(loadSelected)
  const [theme, setThemeState] = useState<ThemeChoice>(loadTheme)

  // The attribute is written synchronously here rather than from an effect.
  //
  // React runs a child's effects before its parent's, so an effect in App
  // would set data-theme *after* TerminalView has already re-read the CSS
  // custom properties to rebuild the xterm palette — leaving the terminal, the
  // largest surface on the page, one theme behind on every switch. Writing to
  // the DOM before the state update means every child sees the new palette.
  const setTheme = useCallback((next: ThemeChoice) => {
    applyTheme(next)
    setThemeState(next)
  }, [])
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    socket.connect()
    const off = socket.onStatus(setStatus)
    return () => {
      off()
      socket.close()
    }
  }, [socket])

  const refresh = useCallback(async () => {
    try {
      const next = await api.state()
      setState(next)
      setError(null)
      // Keep the selection valid across deletions rather than rendering a
      // terminal for a session that no longer exists. The updater form reads
      // the current value without this component having to hold it in a ref.
      setSelected((cur) => {
        if (cur && next.sessions.some((s) => s.id === cur)) return cur
        return next.sessions[0]?.id ?? null
      })
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [])

  useEffect(() => {
    // Self-scheduling rather than setInterval: a slow response must not let
    // requests pile up on top of each other, which is what an interval does
    // the moment the server is busy.
    //
    // Polling at all is a stopgap. Session state already reaches the browser
    // over the WebSocket; the project and session lists should follow, and
    // this loop goes away when they do.
    let cancelled = false
    let timer = 0
    const tick = async () => {
      await refresh()
      if (!cancelled) timer = window.setTimeout(() => void tick(), STATE_POLL_MS)
    }
    void tick()
    return () => {
      cancelled = true
      window.clearTimeout(timer)
    }
  }, [refresh])

  useEffect(() => {
    saveSelected(selected)
  }, [selected])

  // The xterm palette is rebuilt when this key changes. It has to react to the
  // system preference too, not just the toggle, or a laptop switching to dark
  // at sunset leaves the terminal on the light palette.
  const [systemDark, setSystemDark] = useState(
    () => window.matchMedia('(prefers-color-scheme: dark)').matches,
  )
  useEffect(() => {
    const mq = window.matchMedia('(prefers-color-scheme: dark)')
    const on = (e: MediaQueryListEvent) => setSystemDark(e.matches)
    mq.addEventListener('change', on)
    return () => mq.removeEventListener('change', on)
  }, [])
  const themeKey = `${theme}:${systemDark}`

  const sessionsByProject = useMemo(() => {
    const map = new Map<string, Session[]>()
    for (const s of state.sessions) {
      const list = map.get(s.projectId)
      if (list) list.push(s)
      else map.set(s.projectId, [s])
    }
    return map
  }, [state.sessions])

  const current = state.sessions.find((s) => s.id === selected) ?? null
  const currentProject = current
    ? (state.projects.find((p) => p.id === current.projectId) ?? null)
    : null

  const addProject = async () => {
    const path = window.prompt('Project directory', '~/projects/')
    if (!path) return
    try {
      await api.createProject(path)
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  const addSession = async (project: Project, command: string[]) => {
    try {
      const s = await api.createSession(project.id, command)
      await refresh()
      setSelected(s.id)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  const killSession = async (s: Session) => {
    if (!window.confirm(`Kill ${sessionLabel(s)}? The process is terminated.`)) return
    try {
      await api.deleteSession(s.id)
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  return (
    <div className="flex h-full w-full bg-bg text-ink">
      <aside className="flex w-64 shrink-0 flex-col border-r border-hairline vp-blur">
        <header className="flex items-center justify-between px-4 py-3">
          <span className="text-[13px] font-semibold tracking-tight">vibepanel</span>
          <div className="flex items-center gap-1">
            <ThemeToggle theme={theme} onChange={setTheme} />
            <button
              type="button"
              onClick={() => void addProject()}
              title="Add project"
              className="rounded-md p-1.5 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
            >
              <Plus size={15} />
            </button>
          </div>
        </header>

        <nav className="flex-1 overflow-y-auto px-2 pb-3">
          {state.projects.length === 0 && (
            <p className="px-2 py-6 text-[12px] leading-relaxed text-ink-2">
              No projects yet. Add one to point the panel at a directory.
            </p>
          )}
          {state.projects.map((p) => (
            <ProjectGroup
              key={p.id}
              project={p}
              sessions={sessionsByProject.get(p.id) ?? []}
              live={state.live}
              selected={selected}
              onSelect={setSelected}
              onNewSession={(cmd) => void addSession(p, cmd)}
              onKill={(s) => void killSession(s)}
            />
          ))}
        </nav>

        <footer className="border-t border-hairline px-4 py-2 text-[11px] text-ink-2">
          <span
            className="mr-1.5 inline-block h-1.5 w-1.5 rounded-full align-middle"
            style={{
              background:
                status === 'open'
                  ? 'var(--vp-state-done)'
                  : status === 'connecting'
                    ? 'var(--vp-state-waiting)'
                    : 'var(--vp-state-dead)',
            }}
          />
          {status}
        </footer>
      </aside>

      <main className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-11 shrink-0 items-center gap-2 border-b border-hairline px-4 vp-blur">
          {current ? (
            <>
              <StateDot state={current.state} />
              <span className="truncate text-[13px] font-medium">
                {sessionLabel(current)}
              </span>
              <span className="truncate text-[12px] text-ink-2">{currentProject?.name}</span>
              <span className="ml-auto tabular text-[11px] text-ink-2">
                {current.cols}x{current.rows}
              </span>
            </>
          ) : (
            <span className="text-[13px] text-ink-2">No session selected</span>
          )}
        </header>

        {error && (
          <div className="border-b border-hairline px-4 py-2 text-[12px]" style={{ color: 'var(--vp-state-waiting)' }}>
            {error}
          </div>
        )}

        <div className="min-h-0 flex-1" style={{ background: 'var(--vp-terminal-bg)' }}>
          {current ? (
            <TerminalView
              // Remounting per session is deliberate: each needs its own xterm
              // with its own scrollback, and reusing one would bleed output
              // between sessions on every switch.
              key={current.id}
              socket={socket}
              sessionId={current.id}
              themeKey={themeKey}
              className="h-full w-full p-2"
            />
          ) : (
            <div className="flex h-full items-center justify-center text-[13px] text-ink-2">
              Select or create a session
            </div>
          )}
        </div>
      </main>
    </div>
  )
}

function ThemeToggle({
  theme,
  onChange,
}: {
  theme: ThemeChoice
  onChange: (t: ThemeChoice) => void
}) {
  const next: Record<ThemeChoice, ThemeChoice> = { system: 'light', light: 'dark', dark: 'system' }
  const Icon = theme === 'light' ? Sun : theme === 'dark' ? Moon : Monitor
  return (
    <button
      type="button"
      onClick={() => onChange(next[theme])}
      title={`Theme: ${theme}`}
      className="rounded-md p-1.5 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
    >
      <Icon size={15} />
    </button>
  )
}

function ProjectGroup({
  project,
  sessions,
  live,
  selected,
  onSelect,
  onNewSession,
  onKill,
}: {
  project: Project
  sessions: Session[]
  live: string[]
  selected: string | null
  onSelect: (id: string) => void
  onNewSession: (command: string[]) => void
  onKill: (s: Session) => void
}) {
  return (
    <section className="mb-3">
      <div className="group flex items-center gap-1 px-2 py-1">
        <span className="truncate text-[11px] font-semibold tracking-wide text-ink-2 uppercase">
          {project.name}
        </span>
        <button
          type="button"
          onClick={() => onNewSession([])}
          title="New shell in this project"
          className="ml-auto rounded p-1 text-ink-3 opacity-0 transition-opacity duration-200 ease-vp group-hover:opacity-100 hover:text-ink"
        >
          <TerminalIcon size={13} />
        </button>
      </div>
      {sessions.map((s) => {
        const isLive = live.includes(s.id)
        return (
          <div
            key={s.id}
            className={`group flex cursor-pointer items-center gap-2 rounded-vp px-2 py-1.5 transition-colors duration-200 ease-vp ${
              selected === s.id ? 'bg-surface-2' : 'hover:bg-surface-2'
            }`}
            onClick={() => onSelect(s.id)}
          >
            <StateDot state={s.state} />
            <span className="min-w-0 flex-1 truncate text-[12.5px]">{sessionLabel(s)}</span>
            {!isLive && <span className="text-[10px] text-ink-2">idle</span>}
            <button
              type="button"
              onClick={(e) => {
                e.stopPropagation()
                onKill(s)
              }}
              title="Kill session"
              className="rounded p-0.5 text-ink-3 opacity-0 transition-opacity duration-200 ease-vp group-hover:opacity-100 hover:text-ink"
            >
              <X size={12} />
            </button>
          </div>
        )
      })}
    </section>
  )
}
