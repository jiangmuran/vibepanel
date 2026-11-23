import type { IBufferRange, ILink, Terminal as Xterm } from '@xterm/xterm'

/**
 * File and directory paths printed in a terminal, turned into clickable links.
 *
 * Agents emit paths in four shapes: OSC 8 hyperlinks (`\e]8;;file:///…\a`),
 * literal Markdown links (`[panels.go](/abs/path/panels.go#L482)`), quoted or
 * backticked paths that may carry spaces or CJK characters, and bare paths
 * inline in prose — including Chinese sentences with no spaces on either side
 * (`在internal/tmux/tmux.go中`, `记忆~/.claude/…/MEMORY.md。`).
 */

export interface TerminalPathHit {
  start: number
  end: number
  target: string
  /** Unjoined single-row candidate when a long path wrapped at the margin. */
  fallback?: string
}

const WEB_URL_RE = /\bhttps?:\/\/[^\s"'<>`]+/gi
const MD_LINK_RE = /\[([^[\]\r\n]{1,160})\]\(([^()\s\r\n]{1,1024}(?:\([^()\s\r\n]{1,200}\)[^()\s\r\n]{0,400})?)\)/g
const URI_LINK_RE = /\b(?:file|vscode|vscode-insiders|cursor):\/\/[^\s"'<>`，。；：！？、【】《》「」『』（）]+/gi
const DELIMITED_RE = /`([^`\r\n]{2,512})`|'([^'\r\n]{2,512})'|"([^"\r\n]{2,512})"/g

// Unquoted path tokens: ASCII path segments so Chinese prose around an inline
// path (`在desktop_client/data/units.json与…中`) never gets swallowed into the
// path. CJK filenames work via OSC 8, file:// URIs, Markdown links, quotes or
// backticks.
const BARE_SEG = String.raw`(?:[A-Za-z0-9._+@%=~[\]-]+|\([A-Za-z0-9._+@%=~[\]-]+\))+`
const LINE_ANCHOR_SRC = String.raw`(?::\d+(?::\d+)?|#L?\d+(?:C\d+)?(?:-L?\d+(?:C\d+)?)?|\(\d+(?:,\d+)?\))`
const BARE_CANDIDATE_RE = new RegExp(
  String.raw`(?:~\/|\.\.?\/|\/)${BARE_SEG}(?:\/${BARE_SEG})*\/?${LINE_ANCHOR_SRC}?|[A-Za-z0-9._-]+(?:\/${BARE_SEG})+\/?${LINE_ANCHOR_SRC}?|[A-Za-z_][A-Za-z0-9._-]*\.[A-Za-z][A-Za-z0-9]{0,7}${LINE_ANCHOR_SRC}`,
  'g',
)

const KNOWN_SINGLE_FILE_EXTS = new Set([
  'md', 'markdown', 'txt', 'log', 'json', 'jsonl', 'yaml', 'yml', 'toml', 'ini', 'conf', 'cfg',
  'env', 'lock', 'sh', 'bash', 'zsh', 'fish', 'py', 'ipynb', 'go', 'mod', 'sum', 'rs', 'ts',
  'tsx', 'js', 'jsx', 'mjs', 'cjs', 'css', 'scss', 'less', 'html', 'htm', 'xhtml', 'svg', 'xml',
  'sql', 'csv', 'tsv', 'pdf', 'png', 'jpg', 'jpeg', 'gif', 'webp', 'avif', 'ico', 'bmp', 'mp4',
  'webm', 'mov', 'mp3', 'wav', 'ogg', 'flac', 'zip', 'tar', 'gz', 'tgz', 'db', 'sqlite', 'sqlite3',
  'c', 'cc', 'cpp', 'cxx', 'h', 'hpp', 'java', 'kt', 'swift', 'rb', 'php', 'lua', 'gd', 'tscn',
  'tres', 'vue', 'svelte', 'astro', 'pbtxt', 'proto', 'graphql', 'patch', 'diff', 'tex', 'bib',
])

const KNOWN_EXTENSIONLESS_NAMES = new Set([
  'Makefile', 'Dockerfile', 'Containerfile', 'LICENSE', 'NOTICE', 'README',
  'CHANGELOG', 'AGENTS', 'CLAUDE', ' Caddyfile', 'Caddyfile', 'Justfile', 'Procfile',
])

const IGNORED_ABS_PATHS = new Set([
  '/', '/dev/null', '/dev/zero', '/dev/stdin', '/dev/stdout', '/dev/stderr', '/dev/random', '/dev/urandom',
])

const MIME_PREFIXES = new Set([
  'text', 'image', 'audio', 'video', 'application', 'multipart', 'font', 'message', 'model',
])

const COMMON_MIME_SECONDS = new Set([
  'plain', 'html', 'css', 'csv', 'xml', 'javascript', 'json', 'pdf', 'zip', 'gzip',
  'octet-stream', 'xhtml+xml', 'svg+xml', 'x-www-form-urlencoded', 'form-data', 'png',
  'jpeg', 'gif', 'webp', 'mp4', 'mpeg', 'ogg', 'webm', 'wav',
])

/** Classifies an OSC 8 hyperlink URI into a panel path action or external web URL. */
export function classifyOscUri(
  raw: string,
): { kind: 'path'; target: string } | { kind: 'external'; url: string } | null {
  const uri = raw.trim()
  if (!uri) return null
  if (/^(?:file|vscode|vscode-insiders|cursor):\/\//i.test(uri)) {
    return { kind: 'path', target: uri }
  }
  if (/^(?:https?|mailto):/i.test(uri)) {
    return { kind: 'external', url: uri }
  }
  if (uri.startsWith('/') && !uri.startsWith('//')) {
    return IGNORED_ABS_PATHS.has(uri) ? null : { kind: 'path', target: uri }
  }
  if (uri.startsWith('~/') || uri.startsWith('./') || uri.startsWith('../')) {
    return { kind: 'path', target: uri }
  }
  return null
}

function stripLineAnchor(s: string): string {
  return s
    .replace(/#L?\d+(?:C\d+)?(?:-L?\d+(?:C\d+)?)?$/, '')
    .replace(/(?::\d+(?::\d+)?|\(\d+(?:,\d+)?\))$/, '')
}

function extOf(seg: string): string {
  const dot = seg.lastIndexOf('.')
  return dot > 0 && dot < seg.length - 1 ? seg.slice(dot + 1).toLowerCase() : ''
}

function isMimeLike(core: string): boolean {
  const parts = core.split('/')
  if (parts.length !== 2) return false
  const [a, b] = parts
  return MIME_PREFIXES.has(a.toLowerCase()) && COMMON_MIME_SECONDS.has(b.toLowerCase())
}

function looksLikePathTarget(raw: string): boolean {
  const s = raw.trim()
  if (!s || s.startsWith('#') || /^[a-z][a-z0-9+.-]*:/i.test(s)) {
    return /^(?:file|vscode|vscode-insiders|cursor):\/\//i.test(s)
  }
  if (s.startsWith('//') || /[;|$`<>]/.test(s)) return false
  const core = stripLineAnchor(s)
  if (!core || IGNORED_ABS_PATHS.has(core)) return false
  if (core.startsWith('/') || core.startsWith('~/') || core.startsWith('./') || core.startsWith('../')) {
    return core.length > 1
  }
  if (core.includes('/')) {
    return !isMimeLike(core)
  }
  const ext = extOf(core)
  return KNOWN_SINGLE_FILE_EXTS.has(ext) || KNOWN_EXTENSIONLESS_NAMES.has(core)
}

function looksLikeQuotedPath(raw: string): boolean {
  const s = raw.trim()
  if (s !== raw || !s) return false
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f;|$`<>]/.test(s)) return false
  if (/^(?:file|vscode|vscode-insiders|cursor):\/\//i.test(s)) return true
  if (/^[a-z][a-z0-9+.-]*:/i.test(s) || s.startsWith('//')) return false
  const core = stripLineAnchor(s)
  if (!core || IGNORED_ABS_PATHS.has(core)) return false
  if (core.startsWith('/') || core.startsWith('~/') || core.startsWith('./') || core.startsWith('../')) {
    // Allow spaces and CJK inside quoted/backticked paths (`"/root/my file.md"`),
    // but reject shell command strings like `"/bin/sh -c echo"` when flags follow.
    if (/\s-[A-Za-z-]/.test(core)) return false
    return core.length > 1 && (core !== '/' && core !== './' && core !== '../')
  }
  if (/\s/.test(core)) return false
  if (core.includes('/')) {
    return isLikelyRelativePath(core, s !== core)
  }
  const ext = extOf(core)
  return KNOWN_SINGLE_FILE_EXTS.has(ext) || KNOWN_EXTENSIONLESS_NAMES.has(core)
}

function isLikelyRelativePath(core: string, hasLineAnchor: boolean): boolean {
  if (isMimeLike(core)) return false
  const trailingSlash = core.endsWith('/')
  const trimmed = trailingSlash ? core.replace(/\/+$/, '') : core
  const parts = trimmed.split('/').filter(Boolean)
  if (parts.length < 2) return false
  if (parts.every((p) => /^\d+(?:\.\d+)*$/.test(p))) return false
  if (trailingSlash || hasLineAnchor || parts.length >= 3) return true
  const last = parts[parts.length - 1]
  const ext = extOf(last)
  if (ext && /^[a-z][a-z0-9]{0,9}$/.test(ext) && !/^\d+$/.test(ext)) return true
  return KNOWN_EXTENSIONLESS_NAMES.has(last)
}

function countChar(s: string, ch: string): number {
  let n = 0
  for (let i = 0; i < s.length; i++) if (s[i] === ch) n++
  return n
}

function trimUnbalancedTrailing(raw: string): string {
  let s = raw
  while (s.length > 1) {
    const last = s[s.length - 1]
    if (last === '.' || last === ',' || last === ';' || last === '!' || last === '?') {
      if (last === '.' && (s === './' || s === '../')) break
      s = s.slice(0, -1)
      continue
    }
    if (last === ':' && !/:\d+$/.test(s)) {
      s = s.slice(0, -1)
      continue
    }
    if (last === ')' && countChar(s, ')') > countChar(s, '(')) {
      s = s.slice(0, -1)
      continue
    }
    if (last === ']' && countChar(s, ']') > countChar(s, '[')) {
      s = s.slice(0, -1)
      continue
    }
    if (last === '}' || last === '\'' || last === '"') {
      s = s.slice(0, -1)
      continue
    }
    break
  }
  return s
}

function validBareBoundaryBefore(text: string, idx: number): boolean {
  if (idx <= 0) return true
  const prev = text[idx - 1]
  // ASCII letters/digits, `/`, `:`, `~`, `.`, `-`, `_` mean we are mid-token or
  // mid-URL (`https://…/path`, `and/or`, `v1.2/3`). CJK characters and
  // punctuation (`在internal/…`, `（~/.gemini/…`) are valid boundaries.
  return !/[A-Za-z0-9_:/~.-]/.test(prev)
}

function overlaps(spans: readonly { start: number; end: number }[], start: number, end: number): boolean {
  return spans.some((s) => start < s.end && end > s.start)
}

/** Finds every clickable file or folder reference in one logical terminal line. */
export function findTerminalPathHits(text: string): TerminalPathHit[] {
  if (!text) return []
  const hits: TerminalPathHit[] = []
  const blocked: { start: number; end: number }[] = []

  MD_LINK_RE.lastIndex = 0
  for (let m = MD_LINK_RE.exec(text); m !== null; m = MD_LINK_RE.exec(text)) {
    const target = m[2]
    const start = m.index
    const end = start + m[0].length
    if (looksLikePathTarget(target)) {
      hits.push({ start, end, target })
      blocked.push({ start, end })
    }
  }

  WEB_URL_RE.lastIndex = 0
  for (let m = WEB_URL_RE.exec(text); m !== null; m = WEB_URL_RE.exec(text)) {
    blocked.push({ start: m.index, end: m.index + m[0].length })
  }

  URI_LINK_RE.lastIndex = 0
  for (let m = URI_LINK_RE.exec(text); m !== null; m = URI_LINK_RE.exec(text)) {
    const start = m.index
    const cleaned = trimUnbalancedTrailing(m[0])
    const end = start + cleaned.length
    if (cleaned.length > 7 && !overlaps(blocked, start, end)) {
      hits.push({ start, end, target: cleaned })
      blocked.push({ start, end })
    }
  }

  DELIMITED_RE.lastIndex = 0
  for (let m = DELIMITED_RE.exec(text); m !== null; m = DELIMITED_RE.exec(text)) {
    const inner = m[1] ?? m[2] ?? m[3] ?? ''
    const start = m.index + 1
    const end = start + inner.length
    if (overlaps(blocked, start - 1, end + 1)) continue
    if (looksLikeQuotedPath(inner)) {
      hits.push({ start, end, target: inner })
      blocked.push({ start: m.index, end: m.index + m[0].length })
    }
  }

  BARE_CANDIDATE_RE.lastIndex = 0
  for (let m = BARE_CANDIDATE_RE.exec(text); m !== null; m = BARE_CANDIDATE_RE.exec(text)) {
    const start = m.index
    if (!validBareBoundaryBefore(text, start)) continue
    const cleaned = trimUnbalancedTrailing(m[0])
    const end = start + cleaned.length
    if (cleaned.length < 2 || overlaps(blocked, start, end)) continue
    const core = stripLineAnchor(cleaned)
    if (!core || IGNORED_ABS_PATHS.has(core)) continue

    let accept = false
    if (core.startsWith('~/')) {
      accept = core.length > 2
    } else if (core.startsWith('./') || core.startsWith('../')) {
      accept = core !== './' && core !== '../'
    } else if (core.startsWith('/')) {
      if (!core.startsWith('//')) {
        const segs = core.split('/').filter(Boolean)
        accept = segs.length >= 2 || (segs.length === 1 && extOf(segs[0]) !== '')
      }
    } else if (core.includes('/')) {
      accept = isLikelyRelativePath(core, core !== cleaned)
    } else {
      // Single-segment bare token only matched BARE_CANDIDATE_RE because it
      // carried an explicit `:line` or `#Lline` anchor.
      accept = KNOWN_SINGLE_FILE_EXTS.has(extOf(core))
    }

    if (accept) {
      hits.push({ start, end, target: cleaned })
      blocked.push({ start, end })
    }
  }

  hits.sort((a, b) => a.start - b.start)
  return hits
}

interface LogicalWindow {
  startRow: number
  endRow: number
  text: string
  rowOffsets: number[]
}

function logicalWindowAt(term: Xterm, row0: number): LogicalWindow | null {
  const buf = term.buffer.active
  const cur = buf.getLine(row0)
  if (!cur) return null

  let startRow = row0
  let guard = 0
  while (startRow > 0 && guard < 32) {
    const line = buf.getLine(startRow)
    if (!line?.isWrapped) break
    startRow--
    guard++
  }

  let endRow = row0
  guard = 0
  while (endRow + 1 < buf.length && guard < 32) {
    const next = buf.getLine(endRow + 1)
    if (!next?.isWrapped) break
    endRow++
    guard++
  }

  let text = ''
  const rowOffsets: number[] = []
  for (let r = startRow; r <= endRow; r++) {
    rowOffsets.push(text.length)
    const line = buf.getLine(r)
    const trimRight = r === endRow || !buf.getLine(r + 1)?.isWrapped
    text += line ? line.translateToString(trimRight) : ''
  }
  return { startRow, endRow, text, rowOffsets }
}

function mapSpanToBufferRange(
  term: Xterm,
  startRow: number,
  endRow: number,
  startIdx: number,
  endIdx: number,
): IBufferRange | null {
  if (endIdx <= startIdx) return null
  const buf = term.buffer.active
  const cell = buf.getNullCell()
  let seen = 0
  let startPos: { x: number; y: number } | null = null

  for (let r = startRow; r <= endRow; r++) {
    const line = buf.getLine(r)
    if (!line) return null
    const trimRight = r === endRow || !buf.getLine(r + 1)?.isWrapped
    const rowStr = line.translateToString(trimRight)
    const nextSeen = seen + rowStr.length
    if (nextSeen <= startIdx) {
      seen = nextSeen
      continue
    }

    let consumedInRow = 0
    for (let col = 0; col < line.length && seen + consumedInRow < nextSeen; col++) {
      line.getCell(col, cell)
      const w = cell.getWidth()
      if (w === 0) continue
      const chars = cell.getChars()
      const charLen = chars.length || 1
      const cellStart = seen + consumedInRow
      const cellEnd = cellStart + charLen

      if (startPos === null && startIdx < cellEnd) {
        startPos = { x: col + 1, y: r + 1 }
      }
      if (startPos !== null && endIdx <= cellEnd) {
        return {
          start: startPos,
          end: { x: Math.min(term.cols, col + w), y: r + 1 },
        }
      }
      consumedInRow += charLen
    }
    seen = nextSeen
  }
  return null
}

// Hard-wrapped TUI continuation: BubbleTea and Ratatui wrap long lines with
// spaces and CRLF rather than terminal soft-wrap (`isWrapped`). When a bare
// path reaches within 4 columns of the right margin and the next non-wrapped
// row begins with 0..6 spaces followed immediately by path characters, offer
// the joined path first with the single-row path as fallback so `os.Stat` on
// the server picks whichever actually exists on disk.
const HARD_WRAP_HEAD_RE = /^(\s{0,6})([A-Za-z0-9._+@%=~()[\]/-]+(?::\d+(?::\d+)?|#L?\d+(?:-L?\d+)?)?)/

function detectHardWrapContinuation(
  term: Xterm,
  win: LogicalWindow,
  hit: TerminalPathHit,
  range: IBufferRange,
): { joined: string; nextRow0: number; nextStartCol1: number; nextEndCol1: number } | null {
  if (range.end.y - 1 !== win.endRow) return null
  if (range.end.x < Math.max(20, term.cols - 4)) return null
  if (hit.end !== win.text.length) return null
  if (/^(?:file|vscode|vscode-insiders|cursor):\/\//i.test(hit.target) && hit.target.includes('#')) return null

  const nextRow0 = win.endRow + 1
  const nextLine = term.buffer.active.getLine(nextRow0)
  if (!nextLine || nextLine.isWrapped) return null
  const nextText = nextLine.translateToString(true)
  const m = HARD_WRAP_HEAD_RE.exec(nextText)
  if (!m) return null
  const indent = m[1]
  const tail = trimUnbalancedTrailing(m[2])
  if (!tail || tail.startsWith('//') || tail.startsWith('~/') || tail.startsWith('./') || tail.startsWith('../')) {
    return null
  }
  // If the second line starts with `/` and has multiple segments of its own
  // while the first line already ended with a file extension, it is likely a
  // separate path on the next line rather than a continuation.
  if (tail.startsWith('/') && /\.[A-Za-z0-9]{1,6}$/.test(hit.target)) return null

  const r2 = mapSpanToBufferRange(term, nextRow0, nextRow0, indent.length, indent.length + tail.length)
  if (!r2) return null
  return {
    joined: hit.target + tail,
    nextRow0,
    nextStartCol1: r2.start.x,
    nextEndCol1: r2.end.x,
  }
}

/** Builds xterm `ILink` entries for `bufferLineNumber` (1-based). */
export function provideTerminalFileLinks(
  term: Xterm,
  bufferLineNumber: number,
  onOpen: (target: string, fallback?: string) => void,
): ILink[] {
  const row0 = bufferLineNumber - 1
  const win = logicalWindowAt(term, row0)
  if (!win) return []

  const out: ILink[] = []
  for (const hit of findTerminalPathHits(win.text)) {
    const range = mapSpanToBufferRange(term, win.startRow, win.endRow, hit.start, hit.end)
    if (!range) continue
    if (bufferLineNumber >= range.start.y && bufferLineNumber <= range.end.y) {
      const cont = detectHardWrapContinuation(term, win, hit, range)
      const target = cont ? cont.joined : hit.target
      const fallback = cont ? hit.target : hit.fallback
      out.push({
        range,
        text: target,
        activate: (ev) => {
          if (ev.button !== 0 || term.hasSelection()) return
          onOpen(target, fallback)
        },
      })
    }
  }

  // Also check if `row0` is the second half of a hard-wrapped path from `row0 - 1`.
  const line = term.buffer.active.getLine(row0)
  if (line && !line.isWrapped && row0 > 0) {
    const prevWin = logicalWindowAt(term, row0 - 1)
    if (prevWin) {
      const prevHits = findTerminalPathHits(prevWin.text)
      const lastHit = prevHits[prevHits.length - 1]
      if (lastHit) {
        const prevRange = mapSpanToBufferRange(term, prevWin.startRow, prevWin.endRow, lastHit.start, lastHit.end)
        if (prevRange) {
          const cont = detectHardWrapContinuation(term, prevWin, lastHit, prevRange)
          if (cont && cont.nextRow0 === row0) {
            const tailRange: IBufferRange = {
              start: { x: cont.nextStartCol1, y: bufferLineNumber },
              end: { x: cont.nextEndCol1, y: bufferLineNumber },
            }
            // Prepend so the joined continuation wins over a truncated tail fragment.
            out.unshift({
              range: tailRange,
              text: cont.joined,
              activate: (ev) => {
                if (ev.button !== 0 || term.hasSelection()) return
                onOpen(cont.joined, lastHit.target)
              },
            })
          }
        }
      }
    }
  }

  return out
}

function posInRange(cols: number, x1: number, y1: number, r: IBufferRange): boolean {
  const p = y1 * cols + x1
  return r.start.y * cols + r.start.x <= p && p <= r.end.y * cols + r.end.x
}

/**
 * Resolves a link at a 0-based viewport cell `(col0, viewportRow0)` for scaled
 * passive viewers and touch taps, where xterm's unscaled hover-driven Linkifier
 * either misplaces columns or never received a hover before the tap.
 */
export function linkActionAtCell(
  term: Xterm,
  col0: number,
  viewportRow0: number,
  onOpen: (target: string, fallback?: string) => void,
  onExternal: (url: string) => void,
): (() => void) | null {
  const buf = term.buffer.active
  const bufferRow0 = buf.viewportY + viewportRow0
  const line = buf.getLine(bufferRow0)
  if (!line) return null
  const x1 = col0 + 1
  const y1 = bufferRow0 + 1

  // 1. Check OSC 8 hyperlink on the cell (via xterm's internal oscLinkService if present).
  const core = (
    term as unknown as {
      _core?: { _oscLinkService?: { getLinkData?: (id: number) => { uri?: string } | undefined } }
    }
  )._core
  const cell = buf.getNullCell()
  line.getCell(col0, cell)
  const extCell = cell as unknown as {
    hasExtendedAttrs?: () => number | boolean
    extended?: { urlId?: number }
  }
  if (extCell.hasExtendedAttrs?.() && extCell.extended?.urlId) {
    const uri = core?._oscLinkService?.getLinkData?.(extCell.extended.urlId)?.uri
    if (uri) {
      const classified = classifyOscUri(uri)
      if (classified?.kind === 'path') return () => onOpen(classified.target)
      if (classified?.kind === 'external') return () => onExternal(classified.url)
    }
  }

  // 2. Check file/folder links on the buffer line.
  const fileLinks = provideTerminalFileLinks(term, y1, onOpen)
  for (const link of fileLinks) {
    if (posInRange(term.cols, x1, y1, link.range)) {
      return () => {
        const fakeEv = { button: 0 } as MouseEvent
        link.activate(fakeEv, link.text)
      }
    }
  }

  return null
}
