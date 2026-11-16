import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
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
import { formatBytes, tooBigToPreview, PREVIEW_MAX_BYTES } from '../panels/preview'
import { canRender, sandboxFor } from '../panels/render'
import { isMarkdown } from '../panels/markdown'
import { shouldHighlight } from '../panels/highlight'
import { Code, Markdown } from '../panels/Rendered'
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

/** What is on screen: a file, the reason there is not one, or the wait. */
type View =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'text'; text: string; truncated: boolean; markup: Markup | null }
  | { kind: 'image'; url: string }
  | { kind: 'pdf'; url: string }
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
 * Keyed by path where it is used, so every file starts in the safe state:
 * source off, scripts off, find closed. Wrap and line numbers are the
 * browser's (prefs.ts) and carry over.
 *
 * Batch 1 of the rebuild: the shell -- header, breadcrumb, previous and next,
 * find, go to line, wrap and line numbers, the text body, the footer, the
 * keyboard. Markdown, highlighted code, a project's HTML, images and PDFs are
 * still drawn by what drew them before, inside this shell, until the batch
 * that replaces each.
 */
export function FileViewer({
  projectId,
  projectName,
  entry,
  files,
  onOpen,
  onReveal,
  onClose,
}: {
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
}) {
  useLang()
  // Answered from the listing's size before any request: the server refuses
  // it too, which is the enforcement, but a round trip to be told what we can
  // see from here is a round trip spent on a file nobody is going to look at.
  const [view, setView] = useState<View>(() =>
    tooBigToPreview(entry.size) ? { kind: 'tooBig' } : { kind: 'loading' },
  )
  const boxRef = useRef<HTMLDivElement | null>(null)
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
    if (tooBigToPreview(entry.size)) return
    // The object URL is made and revoked inside this one effect, so the bytes
    // are owned by exactly the thing that fetched them.
    let url: string | null = null
    let cancelled = false
    api.preview(projectId, entry.path).then(
      (p) => {
        if (cancelled) return
        if (p.kind === 'image' || p.kind === 'pdf') {
          url = URL.createObjectURL(p.blob)
          setView({ kind: p.kind, url })
          return
        }
        setView(p)
      },
      (e: unknown) => {
        if (!cancelled) setView({ kind: 'error', message: e instanceof Error ? e.message : String(e) })
      },
    )
    return () => {
      cancelled = true
      if (url) URL.revokeObjectURL(url)
    }
  }, [projectId, entry.path, entry.size])

  const text = view.kind === 'text' ? view.text : ''
  const lines = useMemo(() => splitLines(text), [text])
  const rendered =
    view.kind === 'text' && (canRender(view.markup) || isMarkdown(entry.name) || shouldHighlight(entry.name))
  // The plain body is what find and go to line work in. A rendered document
  // or highlighted code is a drawing with no lines to step through yet; the
  // batches that replace them bring find with them.
  const plain = view.kind === 'text' && (source || !rendered)

  const found = useMemo(() => findMatches(plain ? lines : [], findOpen ? query : ''), [plain, lines, findOpen, query])

  // The focus starts in the body, not on the first button: a key pressed the
  // moment the viewer opens is meant for the file. In a layout effect so it
  // is in place before useDialogFocus looks.
  useLayoutEffect(() => {
    boxRef.current?.focus()
  }, [])
  useDialogFocus(boxRef, onClose, { escape: false })

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
    if (!plain) return
    setGotoOpen(false)
    setFindOpen(true)
    // Selected, so a second Ctrl+F replaces the query instead of appending.
    requestAnimationFrame(() => findRef.current?.select())
  }
  const closeFind = () => {
    setFindOpen(false)
    setGotoOpen(false)
    setCurrent(-1)
    boxRef.current?.querySelector<HTMLElement>('[data-testid="viewer-text"]')?.focus()
  }
  const step = (dir: 1 | -1) => {
    const n = found.matches.length
    if (n === 0) return
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
        if (!plain) return
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
      // Zoom is the image body's, in the batch that brings one.
      case 'zoomIn':
      case 'zoomOut':
      case 'zoomReset':
        return
    }
  }

  const onKeyDown = (e: React.KeyboardEvent) => {
    const target = e.target as HTMLElement
    const inField = target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement
    const a = viewerAction(e.nativeEvent, { inField, findOpen: findOpen || gotoOpen, mac: isMac() })
    if (!a) return
    // Enter on a focused button is that button's; the match keys need the
    // find bar open, which viewerAction already checked.
    if ((a === 'nextMatch' || a === 'prevMatch') && gotoOpen) return
    e.preventDefault()
    e.stopPropagation()
    act(a)
  }

  const onTopLine = useCallback((n: number) => {
    topLine.current = n
  }, [])

  const goto = () => {
    const line = parseGoTo(gotoValue, lines.length)
    if (line === null) return
    setScrollTo({ line, seq: ++scrollSeq.current })
    setGotoOpen(false)
  }

  const trail = crumbs(entry.path, projectName)

  return createPortal(
    <div
      className="vp-backdrop @container fixed inset-0 z-50 flex items-stretch justify-center bg-black/40 p-0 @2xl:items-center @2xl:p-4"
      onClick={onClose}
      data-testid="preview-backdrop"
    >
      <div
        ref={boxRef}
        role="dialog"
        aria-modal="true"
        aria-label={t('viewer.label', { name: safeText(entry.name) })}
        tabIndex={-1}
        onClick={(e) => e.stopPropagation()}
        onKeyDown={onKeyDown}
        data-testid="file-preview"
        data-kind={view.kind}
        className="vp-panel-in flex h-full w-full max-w-5xl flex-col overflow-hidden border-hairline bg-surface shadow-xl outline-none @2xl:h-[88vh] @2xl:rounded-vp @2xl:border"
      >
        <header className="flex shrink-0 flex-col gap-1.5 border-b border-hairline px-3 py-2">
          <div className="flex min-w-0 items-center gap-2">
            <nav aria-label={t('viewer.path')} className="flex min-w-0 flex-1 items-center gap-0.5 text-vp-sm text-ink-2">
              {trail.map((c, i) => (
                // On a narrow screen only the last two directories: the file's
                // name is the thing that has to fit.
                <span
                  key={c.path}
                  className={`min-w-0 shrink items-center gap-0.5 ${i < trail.length - 2 ? 'hidden @2xl:flex' : 'flex'}`}
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
                  const label = wantSource ? t('preview.source') : t('preview.rendered.short')
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
            {plain && (
              <>
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
                  : found.matches.length === 0
                    ? t('viewer.noMatches')
                    : t('viewer.matchOf', {
                        i: current < 0 ? '–' : (current + 1).toLocaleString(),
                        n: found.matches.length.toLocaleString() + (found.capped ? '+' : ''),
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
                aria-label={t('viewer.goto')}
                placeholder={t('viewer.gotoPlaceholder', { n: lines.length.toLocaleString() })}
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
              numbers={numbers}
              matches={found.matches}
              current={current}
              scrollTo={scrollTo}
              onTopLine={onTopLine}
            />
          }
          plain={plain}
          download={download}
        />

        {view.kind === 'text' && (
          <footer
            data-testid="viewer-footer"
            className="flex shrink-0 flex-wrap items-center gap-x-2 gap-y-0.5 border-t border-hairline px-3 py-1.5 text-vp-xs text-ink-2"
          >
            <span className="tabular" data-testid="viewer-lines">
              {lines.length === 0
                ? t('preview.empty')
                : lines.length === 1
                  ? t('preview.oneLine')
                  : t('preview.lines', { n: lines.length.toLocaleString() })}
            </span>
            <span aria-hidden="true">·</span>
            <span>UTF-8</span>
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
    </div>,
    document.body,
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
  download,
}: {
  view: View
  entry: FileEntry
  projectId: string
  scripts: boolean
  onScripts: (on: boolean) => void
  plainBody: React.ReactNode
  plain: boolean
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
      return (
        // `m-auto` on the image, not centring on the box: flex centring puts
        // the overflow of a picture larger than the window at negative offsets
        // a scroll container cannot reach, and its top-left is then gone
        // (「图片预览有bug 没法看到开头」).
        <div className="flex min-h-0 flex-1 overflow-auto bg-surface-2 p-3">
          <img
            src={view.url}
            alt={t('preview.imageAlt', { name: safeText(entry.name) })}
            data-testid="preview-image"
            className="m-auto max-h-full max-w-full object-contain"
          />
        </div>
      )
    case 'pdf':
      return (
        <object data={view.url} type="application/pdf" data-testid="preview-pdf" className="min-h-0 flex-1 bg-surface-2">
          {cannotShow(t('preview.pdfFallback'))}
        </object>
      )
    case 'text':
      if (plain) return plainBody
      if (isMarkdown(entry.name)) {
        return (
          <div className="min-h-0 flex-1 overflow-auto">
            <Markdown text={view.text} />
          </div>
        )
      }
      if (shouldHighlight(entry.name)) {
        return (
          <div className="min-h-0 flex-1 overflow-auto p-2">
            <Code code={view.text} name={entry.name} />
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
