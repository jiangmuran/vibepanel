import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  ChevronUp,
  LogOut,
  Menu,
  Moon,
  Monitor,
  PanelRight,
  RotateCcw,
  Settings as SettingsIcon,
  Sun,
} from 'lucide-react'

import { api, UnauthorizedError } from './protocol/api'
import { PanelSocket } from './protocol/socket'
import type { SocketStatus } from './protocol/socket'
import type { AuthState, PanelState, Project, Session } from './protocol/wire'
import { TerminalView } from './components/Terminal'
import { StateDot } from './components/StateDot'
import { Sidebar } from './components/Sidebar'
import { BottomTerminals } from './components/BottomTerminals'
import { RightPanel } from './components/RightPanel'
import { Settings } from './components/Settings'
import { MobileKeyBar } from './components/mobile/MobileKeyBar'
import { ComposeInput } from './components/mobile/ComposeInput'
import { SelectionCopy } from './components/mobile/SelectionCopy'
import type { PanelTab } from './components/RightPanel'
import { disambiguatedLabels, sessionLabel } from './components/label'
import { applyTheme, loadTheme } from './components/theme'
import type { ThemeChoice } from './components/theme'
import { NARROW_QUERY, useMediaQuery } from './hooks/useMediaQuery'
import { EXIT_VANISHED } from './protocol/wire'

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
const BOTTOM_OPEN_KEY = 'vibepanel.bottomOpen'
const BOTTOM_DEFAULT_HEIGHT = 220
const RIGHT_KEY = 'vibepanel.right'
const RIGHT_OPEN_KEY = 'vibepanel.rightOpen'
const RIGHT_TAB_KEY = 'vibepanel.rightTab'
const RIGHT_SPLIT_KEY = 'vibepanel.rightSplit'
const RIGHT_DEFAULT_WIDTH = 280


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

/**
 * A remembered panel size, or the default when nothing was ever chosen.
 *
 * The distinction is the whole function. `Number(null)` is 0 and 0 is the
 * collapsed flag, so reading the raw value as a number meant every first-time
 * visitor was treated as someone who had deliberately closed the right panel
 * and the terminal strip — which is where the files, the system monitor, the
 * notes and the todo list live. They opened to an empty frame and a small
 * button, and the harness never saw it because it opens the panels itself.
 */
function storedSize(key: string, fallback: number): number {
  const raw = readStored(key)
  if (raw === null || raw.trim() === '') return fallback
  const n = Number(raw)
  return Number.isFinite(n) && n >= 0 ? n : fallback
}

/**
 * A panel's remembered size and whether it is open, which have to be two
 * things rather than one.
 *
 * They used to be one: zero meant collapsed, and the comment above the state
 * said "reopening restores the size the user last chose rather than a
 * default". It could not. Collapsing wrote 0 over the only copy of the chosen
 * size, so reopening had nothing to restore and the code two hundred lines
 * away reached for the default — which is exactly what a person dragging the
 * notes panel out to read something, glancing at the terminal, and opening it
 * again did not want.
 *
 * A stored size of 0 is the old encoding meaning collapsed. Honour that once,
 * and hand back the default when it is reopened; 0 is never written again.
 */
function panelState(sizeKey: string, openKey: string, fallback: number) {
  const stored = storedSize(sizeKey, fallback)
  return {
    size: stored > 0 ? stored : fallback,
    open: readStored(openKey) !== 'closed' && stored !== 0,
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

export function App({ auth, onSignOut }: { auth: AuthState; onSignOut: () => void }) {
  const socket = useMemo(() => new PanelSocket(), [])
  const narrow = useMediaQuery(NARROW_QUERY)
  // Width decides the layout; the pointer decides the gestures. A tablet in
  // landscape is not narrow and still has nothing but fingers, so keying
  // press-and-hold selection to the layout breakpoint would leave it with no
  // way to copy at all.
  const coarsePointer = useMediaQuery('(pointer: coarse)')

  const [status, setStatus] = useState<SocketStatus>('closed')
  const [state, setState] = useState<PanelState>({
    projects: [],
    sessions: [],
    live: [],
    projectOrder: 'auto',
    hasProjectOrder: false,
    stateGuessed: false,
    hooksInstalled: false,
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

  // The chosen height and whether the strip is showing are stored apart, so
  // that closing it does not erase the height. `bottomHeight` stays the single
  // value the layout reads, with 0 still meaning hidden.
  const [bottomSize, setBottomSize] = useState(
    () => panelState(BOTTOM_KEY, BOTTOM_OPEN_KEY, BOTTOM_DEFAULT_HEIGHT).size,
  )
  const [bottomOpen, setBottomOpen] = useState(
    () => panelState(BOTTOM_KEY, BOTTOM_OPEN_KEY, BOTTOM_DEFAULT_HEIGHT).open,
  )
  const bottomHeight = bottomOpen ? bottomSize : 0

  // Same shape as the bottom strip, for the same reason.
  const [rightSize, setRightSize] = useState(
    () => panelState(RIGHT_KEY, RIGHT_OPEN_KEY, RIGHT_DEFAULT_WIDTH).size,
  )
  const [rightOpen, setRightOpen] = useState(
    () => panelState(RIGHT_KEY, RIGHT_OPEN_KEY, RIGHT_DEFAULT_WIDTH).open,
  )
  const rightWidth = rightOpen ? rightSize : 0
  const [selection, setSelection] = useState('')
  const [rightTab, setRightTab] = useState<PanelTab>(() => {
    const raw = readStored(RIGHT_TAB_KEY)
    return raw === 'files' || raw === 'monitor' || raw === 'notes' || raw === 'todos'
      ? raw
      : 'files'
  })
  const [rightSplit, setRightSplit] = useState(() => readStored(RIGHT_SPLIT_KEY) === 'on')
  const [splitRatio, setSplitRatio] = useState(0.5)
  const [settingsOpen, setSettingsOpen] = useState(false)

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
      if (e instanceof UnauthorizedError) {
        onSignOut()
        return
      }
      setError(null)
    }
    // onSignOut is stable — the gate memoises it — so this does not restart
    // the resync loop on every render.
  }, [applyState, onSignOut])

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
  // The size, never the collapsed state: that is the whole point of the split.
  useEffect(() => writeStored(BOTTOM_KEY, String(bottomSize)), [bottomSize])
  useEffect(() => writeStored(BOTTOM_OPEN_KEY, bottomOpen ? 'open' : 'closed'), [bottomOpen])
  useEffect(() => writeStored(RIGHT_KEY, String(rightSize)), [rightSize])
  useEffect(() => writeStored(RIGHT_OPEN_KEY, rightOpen ? 'open' : 'closed'), [rightOpen])
  useEffect(() => writeStored(RIGHT_TAB_KEY, rightTab), [rightTab])
  useEffect(() => writeStored(RIGHT_SPLIT_KEY, rightSplit ? 'on' : 'off'), [rightSplit])

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
  const labels = useMemo(() => disambiguatedLabels(mainSessions), [mainSessions])
  const labelOf = (s: Session) => labels.get(s.id) ?? sessionLabel(s)
  // Sorted by age, not by the order they arrive in.
  //
  // The session list is ordered by urgency and recent output, which is right
  // for the sidebar and wrong for a row of tabs: a terminal that printed
  // something jumped to the front, so the tab under the pointer moved. Worse,
  // the automatic label is positional — "term 2" became "term 1" — so the tab
  // was renamed as well as moved, and the one you had been using was neither
  // where you left it nor called what you called it.
  //
  // Creation order never changes, which is the only property a tab strip
  // needs. Ties by id so the result is total.
  const bottomTerminals = useMemo(
    () =>
      (current ? state.sessions.filter((s) => s.parentSessionId === current.id) : []).sort(
        (a, b) => a.createdAt - b.createdAt || a.id.localeCompare(b.id),
      ),
    [state.sessions, current],
  )

  // A connection that stays down might mean the session ended.
  //
  // A browser cannot see the HTTP status of a failed WebSocket handshake, so a
  // socket refused with 401 looks exactly like one refused by a flaky network.
  // The panel therefore went on showing the session list and the last frame of
  // a terminal, with only the connection dot to say otherwise, until some
  // unrelated fetch happened to get a 401 — about twenty seconds, measured.
  //
  // So: after a few seconds down, ask. If the answer is that we are signed
  // out, say so. Asking first matters — signing out on a dropped connection
  // would turn a bad wifi moment into a logout.
  useEffect(() => {
    if (status !== 'closed') return
    const timer = window.setTimeout(() => {
      void api
        .authState()
        .then((state) => {
          if (!state.authenticated) onSignOut()
        })
        .catch(() => {
          // The panel is unreachable rather than refusing us. Leave it to the
          // socket's own reconnection.
        })
    }, 4000)
    return () => clearTimeout(timer)
  }, [status, onSignOut])

  // On a phone the terminal is a display: input arrives from the compose box
  // and the key bar, so tapping it must not raise the software keyboard over
  // the thing being read.
  const sendToCurrent = useCallback(
    (text: string) => {
      if (current) socket.writeText(current.id, text)
    },
    [socket, current],
  )
  const pasteToCurrent = useCallback(
    (text: string, submit: boolean) => {
      if (current) socket.pasteText(current.id, text, submit)
    },
    [socket, current],
  )
  const currentProject = current
    ? (state.projects.find((p) => p.id === current.projectId) ?? null)
    : null

  // Dropping files onto the terminal uploads them next to the session and
  // types the paths at the prompt. That last part is the point: the reason to
  // put a screenshot on the server is to hand it to the agent, and going to
  // find the path afterwards is most of the work.
  // Text the pane copied that the browser refused to accept. Kept so it can
  // be offered behind a click, which is the activation the write needs.
  const [blockedClip, setBlockedClip] = useState('')
  const [dropping, setDropping] = useState(false)
  const [dropNote, setDropNote] = useState('')
  const uploadInto = useCallback(
    async (files: File[]) => {
      if (!current || !currentProject || files.length === 0) return
      // The API takes a path relative to the project root; the session's cwd
      // is absolute and may have wandered outside it, in which case the root
      // is the only place we are allowed to write.
      const root = currentProject.path.replace(/\/+$/, '')
      const cwd = current.cwd || root
      const rel = cwd === root ? '' : cwd.startsWith(root + '/') ? cwd.slice(root.length + 1) : ''
      setDropNote(`Uploading ${files.length} file${files.length === 1 ? '' : 's'}…`)
      try {
        const { paths } = await api.upload(currentProject.id, rel, files)
        // Quoted only when it needs to be: a shell-quoted path that did not
        // need quoting is noise at the prompt, and an unquoted one with a
        // space in it is a bug the user finds after pressing enter.
        const typed = paths
          .map((x) => (/[^\w@%+=:,./-]/.test(x) ? `'${x.replace(/'/g, `'\\''`)}'` : x))
          .join(' ')
        sendToCurrent(typed + ' ')
        setDropNote(`${paths.length} file${paths.length === 1 ? '' : 's'} uploaded`)
      } catch (err) {
        setDropNote(err instanceof Error ? err.message : 'upload failed')
      }
      window.setTimeout(() => setDropNote(''), 4000)
    },
    [current, currentProject, sendToCurrent],
  )

  const guard = async (fn: () => Promise<unknown>) => {
    try {
      await fn()
      setError(null)
    } catch (e) {
      // A session that expired while a tab was asleep should return to the
      // sign-in screen, not paint a permission error inside a panel that no
      // longer works.
      if (e instanceof UnauthorizedError) {
        onSignOut()
        return
      }
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
    if (!bottomOpen) setBottomOpen(true)
    void guard(() => api.createSession(current.projectId, [], { parentSessionId: current.id }))
  }

  // Removing a project kills every session in it, which is the part nobody
  // expects from a control that looks like "take this off the list". So the
  // confirmation counts them, and says what survives: the directory.
  const removeProject = (p: Project) => {
    const running = state.sessions.filter((s) => s.projectId === p.id).length
    const what =
      running === 0
        ? `Remove ${p.name} from the panel?`
        : `Remove ${p.name} from the panel? Its ${running} session${running === 1 ? '' : 's'} ` +
          `will be killed.`
    if (!window.confirm(`${what}\n\nThe directory itself is left alone.`)) return
    void guard(() => api.deleteProject(p.id))
  }

  const killSession = (s: Session) => {
    if (!window.confirm(`Kill ${labelOf(s)}? The process is terminated.`)) return
    void guard(() => api.deleteSession(s.id))
  }

  // No confirmation: unlike killing, restarting a dead session destroys
  // nothing — the pane and its scrollback stay, so the crash is still there to
  // read next to the new prompt.
  const restartSession = (s: Session) => {
    void guard(() => api.restartSession(s.id))
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
          labels={labels}
          live={state.live}
          selected={selected}
          expanded={narrow ? true : docked}
          overlay={showOverlay}
          onToggle={() => (narrow ? setDrawerOpen(false) : setDocked((v) => !v))}
          onSelect={selectSession}
          onAddProject={addProject}
          onNewSession={newSession}
          onRenameProject={(p, name) => void guard(() => api.patchProject(p.id, { name }))}
          onRemoveProject={removeProject}
          onRenameSession={(s, title) => void guard(() => api.patchSession(s.id, { title }))}
          onPinSession={(s, pinned) => void guard(() => api.patchSession(s.id, { pinned }))}
          onSetSessionState={(s, st) => void guard(() => api.patchSession(s.id, { state: st }))}
          onKillSession={killSession}
          onRestartSession={restartSession}
          projectOrder={state.projectOrder}
          onReorderProjects={(ids) => void guard(() => api.reorderProjects(ids))}
          onAutoOrderProjects={() => void guard(() => api.autoOrderProjects())}
          hasProjectOrder={state.hasProjectOrder}
          onRestoreProjectOrder={() => void guard(() => api.restoreProjectOrder())}
          stateGuessed={state.stateGuessed}
          hooksInstalled={state.hooksInstalled}
          onOpenSettings={() => {
            setSettingsOpen(true)
            if (narrow) setDrawerOpen(false)
          }}
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
        {/* vp-safe-top repeats h-11; the two have to move together. See the
            class for why it is written that way rather than with box-sizing. */}
        <header className="flex h-11 shrink-0 items-center gap-2 border-b border-hairline px-3 vp-blur vp-safe-top">
          {narrow && (
            <button
              type="button"
              onClick={() => setDrawerOpen(true)}
              title={waiting > 0 ? `Projects — ${waiting} waiting for you` : 'Projects'}
              className="relative rounded-md p-1.5 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
            >
              <Menu size={16} />
              {/* The count belongs where the list is, because on a phone the
                  list is hidden and this is the only thing on screen that can
                  say something needs you. */}
              {waiting > 0 && (
                <span
                  data-testid="waiting-badge"
                  className="tabular absolute -top-0.5 -right-0.5 flex h-3.5 min-w-3.5 items-center justify-center rounded-full px-1 text-[9px] font-semibold"
                  style={{ background: 'var(--vp-state-waiting)', color: '#fff' }}
                >
                  {waiting}
                </span>
              )}
            </button>
          )}
          {current ? (
            <>
              <StateDot
                state={current.state}
                exited={current.exited}
                exitStatus={current.exitStatus}
                onToggle={(st) => void guard(() => api.patchSession(current.id, { state: st }))}
              />
              <span data-testid="session-title" className="truncate text-[13px] font-medium">
                {labelOf(current)}
              </span>
              {!narrow && (
                <span className="truncate text-[12px] text-ink-2">{currentProject?.name}</span>
              )}
              {/* Where you are when you find out. Reading a stack trace and
                  then having to go hunting in the sidebar for the way to try
                  again is the kind of small friction that sends people back to
                  a real terminal. */}
              {current.exited && (
                <button
                  type="button"
                  data-testid="restart-current"
                  onClick={() => restartSession(current)}
                  className="ml-1 flex shrink-0 items-center gap-1 rounded-full border border-hairline px-2 py-0.5 text-[11px] text-ink-2 transition-colors duration-200 ease-vp hover:text-ink"
                  title={
                    current.exitStatus === EXIT_VANISHED
                      ? 'The tmux session is gone. Start it again in a new one.'
                      : current.exitStatus === 0
                        ? 'The process exited. Run it again in this pane.'
                        : `The process exited with status ${current.exitStatus}. Run it again in this pane.`
                  }
                >
                  <RotateCcw size={11} />
                  restart
                </button>
              )}
              <span data-testid="grid-size" className="ml-auto tabular text-[11px] text-ink-2">
                {current.cols}x{current.rows}
              </span>
            </>
          ) : (
            <span className="text-[13px] text-ink-2">No session selected</span>
          )}
          {!narrow && rightWidth === 0 && (
            <button
              type="button"
              data-testid="right-show"
              onClick={() => setRightOpen(true)}
              title="Show side panel"
              className="ml-1 rounded-md p-1.5 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
            >
              <PanelRight size={15} />
            </button>
          )}
          <button
            type="button"
            data-testid="settings-open"
            onClick={() => setSettingsOpen(true)}
            title="Settings"
            className="ml-1 rounded-md p-1.5 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
          >
            <SettingsIcon size={15} />
          </button>
          <ThemeToggle theme={theme} onChange={setTheme} />
          <button
            type="button"
            data-testid="sign-out"
            onClick={onSignOut}
            title={`Signed in as ${auth.username ?? 'unknown'} — sign out`}
            className="ml-1 rounded-md p-1.5 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
          >
            <LogOut size={15} />
          </button>
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

        <div
          className="relative min-h-0 flex-1"
          style={{ background: 'var(--vp-terminal-bg)' }}
          onDragOver={(e) => {
            // Both handlers, and both preventDefault: without dragover the
            // drop never fires, and the browser navigates to the file instead.
            if (!current) return
            e.preventDefault()
            e.dataTransfer.dropEffect = 'copy'
            setDropping(true)
          }}
          onDragLeave={(e) => {
            // Only when the pointer actually left the container. Dragging over
            // a child fires dragleave for the parent, which made the overlay
            // flicker on every row of the terminal.
            if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setDropping(false)
          }}
          onDrop={(e) => {
            if (!current) return
            e.preventDefault()
            setDropping(false)
            void uploadInto([...e.dataTransfer.files])
          }}
        >
          {dropping && (
            <div
              data-testid="drop-overlay"
              className="pointer-events-none absolute inset-2 z-10 flex items-center justify-center rounded-vp border-2 border-dashed text-[13px]"
              style={{ borderColor: 'var(--vp-accent)', color: 'var(--vp-accent)' }}
            >
              Drop to upload into {current?.cwd || currentProject?.path}
            </div>
          )}
          {blockedClip && (
            // Inside a click, so the write is allowed. execCommand is the
            // fallback rather than a preference: over plain http there is no
            // navigator.clipboard at all, and a self-hosted panel on a LAN
            // address is exactly that.
            <button
              type="button"
              data-testid="clipboard-offer"
              onClick={() => {
                const text = blockedClip
                const legacy = () => {
                  const ta = document.createElement('textarea')
                  ta.value = text
                  ta.style.position = 'fixed'
                  ta.style.opacity = '0'
                  document.body.appendChild(ta)
                  ta.select()
                  try {
                    document.execCommand('copy')
                  } finally {
                    ta.remove()
                  }
                }
                const clip = navigator.clipboard
                if (clip) {
                  void clip.writeText(text).catch(legacy)
                } else {
                  legacy()
                }
                setBlockedClip('')
              }}
              className="absolute top-2 left-1/2 z-10 -translate-x-1/2 rounded-vp border border-hairline px-3 py-1.5 text-[11.5px] vp-solid hover:text-ink"
              title="The browser refused a clipboard write that did not come from a click"
            >
              The terminal copied {blockedClip.length} character
              {blockedClip.length === 1 ? '' : 's'} — click to put it on your clipboard
            </button>
          )}
          {dropNote && (
            <div
              data-testid="drop-note"
              className="absolute top-2 right-2 z-10 rounded-vp border border-hairline px-2 py-1 text-[11px] vp-solid"
            >
              {dropNote}
            </div>
          )}
          {current ? (
            <TerminalView
              // Remounting per session is deliberate: each needs its own xterm
              // with its own scrollback, and reusing one would bleed output
              // between sessions on every switch.
              key={current.id}
              socket={socket}
              sessionId={current.id}
              themeKey={themeKey}
              readOnly={narrow}
              touchSelect={narrow || coarsePointer}
              onSelectionChange={setSelection}
              onClipboard={(text, ok) => setBlockedClip(ok ? '' : text)}
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

        {current && narrow && (
          <>
            <SelectionCopy selection={selection} />
            <ComposeInput sessionId={current.id} onSend={sendToCurrent} onPaste={pasteToCurrent} />
            <MobileKeyBar onSend={sendToCurrent} />
          </>
        )}

        {current && !narrow && bottomHeight > 0 && (
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
            onHeightChange={setBottomSize}
            onCollapse={() => setBottomOpen(false)}
            onNew={newBottomTerminal}
            onClose={(t) => void guard(() => api.deleteSession(t.id))}
            onRename={(t, title) => void guard(() => api.patchSession(t.id, { title }))}
          />
        )}

        {current && !narrow && bottomHeight === 0 && (
          <button
            type="button"
            data-testid="bottom-show"
            onClick={() => setBottomOpen(true)}
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

      {/* Hidden on a narrow screen: a 280px column beside a terminal on a
          phone leaves neither usable.
          
          This used to end "the panels reach mobile in their own layout rather
          than by being squeezed into this one", which was not true of anything
          that exists. There is no mobile route to the file tree, the monitor,
          the notes or the todo list — the narrow layout is the terminal, the
          compose box and the key bar, and that is what the plan scoped. Saying
          otherwise made a gap read as a decision that had already been carried
          out. */}
      {settingsOpen && <Settings onClose={() => setSettingsOpen(false)} />}

      {!narrow && rightWidth > 0 && (
        <RightPanel
          project={currentProject}
          socket={socket}
          tab={rightTab}
          onTab={setRightTab}
          width={rightWidth}
          onWidthChange={setRightSize}
          onCollapse={() => setRightOpen(false)}
          split={rightSplit}
          onSplitChange={setRightSplit}
          splitRatio={splitRatio}
          onSplitRatioChange={setSplitRatio}
        />
      )}
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
