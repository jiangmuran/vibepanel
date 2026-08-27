/**
 * The directory picker's decisions, separated from its pixels.
 *
 * Everything here is a question with a right answer independent of how the
 * dialog looks: what the text in the box means, which rows survive it, which
 * of two things Enter does, where the crumbs go. The component that used to
 * hold all of it could only be checked by opening a browser and looking, which
 * is how the answers drifted -- `Home` moved a caret in one mode and a
 * selection in the other, and nothing said which.
 *
 * The root is passed in rather than assumed. The server roots the listing at
 * the home directory, so `~` is a real place with a real absolute path, and
 * every "is this inside" question below is asked against that path rather than
 * against the tilde, which is a rendering of it.
 */

/** The `~` the crumb bar starts with. A symbol, so it is not translated. */
export const ROOT_CRUMB = '~'

export interface Crumb {
  /** What to draw: `~` for the root, then one directory name per level. */
  label: string
  /** Where clicking it goes -- a path relative to the root, `''` being it. */
  path: string
}

/**
 * The path as a row of places rather than as a string.
 *
 * A breadcrumb is a control and a path is a value, and the picker needs both:
 * you click the third segment to go back two levels, and you select the whole
 * thing to paste it into a terminal. They are the same fact, so they are
 * derived from the same field rather than kept as two.
 */
export function crumbs(path: string): Crumb[] {
  const out: Crumb[] = [{ label: ROOT_CRUMB, path: '' }]
  let walked = ''
  for (const part of path.split('/')) {
    if (!part) continue
    walked = walked ? `${walked}/${part}` : part
    out.push({ label: part, path: walked })
  }
  return out
}

/** The root with any trailing slash gone, so joining never doubles one. */
function bareRoot(root: string): string {
  return root.length > 1 && root.endsWith('/') ? root.replace(/\/+$/, '') : root
}

/** The absolute path of a listing, which is what the server will be handed. */
export function absOf(root: string, path: string): string {
  const base = bareRoot(root)
  if (!path) return base
  return base === '/' ? `/${path}` : `${base}/${path}`
}

/**
 * `/a/./b/../c//d` -> `/a/c/d`.
 *
 * Textual, and that is the point: this runs before anything is sent, to decide
 * whether the typed path is somewhere the picker can show you. The server
 * resolves symlinks and refuses what leaves the root; this only has to agree
 * with it about what the string means.
 */
function normalize(abs: string): string {
  const parts: string[] = []
  for (const part of abs.split('/')) {
    if (!part || part === '.') continue
    if (part === '..') {
      parts.pop()
      continue
    }
    parts.push(part)
  }
  return `/${parts.join('/')}`
}

/**
 * Where an absolute path sits relative to the root, or null for outside it.
 *
 * `''` means the root itself, which is a real answer rather than a missing
 * one -- hence null, not an empty string, for "outside".
 */
export function insideRoot(abs: string, root: string): string | null {
  const base = bareRoot(root)
  if (base === '/') return abs === '/' ? '' : abs.slice(1)
  if (abs === base) return ''
  return abs.startsWith(`${base}/`) ? abs.slice(base.length + 1) : null
}

/**
 * What the text in the box is: a filter over what is on screen, or a place.
 *
 * One box, because the alternative is two boxes that both take text and a
 * person who has to decide which one their sentence belongs in before they
 * have finished thinking it. A leading `/` or `~` is not a name anybody
 * filters for and is exactly how a path starts, so the text says which of the
 * two it is -- and the dialog says back what Enter will do with it, because a
 * mode nobody can see is a mode that surprises somebody.
 */
export type Typed =
  | { kind: 'filter'; query: string }
  | {
      kind: 'path'
      /** The absolute path meant, with `~` expanded and `..` collapsed. */
      abs: string
      /** Its place under the root, or null when it is outside and unlistable. */
      inside: string | null
    }

export function classifyInput(raw: string, root: string): Typed {
  const text = raw.trim()
  if (!text) return { kind: 'filter', query: '' }
  if (text.startsWith('~')) {
    // `~` and `~/x` are the root. `~someone` is another account's home to a
    // shell, and this picker has no way to find out where that is -- it is
    // still a path, and refusing it here would be guessing on the server's
    // behalf, so it goes out as typed and comes back refused with a reason.
    // Nothing to expand it to yet: the first listing is what tells the browser
    // where home is. Expanding against an empty root would turn `~/projects`
    // into `/projects` -- a real path, somewhere else entirely, offered with no
    // sign that a substitution had happened.
    if ((text === '~' || text.startsWith('~/')) && root !== '') {
      const abs = normalize(`${bareRoot(root)}/${text.slice(1)}`)
      return { kind: 'path', abs, inside: insideRoot(abs, root) }
    }
    return { kind: 'path', abs: text, inside: null }
  }
  if (text.startsWith('/')) {
    const abs = normalize(text)
    return { kind: 'path', abs, inside: insideRoot(abs, root) }
  }
  return { kind: 'filter', query: text }
}

/** Where the query matched, so a row can show why it is still on screen. */
export function matchSpan(name: string, query: string): [number, number] | null {
  const q = query.trim()
  if (!q) return null
  const at = name.toLowerCase().indexOf(q.toLowerCase())
  return at < 0 ? null : [at, at + q.length]
}

/**
 * The rows the query leaves, best first.
 *
 * Ordered by where the match starts, which puts the directory whose *name*
 * begins with what was typed above one that merely contains it -- typing `web`
 * where `web` and `my-web-archive` both live should not make anybody read
 * both. Array.prototype.sort is stable, so everything matching at the same
 * offset keeps the server's ordering, which is already the one every file
 * browser uses.
 */
export function filterEntries<T extends { name: string }>(
  entries: readonly T[],
  query: string,
): T[] {
  const q = query.trim()
  if (!q) return [...entries]
  const hits: { entry: T; at: number }[] = []
  for (const entry of entries) {
    const span = matchSpan(entry.name, q)
    if (span) hits.push({ entry, at: span[0] })
  }
  return hits.sort((a, b) => a.at - b.at).map((h) => h.entry)
}

export interface KeyState {
  key: string
  kind: Typed['kind']
  /** Path mode: the typed path is under the root, so it can be listed. */
  navigable: boolean
  /** There is text in the box -- which is what decides who owns the caret. */
  hasText: boolean
  /** Rows currently listed, after filtering. */
  count: number
  active: number
  hasParent: boolean
}

/**
 * What a key means, given what the box holds.
 *
 * `text` is the answer that matters most: it means "this key belongs to the
 * input, do not touch it". The picker has one text box and one list sharing
 * one focus, and every arrangement that does not answer this question ends up
 * with the two of them fighting -- Backspace deleting a character *and* going
 * up a level, Home jumping to the first row while the caret sat in the middle
 * of a path somebody was editing.
 *
 * The rule, in one sentence: the box owns the keys that edit text as soon as
 * there is text to edit. When it is empty there is nothing to edit and they
 * belong to the list, which is why Backspace on an empty box goes up a level
 * the way it does in a browser.
 *
 * Home and End are the deliberate exception in filter mode. A filter is a
 * fragment of a name -- five characters, six -- and Home there moves a caret
 * across three of them, while in a directory of two hundred entries it moves
 * you two hundred rows. In path mode the text is long and worth editing, so
 * they go back to being text keys.
 */
export type Act =
  | { do: 'move'; to: number }
  /** Descend into the active row. */
  | { do: 'open' }
  | { do: 'up' }
  /** Go to the typed path, which is inside the root. */
  | { do: 'go' }
  /** Take a path -- the typed one, or the one being listed. */
  | { do: 'use' }
  /** Start a directory named after what was typed and matched nothing. */
  | { do: 'createNamed' }
  | { do: 'clear' }
  | { do: 'close' }
  /** Not ours: the input gets it, with its default behaviour intact. */
  | { do: 'text' }

function clamp(to: number, count: number): Act {
  if (count <= 0) return { do: 'move', to: -1 }
  return { do: 'move', to: Math.max(0, Math.min(count - 1, to)) }
}

export function resolveKey(s: KeyState): Act {
  if (s.kind === 'path') {
    if (s.key === 'Enter') return s.navigable ? { do: 'go' } : { do: 'use' }
    if (s.key === 'Escape') return { do: 'clear' }
    return { do: 'text' }
  }
  switch (s.key) {
    case 'ArrowDown':
      return clamp(s.active + 1, s.count)
    case 'ArrowUp':
      return clamp(s.active - 1, s.count)
    case 'Home':
      return clamp(0, s.count)
    case 'End':
      return clamp(s.count - 1, s.count)
    case 'Enter':
      if (s.count > 0 && s.active >= 0) return { do: 'open' }
      // Nothing to open. With text in the box, that text is a name nothing
      // matched, and offering to make it is what somebody who typed it is
      // after; with an empty box in an empty directory the only thing left is
      // to take the directory itself.
      return s.hasText ? { do: 'createNamed' } : { do: 'use' }
    case 'Escape':
      return s.hasText ? { do: 'clear' } : { do: 'close' }
    case 'Backspace':
    case 'ArrowLeft':
      if (s.hasText) return { do: 'text' }
      return s.hasParent ? { do: 'up' } : { do: 'text' }
    case 'ArrowRight':
      if (s.hasText) return { do: 'text' }
      return s.count > 0 && s.active >= 0 ? { do: 'open' } : { do: 'text' }
    default:
      return { do: 'text' }
  }
}
