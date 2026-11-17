import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { getDocument, GlobalWorkerOptions, TextLayer, type PDFDocumentProxy, type PDFPageProxy } from 'pdfjs-dist'
import type { TextContent, TextItem } from 'pdfjs-dist/types/src/display/api'
import workerURL from 'pdfjs-dist/build/pdf.worker.min.mjs?url'

import { t } from '../../i18n'
import { findInPages, itemsOf, pageText, type PageText, type PdfMatch } from './pdftext'
import { clampZoom, zoomAbout, type Zoom } from './zoom'

// The worker is a file of its own, fetched when the first PDF is opened:
// parsing a document off the main thread is what keeps a large one from
// freezing the panel while it opens. bundle.test.ts pins that it is not in
// any chunk the panel loads.
GlobalWorkerOptions.workerSrc = workerURL

/** Space around and between pages. */
const GAP = 12
/** Fitted to the width, but never larger than this: a page blown up past
 *  twice its size on a wide screen is letters the size of a thumb. */
const MAX_FIT = 2
/**
 * The most pixels one page's canvas holds. A canvas is memory the browser
 * keeps per page drawn; at sixteen times on a high-density screen one page
 * would be gigabytes, and iOS refuses a canvas past about 16.7 million
 * pixels. Past this the page is drawn at this many and stretched.
 */
const MAX_CANVAS_PIXELS = 16_000_000

const NO_HITS: readonly PdfMatch[] = []

export type PdfFailure = { kind: 'password' } | { kind: 'unreadable'; reason: string }

interface Props {
  url: string
  zoom: Zoom
  onZoom: (z: Zoom) => void
  /** Where the zoom keys read the scale the pages are drawn at now. */
  scaleRef: React.RefObject<() => number>
  /** Find, as on every other body: the query, the current match, the count. */
  query: string
  current: number
  onCount: (n: number, capped: boolean) => void
  /** The number of pages, once the document is open. */
  onPages: (n: number) => void
  /** The page at the top of the view, 0-based, as it scrolls. */
  onPage: (n: number) => void
  /** A page to bring to the top; a new `seq` asks again for the same page. */
  goTo: { page: number; seq: number } | null
  onFail: (f: PdfFailure) => void
}

/**
 * A PDF, drawn by pdf.js: pages one under another, each drawn to a canvas
 * when it comes near the view and released when it goes far from it, with
 * the text over it in a layer of its own, so it can be selected, copied and
 * found.
 *
 * In place of the <object> this replaced, which was a grey box on most
 * phones and on a desktop somebody else's viewer with keys of its own. Links
 * and forms in the document are not drawn as anything that can be clicked:
 * there is no annotation layer, so a PDF cannot take the panel anywhere.
 */
export default function PdfView({ url, zoom, onZoom, scaleRef, query, current, onCount, onPages, onPage, goTo, onFail }: Props) {
  const boxRef = useRef<HTMLDivElement | null>(null)
  const [doc, setDoc] = useState<PDFDocumentProxy | null>(null)
  // Each page's size at scale 1. Every page is the first page's size until it
  // has been read, which is right for nearly every document and corrected
  // page by page for the rest.
  const [sizes, setSizes] = useState<{ w: number; h: number }[]>([])
  const [width, setWidth] = useState(0)

  // The callbacks are the parent's and change on every render; the effects
  // below run on the document, not on them.
  const failRef = useRef(onFail)
  const pagesRef = useRef(onPages)
  useLayoutEffect(() => {
    failRef.current = onFail
    pagesRef.current = onPages
  })

  useEffect(() => {
    let dead = false
    // Read in ranges as the pages are wanted: the inline route answers Range,
    // so a long document's first page is on screen before its last is read.
    const task = getDocument({ url, enableXfa: false })
    task.promise.then(
      async (d) => {
        const first = await d.getPage(1)
        if (dead) return
        const vp = first.getViewport({ scale: 1 })
        setSizes(Array.from({ length: d.numPages }, () => ({ w: vp.width, h: vp.height })))
        setDoc(d)
        pagesRef.current(d.numPages)
      },
      (e: unknown) => {
        if (dead) return
        const name = e instanceof Error ? e.name : ''
        if (name === 'PasswordException') failRef.current({ kind: 'password' })
        else failRef.current({ kind: 'unreadable', reason: e instanceof Error ? e.message : String(e) })
      },
    )
    return () => {
      dead = true
      void task.destroy()
    }
  }, [url])

  useLayoutEffect(() => {
    const box = boxRef.current
    if (!box) return
    const ro = new ResizeObserver(() => setWidth(box.clientWidth))
    ro.observe(box)
    setWidth(box.clientWidth)
    return () => ro.disconnect()
  }, [])

  const widest = sizes.length ? Math.max(sizes[0].w, 1) : 1
  const fit = width > 0 ? Math.min(MAX_FIT, Math.max(0.05, (width - 2 * GAP) / widest)) : 1
  const scale = zoom === 'fit' ? fit : zoom
  useLayoutEffect(() => {
    scaleRef.current = () => scale
  })

  // The canvases are drawn again at a new scale only once the zoom has
  // settled: a pinch is dozens of scales a second, and each would be a render
  // per page. Until then the canvas the page has is stretched to the new size.
  const [drawScale, setDrawScale] = useState(scale)
  useEffect(() => {
    const id = window.setTimeout(() => setDrawScale(scale), 150)
    return () => window.clearTimeout(id)
  }, [scale])

  // Zooming keeps the point under the pointer -- or the middle of the view,
  // for a key -- where it was.
  const anchor = useRef<{ x: number; y: number } | null>(null)
  const lastScale = useRef(scale)
  useLayoutEffect(() => {
    const box = boxRef.current
    const from = lastScale.current
    lastScale.current = scale
    if (!box || from === scale) return
    const a = anchor.current ?? { x: box.clientWidth / 2, y: box.clientHeight / 2 }
    anchor.current = null
    const ratio = scale / from
    box.scrollLeft = (box.scrollLeft + a.x) * ratio - a.x
    box.scrollTop = (box.scrollTop + a.y) * ratio - a.y
  }, [scale])
  const zoomAt = (next: number, clientX: number, clientY: number) => {
    const box = boxRef.current
    if (!box) return
    const r = box.getBoundingClientRect()
    anchor.current = { x: clientX - r.left, y: clientY - r.top }
    onZoom(clampZoom(next))
  }
  const onWheel = (e: React.WheelEvent) => {
    // Ctrl with the wheel is a trackpad's pinch too; without it the wheel
    // scrolls through the pages.
    if (!e.ctrlKey && !e.metaKey) return
    e.preventDefault()
    zoomAt(zoomAbout(scale, e.deltaY < 0 ? 1 : -1, Math.min(4, Math.abs(e.deltaY) / 50)), e.clientX, e.clientY)
  }

  // Two fingers pinch; one scrolls, natively, which is why this is touch
  // events and `touch-action: pan-x pan-y` rather than the image's pointer
  // capture: a document is mostly scrolled, and scrolling it by hand-written
  // dragging loses the momentum a phone's own scroll has.
  const pinch = useRef<{ dist: number; scale: number } | null>(null)
  const scaleNow = useRef(scale)
  const zoomAtRef = useRef(zoomAt)
  useLayoutEffect(() => {
    scaleNow.current = scale
    zoomAtRef.current = zoomAt
  })
  useEffect(() => {
    const box = boxRef.current
    if (!box) return
    const span = (ts: TouchList) => Math.hypot(ts[0].clientX - ts[1].clientX, ts[0].clientY - ts[1].clientY)
    const start = (e: TouchEvent) => {
      if (e.touches.length === 2) pinch.current = { dist: span(e.touches), scale: scaleNow.current }
    }
    const move = (e: TouchEvent) => {
      if (e.touches.length !== 2 || !pinch.current) return
      // Not passive, so this one gesture is the viewer's and not the page's
      // zoom; a one-finger move is never stopped.
      e.preventDefault()
      const [a, b] = [e.touches[0], e.touches[1]]
      zoomAtRef.current(pinch.current.scale * (span(e.touches) / pinch.current.dist), (a.clientX + b.clientX) / 2, (a.clientY + b.clientY) / 2)
    }
    const end = (e: TouchEvent) => {
      if (e.touches.length < 2) pinch.current = null
    }
    box.addEventListener('touchstart', start, { passive: true })
    box.addEventListener('touchmove', move, { passive: false })
    box.addEventListener('touchend', end)
    box.addEventListener('touchcancel', end)
    return () => {
      box.removeEventListener('touchstart', start)
      box.removeEventListener('touchmove', move)
      box.removeEventListener('touchend', end)
      box.removeEventListener('touchcancel', end)
    }
  }, [])

  // Which pages are near the view: drawn while they are, released when they
  // are not, so a long document holds a few canvases and not one per page.
  const pageEls = useRef<(HTMLDivElement | null)[]>([])
  const [near, setNear] = useState<ReadonlySet<number>>(new Set())
  useEffect(() => {
    const box = boxRef.current
    if (!box || !doc) return
    const io = new IntersectionObserver(
      (entries) => {
        setNear((prev) => {
          const next = new Set(prev)
          for (const e of entries) {
            const n = Number((e.target as HTMLElement).dataset.page) - 1
            if (e.isIntersecting) next.add(n)
            else next.delete(n)
          }
          return next
        })
      },
      // Half a screen above and below: the next page is drawn before it is
      // scrolled to, and not every page of a long document at once.
      { root: box, rootMargin: '50% 0px' },
    )
    for (const el of pageEls.current) if (el) io.observe(el)
    return () => io.disconnect()
  }, [doc, sizes.length])

  // The page at the top of the view, for the footer and for PgUp and PgDn.
  const pageRef = useRef(onPage)
  useLayoutEffect(() => {
    pageRef.current = onPage
  })
  const topPage = useRef(0)
  const onScroll = useCallback(() => {
    const box = boxRef.current
    if (!box) return
    // A third of the way down, so the page being read is the one counted and
    // not the bottom edge of the one before it.
    const y = box.scrollTop + box.clientHeight / 3
    const els = pageEls.current
    let lo = 0
    let hi = els.length - 1
    while (lo < hi) {
      const mid = (lo + hi + 1) >> 1
      if ((els[mid]?.offsetTop ?? 0) <= y) lo = mid
      else hi = mid - 1
    }
    if (lo !== topPage.current) {
      topPage.current = lo
      pageRef.current(lo)
    }
  }, [])
  useEffect(onScroll, [onScroll, scale, sizes])

  useEffect(() => {
    const el = goTo ? pageEls.current[goTo.page] : null
    const box = boxRef.current
    if (!el || !box) return
    box.scrollTop = el.offsetTop - GAP
  }, [goTo])

  // Each page's text, read once and shared by find and the text layer, so a
  // match's offsets are offsets into the same items the layer draws.
  const texts = useRef(new Map<number, Promise<TextContent>>())
  const textOf = useCallback(
    (page: PDFPageProxy) => {
      let p = texts.current.get(page.pageNumber - 1)
      if (!p) {
        p = page.getTextContent()
        texts.current.set(page.pageNumber - 1, p)
      }
      return p
    },
    [],
  )
  // Find reads every page's text the first time something is searched for,
  // in order, and the count grows as it does.
  const [read, setRead] = useState<(PageText | null)[]>([])
  const searching = query !== ''
  useEffect(() => {
    if (!doc || !searching) return
    let dead = false
    void (async () => {
      for (let i = 0; i < doc.numPages && !dead; i++) {
        if (read[i]) continue
        try {
          const tc = await textOf(await doc.getPage(i + 1))
          if (dead) return
          const pt = pageText(tc.items.filter(isText))
          setRead((prev) => {
            const next = prev.slice()
            next[i] = pt
            return next
          })
        } catch {
          // A page whose text cannot be read has none to find.
        }
      }
    })()
    return () => {
      dead = true
    }
    // `read` is what this fills; running again on each page it adds would
    // start the loop again from the top every time.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [doc, searching, textOf])
  const found = useMemo(() => findInPages(read, query), [read, query])
  // By page, and the same array from one render to the next, so a page with
  // nothing new to mark is not marked again on every scroll.
  const hitsByPage = useMemo(() => {
    const by = new Map<number, PdfMatch[]>()
    for (const m of found.matches) {
      const list = by.get(m.page) ?? []
      list.push(m)
      by.set(m.page, list)
    }
    return by
  }, [found])
  const allRead = doc !== null && read.filter(Boolean).length === doc.numPages
  useEffect(() => {
    // Not final until every page is read: "12+" while it is still counting.
    onCount(found.matches.length, found.capped || (searching && !allRead))
  }, [found, searching, allRead, onCount])

  // Stepping to a match brings its page to the view and centres the match.
  // A page already drawn has marked it by now -- a child's effects run
  // before this one -- so it is centred here; a page that was far marks it
  // once it is drawn, and centres it then (`claimReveal`).
  const reveal = useRef(-1)
  // Read through a ref: the effect is about stepping, and the matches grow
  // while the pages are read -- pages are read in order, so a match's index
  // never moves -- and running it on each would pull the view back to the
  // current match every time a page was read.
  const foundRef = useRef(found)
  useLayoutEffect(() => {
    foundRef.current = found
  })
  useEffect(() => {
    const m = current >= 0 ? foundRef.current.matches[current] : undefined
    const box = boxRef.current
    const el = m ? pageEls.current[m.page] : null
    if (!m || !el || !box) return
    reveal.current = current
    const hit = el.querySelector<HTMLElement>('.vp-pdf-hit[data-current]')
    if (hit) {
      reveal.current = -1
      hit.scrollIntoView({ block: 'center', inline: 'nearest' })
      return
    }
    const top = el.offsetTop
    if (top + el.offsetHeight < box.scrollTop || top > box.scrollTop + box.clientHeight) box.scrollTop = top - GAP
  }, [current])
  // Stable, so stepping to a match changes the props of the page it is on
  // and nobody else's.
  const claimReveal = useCallback(() => {
    if (reveal.current < 0) return false
    reveal.current = -1
    return true
  }, [])
  const currentMatch = current >= 0 ? found.matches[current] : undefined

  return (
    <div
      ref={boxRef}
      data-testid="viewer-pdf"
      data-pages={doc?.numPages ?? 0}
      data-zoom={zoom === 'fit' ? 'fit' : zoom.toFixed(3)}
      onScroll={onScroll}
      onWheel={onWheel}
      tabIndex={-1}
      // relative: the pages' offsetTop is then measured from here. Without it
      // they were measured from the dialog, the header's height off, and the
      // footer counted page 1 with page 2 filling the view.
      className="relative min-h-0 flex-1 touch-pan-x touch-pan-y overflow-auto bg-surface-2 outline-none"
    >
      <span className="sr-only">{t('viewer.pdfHelp')}</span>
      <div className="flex w-max min-w-full flex-col items-center" style={{ gap: GAP, padding: GAP }}>
        {doc &&
          sizes.map((s, i) => (
            <Page
              key={i}
              doc={doc}
              index={i}
              size={s}
              scale={scale}
              drawScale={drawScale}
              near={near.has(i)}
              textOf={textOf}
              hits={hitsByPage.get(i) ?? NO_HITS}
              current={currentMatch?.page === i ? currentMatch : undefined}
              claimReveal={claimReveal}
              elRef={(el) => {
                pageEls.current[i] = el
              }}
              onSize={(w, h) =>
                setSizes((prev) => {
                  if (prev[i] && prev[i].w === w && prev[i].h === h) return prev
                  const next = prev.slice()
                  next[i] = { w, h }
                  return next
                })
              }
            />
          ))}
      </div>
    </div>
  )
}

function isText(it: TextContent['items'][number]): it is TextItem {
  // A marked-content item has no text; pdf.js draws no run for it.
  return typeof (it as TextItem).str === 'string'
}

function Page({
  doc,
  index,
  size,
  scale,
  drawScale,
  near,
  textOf,
  hits,
  current,
  claimReveal,
  elRef,
  onSize,
}: {
  doc: PDFDocumentProxy
  index: number
  size: { w: number; h: number }
  scale: number
  drawScale: number
  near: boolean
  textOf: (p: PDFPageProxy) => Promise<TextContent>
  hits: readonly PdfMatch[]
  current: PdfMatch | undefined
  claimReveal: () => boolean
  elRef: (el: HTMLDivElement | null) => void
  onSize: (w: number, h: number) => void
}) {
  const canvasRef = useRef<HTMLCanvasElement | null>(null)
  const textRef = useRef<HTMLDivElement | null>(null)
  // The text layer's runs and where each starts in the page's text, once
  // drawn; what a match is marked on.
  const [layer, setLayer] = useState<{ divs: HTMLElement[]; strs: string[]; starts: number[] } | null>(null)
  const onSizeRef = useRef(onSize)
  useLayoutEffect(() => {
    onSizeRef.current = onSize
  })

  useEffect(() => {
    const canvas = canvasRef.current
    const text = textRef.current
    if (!canvas || !text) return
    if (!near) {
      // Far from the view: give the memory back. A zero-sized canvas holds
      // no pixels.
      canvas.width = 0
      canvas.height = 0
      text.replaceChildren()
      return
    }
    let dead = false
    let task: ReturnType<PDFPageProxy['render']> | null = null
    let tl: TextLayer | null = null
    void (async () => {
      try {
        const page = await doc.getPage(index + 1)
        if (dead) return
        const own = page.getViewport({ scale: 1 })
        onSizeRef.current(own.width, own.height)
        const vp = page.getViewport({ scale: drawScale })
        const dpr = window.devicePixelRatio || 1
        const ratio = Math.min(dpr, Math.sqrt(MAX_CANVAS_PIXELS / Math.max(1, vp.width * vp.height)))
        const off = document.createElement('canvas')
        off.width = Math.max(1, Math.floor(vp.width * ratio))
        off.height = Math.max(1, Math.floor(vp.height * ratio))
        task = page.render({ canvas: off, viewport: page.getViewport({ scale: drawScale * ratio }) })
        await task.promise
        if (dead) return
        // Drawn off screen and then swapped in, so a page being drawn again
        // at a new zoom keeps showing the old drawing rather than a blank.
        canvas.width = off.width
        canvas.height = off.height
        canvas.getContext('2d')?.drawImage(off, 0, 0)
        off.width = 0
        off.height = 0

        const tc = await textOf(page)
        if (dead) return
        // One run per text item, in order, so tl.textDivs[i] is item i of
        // the same list find searched.
        text.replaceChildren()
        tl = new TextLayer({ textContentSource: tc, container: text, viewport: vp })
        await tl.render()
        if (dead) return
        setLayer({ divs: tl.textDivs, strs: tl.textContentItemsStr, starts: pageText(tc.items.filter(isText)).starts })
      } catch {
        // Cancelled by scrolling away or zooming again, or a page pdf.js
        // cannot draw: it stays the paper it is.
      }
    })()
    return () => {
      dead = true
      task?.cancel()
      tl?.cancel()
    }
  }, [doc, index, near, drawScale, textOf])

  // The matches on this page, marked inside the runs they are in. A layer
  // whose runs are no longer in the page -- released, or being drawn again --
  // has nothing to mark, and must not spend the reveal on a detached run.
  const live = near ? layer : null
  useLayoutEffect(() => {
    if (!live || (live.divs.length > 0 && !live.divs[0].isConnected)) return
    const { divs, strs, starts } = live
    const ranges = new Map<number, { from: number; to: number; current: boolean }[]>()
    for (const m of hits) {
      const [a, b] = itemsOf(m, starts)
      for (let i = a; i <= b; i++) {
        const from = Math.max(0, m.start - starts[i])
        const to = Math.min(strs[i].length, m.end - starts[i])
        if (to <= from) continue
        const list = ranges.get(i) ?? []
        list.push({ from, to, current: m === current })
        ranges.set(i, list)
      }
    }
    let currentEl: HTMLElement | null = null
    for (const [i, list] of ranges) {
      const parts: Node[] = []
      let at = 0
      for (const r of list) {
        if (r.from > at) parts.push(document.createTextNode(strs[i].slice(at, r.from)))
        const mark = document.createElement('span')
        mark.className = 'vp-pdf-hit'
        if (r.current) {
          mark.dataset.current = 'true'
          currentEl ??= mark
        }
        mark.textContent = strs[i].slice(r.from, r.to)
        parts.push(mark)
        at = r.to
      }
      if (at < strs[i].length) parts.push(document.createTextNode(strs[i].slice(at)))
      divs[i].replaceChildren(...parts)
    }
    if (currentEl && claimReveal()) currentEl.scrollIntoView({ block: 'center', inline: 'nearest' })
    return () => {
      for (const i of ranges.keys()) divs[i].textContent = strs[i]
    }
  }, [live, hits, current, claimReveal])

  return (
    <div
      ref={elRef}
      data-page={index + 1}
      data-drawn={live ? 'true' : undefined}
      className="vp-pdf-page relative shrink-0 shadow-sm"
      style={
        {
          width: Math.floor(size.w * scale),
          height: Math.floor(size.h * scale),
          '--total-scale-factor': scale,
          '--scale-round-x': '1px',
          '--scale-round-y': '1px',
        } as React.CSSProperties
      }
    >
      <canvas ref={canvasRef} aria-hidden="true" className="absolute inset-0 h-full w-full" />
      <div ref={textRef} className="vp-pdf-text" />
    </div>
  )
}
