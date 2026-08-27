// Wire protocol, mirroring internal/ws/protocol.go.
//
// Kept by hand rather than generated because it is small and stable; the Go
// side has the same constant names so a mismatch is easy to spot in review.
// If this file and the Go one ever disagree, the Go one is right.

/** Session states. Mirrors internal/session/state.go, the source of truth. */
export type SessionState = 'working' | 'waiting' | 'done'

/** Ordered by urgency, matching State.SortWeight on the server. */
export const STATE_ORDER: SessionState[] = ['waiting', 'working', 'done']

export const FRAME_DATA = 0x00

/**
 * Scrollback sent when a viewer subscribes, tagged so the client can tell it
 * apart from live output.
 *
 * The buffer contains whatever the application sent, capability queries
 * included. A freshly created xterm answers those as it parses them, and the
 * answer goes to the shell, which types it at the prompt — so replaying
 * without suppressing responses injects junk like "[?1;2c" into the session on
 * every page reload.
 */
export const FRAME_REPLAY = 0x01

const BINARY_HEADER_LEN = 5

/** Builds a binary data frame: [type][uint32 ref][payload]. */
export function encodeData(ref: number, payload: Uint8Array): Uint8Array {
  const out = new Uint8Array(BINARY_HEADER_LEN + payload.length)
  out[0] = FRAME_DATA
  new DataView(out.buffer).setUint32(1, ref, false)
  out.set(payload, BINARY_HEADER_LEN)
  return out
}

/** Splits a binary frame. Returns null for anything malformed. */
export function decodeData(
  buf: ArrayBuffer,
): { ref: number; payload: Uint8Array; replay: boolean } | null {
  if (buf.byteLength < BINARY_HEADER_LEN) return null
  const view = new DataView(buf)
  const kind = view.getUint8(0)
  if (kind !== FRAME_DATA && kind !== FRAME_REPLAY) return null
  return {
    ref: view.getUint32(1, false),
    payload: new Uint8Array(buf, BINARY_HEADER_LEN),
    replay: kind === FRAME_REPLAY,
  }
}

export interface ClientMessage {
  t: 'subscribe' | 'unsubscribe' | 'resize' | 'takeControl' | 'ping' | 'paste'
  sessionId?: string
  cols?: number
  rows?: number
  /** MsgPaste only. Input otherwise travels as binary frames. */
  text?: string
  /** MsgPaste only: send a carriage return once the paste has landed. */
  submit?: boolean
}

export interface ServerMessage {
  t:
    | 'subscribed'
    | 'size'
    | 'clipboard'
    | 'title'
    | 'exit'
    | 'dropped'
    | 'error'
    | 'pong'
    | 'state'
    // A project's note or todo list changed in another viewer.
    | 'panel'
  sessionId?: string
  ref?: number
  cols?: number
  rows?: number
  text?: string
  message?: string
  controlling?: boolean
}

// ── REST shapes, mirroring internal/store ──────────────────────────────────

export interface Project {
  id: string
  name: string
  path: string
  sortIndex: number | null
  pinned: boolean
  lastActiveAt: number
  createdAt: number
}

/**
 * The one exit status that is not a wait status.
 *
 * Mirrors store.ExitStatusVanished. It marks a session whose tmux session
 * simply disappeared — killed from a shell, lost with the server, gone in a
 * reboot — where nothing was around to observe how it ended. Real wait
 * statuses are never negative, so the two cannot be confused.
 *
 * It exists here rather than in a component because three separate places put
 * it in front of a person, and the first version leaked the raw number into
 * all three: a badge reading "exit -1", a tooltip promising "the process
 * exited with status -1", and a project summary that counted it as a crash.
 * A status a person can read as a number has to be a status a process could
 * actually have returned.
 */
export const EXIT_VANISHED = -1

export interface Session {
  id: string
  projectId: string
  tmuxName: string
  title: string
  titleSource: 'auto' | 'manual'
  state: SessionState
  stateSource: 'heuristic' | 'hook' | 'manual'
  stateChangedAt: number
  pinned: boolean
  sortIndex: number | null
  cwd: string
  command: string
  cols: number
  rows: number
  createdAt: number
  archivedAt: number | null

  /**
   * The argv this session was created with — the only version of it that can be
   * run again.
   *
   * `command` above is whatever is in the pane right now, rewritten by the
   * poller every couple of seconds: "node" for an agent, "bash" for a shell
   * somebody used. It is a label. This is what a restore executes, and what the
   * restore dialog shows before anything is pressed.
   */
  launchCommand: string[]
  /**
   * False for a row written before the panel recorded commands.
   *
   * Not the same as an empty `launchCommand`, which is a session deliberately
   * created with no command — a login shell, exactly reproducible. This one
   * means nobody knows, and the UI has to say so rather than quietly offering
   * a shell under an agent's name.
   */
  launchRecorded: boolean
  /** Rebuild this session at startup without asking, if its tmux session is gone. */
  restoreOnBoot: boolean
  /**
   * When this session was last rebuilt from its row, 0 if never.
   *
   * The process behind a restored session is new: the agent's context did not
   * survive the reboot and nothing can bring it back. The pane carries a banner
   * saying so, and banners scroll; this is what keeps the header able to say it.
   */
  restoredAt: number
  /**
   * When the archived scrollback was captured, 0 if there is none.
   *
   * What makes the restore dialog able to promise the right thing. A session
   * created since the last archive pass has nothing stored, and saying "your
   * scrollback comes back" about it would be a lie.
   */
  scrollbackAt: number

  /**
   * The pane's process is gone; tmux is showing its last screen.
   *
   * Orthogonal to `state`, which describes the task. A crashed agent and an
   * agent that finished are both "done" as far as the heuristic can tell, and
   * telling them apart is the whole reason this exists.
   */
  exited: boolean
  /** The wait status, meaningful only while `exited`. */
  exitStatus: number

  /**
   * Set for a scratch terminal opened under a main session.
   *
   * Bottom terminals are ordinary sessions with a parent rather than their own
   * kind of thing, so they arrive in the same list and get state, replay and
   * naming without a second implementation of each.
   */
  parentSessionId: string | null
}

export interface PanelState {
  projects: Project[]
  sessions: Session[]
  live: string[]
  /**
   * Sessions with a full-screen program drawing in them.
   *
   * The browser cannot work this out. tmux emulates the alternate screen per
   * pane and composes the result, and the panel keeps tmux's own client out of
   * the alternate screen so that scrollback exists at all — so a TUI's output
   * looks like any other output on the wire.
   */
  fullscreen: string[]
  /** 'auto' orders projects by recent activity; 'manual' by explicit position. */
  projectOrder: 'auto' | 'manual'
  /**
   * True when an arrangement is stored, whichever ordering is showing.
   *
   * Which is what makes the way back offerable at all: switching to automatic
   * keeps the positions now, so there is something to return to.
   */
  hasProjectOrder: boolean
  /**
   * True when an agent is running and nothing is reporting its state.
   *
   * The inference does not work for the agent most people run here: Claude
   * Code does not ring the terminal bell when it stops for a decision, and the
   * bell is the only signal the heuristic has. Saying so beats quietly
   * under-reporting the one state the panel exists for.
   */
  stateGuessed: boolean
  /**
   * Whether the reporter is installed, which decides which way out to offer
   * when the state is guessed rather than whether to say anything.
   */
  hooksInstalled: boolean

  /**
   * Why the panel has stopped keeping its records up to date, or '' when it
   * has not.
   *
   * A full disk is the case this exists for. The terminals keep working —
   * they belong to tmux — so nothing else on screen looks wrong while every
   * state change, every derived title and every note is being dropped.
   */
  stale: string
}

/**
 * A pushed state snapshot.
 *
 * The server sends the whole picture rather than a delta. The list is small,
 * and a delta protocol would be a second source of truth that drifts from the
 * first in ways nobody notices until the sidebar is showing a session that was
 * killed ten minutes ago.
 */
export interface StateMessage extends PanelState {
  t: 'state'
}

// ── side panels ────────────────────────────────────────────────────────────

/** What one session's process tree is costing. */
export interface SessionUsage {
  /**
   * A share of the whole machine, not top's convention where 100% is one core.
   *
   * The machine meter is an inch away on the same panel, and a session reading
   * 310% beside a machine reading 31% invites exactly one wrong conclusion.
   */
  cpuPercent: number
  /** Summed resident set across the tree; double-counts shared pages. */
  rss: number
  /** How many processes were found. 1 is a bare shell. */
  procs: number
}

export interface UsageSample {
  /** False where there is no /proc to walk, which is not "everything is idle". */
  readable: boolean
  cores: number
  /** Keyed by session id. A session whose pane has gone is absent, not zero. */
  sessions: Record<string, SessionUsage>
}

export interface SystemSample {
  at: number
  /** Null on the very first sample: there is nothing to difference against. */
  cpuPercent: number | null
  /**
   * Whether the counters exist here at all.
   *
   * A null cpuPercent means "no sample yet" or "nothing to sample on this
   * machine", and only one of those is worth saying "sampling…" about.
   */
  cpuReadable: boolean
  cores: number
  load1: number
  load5: number
  load15: number
  memTotal: number
  memAvailable: number
  swapTotal: number
  swapFree: number
  diskTotal: number
  diskFree: number
  diskPath: string
  uptime: number
}

export interface FileEntry {
  name: string
  /**
   * The symlink points outside the project.
   *
   * Shown rather than hidden — pretending a file is not there is its own kind
   * of lie — but nothing is offered for it. The download resolves symlinks and
   * refuses anything that leaves the project, so a link to /etc/passwd in a
   * project used to render a download button that answered 403.
   */
  escapes: boolean
  /** Relative to the project root, forward slashes. */
  path: string
  isDir: boolean
  size: number
  modTime: number
  symlink: boolean
  readable: boolean
}

export interface FileListing {
  path: string
  parent: string | null
  entries: FileEntry[]
  /** How many items the directory holds, which is not entries.length once the
   * server's cap bites. */
  total: number
  truncated: boolean
}

/** A directory listing for the "where should this project live" picker. */
export interface DirListing {
  /** The absolute path the picker is rooted at, shown so "~" means something. */
  root: string
  path: string
  parent: string | null
  entries: FileEntry[]
  total: number
  truncated: boolean
}

export interface Note {
  projectId: string
  content: string
  updatedAt: number
  /**
   * Advances on every write. Sent back with a save so the server can refuse
   * one that would land on top of another window's — which `updatedAt` cannot
   * do, being in whole seconds.
   */
  rev: number
}

export interface Todo {
  id: string
  projectId: string
  text: string
  done: boolean
  sortIndex: number
  createdAt: number
  doneAt: number | null
}

export interface AuthState {
  configured: boolean
  authenticated: boolean
  username?: string
  passkeysUsable: boolean
  /** Why the passkey button is disabled; the browser's own error is opaque. */
  passkeyReason?: string
}

export interface Passkey {
  id: string
  name: string
  createdAt: number
  lastUsedAt: number | null
}

export interface SettingsInfo {
  version: string
  commit: string
  built: string
  go: string
  uptime: number
  tmuxVersion: string
  tmuxSocket: string
  /**
   * The running tmux server was started with a different config from the one
   * this binary carries — the half of an upgrade nothing else can see.
   */
  tmuxConfigStale: boolean
  /** The running server predates the stamp, so the question has no answer. */
  tmuxConfigUnknown: boolean
  sessions: number
  attached: number
  viewers: number
  dataDir: string
  dbBytes: number
  addr: string
  url: string
  tlsMode: string
  /** Unix seconds when the served certificate expires; absent if unknown. */
  certExpiry?: number
  domain: string
  allowAll: boolean
  passkeysUsable: boolean
  passkeyReason?: string
  username: string
}

/** Which agent an install request is about. The server accepts these two and
 *  refuses anything else; it is not a free-text field because it chooses a file
 *  in the user's home directory to edit. */
export type HookAgent = 'claude' | 'codex'

export interface HookStatus {
  settingsPath: string
  scriptPath: string
  installed: boolean
  // Never null. The server sends [] for "nothing installed", which is every
  // fresh panel; this said `string[] | null` and the one reader guarded with
  // `?? []`, which is the symptom patched at the reader rather than the
  // contract fixed at the source.
  events: string[]
  snippet: string
  codexSnippet: string
  /** Where the Codex notify line goes: ~/.codex/config.toml. */
  codexPath: string
  /** Whether that file's `notify` is this panel's. Separate from `installed`,
   *  which is Claude's: the two agents are configured by different mechanisms
   *  and fail separately, so one flag would describe a machine where half of
   *  them are wired as if it were all of them. */
  codexInstalled: boolean
}

/** A credential a program uses instead of the session cookie. */
export interface ApiToken {
  id: string
  /** The first characters of the token, kept so a row can be named. The rest is
   *  only ever readable in the response that created it. */
  prefix: string
  name: string
  createdAt: number
  lastUsedAt: number
}

export interface AuditEntry {
  at: number
  event: string
  username: string
  ip: string
  detail: string
}

/** What `GET /api/update` answers. */
export interface UpdateCheck {
  current: string
  /** The newest release's tag, empty when the repository has none. */
  version?: string
  /** Whether that tag is ahead of what is running. */
  newer?: boolean
  url?: string
  notes?: string
  /** Empty when the release has no archive for this platform. */
  asset?: string
  /**
   * Why GitHub could not be reached. An air-gapped panel is a normal state,
   * not a broken one, so this arrives with 200 rather than as an error.
   */
  unreachable?: string
}

/** What `POST /api/update` answers, before it restarts. */
export interface UpdateResult {
  installed: string
  previous: string
  restarting: boolean
  restartWhy: string
}

// ── read-only share links ──────────────────────────────────────────────────

/**
 * How much a share link is allowed to say. Mirrors store.ShareDetail.
 *
 * 'counts' is shapes and numbers and no text at all; 'names' adds session
 * titles and project names. Neither ever carries a path, a command line or a
 * real id — that is not a mode, it is the shape of the payload.
 */
export type ShareDetail = 'counts' | 'names'

/** A link as the settings page lists it. The token is never in here. */
export interface ShareLink {
  id: string
  /** The first characters, kept so a row can be named on the way to revoking
   *  it. The rest is only ever readable in the response that created it. */
  prefix: string
  name: string
  detail: string
  /** Unix seconds, or 0 for a link that does not expire. */
  expiresAt: number
  createdAt: number
  lastUsedAt: number
}

/**
 * The machine, with the path taken out.
 *
 * Not SystemSample: that carries `diskPath`, which is the panel's data
 * directory and so names a user account and a filesystem layout. The server
 * restates the fields deliberately so each one is a decision somebody made.
 */
export interface ShareMachine {
  cpuReadable: boolean
  cpuPercent: number | null
  cores: number
  load1: number
  load5: number
  load15: number
  memTotal: number
  memAvailable: number
  swapTotal: number
  swapFree: number
  diskTotal: number
  diskFree: number
  uptime: number
}

export interface ShareCounts {
  projects: number
  sessions: number
  waiting: number
  working: number
  done: number
  exited: number
  crashed: number
}

export interface ShareProject {
  /** Pseudonymous and stable for the life of one link; not the panel's id. */
  id: string
  /** Empty under 'counts'. The dashboard numbers the groups instead. */
  name: string
  waiting: number
  working: number
  done: number
  total: number
}

export interface ShareSession {
  id: string
  projectId: string
  /** Empty under 'counts'. */
  name: string
  state: SessionState
  /** 'agent' | 'shell' | 'other' — a summary of the pane's foreground process,
   *  never the command line. Widened to string because the server owns the set
   *  and an unknown value has to render as something rather than crash. */
  kind: string
  stateChangedAt: number
  exited: boolean
  exitStatus: number
  /** A usage reading was found. A session whose pane has gone is absent from
   *  the sampler rather than zero, and zero is a real reading. */
  measured: boolean
  cpuPercent: number
  rss: number
  procs: number
}

/** Everything a share link discloses, in one object. Read it as the list. */
export interface ShareDashboard {
  /** When the server took this reading. The dashboard counts up from it, which
   *  is what stops a frozen page from looking like a quiet system. */
  at: number
  name: string
  detail: string
  /** Unix seconds, 0 when the link does not expire. */
  expiresAt: number
  usageReadable: boolean
  /** The panel has stopped keeping its records up to date. The reason is not
   *  sent: it is a message about this machine's storage, and a wall display can
   *  do nothing with it. */
  stale: boolean
  machine: ShareMachine
  counts: ShareCounts
  projects: ShareProject[]
  sessions: ShareSession[]
}
