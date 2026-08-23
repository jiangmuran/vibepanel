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
export function decodeData(buf: ArrayBuffer): { ref: number; payload: Uint8Array } | null {
  if (buf.byteLength < BINARY_HEADER_LEN) return null
  const view = new DataView(buf)
  if (view.getUint8(0) !== FRAME_DATA) return null
  return {
    ref: view.getUint32(1, false),
    payload: new Uint8Array(buf, BINARY_HEADER_LEN),
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
}

export interface PanelState {
  projects: Project[]
  sessions: Session[]
  live: string[]
}
