import { lazy, Suspense, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import {
  ChevronLeft,
  ChevronRight,
  Code2,
  Download,
  Eye,
  FileQuestion,
  Hash,
  Loader2,
  Minus,
  Plus,
  Search,
  WrapText,
  X,
  Zap,
} from 'lucide-react'

import { api } from '../../protocol/api'
import type { Markup } from '../../protocol/api'
import type { FileEntry } from '../../protocol/wire'
import { copyTextInGesture } from '../../clipboard'
import { safeText } from '../text'
import { t, useLang } from '../../i18n'
import { useDialogFocus } from '../useDialogFocus'
import { isMac } from '../keyprefs'
import { formatBytes, PREVIEW_MAX_BYTES } from '../panels/preview'
import { canRender, sandboxFor } from '../panels/render'
import { isMarkdown } from './mdlinks'
import { CsvTable } from './CsvTable'
import { isDelimited, parseCsv, sniffDelimiter } from './csv'
import { hexLines } from './hex'
import { JsonTree } from './JsonTree'
import { isJson, parseJson } from './json'
import { isNotebook, parseNotebook } from './notebook'
import { FONT_MAX_BYTES, fontFormat } from './fontkind'
import { looksLikeDiff } from './diff'
import { grammarFor } from './grammars'
import { loadGrammar } from './tokens'
import { viewerAction, type ViewerAction } from './keys'
import { crumbs, neighbours } from './nav'
import {
  lineNumbersPreference,
  setLineNumbersPreference,
  setWrapPreference,
  wrapPreference,
} from './prefs'
import { findMatches, firstMatchFrom, lineEndings, parseGoTo, splitLines, stepMatch } from './text'
import { TextBody, type ScrollRequest } from './TextBody'
import { ImageView } from './ImageView'
import type { PdfFailure } from './PdfView'
import { MAX_ZOOM, MIN_ZOOM, zoomAbout, percent, type Zoom } from './zoom'

/**
 * The document renderer, fetched the first time a Markdown file is opened.
 * The parser is most of a hundred kilobytes that a log or a picture never
 * needs, so it is a chunk of its own (bundle.test.ts).
 */
const Markdown = lazy(() =>
  import('./Markdown').catch(() => ({
    // A tab older than the panel: an upgrade replaced the chunk it knows the
    // name of. Said in the document's place, as lazy.tsx does for the viewer.
    default: () => <p className="px-5 py-4 text-vp-base text-ink-2">{t('viewer.stale')}</p>,
  })),
)

/**
 * A notebook's cells, fetched the first time one is opened. Its markdown
 * cells are the document renderer's, so it shares that chunk.
 */
const Notebook = lazy(() =>
  import('./NotebookView').catch(() => ({
    default: () => <p className="px-5 py-4 text-vp-base text-ink-2">{t('viewer.stale')}</p>,
  })),
)

/** A font's specimen: small, but only a font file ever needs it. */
const FontSpecimen = lazy(() =>
  import('./FontSpecimen').catch(() => ({
    default: () => <p className="px-5 py-4 text-vp-base text-ink-2">{t('viewer.stale')}</p>,
  })),
)

/**
 * pdf.js, fetched the first time a PDF is opened: a few hundred kilobytes,
 * and its worker another file again (bundle.test.ts).
 */
const PdfView = lazy(() =>
  import('./PdfView').catch(() => ({
    default: () => <p className="px-5 py-4 text-vp-base text-ink-2">{t('viewer.stale')}</p>,
  })),
)

/** What is on screen: a file, the reason there is not one, or the wait. */
type View =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'text'; text: string; truncated: boolean; markup: Markup | null; encoding: string | null }
  | { kind: 'binary'; bytes: Uint8Array; truncated: boolean }
  /** A medium, streamed from the inline route by the element that draws it. */
  | { kind: 'image' | 'pdf' | 'video' | 'audio'; url: string }
  | { kind: 'tooBig' }
  | { kind: 'none' }

/**
 * One file, over the whole window: docs/file-preview.md is the design.
 *
 * Full-window rather than inside the panel because the panel is 280 pixels,
 * and reading a file is a different job from finding it. A portal, because
 * the panel carries `backdrop-filter`, which makes it the containing block of
 * any `position: fixed` inside it -- a modal rendered there is clipped to the
 * 280 pixels it exists to escape.
 *
 * Two parts, and the split is about the focus. The dialog -- portal, focus,
 * the keyboard -- is mounted once for the whole visit; what is inside it is
 * keyed by path, so every file starts in the safe state: source off, scripts
 * off, find closed. Keying the whole dialog was the first version, and `]`
 * then unmounted one dialog and mounted another: the old one's cleanup gave
 * the focus back to the tree's row after the new one had taken it, so the
 * next key went to the tree, and closing could not find the row any more.
 * Wrap and line numbers are the browser's (prefs.ts) and carry over.
 *
 * The shell is batch 1 of the rebuild -- header, breadcrumb, previous and
 * next, find, go to line, wrap and line numbers, the text body, the footer,
 * the keyboard -- and batch 2 coloured the text body: code and diffs are the
 * same line body as a log, with a grammar (grammars.ts, tokens.ts). Later
 * batches drew the rest (docs/file-preview.md, "Batches"); a PDF is pdf.js
 * (PdfView.tsx) since 7a.
 */
interface ViewerProps {
  projectId: string
  /** The breadcrumb's root. */
  projectName: string
  entry: FileEntry
  /** The files of the directory on screen, in the tree's order. */
  files: readonly FileEntry[]
  /** Previous and next: opens another file in the viewer. */
  onOpen: (e: FileEntry) => void
  /** A breadcrumb segment: closes the viewer and moves the tree there. */
  onReveal: (dir: string) => void
  onClose: () => void
}

export function FileViewer(props: ViewerProps) {
  useLang()
  const boxRef = useRef<HTMLDivElement | null>(null)
  // The current file's key handler. The content registers it on every render,
  // so the dialog's one listener always reaches the file on screen.
  const keysRef = useRef<((e: React.KeyboardEvent) => void) | null>(null)
  useDialogFocus(boxRef, props.onClose, { escape: false })
  return createPortal(
    <div
      className="vp-backdrop @container fixed inset-0 z-50 flex items-stretch justify-center bg-black/40"
      onClick={props.onClose}
      data-testid="preview-backdrop"
    >
      <div
        ref={boxRef}
        role="dialog"
        aria-modal="true"
        aria-label={t('viewer.label', { name: safeText(props.entry.name) })}
        tabIndex={-1}
        onClick={(e) => e.stopPropagation()}
        onKeyDown={(e) => keysRef.current?.(e)}
        data-testid="file-viewer"
        // The responsive rules are this element's and not the backdrop's: the
        // backdrop is the container, and a container query never applies to
        // the container itself -- `@2xl:items-center` there did nothing and
        // the dialog sat against the top edge.
        className="vp-panel-in flex h-full w-full max-w-5xl flex-col overflow-hidden border-hairline bg-surface shadow-xl outline-none @2xl:mx-4 @2xl:h-[88vh] @2xl:self-center @2xl:rounded-vp @2xl:border"
      >
        <FileContent key={props.entry.path} {...props} boxRef={boxRef} keysRef={keysRef} />
      </div>
    </div>,
    document.body,
  )
}

function FileContent({
  projectId,
  projectName,
  entry,
  files,
  onOpen,
  onReveal,
  onClose,
  boxRef,
  keysRef,
}: ViewerProps & {
  boxRef: React.RefObject<HTMLDivElement | null>
  keysRef: React.RefObject<((e: React.KeyboardEvent) => void) | null>
}) {
  useLang()
  // Every kind has a bounded answer now -- text is cut, a binary is its
  // first bytes, a medium streams -- so there is no size to refuse at before
  // asking; a file that is too large for one of them says so in the answer.
  const [view, setView] = useState<View>({ kind: 'loading' })
  const findRef = useRef<HTMLInputElement | null>(null)
  const gotoRef = useRef<HTMLInputElement | null>(null)

  // `source` is the way back to the bytes and `scripts` the one switch that
  // runs anything; both start off on every file and neither persists. See
  // the paragraph in docs/file-preview.md, "What it is".
  const [source, setSource] = useState(false)
  const [scripts, setScripts] = useState(false)
  const [wrap, setWrap] = useState(wrapPreference)
  const [numbers, setNumbers] = useState(lineNumbersPreference)
  const [findOpen, setFindOpen] = useState(false)
  const [query, setQuery] = useState('')
  const [current, setCurrent] = useState(-1)
  const [gotoOpen, setGotoOpen] = useState(false)
  const [gotoValue, setGotoValue] = useState('')
  const [scrollTo, setScrollTo] = useState<ScrollRequest | null>(null)
  const topLine = useRef(0)
  const scrollSeq = useRef(0)
  const [copied, setCopied] = useState(false)

  useEffect(() => {
    let cancelled = false
    api.preview(projectId, entry.path).then(
      (p) => {
        if (cancelled) return
        if (p.kind === 'text' || p.kind === 'binary' || p.kind === 'tooBig' || p.kind === 'none') {
          setView(p)
          return
        }
        // A medium: streamed by the element that draws it, from the inline
        // route -- no copy in memory, no cap, ranges for a video to seek with.
        setView({ kind: p.kind, url: api.inlineURL(projectId, entry.path) })
      },
      (e: unknown) => {
        if (!cancelled) setView({ kind: 'error', message: e instanceof Error ? e.message : String(e) })
      },
    )
    return () => {
      cancelled = true
    }
  }, [projectId, entry.path])

  // The image's zoom, here rather than in the image view so the keys reach it,
  // and its size once it has loaded, for the footer.
  const [zoom, setZoom] = useState<Zoom>('fit')
  const [imageSize, setImageSize] = useState<{ w: number; h: number } | null>(null)
  const imageScale = useRef<() => number>(() => 1)
  // A picture zooms about the middle of its view; a PDF does that itself.
  const imageZoom = useRef<((z: Zoom) => void) | null>(null)
  const zoomTo = (z: Zoom) => (imageZoom.current ?? setZoom)(z)
  const zoomStep = (dir: 1 | -1) => zoomTo(zoomAbout(imageScale.current(), dir))
  // A PDF's pages: how many, the one at the top of the view, and a page to go
  // to. The zoom and its keys are the image's.
  const pdf = view.kind === 'pdf'
  const [pageCount, setPageCount] = useState(0)
  const [pageNow, setPageNow] = useState(0)
  const [pageGoTo, setPageGoTo] = useState<{ page: number; seq: number } | null>(null)
  const pageSeq = useRef(0)
  const toPage = (n: number) => {
    if (pageCount === 0) return
    setPageGoTo({ page: Math.max(0, Math.min(pageCount - 1, n)), seq: ++pageSeq.current })
  }
  const onPdfFail = useCallback((f: PdfFailure) => {
    setView({
      kind: 'error',
      message: f.kind === 'password' ? t('viewer.pdfPassword') : t('viewer.pdfUnreadable', { reason: f.reason }),
    })
  }, [])

  const text = view.kind === 'text' ? view.text : ''
  const binary = view.kind === 'binary'
  // A binary is drawn as hexdump -C lines in the same body, so find and go to
  // line work on its bytes as they do on a log (hex.ts).
  const lines = useMemo(() => (view.kind === 'binary' ? hexLines(view.bytes) : splitLines(text)), [view, text])
  // Structured text: a JSON document as a tree, delimited text as a table.
  // Each falls back to the line body -- code, coloured -- when it is not one:
  // a JSON file that does not parse, or one the server cut.
  const json = useMemo(
    () => (view.kind === 'text' && isJson(entry.name) ? parseJson(view.text, entry.name, view.truncated) : null),
    [view, entry.name],
  )
  // A notebook is JSON that is a notebook: cells, not a tree. One the server
  // cut is not JSON any more, and is the text it is.
  const notebook = useMemo(
    () => (view.kind === 'text' && !view.truncated && isNotebook(entry.name) ? parseNotebook(view.text) : null),
    [view, entry.name],
  )
  const table = useMemo(
    () => (view.kind === 'text' && isDelimited(entry.name) ? parseCsv(view.text, sniffDelimiter(view.text, entry.name)) : null),
    [view, entry.name],
  )
  // Three kinds of text. A document (Markdown) and a project's own page (HTML
  // or SVG through the sandbox) have a drawing that is not lines; everything
  // else is the line body, coloured when there is a grammar for it -- so find,
  // go to line, wrap and copy work on code exactly as on a log. Source is the
  // line body without colour, for every kind.
  const drawn =
    view.kind === 'text' &&
    (canRender(view.markup) || isMarkdown(entry.name) || json !== null || table !== null || notebook !== null)
  const lang = view.kind === 'text' && !drawn ? (grammarFor(entry.name) ?? (looksLikeDiff(text) ? 'diff' : null)) : null
  // A font is a binary drawn as a specimen; Source is its bytes. One past the
  // cap is only its bytes: a specimen loads the whole file.
  const font = view.kind === 'binary' && entry.size <= FONT_MAX_BYTES ? fontFormat(view.bytes) : null
  const rendered = drawn || lang !== null || font !== null
  const plain = (view.kind === 'text' && (source || !drawn)) || (binary && (source || font === null))
  // The tree and the table take find as well; they count their own matches.
  const structured = view.kind === 'text' && !source && (json !== null || table !== null)
  const [ext, setExt] = useState({ n: 0, capped: false })
  const onCount = useCallback((n: number, capped: boolean) => setExt({ n, capped }), [])
  // An SVG is a picture: drawn as one through <img>, which runs nothing, and
  // zoomed like one. Its source is the text it is.
  const svg = view.kind === 'text' && view.markup === 'svg' && !source
  const showsImage = view.kind === 'image' || svg
  // The grammar, loaded the first time a file in it is opened. Until it
  // arrives the body is plain, and colours in when it does.
  const [loaded, setLoaded] = useState<string | null>(null)
  useEffect(() => {
    if (!lang) return
    let live = true
    loadGrammar(lang).then(
      () => live && setLoaded(lang),
      () => {
        /* no colour; the text is still the text */
      },
    )
    return () => {
      live = false
    }
  }, [lang])
  const colour = !source && lang !== null && loaded === lang ? lang : null

  const found = useMemo(() => findMatches(plain ? lines : [], findOpen ? query : ''), [plain, lines, findOpen, query])
  const matchCount = plain ? found.matches.length : ext.n
  const matchCapped = plain ? found.capped : ext.capped

  // The focus starts in the dialog, not on its first button: a key pressed
  // the moment the viewer opens is meant for the file. Again on every file,
  // because the element that had it may have been this content's and is gone.
  // A layout effect, so it is in place before useDialogFocus looks.
  useLayoutEffect(() => {
    if (!boxRef.current?.contains(document.activeElement)) boxRef.current?.focus()
  }, [boxRef])

  const { prev, next } = neighbours(files, entry.path)

  const toggleWrap = () => {
    setWrap((w) => {
      setWrapPreference(!w)
      return !w
    })
  }
  const toggleNumbers = () => {
    setNumbers((n) => {
      setLineNumbersPreference(!n)
      return !n
    })
  }
  const openFind = () => {
    if (!plain && !structured && !pdf) return
    setGotoOpen(false)
    setFindOpen(true)
    // Selected, so a second Ctrl+F replaces the query instead of appending.
    requestAnimationFrame(() => findRef.current?.select())
  }
  const focusBody = () =>
    (boxRef.current?.querySelector<HTMLElement>('[data-testid="viewer-text"], [data-testid="viewer-pdf"]') ?? boxRef.current)?.focus()
  const closeFind = () => {
    setFindOpen(false)
    setGotoOpen(false)
    setCurrent(-1)
    focusBody()
  }
  const step = (dir: 1 | -1) => {
    const n = matchCount
    if (n === 0) return
    if (!plain) {
      // The tree, the table and a PDF bring the current match into view
      // themselves.
      setCurrent(stepMatch(current, n, dir))
      return
    }
    const i = current < 0 && dir === 1 ? firstMatchFrom(found.matches, topLine.current) : stepMatch(current, n, dir)
    setCurrent(i)
    setScrollTo({ line: found.matches[i].line, seq: ++scrollSeq.current })
  }
  const download = () => {
    const a = document.createElement('a')
    a.href = api.downloadURL(projectId, entry.path)
    a.download = entry.name
    a.click()
  }
  const copyPath = () => {
    copyTextInGesture(entry.path, (ok) => {
      setCopied(ok)
      if (ok) window.setTimeout(() => setCopied(false), 1500)
    })
  }

  const act = (a: ViewerAction) => {
    switch (a) {
      case 'close':
        onClose()
        return
      case 'closeFind':
        closeFind()
        return
      case 'prevFile':
        if (prev) onOpen(prev)
        return
      case 'nextFile':
        if (next) onOpen(next)
        return
      case 'find':
        openFind()
        return
      case 'goToLine':
        if (!plain && !(pdf && pageCount > 0)) return
        setFindOpen(false)
        setGotoOpen(true)
        requestAnimationFrame(() => gotoRef.current?.select())
        return
      case 'nextMatch':
        step(1)
        return
      case 'prevMatch':
        step(-1)
        return
      case 'toggleWrap':
        toggleWrap()
        return
      case 'toggleLineNumbers':
        toggleNumbers()
        return
      case 'toggleSource':
        if (rendered) setSource((s) => !s)
        return
      case 'download':
        download()
        return
      case 'copyPath':
        copyPath()
        return
      // An image's, and only while one is drawn.
      case 'zoomIn':
      case 'zoomOut':
        if (showsImage || pdf) zoomStep(a === 'zoomIn' ? 1 : -1)
        return
      case 'zoomReset':
        if (showsImage || pdf) zoomTo('fit')
        return

      case 'prevPage':
        toPage(pageNow - 1)
        return
      case 'nextPage':
        toPage(pageNow + 1)
        return
      case 'firstPage':
        toPage(0)
        return
      case 'lastPage':
        toPage(pageCount - 1)
        return
    }
  }

  const onKeyDown = (e: React.KeyboardEvent) => {
    const target = e.target as HTMLElement
    const inField = target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement
    const a = viewerAction(e.nativeEvent, { inField, findOpen: findOpen || gotoOpen, mac: isMac(), pages: pdf && pageCount > 0 })
    if (!a) return
    // Enter on a focused button is that button's; the match keys need the
    // find bar open, which viewerAction already checked.
    if ((a === 'nextMatch' || a === 'prevMatch') && gotoOpen) return
    e.preventDefault()
    e.stopPropagation()
    act(a)
  }

  useLayoutEffect(() => {
    keysRef.current = onKeyDown
  })

  const onTopLine = useCallback((n: number) => {
    topLine.current = n
  }, [])

  const goto = () => {
    const line = parseGoTo(gotoValue, pdf ? pageCount : lines.length)
    if (line === null) return
    if (pdf) toPage(line)
    else setScrollTo({ line, seq: ++scrollSeq.current })
    setGotoOpen(false)
    // The input that had the focus is about to go; without this it falls to
    // the page and the next key reaches nothing in the viewer.
    focusBody()
  }

  /** What the view that is not Source is called, for this file. */
  const drawnLabel = () => {
    if (svg || (view.kind === 'text' && view.markup === 'svg')) return t('viewer.picture')
    if (json !== null) return t('viewer.tree')
    if (notebook !== null) return t('viewer.notebook')
    if (font !== null) return t('viewer.specimen')
    if (table !== null) return t('viewer.table')
    if (drawn) return t('preview.rendered.short')
    return t('viewer.highlighted')
  }

  const trail = crumbs(entry.path, projectName)

  return (
    <div data-testid="viewer-content" data-kind={view.kind} className="contents">
      <header className="flex shrink-0 flex-col gap-1.5 border-b border-hairline px-3 py-2">
        <div className="flex min-w-0 items-center gap-2">
          <nav aria-label={t('viewer.path')} className="flex min-w-0 flex-1 items-center gap-0.5 text-vp-sm text-ink-2">
            {trail.map((c, i) => (
              // The directories nearest the file, as many as fit: one on a
              // phone, two on a tablet, three wider. With two on a phone both
              // shrank to a letter each and the trail read `f/r/`. The whole
              // path is the name's tooltip.
              <span
                key={c.path}
                // They do not shrink: a long name is what gets cut, not the
                // directories beside it. At most three, each at most 30%, so
                // the name always has a tenth of the row.
                className={`min-w-0 max-w-[30%] shrink-0 items-center gap-0.5 ${
                  i < trail.length - 3
                    ? 'hidden'
                    : i < trail.length - 2
                      ? 'hidden @2xl:flex'
                      : i < trail.length - 1
                        ? 'hidden @md:flex'
                        : 'flex'
                }`}
              >
                <button
                  type="button"
                  onClick={() => onReveal(c.path)}
                  data-testid="viewer-crumb"
                  data-path={c.path}
                  title={t('viewer.reveal', { name: safeText(c.name) })}
                  className="vp-press min-w-0 truncate rounded-md px-1 hover:bg-surface-2 hover:text-ink"
                >
                  {safeText(c.name)}
                </button>
                <span aria-hidden="true">/</span>
              </span>
            ))}
            <span className="min-w-0 truncate px-1 font-mono text-vp-base text-ink" title={safeText(entry.path)}>
              {safeText(entry.name)}
            </span>
          </nav>
          <span className="tabular shrink-0 text-vp-xs text-ink-2">{formatBytes(entry.size)}</span>
          <button
            type="button"
            onClick={onClose}
            data-testid="preview-close"
            title={t('preview.close')}
            aria-label={t('preview.close')}
            className="vp-control shrink-0"
          >
            <X size={14} />
          </button>
        </div>
        <div className="flex min-w-0 flex-wrap items-center gap-1.5">
          <button
            type="button"
            onClick={() => prev && onOpen(prev)}
            disabled={!prev}
            data-testid="viewer-prev"
            title={prev ? t('viewer.prev', { name: safeText(prev.name) }) : t('viewer.first')}
            aria-label={t('viewer.prevShort')}
            className="vp-control disabled:opacity-40"
          >
            <ChevronLeft size={14} />
          </button>
          <button
            type="button"
            onClick={() => next && onOpen(next)}
            disabled={!next}
            data-testid="viewer-next"
            title={next ? t('viewer.next', { name: safeText(next.name) }) : t('viewer.last')}
            aria-label={t('viewer.nextShort')}
            className="vp-control disabled:opacity-40"
          >
            <ChevronRight size={14} />
          </button>
          {rendered && (
            <div data-testid="preview-mode" role="tablist" className="vp-segmented shrink-0">
              {([false, true] as const).map((wantSource) => {
                // "Page" for a document or a page; highlighted code is not a page.
                const label = wantSource ? t('preview.source') : drawnLabel()
                const Icon = wantSource ? Code2 : Eye
                return (
                  <button
                    key={String(wantSource)}
                    type="button"
                    role="tab"
                    onClick={() => setSource(wantSource)}
                    data-testid={wantSource ? 'preview-as-source' : 'preview-as-page'}
                    aria-selected={source === wantSource}
                    className="vp-tab text-vp-xs"
                  >
                    <Icon size={11} className="shrink-0" />
                    {label}
                  </button>
                )
              })}
            </div>
          )}
          <span className="flex-1" />
          {(plain || structured || pdf) && (
            <button
              type="button"
              onClick={() => (findOpen ? closeFind() : openFind())}
              data-testid="viewer-find-toggle"
              aria-pressed={findOpen}
              title={t('viewer.find')}
              className="vp-control text-vp-xs"
            >
              <Search size={12} />
              <span className="hidden @md:inline">{t('viewer.findShort')}</span>
            </button>
          )}
          {/* Not for a binary: its rows are a fixed width and its offsets are
              its line numbers. */}
          {plain && !binary && (
            <>
              <button
                type="button"
                onClick={toggleWrap}
                data-testid="viewer-wrap"
                aria-pressed={wrap}
                title={t('viewer.wrap')}
                className="vp-control text-vp-xs"
              >
                <WrapText size={12} />
                <span className="hidden @md:inline">{t('viewer.wrapShort')}</span>
              </button>
              <button
                type="button"
                onClick={toggleNumbers}
                data-testid="viewer-numbers"
                aria-pressed={numbers}
                title={t('viewer.numbers')}
                className="vp-control text-vp-xs"
              >
                <Hash size={12} />
                <span className="hidden @md:inline">{t('viewer.numbersShort')}</span>
              </button>
            </>
          )}
          <button
            type="button"
            onClick={download}
            data-testid="preview-download"
            title={t('files.downloadOne', { name: safeText(entry.name) })}
            className="vp-control text-vp-xs"
          >
            <Download size={12} />
            <span className="hidden @md:inline">{t('files.download')}</span>
          </button>
        </div>
        {findOpen && (
          <div className="flex min-w-0 items-center gap-1.5" data-testid="viewer-findbar">
            <input
              ref={findRef}
              value={query}
              onChange={(e) => {
                setQuery(e.target.value)
                setCurrent(-1)
              }}
              autoFocus
              spellCheck={false}
              data-testid="viewer-find"
              aria-label={t('viewer.find')}
              placeholder={t('viewer.findPlaceholder')}
              className="min-w-0 flex-1 rounded-vp border border-hairline bg-surface-2 px-2 py-1 font-mono text-vp-sm text-ink outline-none focus:border-accent"
            />
            <span className="tabular shrink-0 text-vp-xs text-ink-2" data-testid="viewer-find-count" aria-live="polite">
              {query === ''
                ? ''
                : matchCount === 0
                  ? t('viewer.noMatches')
                  : t('viewer.matchOf', {
                      i: current < 0 ? '–' : (current + 1).toLocaleString(),
                      n: matchCount.toLocaleString() + (matchCapped ? '+' : ''),
                    })}
            </span>
            <button type="button" onClick={() => step(-1)} className="vp-control" aria-label={t('viewer.prevMatch')} title={t('viewer.prevMatch')}>
              <ChevronLeft size={13} className="rotate-90" />
            </button>
            <button type="button" onClick={() => step(1)} className="vp-control" aria-label={t('viewer.nextMatch')} title={t('viewer.nextMatch')}>
              <ChevronRight size={13} className="rotate-90" />
            </button>
            <button type="button" onClick={closeFind} className="vp-control" aria-label={t('viewer.closeFind')} title={t('viewer.closeFind')}>
              <X size={13} />
            </button>
          </div>
        )}
        {gotoOpen && (
          <form
            className="flex min-w-0 items-center gap-1.5"
            data-testid="viewer-gotobar"
            onSubmit={(e) => {
              e.preventDefault()
              goto()
            }}
          >
            <input
              ref={gotoRef}
              value={gotoValue}
              onChange={(e) => setGotoValue(e.target.value)}
              autoFocus
              inputMode="numeric"
              data-testid="viewer-goto"
              aria-label={pdf ? t('viewer.gotoPage') : t('viewer.goto')}
              placeholder={
                pdf
                  ? t('viewer.gotoPagePlaceholder', { n: pageCount.toLocaleString() })
                  : t('viewer.gotoPlaceholder', { n: lines.length.toLocaleString() })
              }
              className="min-w-0 flex-1 rounded-vp border border-hairline bg-surface-2 px-2 py-1 font-mono text-vp-sm text-ink outline-none focus:border-accent"
            />
            <button type="submit" className="vp-control text-vp-xs">
              {t('viewer.gotoGo')}
            </button>
            <button type="button" onClick={closeFind} className="vp-control" aria-label={t('viewer.closeFind')} title={t('viewer.closeFind')}>
              <X size={13} />
            </button>
          </form>
        )}
      </header>

      <Body
        view={view}
        entry={entry}
        projectId={projectId}
        scripts={scripts}
        onScripts={setScripts}
        plainBody={
          <TextBody
            lines={lines}
            wrap={wrap}
            numbers={numbers && !binary}
            matches={found.matches}
            current={current}
            scrollTo={scrollTo}
            onTopLine={onTopLine}
            lang={colour}
            diff={colour === 'diff'}
          />
        }
        plain={plain}
        drawnBody={
          notebook !== null && !source ? (
            <Suspense fallback={null}>
              <Notebook doc={notebook} projectId={projectId} path={entry.path} />
            </Suspense>
          ) : font !== null && !source ? (
            <Suspense fallback={null}>
              <FontSpecimen url={api.downloadURL(projectId, entry.path)} fileName={entry.name} format={font} />
            </Suspense>
          ) : null
        }
        pdfBody={
          view.kind === 'pdf' ? (
            <Suspense fallback={null}>
              <PdfView
                url={view.url}
                zoom={zoom}
                onZoom={setZoom}
                scaleRef={imageScale}
                query={findOpen ? query : ''}
                current={current}
                onCount={onCount}
                onPages={setPageCount}
                onPage={setPageNow}
                goTo={pageGoTo}
                onFail={onPdfFail}
              />
            </Suspense>
          ) : null
        }
        imageBody={
          showsImage ? (
            <ImageView
              url={api.inlineURL(projectId, entry.path)}
              alt={t('preview.imageAlt', { name: safeText(entry.name) })}
              zoom={zoom}
              onZoom={setZoom}
              onSize={(w, h) => setImageSize({ w, h })}
              scaleRef={imageScale}
              zoomRef={imageZoom}
            />
          ) : null
        }
        structured={
          !structured ? null : json !== null ? (
            <JsonTree value={json.value} query={findOpen ? query : ''} current={current} onCount={onCount} />
          ) : (
            <CsvTable rows={table!.rows} more={table!.more} query={findOpen ? query : ''} current={current} onCount={onCount} />
          )
        }
        download={download}
      />

      {showsImage && view.kind === 'image' && (
        <footer
          data-testid="viewer-footer"
          className="flex shrink-0 flex-wrap items-center gap-x-2 border-t border-hairline px-3 py-1.5 text-vp-xs text-ink-2"
        >
          {imageSize && (
            <>
              <span className="tabular" data-testid="viewer-image-size">
                {imageSize.w.toLocaleString()} × {imageSize.h.toLocaleString()}
              </span>
              <span aria-hidden="true">·</span>
            </>
          )}
          <span className="tabular">{formatBytes(entry.size)}</span>
          <span aria-hidden="true">·</span>
          <ZoomControls zoom={zoom} onStep={zoomStep} onTo={zoomTo} />
          {copied && <span className="text-ink">{t('viewer.pathCopied')}</span>}
        </footer>
      )}
      {pdf && (
        <footer
          data-testid="viewer-footer"
          className="flex shrink-0 flex-wrap items-center gap-x-2 border-t border-hairline px-3 py-1.5 text-vp-xs text-ink-2"
        >
          {pageCount > 0 && (
            <>
              <span className="tabular" data-testid="viewer-page" data-page={pageNow + 1} aria-live="polite">
                {t('viewer.pageOf', { i: (pageNow + 1).toLocaleString(), n: pageCount.toLocaleString() })}
              </span>
              <span aria-hidden="true">·</span>
            </>
          )}
          <span className="tabular">{formatBytes(entry.size)}</span>
          <span aria-hidden="true">·</span>
          <ZoomControls zoom={zoom} onStep={zoomStep} onTo={zoomTo} />
          {copied && <span className="text-ink">{t('viewer.pathCopied')}</span>}
        </footer>
      )}
      {view.kind === 'binary' && (
        <footer
          data-testid="viewer-footer"
          className="flex shrink-0 flex-wrap items-center gap-x-2 border-t border-hairline px-3 py-1.5 text-vp-xs text-ink-2"
        >
          <span className="tabular" data-testid="viewer-format">
            {font !== null && !source ? font : t('viewer.binary')}
          </span>
          {view.truncated && (
            <span data-testid="preview-truncated" className="min-w-0 flex-1 text-right">
              {t('viewer.truncated', { shown: formatBytes(view.bytes.length), size: formatBytes(entry.size) })}
            </span>
          )}
          {copied && <span className="text-ink">{t('viewer.pathCopied')}</span>}
        </footer>
      )}
      {view.kind === 'text' && (
        <footer
          data-testid="viewer-footer"
          className="flex shrink-0 flex-wrap items-center gap-x-2 gap-y-0.5 border-t border-hairline px-3 py-1.5 text-vp-xs text-ink-2"
        >
          {notebook !== null && !source && (
            <>
              <span className="tabular" data-testid="viewer-cells">
                {t('nb.cells', { n: notebook.cells.length.toLocaleString() })}
              </span>
              <span aria-hidden="true">·</span>
            </>
          )}
          {table !== null && !source && (
            <>
              <span className="tabular" data-testid="viewer-rows">
                {t('csv.rows', { n: Math.max(0, table.rows.length - 1).toLocaleString() })}
              </span>
              <span aria-hidden="true">·</span>
            </>
          )}
          <span className="tabular" data-testid="viewer-lines">
            {lines.length === 0
              ? t('preview.empty')
              : lines.length === 1
                ? t('preview.oneLine')
                : t('preview.lines', { n: lines.length.toLocaleString() })}
          </span>
          <span aria-hidden="true">·</span>
          <span data-testid="viewer-encoding">{view.encoding ?? 'UTF-8'}</span>
          {lineEndings(view.text) !== 'none' && (
            <>
              <span aria-hidden="true">·</span>
              <span data-testid="viewer-eol">
                {lineEndings(view.text) === 'mixed' ? t('viewer.eolMixed') : lineEndings(view.text)}
              </span>
            </>
          )}
          {view.truncated && (
            <span data-testid="preview-truncated" className="min-w-0 flex-1 text-right">
              {t('viewer.truncated', {
                shown: formatBytes(new TextEncoder().encode(view.text).length),
                size: formatBytes(entry.size),
              })}
            </span>
          )}
          {copied && <span className="text-ink">{t('viewer.pathCopied')}</span>}
        </footer>
      )}
    </div>
  )
}

/**
 * The zoom in a footer, as buttons: the keys and Ctrl with the wheel are
 * invisible, and a picture with nothing on screen to zoom it was a picture
 * that could not be zoomed (「图片要能缩放」).
 */
function ZoomControls({ zoom, onStep, onTo }: { zoom: Zoom; onStep: (dir: 1 | -1) => void; onTo: (z: Zoom) => void }) {
  return (
    <span className="ml-auto flex items-center gap-1" data-testid="viewer-zoom-controls">
      <button
        type="button"
        onClick={() => onStep(-1)}
        disabled={zoom !== 'fit' && zoom <= MIN_ZOOM}
        data-testid="viewer-zoom-out"
        aria-label={t('viewer.zoomOut')}
        title={t('viewer.zoomOut')}
        className="vp-control disabled:opacity-40"
      >
        <Minus size={12} />
      </button>
      <span className="tabular min-w-[3.5em] text-center" data-testid="viewer-zoom" aria-live="polite">
        {zoom === 'fit' ? t('viewer.fit') : percent(zoom)}
      </span>
      <button
        type="button"
        onClick={() => onStep(1)}
        disabled={zoom !== 'fit' && zoom >= MAX_ZOOM}
        data-testid="viewer-zoom-in"
        aria-label={t('viewer.zoomIn')}
        title={t('viewer.zoomIn')}
        className="vp-control disabled:opacity-40"
      >
        <Plus size={12} />
      </button>
      {/* Says where it goes: 100% from fitted, fitted from anything else. */}
      <button
        type="button"
        onClick={() => onTo(zoom === 'fit' ? 1 : 'fit')}
        data-testid="viewer-zoom-toggle"
        className="vp-control tabular text-vp-xs"
      >
        {zoom === 'fit' ? '100%' : t('viewer.fit')}
      </button>
    </span>
  )
}

function Body({
  view,
  entry,
  projectId,
  scripts,
  onScripts,
  plainBody,
  plain,
  drawnBody,
  pdfBody,
  imageBody,
  structured,
  download,
}: {
  view: View
  entry: FileEntry
  projectId: string
  scripts: boolean
  onScripts: (on: boolean) => void
  plainBody: React.ReactNode
  plain: boolean
  /** A drawing of its own that is neither find's tree nor table: a notebook. */
  drawnBody: React.ReactNode
  pdfBody: React.ReactNode
  imageBody: React.ReactNode
  structured: React.ReactNode
  download: () => void
}) {
  /** What the file is, how big, and the way out. */
  const cannotShow = (message: string) => (
    <div data-testid="preview-unavailable" className="flex flex-1 flex-col items-center justify-center gap-3 px-6 py-12 text-center">
      <FileQuestion size={20} className="text-ink-2" />
      <p className="max-w-sm text-vp-base text-ink-2">{message}</p>
      <button type="button" onClick={download} className="vp-control text-vp-sm">
        <Download size={12} />
        {t('files.download')}
      </button>
    </div>
  )
  switch (view.kind) {
    case 'loading':
      return (
        <p className="flex flex-1 items-center justify-center gap-2 px-6 py-12 text-vp-base text-ink-2">
          <Loader2 size={13} className="animate-spin" />
          {t('preview.loading')}
        </p>
      )
    case 'error':
      return cannotShow(safeText(view.message))
    case 'tooBig':
      return cannotShow(t('preview.tooBig', { size: formatBytes(entry.size), limit: formatBytes(PREVIEW_MAX_BYTES) }))
    case 'none':
      return cannotShow(t('preview.none', { size: formatBytes(entry.size) }))
    case 'image':
      return imageBody
    case 'pdf':
      return pdfBody
    case 'video':
      return (
        <div className="flex min-h-0 flex-1 bg-surface-2 p-3">
          {/* metadata only until it is played: a viewer opened to see what a
              file is should not pull the whole of it. */}
          <video src={view.url} controls preload="metadata" data-testid="preview-video" className="m-auto max-h-full max-w-full">
            {cannotShow(t('preview.mediaFallback'))}
          </video>
        </div>
      )
    case 'audio':
      return (
        <div className="flex min-h-0 flex-1 items-center justify-center bg-surface-2 p-6">
          <audio src={view.url} controls preload="metadata" data-testid="preview-audio" className="w-full max-w-xl">
            {cannotShow(t('preview.mediaFallback'))}
          </audio>
        </div>
      )
    case 'binary':
      return plain ? plainBody : drawnBody
    case 'text':
      if (plain) return plainBody
      if (drawnBody) return drawnBody
      if (structured) return structured
      if (imageBody) return imageBody
      // Not the line body, so a document or a page. The sandboxed page is
      // asked about first: the preview this replaced asked its highlighter
      // first, which claimed .html and .svg, so "Page" on a project's own page
      // drew its source in colour and the frame was never reached.
      if (!canRender(view.markup)) {
        return (
          <div className="min-h-0 flex-1 overflow-auto">
            <Suspense fallback={null}>
              <Markdown text={view.text} projectId={projectId} path={entry.path} />
            </Suspense>
          </div>
        )
      }
      return (
        <div className="flex min-h-0 flex-1 flex-col">
          <iframe
            // Keyed by the sandbox: `sandbox` is read when the document is
            // created, so turning scripts off on a loaded frame would leave the
            // running one running. A new key is a new document.
            key={scripts ? 'scripts' : 'static'}
            src={api.renderURL(projectId, entry.path, scripts)}
            sandbox={sandboxFor(scripts)}
            referrerPolicy="no-referrer"
            data-testid="preview-frame"
            data-scripts={scripts ? 'on' : 'off'}
            title={t('preview.rendered', { name: safeText(entry.name) })}
            // White: what is inside is somebody else's page and assumes one.
            className="min-h-0 flex-1 border-0 bg-white"
          />
          <div className="flex shrink-0 items-center justify-end border-t border-hairline px-3 py-1.5">
            <button
              type="button"
              onClick={() => onScripts(!scripts)}
              data-testid="preview-scripts"
              aria-pressed={scripts}
              className="vp-control text-vp-xs"
            >
              <Zap size={11} fill={scripts ? 'currentColor' : 'none'} />
              {scripts ? t('preview.scriptsOn') : t('preview.scriptsOff')}
            </button>
          </div>
        </div>
      )
  }
}
