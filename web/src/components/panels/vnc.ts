/**
 * Telling a frozen display apart from a quiet one.
 *
 * The problem RFB does not solve: a desktop where nothing is happening sends
 * nothing, and a desktop whose server has wedged with the TCP connection still
 * open also sends nothing. They look identical, for minutes, and the second one
 * is the case somebody needs to know about — a browser under test that stopped
 * repainting reads as "the run is quiet" until far too late.
 *
 * There is no keepalive in the protocol, so the client has to make one. A
 * non-incremental FramebufferUpdateRequest for a single pixel is a message a
 * server *must* answer with an update; send one after a quiet spell, and
 * silence afterwards is silence with a question standing in it.
 *
 * The state this decides is a UI affordance, not a guard, which is why it lives
 * on this side. Everything that decides what may be reached or what may be sent
 * is on the server; see internal/vnc.
 */

/** A quiet stretch this long earns a probe. */
export const QUIET_MS = 5_000

/**
 * How long an unanswered probe waits before the display is called stalled.
 *
 * Five seconds of no answer to a message the server is required to answer. Any
 * shorter and a busy VNC server compressing a full-screen repaint gets called
 * frozen mid-frame; any longer and the indicator is later than the person
 * watching.
 */
export const PROBE_GRACE_MS = 5_000

/**
 * A non-incremental FramebufferUpdateRequest for one pixel at the origin.
 *
 * [type=3][incremental=0][x=0][y=0][w=1][h=1], big-endian. Non-incremental is
 * the load-bearing byte: an incremental request may legitimately be answered
 * with nothing at all, which is exactly the silence this is trying to break.
 */
export const PROBE = new Uint8Array([3, 0, 0, 0, 0, 0, 0, 1, 0, 1])

export interface Liveness {
  /** When a byte last arrived from the display. */
  lastByteAt: number
  /** When the outstanding probe was sent; 0 when there is none. */
  probeAt: number
}

export function fresh(now: number): Liveness {
  return { lastByteAt: now, probeAt: 0 }
}

/**
 * Called for every inbound frame. Any byte at all counts as an answer.
 *
 * The previous state is deliberately not consulted: a probe is answered by the
 * display saying anything, not by it saying the right thing. Matching an
 * update to the request that caused it would mean parsing the server stream,
 * which is the one thing this proxy exists not to do.
 */
export function sawBytes(_l: Liveness, now: number): Liveness {
  return { lastByteAt: now, probeAt: 0 }
}

export interface Tick {
  next: Liveness
  /** Send PROBE now. */
  probe: boolean
  /** The display has not answered a message it is required to answer. */
  stalled: boolean
}

export function tick(l: Liveness, now: number): Tick {
  if (now - l.lastByteAt < QUIET_MS) {
    // Traffic is flowing. Any outstanding probe has been answered by
    // definition, because an answer is bytes.
    return { next: { lastByteAt: l.lastByteAt, probeAt: 0 }, probe: false, stalled: false }
  }
  if (l.probeAt === 0) {
    return { next: { lastByteAt: l.lastByteAt, probeAt: now }, probe: true, stalled: false }
  }
  return { next: l, probe: false, stalled: now - l.probeAt >= PROBE_GRACE_MS }
}

/**
 * What the panel is showing about a display.
 *
 * 'refused' is terminal and separate from 'closed' for the reason the share
 * dashboard's 'gone' is separate from 'disconnected': an address the server's
 * policy will not reach is not going to start working, and saying
 * "reconnecting" about it forever is a lie somebody eventually acts on.
 */
export type DisplayState = 'connecting' | 'live' | 'stalled' | 'closed' | 'refused'

/** WebSocket close codes the server uses; see internal/httpapi/vnc.go. */
const POLICY_VIOLATION = 1008

/**
 * A close becomes a state.
 *
 * The distinction that matters is "fix the configuration" against "switch the
 * machine on", and it arrives as a close code rather than being guessed from
 * the reason text — a string comparison against an error message is a thing
 * that breaks the day somebody rewords the error.
 */
export function stateForClose(code: number): DisplayState {
  return code === POLICY_VIOLATION ? 'refused' : 'closed'
}

/** Whether the panel should try again on its own. */
export function shouldRetry(state: DisplayState): boolean {
  return state === 'closed'
}

/**
 * How long to wait before reconnecting, growing with consecutive failures.
 *
 * A display on a machine that is switched off is a connection that fails
 * instantly, forever. Without a backoff that is a request every few
 * milliseconds against loopback and a log line for each.
 */
export function retryDelay(failures: number): number {
  return Math.min(1_000 * 2 ** Math.max(0, failures - 1), 30_000)
}
