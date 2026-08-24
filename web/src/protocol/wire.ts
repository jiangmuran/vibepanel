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
  t: 'subscribe' | 'unsubscribe' | 'resize' | 'takeControl' | 'ping'
  sessionId?: string
  cols?: number
  rows?: number
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
  lastOutputAt: number
  createdAt: number
  archivedAt: number | null

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
  /** 'auto' orders projects by recent activity; 'manual' by explicit position. */
  projectOrder: 'auto' | 'manual'
  /**
   * True when an agent is running and nothing is reporting its state.
   *
   * The inference does not work for the agent most people run here: Claude
   * Code does not ring the terminal bell when it stops for a decision, and the
   * bell is the only signal the heuristic has. Saying so beats quietly
   * under-reporting the one state the panel exists for.
   */
  stateGuessed: boolean
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

export interface SystemSample {
  at: number
  /** Null on the very first sample: there is nothing to difference against. */
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
  diskPath: string
  uptime: number
}

export interface FileEntry {
  name: string
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
}

export interface Note {
  projectId: string
  content: string
  updatedAt: number
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
  sessions: number
  attached: number
  viewers: number
  dataDir: string
  dbBytes: number
  addr: string
  url: string
  tlsMode: string
  domain: string
  allowAll: boolean
  passkeysUsable: boolean
  passkeyReason?: string
  username: string
}

export interface HookStatus {
  settingsPath: string
  scriptPath: string
  installed: boolean
  events: string[] | null
  snippet: string
  codexSnippet: string
}

export interface AuditEntry {
  at: number
  event: string
  username: string
  ip: string
  detail: string
}
