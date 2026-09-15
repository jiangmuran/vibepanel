/**
 * The Preview pane's dealings with the frame it draws, as functions with no
 * React in them.
 *
 * Two of the three are security decisions and one is a timing decision, and
 * all three are the kind of thing a component quietly gets wrong in a way
 * nothing on screen shows. So they live here, under tests.
 */

/** What a preview frame may post to the pane. Anything else is dropped. */
export type FrameMessage =
  | { type: 'vp.errors'; errors: FrameError[] }
  | { type: 'vp.picked'; selector: string; text: string; rect: Rect; viewport: { w: number; h: number } }
  | { type: 'vp.pick'; on: boolean }
  | { type: 'vp.status'; status: string }
  | { type: 'vp.ready' }

export interface FrameError {
  kind: string
  message: string
  source: string
  line: number
}

interface Rect {
  x: number
  y: number
  w: number
  h: number
}

/** The most errors kept from one frame, and the longest message. The frame is a page. */
export const MAX_FRAME_ERRORS = 50
const MAX_MESSAGE = 500

const str = (v: unknown, max: number): string => (typeof v === 'string' ? v.slice(0, max) : '')
const num = (v: unknown): number => (typeof v === 'number' && Number.isFinite(v) ? Math.round(v) : 0)

/**
 * Accepts a message only from one of the pane's own frames, and only in a
 * shape it expects.
 *
 * The check is the window, never the origin. A sandboxed frame's origin is the
 * string "null", which every sandboxed frame on the internet shares; the thing
 * that says this message came from the preview is that `event.source` is the
 * preview's `contentWindow`. The payload is then rebuilt field by field, so a
 * frame that posts an object with a getter, a prototype or a megabyte string
 * gets none of them through.
 */
export function readFrameMessage(
  source: unknown,
  data: unknown,
  frames: readonly (Window | null | undefined)[],
): FrameMessage | null {
  if (source === null || source === undefined) return null
  if (!frames.some((w) => w !== null && w !== undefined && w === source)) return null
  if (typeof data !== 'object' || data === null) return null
  const d = data as Record<string, unknown>
  switch (d.type) {
    case 'vp.errors': {
      const list = Array.isArray(d.errors) ? d.errors.slice(0, MAX_FRAME_ERRORS) : []
      return {
        type: 'vp.errors',
        errors: list.map((e) => {
          const r = (typeof e === 'object' && e !== null ? e : {}) as Record<string, unknown>
          return {
            kind: str(r.kind, 20),
            message: str(r.message, MAX_MESSAGE),
            source: str(r.source, 200),
            line: num(r.line),
          }
        }),
      }
    }
    case 'vp.picked': {
      const rect = (typeof d.rect === 'object' && d.rect !== null ? d.rect : {}) as Record<string, unknown>
      const vp = (typeof d.viewport === 'object' && d.viewport !== null ? d.viewport : {}) as Record<
        string,
        unknown
      >
      return {
        type: 'vp.picked',
        selector: str(d.selector, 200),
        text: str(d.text, 200),
        rect: { x: num(rect.x), y: num(rect.y), w: num(rect.w), h: num(rect.h) },
        viewport: { w: num(vp.w), h: num(vp.h) },
      }
    }
    case 'vp.pick':
      return { type: 'vp.pick', on: d.on === true }
    case 'vp.status':
      return { type: 'vp.status', status: str(d.status, 20) }
    case 'vp.ready':
      return { type: 'vp.ready' }
  }
  return null
}

/**
 * Everything that must not reach a terminal from a picked element's text.
 *
 * C0 and C1 controls, DEL, and the Unicode controls that reorder or hide text.
 * The first set is the one that bites: this text is pasted into a PTY, a line
 * break there is Enter, and in a shell Enter runs whatever came before it —
 * and the text can be a session title, which is somebody else's words. ESC is
 * in it too, which is what `ESC[201~` would need to end a bracketed paste
 * early.
 */
// eslint-disable-next-line no-control-regex
const UNSAFE = /[\u0000-\u001f\u007f-\u009f\u200b-\u200f\u202a-\u202e\u2060-\u206f\ufeff]/g

/** One line of text, safe to paste, at most `max` characters. */
export function terminalSafe(s: string, max: number): string {
  const flat = s.replace(UNSAFE, ' ').replace(/\s+/g, ' ').trim()
  const chars = [...flat]
  return chars.length > max ? `${chars.slice(0, max - 1).join('')}…` : flat
}

/**
 * The line pasted into the agent's prompt when an element is picked.
 *
 * Ends in a space and never in a newline: the person types what to do about
 * it and presses Enter themselves. A pick is pointing, not asking.
 */
export function pickLine(msg: Extract<FrameMessage, { type: 'vp.picked' }>, file = 'index.html'): string {
  const selector = terminalSafe(msg.selector, 120) || 'element'
  const text = terminalSafe(msg.text, 60)
  const where = `${msg.rect.x},${msg.rect.y} ${msg.rect.w}×${msg.rect.h}`
  const screen = msg.viewport.w > 0 ? ` · ${msg.viewport.w}×${msg.viewport.h}` : ''
  return `[${file}${screen}] ${selector}${text ? ` “${text}”` : ''} (${where}) `
}

/**
 * When the frame should reload, given the fingerprint it last loaded, the
 * one seen on the previous poll and the one seen now.
 *
 * Only once two polls in a row agree on something new. An agent writes a file
 * in pieces, and reloading on the first write draws a half-written page that
 * is fixed a moment later — which reads as the page being broken.
 */
export function shouldReload(loaded: string, previous: string, current: string): boolean {
  return current !== '' && current !== loaded && current === previous
}

/** Errors worth telling the agent about, deduplicated, newest kept. */
export function mergeErrors(existing: FrameError[], incoming: FrameError[]): FrameError[] {
  const seen = new Set<string>()
  const out: FrameError[] = []
  for (const e of [...incoming, ...existing]) {
    const key = `${e.kind}|${e.source}|${e.line}|${e.message}`
    if (seen.has(key)) continue
    seen.add(key)
    out.push(e)
    if (out.length >= MAX_FRAME_ERRORS) break
  }
  return out
}
