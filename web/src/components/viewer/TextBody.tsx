import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'

import { safeBody } from '../text'
import { t } from '../../i18n'
import { diffRow } from './diff'
import { blocks as blocksOf, blockOf, cutLines, LONG_LINE, type Block, type Match } from './text'
import { pieces } from './pieces'
import { blockTokens, type Tok, type TokKind } from './tokens'

/**
 * A text, every line of it in the DOM, drawn in blocks the browser skips while
 * they are off screen.
 *
 * docs/file-preview.md, "Size", is the reasoning: windowing was the first idea
 * and it loses wrap (rows with no fixed height) and copy (a drag across lines
 * not in the DOM copies a hole). Here a block is `content-visibility: auto`,
 * so its layout and paint are skipped until it nears the viewport, with
 * `contain-intrinsic-size: auto <estimate>` -- the `auto` is what corrects
 * the estimate once the block has been laid out, so the scrollbar stops
 * jumping after the first pass over a block.
 *
 * And a block is one text node until it is first seen. A row per line is
 * three elements, and 50,000 lines of that is the half a million elements the
 * design says a file must not start with; one `<pre>` per block is two. A
 * block becomes rows the first time it comes near the viewport and stays rows.
 * Selection and copy work across both shapes, because both are text, and the
 * gutter of a row is `user-select: none` so a copy is the file's lines and
 * not its line numbers.
 *
 * `safeBody`, never `safeText`, on everything a file contributes here.
 * `safeText` is for one line -- a name -- and turns a newline or a tab into
 * U+FFFD; over a file's contents it drew every line of a log run together with
 * a black diamond between them, which is what the preview this replaced did to
 * every plain text file (docs/build-log.md, the hands-on list).
 */

/** Rows as tall as this, until a block has been laid out and says otherwise. */
const ESTIMATED_ROW_PX = 20

export interface ScrollRequest {
  line: number
  /** A new request for the same line is still a request. */
  seq: number
}

interface Props {
  lines: readonly string[]
  wrap: boolean
  numbers: boolean
  matches: readonly Match[]
  current: number
  scrollTo: ScrollRequest | null
  /** The first line on screen, reported as the reader scrolls. */
  onTopLine?: (line: number) => void
  /**
   * A Prism grammar that has loaded, or null for plain text. Each block is
   * tokenised the first time it is drawn as rows, never before: a 50,000 line
   * file does not start with half a million elements.
   */
  lang?: string | null
  /** A diff: added and removed rows are tinted as well as signed. */
  diff?: boolean
}

export function TextBody({ lines, wrap, numbers, matches, current, scrollTo, onTopLine, lang = null, diff = false }: Props) {
  const all = useMemo(() => blocksOf(lines.length), [lines.length])
  // Long lines are drawn cut (text.ts, LONG_LINE) until opened; the cut copy
  // is also what is tokenised, since Prism on a two-megabyte line is its own
  // ten seconds.
  const shown = useMemo(() => cutLines(lines), [lines])
  const [opened, setOpened] = useState<ReadonlySet<number>>(() => new Set())
  const open = useCallback((n: number) => setOpened((prev) => new Set(prev).add(n)), [])
  const boxRef = useRef<HTMLDivElement | null>(null)
  const [seen, setSeen] = useState<ReadonlySet<number>>(() => new Set([0]))

  // Matches by line, built once per search rather than filtered per row.
  const byLine = useMemo(() => {
    const m = new Map<number, number[]>()
    matches.forEach((x, i) => {
      const list = m.get(x.line)
      if (list) list.push(i)
      else m.set(x.line, [i])
    })
    return m
  }, [matches])

  // A block becomes rows as it nears the viewport: two screens ahead, so the
  // swap has happened before anybody can see it.
  useEffect(() => {
    const box = boxRef.current
    if (!box || typeof IntersectionObserver === 'undefined') return
    const io = new IntersectionObserver(
      (entries) => {
        const add: number[] = []
        for (const e of entries) {
          if (e.isIntersecting) add.push(Number((e.target as HTMLElement).dataset.block))
        }
        if (add.length) {
          setSeen((prev) => {
            if (add.every((b) => prev.has(b))) return prev
            const next = new Set(prev)
            add.forEach((b) => next.add(b))
            return next
          })
        }
      },
      { root: box, rootMargin: '200% 0px' },
    )
    box.querySelectorAll('[data-block]').forEach((el) => io.observe(el))
    return () => io.disconnect()
  }, [all])

  // Go to a line: its block is drawn as rows in this render whether or not it
  // has been seen, and the row is put in the middle once it exists. Scrolling
  // the block alone would land on its estimated height, not on the line.
  // It stays rows afterwards because it has been on screen, and the observer
  // marks it seen like any other -- which needs each block to keep its
  // element when it changes shape (BlockView).
  const target = scrollTo ? blockOf(scrollTo.line) : -1
  useLayoutEffect(() => {
    if (!scrollTo) return
    boxRef.current?.querySelector(`[data-line="${scrollTo.line}"]`)?.scrollIntoView({ block: 'center' })
  }, [scrollTo])

  // The reader's position, for a find that starts where they are.
  useEffect(() => {
    const box = boxRef.current
    if (!box || !onTopLine) return
    let frame = 0
    const onScroll = () => {
      cancelAnimationFrame(frame)
      frame = requestAnimationFrame(() => {
        const top = box.getBoundingClientRect().top
        const row = document.elementFromPoint(box.getBoundingClientRect().left + 40, top + 4)
        const line = row?.closest<HTMLElement>('[data-line]')?.dataset.line
        if (line !== undefined) onTopLine(Number(line))
      })
    }
    box.addEventListener('scroll', onScroll, { passive: true })
    return () => {
      box.removeEventListener('scroll', onScroll)
      cancelAnimationFrame(frame)
    }
  }, [onTopLine])

  const gutter = `${String(lines.length).length + 1}ch`
  return (
    <div
      ref={boxRef}
      data-testid="viewer-text"
      data-wrap={wrap ? 'on' : 'off'}
      data-numbers={numbers ? 'on' : 'off'}
      data-lang={lang ?? 'plain'}
      className={`min-h-0 flex-1 overflow-auto py-2 font-mono text-vp-sm leading-5 text-ink ${wrap ? '' : 'overflow-x-auto'}`}
      tabIndex={0}
    >
      {all.map((b, i) => (
        <BlockView
          key={i}
          index={i}
          block={b}
          lines={lines}
          shown={shown}
          opened={opened}
          onOpen={open}
          rows={seen.has(i) || i === target}
          wrap={wrap}
          numbers={numbers}
          gutter={gutter}
          byLine={byLine}
          matches={matches}
          current={current}
          lang={lang}
          diff={diff}
        />
      ))}
    </div>
  )
}

interface BlockProps {
  index: number
  block: Block
  lines: readonly string[]
  /** The lines with each long one cut, as drawn until it is opened. */
  shown: readonly string[]
  opened: ReadonlySet<number>
  onOpen: (line: number) => void
  rows: boolean
  wrap: boolean
  numbers: boolean
  gutter: string
  byLine: Map<number, number[]>
  matches: readonly Match[]
  current: number
  lang: string | null
  diff: boolean
}

const BlockView = memo(function BlockView({
  index,
  block,
  lines,
  shown,
  opened,
  onOpen,
  rows,
  wrap,
  numbers,
  gutter,
  byLine,
  matches,
  current,
  lang,
  diff,
}: BlockProps) {
  // Tokenised here, once, when the block first becomes rows. A memo rather
  // than a cache beside it: the block's deps do not change while it is
  // drawn, and a block that is still one text node asks for nothing.
  const tokens = useMemo(
    () => (rows && lang ? blockTokens(shown, block.start, block.end, lang) : null),
    [rows, lang, shown, block.start, block.end],
  )
  const count = block.end - block.start
  const style = {
    contentVisibility: 'auto',
    containIntrinsicSize: `auto ${count * ESTIMATED_ROW_PX}px`,
  } as const
  const space = wrap ? 'whitespace-pre-wrap [overflow-wrap:anywhere]' : 'whitespace-pre'
  // The same element in both shapes: the observer that turns a block into
  // rows is watching this one, and a block that swapped its element for
  // another was a block it had stopped watching.
  if (!rows) {
    // One text node for the whole block. Indented by the gutter it does not
    // draw yet, so the swap to rows does not shift the text sideways.
    return (
      <div data-block={index} style={style}>
        <pre
          style={{ paddingLeft: numbers ? `calc(${gutter} + 1.5rem)` : '0.75rem' }}
          className={`m-0 pr-3 font-mono ${space}`}
        >
          {safeBody(shown.slice(block.start, block.end).join('\n'))}
        </pre>
      </div>
    )
  }
  const out = []
  for (let n = block.start; n < block.end; n++) {
    out.push(
      <div key={n} data-line={n} className={`flex ${diff ? DIFF_ROW[diffRow(lines[n])] : ''}`}>
        {numbers && (
          <span
            aria-hidden="true"
            // The digits' width plus the padding: a width of the digits
            // alone with the padding inside it left the number no room and
            // it ran into the line (39986line).
            style={{ width: `calc(${gutter} + 1.5rem)` }}
            className="tabular box-border shrink-0 select-none px-3 text-right text-ink-2"
          >
            {n + 1}
          </span>
        )}
        <span className={`min-w-0 flex-1 pr-3 ${numbers ? '' : 'pl-3'} ${space}`}>
          {(() => {
            // Which part of a long line is drawn: its start, or -- when the
            // current match lies past the cut -- the stretch around that
            // match, so stepping to it does not lay out the whole line. The
            // whole line is one button away, as the reader's choice.
            const full = lines[n]
            const isLong = full.length > LONG_LINE && !opened.has(n)
            let from = 0
            let to = full.length
            if (isLong) {
              const cur = current >= 0 && matches[current]?.line === n ? matches[current] : undefined
              if (cur && cur.end > LONG_LINE) {
                from = Math.max(0, Math.min(cur.start - LONG_LINE / 2, full.length - LONG_LINE))
                to = Math.min(full.length, from + LONG_LINE)
              } else {
                to = LONG_LINE
              }
            }
            const more = (k: number) => (
              <button
                type="button"
                onClick={() => onOpen(n)}
                data-testid="viewer-long-line"
                className="mx-1 rounded-md border border-hairline px-1.5 font-sans text-vp-xs text-ink-2 hover:text-ink"
              >
                {t('viewer.longLine', { n: k.toLocaleString() })}
              </button>
            )
            return (
              <>
                {from > 0 && more(from)}
                <LineText
                  text={isLong ? full.slice(from, to) : full}
                  offset={from}
                  tokens={opened.has(n) || from > 0 ? undefined : tokens?.[n - block.start]}
                  hits={byLine.get(n)}
                  matches={matches}
                  current={current}
                />
                {to < full.length && more(full.length - to)}
              </>
            )
          })()}
        </span>
      </div>,
    )
  }
  return (
    <div data-block={index} style={style}>
      {out}
    </div>
  )
})

/**
 * The classes a token is drawn with: one theme token each, so the colours are
 * the theme's (red line 5) and each is pinned at 4.5:1 by contrast.test.ts.
 * Written out whole because Tailwind finds classes by reading the source.
 */
const TOK_CLASS: Record<TokKind, string> = {
  plain: '',
  comment: 'text-[var(--vp-tok-comment)]',
  keyword: 'text-[var(--vp-tok-keyword)]',
  string: 'text-[var(--vp-tok-string)]',
  number: 'text-[var(--vp-tok-number)]',
  function: 'text-[var(--vp-tok-function)]',
  type: 'text-[var(--vp-tok-type)]',
  property: 'text-[var(--vp-tok-property)]',
  tag: 'text-[var(--vp-tok-tag)]',
  regex: 'text-[var(--vp-tok-regex)]',
  variable: 'text-[var(--vp-tok-variable)]',
  punct: 'text-[var(--vp-tok-punct)]',
  inserted: 'text-[var(--vp-tok-inserted)]',
  deleted: 'text-[var(--vp-tok-deleted)]',
}

/**
 * A diff row's tint. Never the only difference: the row still starts with its
 * `+` or `-` as the file has it (red line 4), and the tint is mixed from the
 * same token colour so a theme that changes one changes both.
 */
const DIFF_ROW: Record<ReturnType<typeof diffRow>, string> = {
  add: 'bg-[color-mix(in_srgb,var(--vp-tok-inserted)_14%,transparent)]',
  del: 'bg-[color-mix(in_srgb,var(--vp-tok-deleted)_14%,transparent)]',
  hunk: 'bg-[color-mix(in_srgb,var(--vp-tok-type)_10%,transparent)]',
  meta: '',
  context: '',
}

function LineText({
  text,
  offset,
  tokens,
  hits,
  matches,
  current,
}: {
  text: string
  /** Where `text` starts in the line, when it is a window of a long one. */
  offset: number
  tokens: readonly Tok[] | undefined
  hits: number[] | undefined
  matches: readonly Match[]
  current: number
}) {
  // An empty line still has a height: without a character the row collapses
  // and the gutter's number sits against the next one.
  if (text === '') return <>{'\u200b'}</>
  if (!hits && !tokens) return <>{safeBody(text)}</>
  const ps = pieces(text, tokens, hits ?? [], matches, offset)
  const out: React.ReactNode[] = []
  for (let i = 0; i < ps.length; ) {
    const m = ps[i].match
    const run: React.ReactNode[] = []
    for (; i < ps.length && ps[i].match === m; i++) {
      const p = ps[i]
      run.push(
        p.k === 'plain' ? (
          safeBody(p.v)
        ) : (
          <span key={i} className={TOK_CLASS[p.k]} data-tok={p.k}>
            {safeBody(p.v)}
          </span>
        ),
      )
    }
    if (m < 0) {
      out.push(...run)
      continue
    }
    const isCurrent = m === current
    out.push(
      // The current match is outlined as well as tinted: colour is never the
      // only carrier (red line 4), and two shades of one highlight are not
      // something everybody can tell apart.
      <mark
        key={`m${m}`}
        data-match={m}
        data-current={isCurrent ? 'true' : undefined}
        className={
          isCurrent
            ? 'rounded-md bg-[var(--vp-find-current)] text-ink outline outline-2 outline-[var(--vp-find-ring)]'
            : 'rounded-md bg-[var(--vp-find)] text-ink'
        }
      >
        {run}
      </mark>,
    )
  }
  return <>{out}</>
}
