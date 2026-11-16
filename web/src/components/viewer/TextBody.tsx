import { memo, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'

import { safeText } from '../text'
import { blocks as blocksOf, blockOf, type Block, type Match } from './text'

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
}

export function TextBody({ lines, wrap, numbers, matches, current, scrollTo, onTopLine }: Props) {
  const all = useMemo(() => blocksOf(lines.length), [lines.length])
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
      className={`min-h-0 flex-1 overflow-auto py-2 font-mono text-vp-sm leading-5 text-ink ${wrap ? '' : 'overflow-x-auto'}`}
      tabIndex={0}
    >
      {all.map((b, i) => (
        <BlockView
          key={i}
          index={i}
          block={b}
          lines={lines}
          rows={seen.has(i) || i === target}
          wrap={wrap}
          numbers={numbers}
          gutter={gutter}
          byLine={byLine}
          matches={matches}
          current={current}
        />
      ))}
    </div>
  )
}

interface BlockProps {
  index: number
  block: Block
  lines: readonly string[]
  rows: boolean
  wrap: boolean
  numbers: boolean
  gutter: string
  byLine: Map<number, number[]>
  matches: readonly Match[]
  current: number
}

const BlockView = memo(function BlockView({
  index,
  block,
  lines,
  rows,
  wrap,
  numbers,
  gutter,
  byLine,
  matches,
  current,
}: BlockProps) {
  const count = block.end - block.start
  const style = {
    contentVisibility: 'auto',
    containIntrinsicSize: `auto ${count * ESTIMATED_ROW_PX}px`,
  } as const
  const space = wrap ? 'whitespace-pre-wrap [overflow-wrap:anywhere]' : 'whitespace-pre'
  if (!rows) {
    // One text node for the whole block. Indented by the gutter it does not
    // draw yet, so the swap to rows does not shift the text sideways.
    return (
      <pre
        data-block={index}
        style={{ ...style, paddingLeft: numbers ? `calc(${gutter} + 1.5rem)` : '0.75rem' }}
        className={`m-0 pr-3 font-mono ${space}`}
      >
        {safeText(lines.slice(block.start, block.end).join('\n'))}
      </pre>
    )
  }
  const out = []
  for (let n = block.start; n < block.end; n++) {
    out.push(
      <div key={n} data-line={n} className="flex">
        {numbers && (
          <span
            aria-hidden="true"
            style={{ width: gutter }}
            className="tabular shrink-0 select-none pr-3 pl-3 text-right text-ink-2"
          >
            {n + 1}
          </span>
        )}
        <span className={`min-w-0 flex-1 pr-3 ${numbers ? '' : 'pl-3'} ${space}`}>
          <LineText text={lines[n]} hits={byLine.get(n)} matches={matches} current={current} />
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

function LineText({
  text,
  hits,
  matches,
  current,
}: {
  text: string
  hits: number[] | undefined
  matches: readonly Match[]
  current: number
}) {
  // An empty line still has a height: without a character the row collapses
  // and the gutter's number sits against the next one.
  if (!hits) return <>{text === '' ? '​' : safeText(text)}</>
  const parts = []
  let at = 0
  for (const i of hits) {
    const m = matches[i]
    if (m.start > at) parts.push(safeText(text.slice(at, m.start)))
    const isCurrent = i === current
    parts.push(
      // The current match is outlined as well as tinted: colour is never the
      // only carrier (red line 4), and two shades of one highlight are not
      // something everybody can tell apart.
      <mark
        key={i}
        data-match={i}
        data-current={isCurrent ? 'true' : undefined}
        className={
          isCurrent
            ? 'rounded-md bg-[var(--vp-find-current)] text-ink outline outline-2 outline-[var(--vp-find-ring)]'
            : 'rounded-md bg-[var(--vp-find)] text-ink'
        }
      >
        {safeText(text.slice(m.start, m.end))}
      </mark>,
    )
    at = m.end
  }
  if (at < text.length) parts.push(safeText(text.slice(at)))
  return <>{parts}</>
}
