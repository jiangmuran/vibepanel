import { useCallback, useEffect, useMemo, useState } from 'react'
import { ChevronUp, Menu, Moon, Monitor, Sun } from 'lucide-react'

import { api } from './protocol/api'
import { PanelSocket } from './protocol/socket'
import type { SocketStatus } from './protocol/socket'
import type { PanelState, Project, Session } from './protocol/wire'
import { TerminalView } from './components/Terminal'
import { StateDot } from './components/StateDot'
import { Sidebar } from './components/Sidebar'
import { BottomTerminals } from './components/BottomTerminals'
import { applyTheme, loadTheme } from './components/theme'
import type { ThemeChoice } from './components/theme'
import { NARROW_QUERY, useMediaQuery } from './hooks/useMediaQuery'

/**
 * Safety net only.
 *
 * State arrives pushed over the WebSocket; this exists so that a viewer whose
 * socket dropped a message — or that was asleep in a background tab while the
 * socket reconnected — repairs itself instead of showing a stale list forever.
 */
const STATE_RESYNC_MS = 30_000

const SELECTED_KEY = 'vibepanel.selected'
const SIDEBAR_KEY = 'vibepanel.sidebar'
const BOTTOM_KEY = 'vibepanel.bottom'
const BOTTOM_DEFAULT_HEIGHT = 220

function sessionLabel(s: Session): string {
  return s.title || s.command || 'session'
}

/**
 * The session the user was last looking at.
 *
 * Without this a reload drops you on whichever session sorts first, and the
 * sort is by recent output — so a session that prints constantly steals your
 * place every time you refresh. The page is meant to be something you can
 * close and reopen without losing anything.
 */
function readStored(key: string): string | null {
  try {
    return localStorage.getItem(key)
  } catch {
    return null
  }
}

function writeStored(key: string, value: string | null) {
  try {
    if (value === null) localStorage.removeItem(key)
    else localStorage.setItem(key, value)
  } catch {
    /* private mode: the choice simply does not persist */
  }
}

export function App() {
  const socket = useMemo(() => new PanelSocket(), [])
  const narrow = useMediaQuery(NARROW_QUERY)

  const [status, setStatus] = useState<SocketStatus>('closed')
  const [state, setState] = useState<PanelState>({
    projects: [],
    sessions: [],
    live: [],
    projectOrder: 'auto',
  })
  const [selected, setSelected] = useState<string | null>(() => readStored(SELECTED_KEY))
  const [error, setError] = useState<string | null>(null)
  const [theme, setThemeState] = useState<ThemeChoice>(loadTheme)

  // Two separate ideas that were briefly one, to their cost.
  //
  // `docked` is the wide-layout preference: whether the sidebar takes a column
  // or collapses to a rail. It is remembered across visits.
  //
  // `drawerOpen` is the narrow-layout overlay. It is per-visit and starts
  // closed, because a phone should open on the terminal. Sharing one flag
  // meant the remembered desktop preference opened the drawer over the whole
  // phone screen on first load — with the menu button that closes it
  // underneath the drawer.
  const [docked, setDocked] = useState(() => readStored(SIDEBAR_KEY) !== 'collapsed')
  const [drawerOpen, setDrawerOpen] = useState(false)

  // Height doubles as the collapsed flag: 0 means hidden. One value to store,
  // and reopening restores the size the user last chose rather than a default.
  const [bottomHeight, setBottomHeight] = useState(() => {
    const raw = Number(readStored(BOTTOM_KEY))
    return Number.isFinite(raw) && raw >= 0 ? raw : 0
  })

  // The attribute is written synchronously here rather than from an effect.
  //
  // React runs a child's effects before its parent's, so an effect in App
  // would set data-theme *after* TerminalView has already re-read the CSS
  // custom properties to rebuild the xterm palette — leaving the terminal, the
  // largest surface on the page, one theme behind on every switch.
  const setTheme = useCallback((next: ThemeChoice) => {
    applyTheme(next)
    setThemeState(next)
  }, [])

  useEffect(() => {
    socket.connect()
    const off = socket.onStatus(setStatus)
    return () => {
      off()
      socket.close()
    }
  }, [socket])

  const applyState = useCallback((next: PanelState) => {
    setState(next)
    setSelected((cur) => {
      if (cur && next.sessions.some((s) => s.id === cur)) return cur
      return next.sessions[0]?.id ?? null
    })
  }, [])

  // Pushed updates are the primary path.
  useEffect(() => socket.onState(applyState), [socket, applyState])

  const refresh = useCallback(async () => {
    try {
      applyState(await api.state())
      setError(null)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [applyState])

  useEffect(() => {
    // Self-scheduling rather than setInterval: a slow response must not let
    // requests pile up, which is what an interval does the moment the server
    // is busy.
    let cancelled = false
    let timer = 0
    const tick = async () => {
      await refresh()
      if (!cancelled) timer = window.setTimeout(() => void tick(), STATE_RESYNC_MS)
    }
    void tick()
    return () => {
      cancelled = true
      window.clearTimeout(timer)
    }
  }, [refresh])

  // How many sessions are asking for something, in the tab title.
  //
  // The panel is usually not the tab you are looking at, and the whole point of
  // the waiting state is that noticing it late costs you. A number in the title
  // is the one place a browser will show it to you from another tab.
  const waiting = state.sessions.filter((s) => s.state === 'waiting').length
  useEffect(() => {
    document.title = waiting > 0 ? `(${waiting}) vibepanel` : 'vibepanel'
  }, [waiting])

  useEffect(() => writeStored(SELECTED_KEY, selected), [selected])
  useEffect(() => writeStored(SIDEBAR_KEY, docked ? 'open' : 'collapsed'), [docked])
  useEffect(() => writeStored(BOTTOM_KEY, String(bottomHeight)), [bottomHeight])

  // The xterm palette is rebuilt when this changes. It has to react to the
  // system preference as well as the toggle, or a laptop switching to dark at
  // sunset leaves the terminal on the light palette.
  const systemDark = useMediaQuery('(prefers-color-scheme: dark)')
  const themeKey = `${theme}:${systemDark}`

  // Scratch terminals are ordinary sessions with a parent, so they arrive in
  // the same list and are separated here rather than by a second query.
  const mainSessions = useMemo(
    () => state.sessions.filter((s) => !s.parentSessionId),
    [state.sessions],
  )
  const current = mainSessions.find((s) => s.id === selected) ?? null
  const bottomTerminals = useMemo(
    () => (current ? state.sessions.filter((s) => s.parentSessionId === current.id) : []),
    [state.sessions, current],
  )
  const currentProject = current
    ? (state.projects.find((p) => p.id === current.projectId) ?? null)
    : null

  const guard = async (fn: () => Promise<unknown>) => {
    try {
      await fn()
      setError(null)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  const addProject = () => {
    const path = window.prompt('Project directory', '~/projects/')
    if (!path) return
    void guard(() => api.createProject(path))
  }

  const newSession = (project: Project) => void guard(() => api.createSession(project.id, []))

  const newBottomTerminal = () => {
    if (!current) return
    if (bottomHeight === 0) setBottomHeight(BOTTOM_DEFAULT_HEIGHT)
    void guard(() => api.createSession(current.projectId, [], { parentSessionId: current.id }))
  }

  const killSession = (s: Session) => {
    if (!window.confirm(`Kill ${sessionLabel(s)}? The process is terminated.`)) return
    void guard(() => api.deleteSession(s.id))
  }

  const selectSession = (id: string) => {
    setSelected(id)
    // On a narrow screen the list is an overlay covering the terminal; leaving
    // it up after a choice hides the thing that was just chosen.
    if (narrow) setDrawerOpen(false)
  }

  const showOverlay = narrow && drawerOpen
  const showSidebar = narrow ? drawerOpen : true

  return (
    <div
      className="relative flex h-full w-full overflow-hidden bg-bg text-ink"
      // Which layout is in force, for the render check to assert on. Layout
      // bugs here are invisible to a screenshot diff but obvious as a wrong
      // mode, and chasing one without this took longer than it should have.
      data-layout={narrow ? 'narrow' : 'wide'}
    >
      {showSidebar && (
        <Sidebar
          projects={state.projects}
          sessions={mainSessions}
          live={state.live}
          selected={selected}
          expanded={narrow ? true : docked}
          overlay={showOverlay}
          onToggle={() => (narrow ? setDrawerOpen(false) : setDocked((v) => !v))}
          onSelect={selectSession}
          onAddProject={addProject}
          onNewSession={newSession}
          onRenameProject={(p, name) => void guard(() => api.patchProject(p.id, { name }))}
          onRenameSession={(s, title) => void guard(() => api.patchSession(s.id, { title }))}
          onPinSession={(s, pinned) => void guard(() => api.patchSession(s.id, { pinned }))}
          onSetSessionState={(s, st) => void guard(() => api.patchSession(s.id, { state: st }))}
          onKillSession={killSession}
          projectOrder={state.projectOrder}
          onReorderProjects={(ids) => void guard(() => api.reorderProjects(ids))}
          onAutoOrderProjects={() => void guard(() => api.autoOrderProjects())}
        />
      )}

      {showOverlay && (
        <button
          type="button"
          aria-label="Close projects"
          onClick={() => setDrawerOpen(false)}
          className="absolute inset-0 z-10 bg-black/30"
        />
      )}

      <main className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-11 shrink-0 items-center gap-2 border-b border-hairline px-3 vp-blur">
          {narrow && (
            <button
              type="button"
              onClick={() => setDrawerOpen(true)}
              title="Projects"
              className="rounded-md p-1.5 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
            >
              <Menu size={16} />
            </button>
          )}
          {current ? (
            <>
              <StateDot
                state={current.state}
                onToggle={(st) => void guard(() => api.patchSession(current.id, { state: st }))}
              />
              <span data-testid="session-title" className="truncate text-[13px] font-medium">
                {sessionLabel(current)}
              </span>
              {!narrow && (
                <span className="truncate text-[12px] text-ink-2">{currentProject?.name}</span>
              )}
              <span className="ml-auto tabular text-[11px] text-ink-2">
                {current.cols}x{current.rows}
              </span>
            </>
          ) : (
            <span className="text-[13px] text-ink-2">No session selected</span>
          )}
          <ThemeToggle theme={theme} onChange={setTheme} />
          <ConnectionDot status={status} />
        </header>

        {error && (
          <div
            className="border-b border-hairline px-4 py-2 text-[12px]"
            style={{ color: 'var(--vp-state-waiting)' }}
          >
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
            <div className="flex h-full items-center justify-center px-6 text-center text-[13px] text-ink-2">
              {state.projects.length === 0
                ? 'Add a project to get started'
                : 'Select or create a session'}
            </div>
          )}
        </div>

        {current && bottomHeight > 0 && (
          <BottomTerminals
            // Remount per parent: each main session has its own set of tabs,
            // and carrying the previous one's active tab across a switch shows
            // a terminal belonging to something you are no longer looking at.
            key={current.id}
            socket={socket}
            parent={current}
            terminals={bottomTerminals}
            themeKey={themeKey}
            height={bottomHeight}
            onHeightChange={setBottomHeight}
            onCollapse={() => setBottomHeight(0)}
            onNew={newBottomTerminal}
            onClose={(t) => void guard(() => api.deleteSession(t.id))}
            onRename={(t, title) => void guard(() => api.patchSession(t.id, { title }))}
          />
        )}

        {current && bottomHeight === 0 && (
          <button
            type="button"
            data-testid="bottom-show"
            onClick={() => setBottomHeight(BOTTOM_DEFAULT_HEIGHT)}
            title="Show terminals"
            className="flex h-6 shrink-0 items-center justify-center gap-1 border-t border-hairline text-[11px] text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink vp-blur"
          >
            <ChevronUp size={12} />
            terminals
            {bottomTerminals.length > 0 && (
              <span className="tabular">({bottomTerminals.length})</span>
            )}
          </button>
        )}
      </main>
    </div>
  )
}

function ConnectionDot({ status }: { status: SocketStatus }) {
  const colour =
    status === 'open'
      ? 'var(--vp-state-done)'
      : status === 'connecting'
        ? 'var(--vp-state-waiting)'
        : 'var(--vp-state-dead)'
  return (
    <span
      data-testid="connection"
      data-status={status}
      title={`Connection: ${status}`}
      className="ml-1 inline-block h-1.5 w-1.5 shrink-0 rounded-full"
      style={{ background: colour }}
    />
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
      data-testid="theme-toggle"
      onClick={() => onChange(next[theme])}
      title={`Theme: ${theme}`}
      className="ml-1 rounded-md p-1.5 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
    >
      <Icon size={15} />
    </button>
  )
}
