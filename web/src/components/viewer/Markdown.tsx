import { useMemo, type ReactNode } from 'react'
import { fromMarkdown } from 'mdast-util-from-markdown'
import { gfmFromMarkdown } from 'mdast-util-gfm'
import { gfm } from 'micromark-extension-gfm'
import type { Nodes, Parents, Root, Table, TableCell } from 'mdast'
import { ImageOff } from 'lucide-react'

import { api, SERVER_FILES } from '../../protocol/api'
import { safeBody } from '../text'
import { t } from '../../i18n'
import { Code } from './CodeBlock'
import { linkKind, projectImage, safeHref, serverImage, slugger } from './mdlinks'

/**
 * A Markdown document as React elements, through mdast with GitHub's
 * extensions: tables, task lists, strikethrough, autolinks, footnotes, nested
 * lists. docs/file-preview.md, "Types", is the table this follows.
 *
 * The line this file holds is the one the hand-written parser it replaces
 * held, and the reason has not changed: the panel's origin holds a session
 * cookie that is a writable terminal, so a document out of somebody's
 * repository must not be able to put markup into the page. mdast is a tree of
 * plain data; this draws React elements from it and never makes an HTML string,
 * never calls `dangerouslySetInnerHTML`. Raw HTML in a document is a node like
 * any other and is drawn as its text. Every string a file contributes goes
 * through `safeBody`.
 *
 * Its own chunk (lazy, in FileViewer): the parser is most of a hundred
 * kilobytes, and only a document needs it.
 */
export default function Markdown({
  text,
  projectId,
  path,
}: {
  text: string
  projectId: string
  /** The document's path in the project: images are found relative to it. */
  path: string
}) {
  const tree = useMemo(
    () => fromMarkdown(text, { extensions: [gfm()], mdastExtensions: [gfmFromMarkdown()] }),
    [text],
  )
  const ctx = useMemo(() => context(tree, projectId, path), [tree, projectId, path])
  return (
    <div data-testid="preview-markdown" className="px-5 py-4 text-vp-base leading-relaxed text-ink">
      {kids(tree, ctx, 'r')}
      {ctx.footnotes.length > 0 && (
        <section data-testid="md-footnotes" className="mt-6 border-t border-hairline pt-3 text-vp-sm text-ink-2">
          <ol className="list-decimal pl-6">
            {ctx.footnotes.map((f, i) => (
              <li key={f.identifier} id={ctx.footId(f.identifier)}>
                {kids(f, ctx, `fn${i}`)}{' '}
                <a
                  href={`#${ctx.refId(f.identifier)}`}
                  onClick={ctx.jump}
                  aria-label={t('md.footnoteBack')}
                  className="text-accent no-underline"
                >
                  ↩
                </a>
              </li>
            ))}
          </ol>
        </section>
      )}
    </div>
  )
}

interface Ctx {
  projectId: string
  path: string
  /** Link reference definitions, `[x]: url`, by their normalised label. */
  defs: Map<string, { url: string; title?: string | null }>
  footnotes: Extract<Nodes, { type: 'footnoteDefinition' }>[]
  slugs: Map<Nodes, string>
  footId: (id: string) => string
  refId: (id: string) => string
  /** An in-document link: scroll within the viewer, leave the panel's URL. */
  jump: (e: React.MouseEvent<HTMLAnchorElement>) => void
}

/** Ids for this document, prefixed so a heading cannot collide with the panel's. */
const ID = 'md-'

function context(tree: Root, projectId: string, path: string): Ctx {
  const defs = new Map<string, { url: string; title?: string | null }>()
  const footnotes: Ctx['footnotes'] = []
  const slugs = new Map<Nodes, string>()
  const slug = slugger()
  const walk = (n: Nodes) => {
    if (n.type === 'definition') defs.set(n.identifier, { url: n.url, title: n.title })
    if (n.type === 'footnoteDefinition') footnotes.push(n)
    if (n.type === 'heading') slugs.set(n, slug(plain(n)))
    if ('children' in n) for (const c of n.children) walk(c as Nodes)
  }
  walk(tree)
  return {
    projectId,
    path,
    defs,
    footnotes,
    slugs,
    // Not through `slug`, which numbers a repeat: a footnote's id is asked
    // for twice, once by its reference and once by its definition.
    footId: (id) => `${ID}fn-${idPart(id)}`,
    refId: (id) => `${ID}fnref-${idPart(id)}`,
    jump: (e) => {
      const target = e.currentTarget.getAttribute('href')?.slice(1)
      if (!target) return
      e.preventDefault()
      const root = e.currentTarget.closest('[data-testid="preview-markdown"]')
      // The document's own ids carry a prefix; a link written for GitHub
      // (`#getting-started`) names the heading without it.
      const el =
        root?.querySelector(`[id="${CSS.escape(target)}"]`) ?? root?.querySelector(`[id="${CSS.escape(ID + target)}"]`)
      el?.scrollIntoView({ block: 'start' })
    },
  }
}

/** A footnote label as part of an id. */
function idPart(label: string): string {
  return label.toLowerCase().replace(/[^\p{L}\p{N}_-]/gu, '-')
}

/** A node's text, for a heading's id and an image's description. */
function plain(n: Nodes): string {
  if ('value' in n && typeof n.value === 'string') return n.value
  if ('children' in n) return (n.children as Nodes[]).map(plain).join('')
  if (n.type === 'image') return n.alt ?? ''
  return ''
}

function kids(n: Parents, ctx: Ctx, key: string): ReactNode[] {
  return (n.children as Nodes[]).map((c, i) => node(c, ctx, `${key}.${i}`, n))
}

// The scale's steps, largest first; h5 and h6 are the body size and are told
// apart by weight and ink, as they are on GitHub.
const HEADING = ['', 'text-vp-lg', 'text-vp-md', 'text-vp-base', 'text-vp-base', 'text-vp-sm', 'text-vp-sm text-ink-2']

function node(n: Nodes, ctx: Ctx, key: string, parent: Parents): ReactNode {
  switch (n.type) {
    case 'heading': {
      const H = `h${n.depth}` as 'h1'
      const id = `${ID}${ctx.slugs.get(n)}`
      return (
        <H key={key} id={id} className={`group mt-5 mb-2 font-semibold first:mt-0 ${HEADING[n.depth]}`}>
          {kids(n, ctx, key)}
          <a
            href={`#${id}`}
            onClick={ctx.jump}
            aria-label={t('md.anchor')}
            className="ml-2 text-ink-3 no-underline opacity-0 group-hover:opacity-100 focus:opacity-100"
          >
            #
          </a>
        </H>
      )
    }
    case 'paragraph':
      return (
        <p key={key} className="my-2">
          {kids(n, ctx, key)}
        </p>
      )
    case 'text':
      return safeBody(n.value)
    case 'emphasis':
      return <em key={key}>{kids(n, ctx, key)}</em>
    case 'strong':
      return <strong key={key}>{kids(n, ctx, key)}</strong>
    case 'delete':
      return <del key={key}>{kids(n, ctx, key)}</del>
    case 'inlineCode':
      return (
        <code key={key} className="rounded-md bg-surface-2 px-1 py-0.5 font-mono text-vp-sm">
          {safeBody(n.value)}
        </code>
      )
    case 'break':
      return <br key={key} />
    case 'thematicBreak':
      return <hr key={key} className="my-4 border-hairline" />
    case 'blockquote':
      return (
        <blockquote key={key} className="my-3 border-l-2 border-hairline-strong pl-3 text-ink-2">
          {kids(n, ctx, key)}
        </blockquote>
      )
    case 'list': {
      const tasks = n.children.some((li) => typeof li.checked === 'boolean')
      // A list inside an item sits right under it, not a paragraph's gap away.
      const gap = parent.type === 'listItem' ? 'my-0' : 'my-2'
      const cls = `${gap} ${tasks ? 'list-none pl-1' : n.ordered ? 'list-decimal pl-6' : 'list-disc pl-6'}`
      return n.ordered ? (
        <ol key={key} start={n.start ?? undefined} className={cls}>
          {kids(n, ctx, key)}
        </ol>
      ) : (
        <ul key={key} className={cls}>
          {kids(n, ctx, key)}
        </ul>
      )
    }
    case 'listItem': {
      // A tight list's items are paragraphs in the tree and lines on the
      // page; a spread one keeps its gaps.
      const tight = parent.type === 'list' && !parent.spread
      const body = n.children.map((c, i) =>
        tight && c.type === 'paragraph' ? (
          <span key={i}>{kids(c, ctx, `${key}.${i}`)}</span>
        ) : (
          node(c, ctx, `${key}.${i}`, n)
        ),
      )
      if (typeof n.checked !== 'boolean') return <li key={key} className="my-0.5">{body}</li>
      // A task: a real checkbox, disabled, so its state is a shape and a word
      // to a screen reader as well as a tick (red line 4).
      return (
        <li key={key} className="my-0.5 flex items-start gap-2" data-task={n.checked ? 'done' : 'open'}>
          {/* Read-only rather than disabled: a disabled box is drawn greyed out,
              and a done task should read as done, not as unavailable. */}
          <input
            type="checkbox"
            checked={n.checked}
            readOnly
            tabIndex={-1}
            aria-readonly="true"
            aria-label={n.checked ? t('md.taskDone') : t('md.taskOpen')}
            className="pointer-events-none mt-1.5 shrink-0 accent-[var(--vp-accent)]"
          />
          <div className="min-w-0">{body}</div>
        </li>
      )
    }
    case 'code':
      return <Code key={key} code={n.value} lang={n.lang ?? undefined} />
    case 'html':
      // Shown as what it is. A document's markup does not reach the page.
      return (
        <code key={key} data-testid="md-raw-html" className="font-mono text-vp-sm text-ink-2 whitespace-pre-wrap">
          {safeBody(n.value)}
        </code>
      )
    case 'link':
      return link(n.url, n.title, kids(n, ctx, key), ctx, key)
    case 'linkReference': {
      const def = ctx.defs.get(n.identifier)
      if (!def) return <span key={key}>{kids(n, ctx, key)}</span>
      return link(def.url, def.title, kids(n, ctx, key), ctx, key)
    }
    case 'image':
      return image(n.url, n.alt ?? '', ctx, key)
    case 'imageReference': {
      const def = ctx.defs.get(n.identifier)
      return image(def?.url ?? '', n.alt ?? '', ctx, key)
    }
    case 'table':
      return table(n, ctx, key)
    case 'footnoteReference': {
      const i = ctx.footnotes.findIndex((f) => f.identifier === n.identifier)
      if (i < 0) return <sup key={key}>[{safeBody(n.label ?? n.identifier)}]</sup>
      return (
        <sup key={key} id={ctx.refId(n.identifier)}>
          <a href={`#${ctx.footId(n.identifier)}`} onClick={ctx.jump} className="text-accent no-underline">
            [{i + 1}]
          </a>
        </sup>
      )
    }
    case 'definition':
    case 'footnoteDefinition':
    case 'yaml':
      return null
    default: {
      // A node type this does not draw by name -- an extension nobody enabled,
      // a newer mdast. Its text, never nothing: a document that loses words
      // is worse than one that loses formatting.
      const other = n as Nodes
      if ('children' in other) return <span key={key}>{kids(other as Parents, ctx, key)}</span>
      return safeBody(plain(other))
    }
  }
}

function link(url: string, title: string | null | undefined, body: ReactNode[], ctx: Ctx, key: string): ReactNode {
  const href = safeHref(url)
  if (href === null) {
    // Refused, and it still says where it pointed: that is the thing worth
    // seeing about a link to `javascript:`.
    return (
      <span key={key} data-testid="md-refused-link">
        {body} <span className="text-ink-3">({safeBody(url)})</span>
      </span>
    )
  }
  const kind = linkKind(href)
  if (kind === 'anchor') {
    return (
      <a key={key} href={href} onClick={ctx.jump} className="text-accent underline underline-offset-2">
        {body}
      </a>
    )
  }
  if (kind === 'external') {
    return (
      <a
        key={key}
        href={href}
        title={title ? safeBody(title) : safeBody(href)}
        target="_blank"
        rel="noopener noreferrer"
        className="text-accent underline underline-offset-2"
      >
        {body}
      </a>
    )
  }
  // Another file in the repository. Followed as a URL it would leave the
  // panel for a path the panel does not serve; it is shown as a link that
  // says where it points and goes nowhere.
  return (
    <span key={key} data-testid="md-relative-link" title={safeBody(href)} className="text-accent underline decoration-dotted underline-offset-2">
      {body}
    </span>
  )
}

function image(url: string, alt: string, ctx: Ctx, key: string): ReactNode {
  const inProject =
    ctx.projectId === SERVER_FILES ? serverImage(ctx.path, url) : projectImage(ctx.path, url)
  if (inProject === null) {
    // A remote image is never requested: a document should not be able to
    // tell somebody's server that it was opened, and when. Its address is
    // shown instead, and the reader can follow it if they choose.
    const href = safeHref(url)
    return (
      <span
        key={key}
        data-testid="md-remote-image"
        className="my-1 inline-flex max-w-full items-center gap-2 rounded-md border border-dashed border-hairline-strong px-2 py-1 text-vp-sm text-ink-2"
      >
        <ImageOff size={13} className="shrink-0" />
        <span className="min-w-0 truncate">
          {alt ? `${safeBody(alt)} · ` : ''}
          {href && linkKind(href) === 'external' ? (
            <a href={href} target="_blank" rel="noopener noreferrer" className="text-accent underline">
              {safeBody(url)}
            </a>
          ) : (
            safeBody(url)
          )}
        </span>
      </span>
    )
  }
  // Inside the project: through the download route, the same origin and the
  // same cookie, resolved and contained by the server like any other file.
  return (
    <img
      key={key}
      src={api.downloadURL(ctx.projectId, inProject)}
      alt={safeBody(alt)}
      data-testid="md-image"
      data-path={inProject}
      loading="lazy"
      className="my-2 inline-block max-w-full rounded-md"
    />
  )
}

const ALIGN = { left: 'text-left', center: 'text-center', right: 'text-right' } as const

function table(n: Table, ctx: Ctx, key: string): ReactNode {
  const [head, ...rows] = n.children
  const cell = (c: TableCell, i: number, k: string, th: boolean) => {
    const a = n.align?.[i]
    const cls = `border-t border-hairline px-3 py-1.5 align-top ${a ? ALIGN[a] : 'text-left'}`
    return th ? (
      <th key={k} className={`${cls} font-semibold`}>
        {kids(c, ctx, k)}
      </th>
    ) : (
      <td key={k} className={cls}>
        {kids(c, ctx, k)}
      </td>
    )
  }
  return (
    <div key={key} className="my-3 overflow-x-auto rounded-vp border border-hairline">
      <table className="w-full border-collapse text-vp-sm [&_tr:first-child>*]:border-t-0">
        <thead className="bg-surface-2">
          <tr>{head?.children.map((c, i) => cell(c, i, `${key}.h${i}`, true))}</tr>
        </thead>
        <tbody>
          {rows.map((r, j) => (
            <tr key={`${key}.r${j}`}>{r.children.map((c, i) => cell(c, i, `${key}.r${j}.${i}`, false))}</tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

