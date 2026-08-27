import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
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
import { disambiguatedLabels, projectLabel, sessionLabel, exitReason } from './components/label'
import { applyTheme, loadTheme } from './components/theme'
import type { ThemeChoice } from './components/theme'
import { NARROW_QUERY, useMediaQuery } from './hooks/useMediaQuery'
import { EXIT_VANISHED } from './protocol/wire'
import { shellQuote } from './shell'
import { safeText } from './components/text'
import { DirectoryPicker } from './components/DirectoryPicker'
import { Toasts } from './components/Toasts'
import { ConfirmDialog } from './components/ConfirmDialog'
import { askConfirm } from './components/ask'
import { dismissToast, showToast } from './components/toasts'
import { focusTerminal } from './components/focus'
import { RestoreDialog } from './components/RestoreDialog'
import { t, useLang } from './i18n'

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
  useLang()
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
    fullscreen: [],
    projectOrder: 'auto',
    stale: '',
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
  const [restoreOpen, setRestoreOpen] = useState(false)
  // Dismissing the offer is per visit, not remembered.
  //
  // A remembered dismissal is how somebody loses a day's sessions for good: the
  // notice never comes back, the rows keep saying "gone", and the archived
  // scrollback sits in the database until the row is deleted. Reloading brings
  // the offer back, which is the behaviour a person expects from "later".
  const [restoreDismissed, setRestoreDismissed] = useState(false)

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

  // What the server says went wrong, said to the person it happened to.
  //
  // Every one of these frames used to be dropped on the floor -- there was no
  // case for them in the socket's switch at all -- and three of the six senders
  // are `write failed` twice and `paste failed`. So a write that failed
  // server-side looked exactly like a network problem: you type, nothing
  // appears, and the panel is serene about it.
  //
  // Carries a sequence number rather than being keyed by the message, because
  // the same failure twice in a row is the common case and setting identical
  // state would not restart the timer below.
  const [socketError, setSocketError] = useState<{ message: string; seq: number } | null>(null)
  useEffect(
    () =>
      socket.onError((_sessionId, message) => {
        setSocketError((prev) => ({ message, seq: (prev?.seq ?? 0) + 1 }))
      }),
    [socket],
  )
  useEffect(() => {
    if (!socketError) return
    // Transient: these describe one request that failed, not a condition. The
    // stale banner below is the opposite and stays until the condition clears.
    const timer = window.setTimeout(() => setSocketError(null), 8000)
    return () => clearTimeout(timer)
  }, [socketError])

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

  // Sessions whose tmux session is gone, which after a reboot is all of them.
  //
  // Derived from the snapshot rather than fetched: the state push already
  // carries every fact the restore dialog needs — the argv, the directory, the
  // project, whether there is archived scrollback — so a second endpoint would
  // be a second answer that can disagree with the list on screen.
  //
  // Scratch terminals are left out. They are tabs under a session, they hold a
  // shell somebody opened for a minute, and offering to rebuild two dozen of
  // them alongside the agents would bury the choice that matters.
  const restorable = useMemo(
    () => mainSessions.filter((s) => s.exited && s.exitStatus === EXIT_VANISHED),
    [mainSessions],
  )
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

  // Has the panel underneath this tab been replaced?
  //
  // Restarting the panel is safe by design -- the tmux server outlives the Go
  // process, which is the whole premise -- and `install.sh` restarts on
  // upgrade. What survives that restart on *this* side is a tab still running
  // the frontend it downloaded before, talking to a binary that may have
  // changed the wire underneath it. Nothing said so: the socket reconnects,
  // the terminals come back, and a protocol difference then shows up as
  // something subtler and much harder to place.
  //
  // Checked on reconnect rather than on a timer, because a reconnect is
  // exactly when a restart has happened, and a panel that polls its own
  // version every minute is spending a request on a question whose answer
  // almost never changes.
  //
  // Offered, not forced. Reloading out from under somebody mid-command is a
  // worse failure than showing an old interface for another minute.
  const bootBuild = useRef<string | null>(null)
  const [upgraded, setUpgraded] = useState(false)
  useEffect(() => {
    if (status !== 'open') return
    let cancelled = false
    void api
      .health()
      .then((h) => {
        if (cancelled) return
        const build = `${h.version}@${h.commit}`
        if (bootBuild.current === null) {
          bootBuild.current = build
          return
        }
        if (build !== bootBuild.current) setUpgraded(true)
      })
      .catch(() => {
        // Unreachable is not upgraded. The socket's own reconnection is what
        // handles that, and guessing here would show the banner every time the
        // wifi hiccupped.
      })
    return () => {
      cancelled = true
    }
  }, [status])

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
  const uploadInto = useCallback(
    async (files: File[]) => {
      if (!current || !currentProject || files.length === 0) return
      // The API takes a path relative to the project root; the session's cwd
      // is absolute and may have wandered outside it, in which case the root
      // is the only place we are allowed to write.
      const root = currentProject.path.replace(/\/+$/, '')
      const cwd = current.cwd || root
      const rel = cwd === root ? '' : cwd.startsWith(root + '/') ? cwd.slice(root.length + 1) : ''
      // The stack, not a note pinned to the corner of the terminal. The note
      // was one string in one place, so a second upload overwrote the first
      // one's result, and every one of its three sentences was English on a
      // Chinese page -- it had no way to reach the dictionary at all.
      const progress = showToast({
        kind: 'info',
        key: files.length === 1 ? 'toast.uploadingOne' : 'toast.uploadingMany',
        params: { n: files.length },
      })
      try {
        const { paths } = await api.upload(currentProject.id, rel, files)
        // Quoted only when it needs to be: a shell-quoted path that did not
        // need quoting is noise at the prompt, and an unquoted one with a
        // space in it is a bug the user finds after pressing enter.
        //
        // The safe set and the '\'' escape are shlex.quote's, and \w is ASCII
        // in a regex without the u flag exactly as it is under re.ASCII, so a
        // non-Latin filename gets quoted rather than passed through. Injection
        // is covered.
        //
        // Quoting is in shell.ts, with the measurements. The short version:
        // a tab in a filename survives the browser, the MIME parser and the
        // upload, and readline reads it as completion rather than as part of
        // the path -- so half the name never reaches the prompt. A name that
        // reaches the *screen* goes through safeText for the same reason; this
        // was the one place a filename's bytes reached a shell unexamined.
        const typed = paths.map(shellQuote).join(' ')
        sendToCurrent(typed + ' ')
        // Taken back rather than left to expire: "uploading…" sitting above
        // "uploaded" for another three seconds is the panel disagreeing with
        // itself about something that has already finished.
        dismissToast(progress)
        showToast({
          kind: 'success',
          key: paths.length === 1 ? 'toast.uploadedOne' : 'toast.uploadedMany',
          params: { n: paths.length },
        })
      } catch (err) {
        dismissToast(progress)
        // The server's own words, which name the file often enough that
        // dropping them would leave "the upload failed" and nothing to act on.
        showToast({
          kind: 'error',
          key: 'toast.uploadFailed',
          detail: err instanceof Error ? err.message : String(err),
        })
      }
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

  // A picker, not a prompt. window.prompt asks you to know the answer already,
  // spells nothing, and cannot tell you the path is missing until the server
  // says so afterwards.
  const [picking, setPicking] = useState(false)
  const addProject = () => setPicking(true)

  const newSession = (project: Project) => void guard(() => api.createSession(project.id, []))

  const newBottomTerminal = () => {
    if (!current) return
    if (!bottomOpen) setBottomOpen(true)
    void guard(() => api.createSession(current.projectId, [], { parentSessionId: current.id }))
  }

  // Removing a project kills every session in it, which is the part nobody
  // expects from a control that looks like "take this off the list". So the
  // confirmation counts them, and says what survives: the directory.
  const removeProject = async (p: Project) => {
    const running = state.sessions.filter((s) => s.projectId === p.id).length
    // Singular and plural are separate lines of the dictionary rather than a
    // conditional 's' in the caller: the caller that appends one has already
    // decided the sentence is English.
    const body =
      running === 0
        ? t('ask.removeProjectNone')
        : running === 1
          ? t('ask.removeProjectOne')
          : t('ask.removeProjectMany', { n: running })
    const yes = await askConfirm({
      title: t('ask.removeProjectTitle', { name: projectLabel(p) }),
      body,
      confirm: t('ask.remove'),
      cancel: t('ask.cancel'),
      destructive: true,
    })
    if (!yes) return
    void guard(() => api.deleteProject(p.id))
  }

  const killSession = async (s: Session) => {
    const yes = await askConfirm({
      title: t('ask.killTitle', { name: labelOf(s) }),
      body: t('ask.killBody'),
      confirm: t('ask.kill'),
      cancel: t('ask.cancel'),
      destructive: true,
    })
    if (!yes) return
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
    // And the keyboard follows the choice. Picking a session and then having to
    // click the terminal before you can type is a step nobody asked for -- see
    // focusTerminal for when it declines to do this, which is the half that
    // matters.
    focusTerminal(id)
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
          onRemoveProject={(p) => void removeProject(p)}
          onRenameSession={(s, title) => void guard(() => api.patchSession(s.id, { title }))}
          onPinSession={(s, pinned) => void guard(() => api.patchSession(s.id, { pinned }))}
          onSetSessionState={(s, st) => void guard(() => api.patchSession(s.id, { state: st }))}
          onKillSession={(s) => void killSession(s)}
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
          aria-label={t('app.closeProjects')}
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
              title={
                waiting > 0
                  ? t('app.projectsWaiting', { n: String(waiting) })
                  : t('app.projects')
              }
              data-testid="menu-button"
              className="vp-press relative rounded-md p-1.5 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
            >
              <Menu size={16} />
              {/* The count belongs where the list is, because on a phone the
                  list is hidden and this is the only thing on screen that can
                  say something needs you. */}
              {waiting > 0 && (
                <span
                  data-testid="waiting-badge"
                  // Inside the button, not hanging off it. At -top-0.5 on a
                  // phone the badge sat above the header's first pixel and was
                  // sliced in half by the edge of the viewport -- the count of
                  // things waiting for you, unreadable, in the corner the whole
                  // product is about.
                  className="tabular absolute top-0 right-0 flex h-3.5 min-w-3.5 items-center justify-center rounded-full px-1 text-vp-xs font-semibold"
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
              <span data-testid="session-title" className="truncate text-vp-md font-medium">
                {labelOf(current)}
              </span>
              {!narrow && (
                <span className="truncate text-vp-base text-ink-2">{currentProject?.name}</span>
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
                  className="vp-press ml-1 flex shrink-0 items-center gap-1 rounded-full border border-hairline px-2 py-0.5 text-vp-sm text-ink-2 transition-colors duration-200 ease-vp hover:text-ink"
                  title={
                    // Two different actions behind one button, and the tooltip
                    // is where the difference is said. A dead pane is respawned
                    // in place; a session whose tmux session went with the
                    // machine is rebuilt from the row, with its recorded
                    // command and its archived scrollback.
                    current.exitStatus === EXIT_VANISHED
                      ? t('restore.gone')
                      : `The process ${exitReason(current.exitStatus)}. Run it again in this pane.`
                  }
                >
                  <RotateCcw size={11} />
                  restart
                </button>
              )}
              {/* The banner in the pane says the same thing and scrolls away.
                  This does not: somebody who joins the session an hour later,
                  or scrolls up past the separator, still has to be able to find
                  out that everything above it belongs to a process that no
                  longer exists. */}
              {current.restoredAt > 0 && !current.exited && (
                <span
                  data-testid="restored-badge"
                  title={t('restore.badgeWhy', {
                    when: new Date(current.restoredAt * 1000).toLocaleString(),
                  })}
                  className="shrink-0 rounded-full border border-hairline px-2 py-0.5 text-vp-xs text-ink-2"
                >
                  {t('restore.badge')}
                </span>
              )}
              {/* A chip, not loose text. Bare "130x46" floating between a
                  session title and a row of icons reads as a debug print that
                  nobody took out; the same characters in a token read as a
                  readout, which is what it is. */}
              <span
                data-testid="grid-size"
                title={t('app.gridSize')}
                className="ml-auto shrink-0 rounded-md bg-surface-2 px-1.5 py-0.5 tabular text-vp-xs text-ink-2"
              >
                {current.cols}×{current.rows}
              </span>
            </>
          ) : (
            <span className="text-vp-md text-ink-2">{t('app.noSessionShort')}</span>
          )}
          {!narrow && rightWidth === 0 && (
            <button
              type="button"
              data-testid="right-show"
              onClick={() => setRightOpen(true)}
              title={t('app.showPanelShort')}
              className="vp-press ml-1 rounded-md p-1.5 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
            >
              <PanelRight size={15} />
            </button>
          )}
          <button
            type="button"
            data-testid="settings-open"
            onClick={() => setSettingsOpen(true)}
            title={t('app.settings')}
            className="vp-press ml-1 rounded-md p-1.5 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
          >
            <SettingsIcon size={15} />
          </button>
          <ThemeToggle theme={theme} onChange={setTheme} />
          <button
            type="button"
            data-testid="sign-out"
            onClick={onSignOut}
            title={`Signed in as ${auth.username ?? 'unknown'} — sign out`}
            className="vp-press ml-1 rounded-md p-1.5 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
          >
            <LogOut size={15} />
          </button>
          <ConnectionDot status={status} />
        </header>

        {/* safeText, because a server error message is a name-carrying channel
            nobody funnels. Several of them echo a filename back: `base + " already
            exists"` on an upload conflict, `"writing " + base + ": "`, `abs + " is
            not a directory"`. safeText is applied to fields the frontend knows are
            names, and an error is not one -- so a file whose name carries a
            directional override reverses the text around it, in the banner, at the
            moment you are deciding whether to rename and retry. */}
        {picking && (
          <DirectoryPicker
            onClose={() => setPicking(false)}
            onPick={async (path) => {
              // Not through guard(): the picker wants the rejection so it can
              // stay open and say why, and guard() swallows it into a banner
              // behind a modal that has already closed.
              try {
                await api.createProject(path)
              } catch (e) {
                // The one error that is not about the directory. A session that
                // expired while the tab was asleep belongs on the sign-in
                // screen, not inside a modal about paths.
                if (e instanceof UnauthorizedError) {
                  onSignOut()
                  return
                }
                throw e
              }
              setPicking(false)
              setError(null)
            }}
          />
        )}

        {error && (
          <div
            className="border-b border-hairline px-4 py-2 text-vp-base"
            style={{ color: 'var(--vp-state-waiting)' }}
          >
            {safeText(error)}
          </div>
        )}

        {/* A panel that cannot write is the one failure with no symptom: the
            terminals belong to tmux and keep working, so the only thing that
            changes is that nothing is being recorded any more. Measured with
            the database's writes capped — /api/health still said ok, and the
            only person who found out was the one who happened to press a
            button. */}
        {upgraded && (
          <div
            data-testid="upgrade-notice"
            className="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-1 border-b border-hairline px-4 py-2 text-vp-base"
            style={{ background: 'var(--vp-surface-2)' }}
          >
            <span className="font-semibold text-ink">{t('upgrade.title')}</span>
            <span className="min-w-0 flex-1 text-ink-2">{t('upgrade.body')}</span>
            <button
              type="button"
              data-testid="upgrade-reload"
              onClick={() => window.location.reload()}
              className="shrink-0 rounded-vp px-2.5 py-1 text-vp-base"
              style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
            >
              {t('upgrade.reload')}
            </button>
            <button
              type="button"
              onClick={() => setUpgraded(false)}
              className="vp-press shrink-0 rounded-vp px-2 py-1 text-vp-base text-ink-2 transition-colors duration-150 ease-vp hover:text-ink"
            >
              {t('upgrade.later')}
            </button>
          </div>
        )}

        {socketError && (
          <div
            data-testid="socket-error"
            className="border-b border-hairline px-4 py-2 text-vp-base"
            style={{ color: 'var(--vp-state-crashed)' }}
          >
            {/* And this one holds whatever the server put in an error frame,
                including a message type echoed back from another client. */}
            {safeText(socketError.message)}
          </div>
        )}

        {state.stale && (
          <div
            data-testid="stale-notice"
            className="border-b border-hairline px-4 py-2 text-vp-base"
            style={{ color: 'var(--vp-state-waiting)' }}
          >
            {t('app.stale')} {safeText(state.stale)}
          </div>
        )}

        {/* The one thing the panel had nothing to say about after a reboot.
            Every row read "gone", the restart button on each of them started a
            login shell, and there was no way to find out that was what it did
            until you typed at it. */}
        {restorable.length > 0 && !restoreDismissed && (
          <div
            data-testid="restore-notice"
            className="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-1 border-b border-hairline px-4 py-2 text-vp-base"
            style={{ background: 'var(--vp-surface-2)' }}
          >
            <span className="font-semibold text-ink">{t('restore.title')}</span>
            <span className="min-w-0 flex-1 text-ink-2">
              {t('restore.body', { n: String(restorable.length) })}
            </span>
            <button
              type="button"
              data-testid="restore-open"
              onClick={() => setRestoreOpen(true)}
              className="shrink-0 rounded-vp px-2.5 py-1 text-vp-base"
              style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
            >
              {t('restore.open')}
            </button>
            <button
              type="button"
              onClick={() => setRestoreDismissed(true)}
              className="vp-press shrink-0 rounded-vp px-2 py-1 text-vp-base text-ink-2 transition-colors duration-150 ease-vp hover:text-ink"
            >
              {t('restore.later')}
            </button>
          </div>
        )}

        {/* The terminal is a surface set into the chrome, not a hole cut out of
            it. Flush against the sidebar and the panel it read as an absence --
            the same colour arriving where a wall stopped. A radius, a hairline
            and a few pixels of chrome showing around it are what say "this is
            the thing, that was the frame". */}
        <div
          className="relative m-2 min-h-0 flex-1 overflow-hidden rounded-vp border border-hairline"
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
              className="pointer-events-none absolute inset-2 z-10 flex items-center justify-center rounded-vp border-2 border-dashed text-vp-md"
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
                // The click is the whole point of this button -- it is what
                // makes the write legal -- so the button vanishing is the only
                // thing that ever said it worked.
                showToast({ kind: 'success', key: 'toast.copied' })
              }}
              className="absolute top-2 left-1/2 z-10 -translate-x-1/2 rounded-vp border border-hairline px-3 py-1.5 text-vp-sm vp-solid hover:text-ink"
              title={t('app.clipboardRefused')}
            >
              The terminal copied {blockedClip.length} character
              {blockedClip.length === 1 ? '' : 's'} — click to put it on your clipboard
            </button>
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
              fullscreen={state.fullscreen.includes(current.id)}
              onSelectionChange={setSelection}
              onClipboard={(text, ok) => setBlockedClip(ok ? '' : text)}
              // The same road a dropped file takes: upload into the project,
              // then put the path on the command line. A screenshot is the
              // most common thing anyone pastes at an agent.
              onPasteFiles={(files) => void uploadInto(files)}
              className="h-full w-full p-2"
            />
          ) : (
            <div className="flex h-full items-center justify-center px-6 text-center text-vp-md text-ink-2">
              {state.projects.length === 0
                ? t('app.noProjects')
                : t('app.noSession')}
            </div>
          )}
        </div>

        {/* Before the phone's compose box and key bar, and after the terminal:
            on a narrow screen the stack anchors itself here. See Toasts. */}
        <Toasts narrow={narrow} />

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
            title={t('app.showTerminals')}
            className="vp-press flex h-6 shrink-0 items-center justify-center gap-1 border-t border-hairline text-vp-sm text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink vp-blur"
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

      {restoreOpen && restorable.length > 0 && (
        <RestoreDialog
          sessions={restorable}
          projects={state.projects}
          labels={labels}
          onClose={() => setRestoreOpen(false)}
          onDone={() => {
            setRestoreOpen(false)
            setRestoreDismissed(false)
          }}
        />
      )}

      {!narrow && rightWidth > 0 && (
        <RightPanel
          project={currentProject}
          sessions={state.sessions}
          socket={socket}
          tab={rightTab}
          onTab={(next) => {
            setRightTab(next)
            if (current) focusTerminal(current.id)
          }}
          width={rightWidth}
          onWidthChange={setRightSize}
          onCollapse={() => setRightOpen(false)}
          split={rightSplit}
          onSplitChange={setRightSplit}
          splitRatio={splitRatio}
          onSplitRatioChange={setSplitRatio}
        />
      )}

      {/* Last in the tree and z-50, so it is over the settings dialog that two
          of its questions are asked from. */}
      <ConfirmDialog />
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
      className="vp-press ml-1 rounded-md p-1.5 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
    >
      <Icon size={15} />
    </button>
  )
}
