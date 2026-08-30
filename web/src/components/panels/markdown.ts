/**
 * Markdown, parsed into a tree. Nothing here produces HTML.
 *
 * That is the security design and not an implementation detail. The panel's
 * own origin holds a session cookie that is a writable terminal, so a document
 * from somebody's repository getting script access to it is not "an XSS", it
 * is a shell -- the same reasoning written at length above
 * internal/httpapi/preview_render.go, which is why a project's *own* HTML is
 * only ever served into a sandboxed iframe on a separate route.
 *
 * A markdown renderer is the obvious way to reintroduce that problem: parse to
 * an HTML string, sanitise it, hand it to `dangerouslySetInnerHTML`, and now
 * the panel's safety rests on a sanitiser's blocklist keeping up with browsers.
 * So this parses to a tree of plain data and the component renders React
 * elements from it. There is no HTML string at any point, no
 * `dangerouslySetInnerHTML`, and nothing to sanitise -- an injection would have
 * to be a bug in React itself.
 *
 * The cost, stated: raw HTML in a markdown file is shown as text rather than
 * rendered, and this is a subset of CommonMark rather than the whole of it.
 * Both are the right trade for a file preview. Somebody who needs the document
 * exactly is one click from the source.
 */

export type Inline =
  | { t: 'text'; v: string }
  | { t: 'code'; v: string }
  | { t: 'strong'; kids: Inline[] }
  | { t: 'em'; kids: Inline[] }
  | { t: 'del'; kids: Inline[] }
  | { t: 'link'; href: string; kids: Inline[] }

export type Block =
  | { t: 'h'; level: number; kids: Inline[] }
  | { t: 'p'; kids: Inline[] }
  | { t: 'pre'; lang: string; code: string }
  | { t: 'quote'; kids: Block[] }
  | { t: 'list'; ordered: boolean; items: Block[][] }
  | { t: 'rule' }
  | { t: 'table'; head: Inline[][]; rows: Inline[][][] }

/**
 * Which link schemes are followed.
 *
 * An allow list, because the interesting ones are the schemes nobody thinks of:
 * `javascript:` is the obvious one and `data:` is the one that gets forgotten.
 * Anything else renders as text with its target visible, which is more useful
 * than a dead link and is the honest thing to show for `file:///etc/passwd`.
 */
export function safeHref(raw: string): string | null {
  const href = raw.trim()
  if (href === '') return null
  // Relative links are fine and common in a repository's markdown; they are
  // also the only ones with no scheme at all.
  if (/^[a-z][a-z0-9+.-]*:/i.test(href)) {
    return /^(https?|mailto):/i.test(href) ? href : null
  }
  // Not a protocol-relative URL either: `//evil.example` inherits the page's
  // scheme and is a link off the panel that does not look like one.
  if (href.startsWith('//')) return null
  return href
}

/** Inline spans: code, bold, italic, strikethrough, links. */
export function parseInline(src: string): Inline[] {
  const out: Inline[] = []
  let text = ''
  const flush = () => {
    if (text !== '') {
      out.push({ t: 'text', v: text })
      text = ''
    }
  }
  let i = 0
  while (i < src.length) {
    const rest = src.slice(i)

    // Code first, and it wins: backticks suppress every other marker inside
    // them, which is most of what people use them for in a README.
    const code = /^(`+)([\s\S]*?)\1/.exec(rest)
    if (code) {
      flush()
      out.push({ t: 'code', v: code[2].trim() })
      i += code[0].length
      continue
    }

    // The target allows one level of balanced parentheses, because URLs have
    // them -- a Wikipedia link, and `javascript:alert(1)`, which is the one
    // that matters: stopping at the first `)` captured `javascript:alert(1`,
    // refused *that*, and left a stray `)` behind. The refusal still worked;
    // what it printed was wrong, and a parser that mis-reads the target is one
    // step from a parser that mis-reads which scheme it is.
    const link = /^\[([^\]]*)\]\(((?:[^()\s]|\([^()\s]*\))*)(?:\s+"[^"]*")?\)/.exec(rest)
    if (link) {
      const href = safeHref(link[2])
      flush()
      if (href === null) {
        // Shown as what it says, with the target, rather than silently as
        // plain text: a link that was refused is worth seeing.
        out.push({ t: 'text', v: `${link[1]} (${link[2]})` })
      } else {
        out.push({ t: 'link', href, kids: parseInline(link[1]) })
      }
      i += link[0].length
      continue
    }

    const strong = /^(\*\*|__)(?=\S)([\s\S]*?\S)\1/.exec(rest)
    if (strong) {
      flush()
      out.push({ t: 'strong', kids: parseInline(strong[2]) })
      i += strong[0].length
      continue
    }

    const del = /^~~(?=\S)([\s\S]*?\S)~~/.exec(rest)
    if (del) {
      flush()
      out.push({ t: 'del', kids: parseInline(del[1]) })
      i += del[0].length
      continue
    }

    // After strong, or `**a**` is read as an empty emphasis followed by one.
    const em = /^(\*|_)(?=\S)([\s\S]*?\S)\1/.exec(rest)
    if (em) {
      flush()
      out.push({ t: 'em', kids: parseInline(em[2]) })
      i += em[0].length
      continue
    }

    text += src[i]
    i++
  }
  flush()
  return out
}

/** A markdown document as blocks. */
export function parseMarkdown(src: string): Block[] {
  const lines = src.replace(/\r\n?/g, '\n').split('\n')
  const out: Block[] = []
  let i = 0

  const paragraph = (buf: string[]) => {
    const text = buf.join('\n').trim()
    if (text !== '') out.push({ t: 'p', kids: parseInline(text) })
  }

  let para: string[] = []
  const endPara = () => {
    paragraph(para)
    para = []
  }

  while (i < lines.length) {
    const line = lines[i]

    // A fence, and everything to the closing one is code -- including lines
    // that look like headings, which is the whole point of a fence.
    const fence = /^\s*(```+|~~~+)\s*([\w+#.-]*)\s*$/.exec(line)
    if (fence) {
      endPara()
      const marker = fence[1][0]
      const body: string[] = []
      i++
      while (i < lines.length && !new RegExp(`^\\s*${marker === '`' ? '```' : '~~~'}+\\s*$`).test(lines[i])) {
        body.push(lines[i])
        i++
      }
      i++ // the closing fence, or the end of the file
      // Trailing blank lines dropped: a file ending in a newline leaves an
      // empty last element from the split, and an unterminated fence swallowed
      // it as a line of code.
      while (body.length > 0 && body[body.length - 1].trim() === '') body.pop()
      out.push({ t: 'pre', lang: fence[2].toLowerCase(), code: body.join('\n') })
      continue
    }

    const heading = /^(#{1,6})\s+(.*?)\s*#*\s*$/.exec(line)
    if (heading) {
      endPara()
      out.push({ t: 'h', level: heading[1].length, kids: parseInline(heading[2]) })
      i++
      continue
    }

    if (/^\s*(\*\s*){3,}$|^\s*(-\s*){3,}$|^\s*(_\s*){3,}$/.test(line)) {
      endPara()
      out.push({ t: 'rule' })
      i++
      continue
    }

    if (/^\s*>/.test(line)) {
      endPara()
      const body: string[] = []
      while (i < lines.length && /^\s*>/.test(lines[i])) {
        body.push(lines[i].replace(/^\s*>\s?/, ''))
        i++
      }
      out.push({ t: 'quote', kids: parseMarkdown(body.join('\n')) })
      continue
    }

    const bullet = /^(\s*)([-*+]|\d+[.)])\s+(.*)$/.exec(line)
    if (bullet) {
      endPara()
      const ordered = /\d/.test(bullet[2])
      const items: Block[][] = []
      while (i < lines.length) {
        const m = /^(\s*)([-*+]|\d+[.)])\s+(.*)$/.exec(lines[i])
        if (!m || /\d/.test(m[2]) !== ordered) break
        // The item's own continuation lines, so a wrapped bullet stays one
        // item rather than becoming a paragraph after the list.
        const body = [m[3]]
        i++
        while (i < lines.length && lines[i].trim() !== '' &&
               !/^(\s*)([-*+]|\d+[.)])\s+/.test(lines[i])) {
          body.push(lines[i].trim())
          i++
        }
        items.push(parseMarkdown(body.join('\n')))
        // One blank line inside a list does not end it; two do.
        if (i < lines.length && lines[i].trim() === '' &&
            /^(\s*)([-*+]|\d+[.)])\s+/.test(lines[i + 1] ?? '')) {
          i++
        }
      }
      out.push({ t: 'list', ordered, items })
      continue
    }

    // A pipe table, which READMEs are full of and which reads as noise
    // unrendered.
    if (/^\s*\|.*\|\s*$/.test(line) && /^\s*\|[\s:|-]+\|\s*$/.test(lines[i + 1] ?? '')) {
      endPara()
      const cells = (l: string) =>
        l.trim().replace(/^\||\|$/g, '').split('|').map((c) => parseInline(c.trim()))
      const head = cells(line)
      i += 2
      const rows: Inline[][][] = []
      while (i < lines.length && /^\s*\|.*\|\s*$/.test(lines[i])) {
        rows.push(cells(lines[i]))
        i++
      }
      out.push({ t: 'table', head, rows })
      continue
    }

    if (line.trim() === '') {
      endPara()
      i++
      continue
    }

    para.push(line)
    i++
  }
  endPara()
  return out
}

/** Whether a filename is markdown, by the extensions people actually use. */
export function isMarkdown(name: string): boolean {
  return /\.(md|markdown|mdown|mkd)$/i.test(name)
}
