/**
 * Where a document's links and images may point, decided before anything is
 * drawn. Nothing here touches the DOM.
 */

/**
 * Which link targets are followed.
 *
 * An allow list, because the interesting schemes are the ones nobody thinks
 * of: `javascript:` is the obvious one and `data:` the one that gets
 * forgotten, and this origin holds a session cookie that is a writable
 * terminal. Anything else is shown as text with its target, which is more
 * useful than a dead link and honest about `file:///etc/passwd`.
 *
 * The target is read the way the browser's URL parser will read it: tabs and
 * newlines anywhere in it are deleted and leading control characters and
 * spaces are dropped. The rule this replaced checked the scheme first, so
 * `java\tscript:alert(1)` had no scheme by its regex, passed as a relative
 * link, and was `javascript:` by the time the browser followed it.
 */
export function safeHref(raw: string): string | null {
  // eslint-disable-next-line no-control-regex
  const href = raw.replace(/[\t\n\r]/g, '').replace(/^[\u0000- ]+|[\u0000- ]+$/g, '')
  if (href === '') return null
  // Relative links are fine and common in a repository's markdown; they are
  // also the only ones with no scheme at all.
  if (/^[a-z][a-z0-9+.-]*:/i.test(href)) {
    return /^(https?|mailto):/i.test(href) ? href : null
  }
  // Nor protocol-relative: `//evil.example` inherits the page's scheme and is
  // a link off the panel that does not look like one. A backslash is a slash
  // to the URL parser.
  if (/^[/\\]{2}/.test(href)) return null
  return href
}

/** A link's place: in this document, off the panel, or another file. */
export type LinkKind = 'anchor' | 'external' | 'relative'

export function linkKind(href: string): LinkKind {
  if (href.startsWith('#')) return 'anchor'
  if (/^(https?|mailto):/i.test(href)) return 'external'
  return 'relative'
}

/**
 * The project path an image in a document refers to, or null when it is not
 * one: a remote address, a scheme, an absolute path, or a path that climbs
 * out of the project.
 *
 * Relative to the document's own directory, the way a repository's README
 * means it: `img/logo.png` in `docs/guide.md` is `docs/img/logo.png`. A query
 * or fragment is not part of a file's name.
 */
export function projectImage(docPath: string, src: string): string | null {
  // eslint-disable-next-line no-control-regex
  const s = src.replace(/[\t\n\r]/g, '').replace(/^[\u0000- ]+/, '').split(/[?#]/)[0]
  if (s === '' || /^[a-z][a-z0-9+.-]*:/i.test(s) || /^[/\\]/.test(s)) return null
  let decoded: string
  try {
    decoded = decodeURIComponent(s)
  } catch {
    return null
  }
  const parts = docPath.split('/').filter(Boolean)
  parts.pop()
  for (const seg of decoded.split(/[/\\]/)) {
    if (seg === '' || seg === '.') continue
    if (seg === '..') {
      if (parts.length === 0) return null
      parts.pop()
      continue
    }
    parts.push(seg)
  }
  return parts.length ? parts.join('/') : null
}

/**
 * A heading's id, GitHub's way, so a link written for a README on GitHub
 * lands on the same heading here: lower case, punctuation dropped, spaces to
 * hyphens, and a repeat numbered `-1`, `-2` in order.
 */
export function slugger(): (text: string) => string {
  const seen = new Map<string, number>()
  return (text) => {
    const base = text
      .toLowerCase()
      .trim()
      .replace(/[^\p{L}\p{N}\p{M}\s_-]/gu, '')
      .replace(/\s/g, '-')
    const n = seen.get(base)
    seen.set(base, (n ?? -1) + 1)
    return n === undefined ? base : `${base}-${n + 1}`
  }
}

/** Whether a file is a document the viewer draws as one. */
export function isMarkdown(name: string): boolean {
  return /\.(md|markdown|mdown|mkd)$/i.test(name)
}
