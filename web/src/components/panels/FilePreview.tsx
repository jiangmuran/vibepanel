import { useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { Code2, Download, Eye, FileQuestion, Loader2, X, Zap } from 'lucide-react'

import { api } from '../../protocol/api'
import type { Markup } from '../../protocol/api'
import type { FileEntry } from '../../protocol/wire'
import { safeText } from '../text'
import { t, useLang } from '../../i18n'
import { countLines, formatBytes, tooBigToPreview, PREVIEW_MAX_BYTES } from './preview'
import { canRender, sandboxFor } from './render'
import { isMarkdown } from './markdown'
import { shouldHighlight } from './highlight'
import { Code, Markdown } from './Rendered'

/** What is on screen: a preview, the reason there is not one, or the wait. */
type View =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'text'; text: string; truncated: boolean; markup: Markup | null }
  | { kind: 'image'; url: string }
  | { kind: 'pdf'; url: string }
  | { kind: 'tooBig' }
  | { kind: 'none' }

/**
 * One file, shown over the whole window rather than inside the panel.
 *
 * The panel is 280 pixels by default and 200 at its narrowest, and that decides
 * this. A picture or a PDF at that width is not a preview, it is a thumbnail
 * with a scrollbar, and wrapped source at that width is a column of five words.
 * The panel's job is finding the file; reading it is a different job and wants
 * the window.
 *
 * A modal rather than a takeover of the panel, which was the other candidate.
 * A takeover has to restore the directory, the scroll position and the row you
 * were on when you come back, and it needs a back affordance that is not the
 * one already sitting in the header for "up one level" — two controls that look
 * alike and mean different things, in a column too narrow to label either. The
 * modal leaves the list untouched behind it, so closing is the restore.
 *
 * Through a portal, and that is not a preference. The panel carries `vp-blur`,
 * which is `backdrop-filter`, and an element with a backdrop-filter is a
 * containing block for `position: fixed` descendants — so a `fixed inset-0`
 * modal rendered inside the panel is clipped to the panel, which is exactly the
 * 280 pixels this exists to escape.
 */
export function FilePreview({
  projectId,
  entry,
  onClose,
}: {
  projectId: string
  entry: FileEntry
  onClose: () => void
}) {
  useLang()
  // Answered before any request when the listing's own size already settles
  // it. The server refuses it too — that is the enforcement — but a round trip
  // to be told what we could see from here is a round trip spent on a file
  // nobody is going to look at.
  const [view, setView] = useState<View>(() =>
    tooBigToPreview(entry.size) ? { kind: 'tooBig' } : { kind: 'loading' },
  )
  const boxRef = useRef<HTMLDivElement | null>(null)

  // Two pieces of state for a page, and both start in the safer position on
  // every file.
  //
  // `source` is the always-available way back to the bytes: a rendered document
  // is a drawing of what the file says, and the only honest view of a file an
  // agent wrote is the file. The control for it sits in the header at all
  // times, not behind a menu.
  //
  // `scripts` resets to false here and never persists — not to localStorage,
  // not across files, not across reopening the same file. A remembered "yes"
  // is a decision made about one document being applied to the next one, and
  // the next one is the one that was cloned this morning. The component is
  // keyed by path where it is used, so a different file remounts and both of
  // these start over.
  const [source, setSource] = useState(false)
  const [scripts, setScripts] = useState(false)

  useEffect(() => {
    if (tooBigToPreview(entry.size)) return
    // The object URL is created and revoked inside this one effect, so the
    // bytes are owned by exactly the thing that fetched them. A URL made during
    // render survives a render React throws away, and that is a leak of the
    // whole file.
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

  // Escape closes. A modal that can only be dismissed by finding its button is
  // one people close by reloading the page.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  // Focus moves into the dialog, which is what makes Escape work without
  // touching the mouse first and what makes a screen reader read the thing that
  // just appeared instead of the list behind it.
  useEffect(() => {
    boxRef.current?.focus()
  }, [])

  const download = (
    <a
      href={api.downloadURL(projectId, entry.path)}
      download={entry.name}
      data-testid="preview-download"
      title={t('files.downloadOne', { name: safeText(entry.name) })}
      className="vp-press inline-flex shrink-0 items-center gap-1 rounded-md border border-hairline px-2 py-1 text-vp-sm text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
    >
      <Download size={12} />
      {t('files.download')}
    </a>
  )

  /** The honest empty state: what the file is, how big, and the way out. */
  const cannotShow = (message: string) => (
    <div
      data-testid="preview-unavailable"
      className="flex flex-col items-center gap-3 px-6 py-12 text-center"
    >
      <FileQuestion size={20} className="text-ink-2" />
      <p className="max-w-sm text-vp-base text-ink-2">{message}</p>
      {download}
    </div>
  )

  const body = () => {
    switch (view.kind) {
      case 'loading':
        return (
          <p className="flex items-center justify-center gap-2 px-6 py-12 text-vp-base text-ink-2">
            <Loader2 size={13} className="animate-spin" />
            {t('preview.loading')}
          </p>
        )
      case 'error':
        return cannotShow(safeText(view.message))
      case 'tooBig':
        return cannotShow(
          t('preview.tooBig', {
            size: formatBytes(entry.size),
            limit: formatBytes(PREVIEW_MAX_BYTES),
          }),
        )
      case 'none':
        return cannotShow(t('preview.none', { size: formatBytes(entry.size) }))
      case 'image':
        return (
          <div className="flex min-h-0 flex-1 items-center justify-center overflow-auto bg-surface-2 p-3">
            <img
              src={view.url}
              alt={t('preview.imageAlt', { name: safeText(entry.name) })}
              data-testid="preview-image"
              className="max-h-full max-w-full object-contain"
            />
          </div>
        )
      case 'pdf':
        // <object> rather than <iframe>, for its children: a browser that will
        // not render a PDF inline shows them instead of an empty grey box, and
        // on a phone that is most of them.
        return (
          <object
            data={view.url}
            type="application/pdf"
            data-testid="preview-pdf"
            className="min-h-0 flex-1 bg-surface-2"
          >
            {cannotShow(t('preview.pdfFallback'))}
          </object>
        )
      case 'text': {
        // Markdown, laid out, and code with its keywords and strings apart.
        //
        // Both are the panel drawing React elements from the file's text --
        // never an HTML string, never `dangerouslySetInnerHTML`. That is the
        // same line the sandboxed iframe below draws for a project's *own*
        // HTML, and the reasoning is in markdown.ts: this origin holds a
        // session cookie that is a writable terminal.
        if (!source && isMarkdown(entry.name)) {
          return (
            <div className="flex min-h-0 flex-1 flex-col">
              <div className="min-h-0 flex-1 overflow-auto">
                <Markdown text={view.text} />
              </div>
              <TextFooter view={view} />
            </div>
          )
        }
        if (!source && shouldHighlight(entry.name)) {
          return (
            <div className="flex min-h-0 flex-1 flex-col">
              <div className="min-h-0 flex-1 overflow-auto p-2">
                <Code code={view.text} name={entry.name} />
              </div>
              <TextFooter view={view} />
            </div>
          )
        }
        if (canRender(view.markup) && !source) {
          return (
            <div className="flex min-h-0 flex-1 flex-col">
              <iframe
                // Keyed by the sandbox, not only pointed at a different URL.
                //
                // `sandbox` is read when the document is created. React
                // updating the attribute on a frame that has already loaded
                // changes the attribute and not the document, so turning
                // scripts back off would leave the running one running. A new
                // key is a new element, which is a new document.
                key={scripts ? 'scripts' : 'static'}
                src={api.renderURL(projectId, entry.path, scripts)}
                sandbox={sandboxFor(scripts)}
                referrerPolicy="no-referrer"
                data-testid="preview-frame"
                data-scripts={scripts ? 'on' : 'off'}
                title={t('preview.rendered', { name: safeText(entry.name) })}
                // White, not a surface token. What is inside the frame is
                // somebody else's document and assumes a page background; a
                // dark panel token behind unstyled black text is unreadable.
                className="min-h-0 flex-1 border-0 bg-white"
              />
              {/* The switch, and nothing beside it.
                *
                * There was a sentence here — "drawn in a sandbox, with no
                * network" — and a tooltip explaining that the switch resets
                * per file. Both were the panel defending itself to a reader
                * who had not objected, which is the thing that has now been
                * asked for three times. The button says which state it is in;
                * the isolation is written down in
                * internal/httpapi/preview_render.go, where somebody who wants
                * to know can read all of it. */}
              <div className="flex shrink-0 items-center justify-end border-t border-hairline px-3 py-1.5">
                <button
                  type="button"
                  onClick={() => setScripts(!scripts)}
                  data-testid="preview-scripts"
                  aria-pressed={scripts}
                  // `.vp-control`, which already draws `aria-pressed` as on.
                  // The bordered button this replaced was a fourth kind of
                  // button in a window that has three.
                  className="vp-control text-vp-xs"
                >
                  {/* Colour is not the carrier: the icon fills in and the word
                      changes with it. Red line 4. */}
                  <Zap size={11} fill={scripts ? 'currentColor' : 'none'} />
                  {scripts ? t('preview.scriptsOn') : t('preview.scriptsOff')}
                </button>
              </div>
            </div>
          )
        }
        return (
          <div className="flex min-h-0 flex-1 flex-col">
            {/* Wrapped, not scrolled sideways. Two scroll directions in one box
                means every long line is read by dragging, and source is the
                thing here with long lines. `break-words` is for the other case:
                a minified bundle is one line with no spaces in it, and without
                it that line decides the width of the dialog. */}
            <pre
              data-testid="preview-text"
              className="min-h-0 flex-1 overflow-auto px-3 py-2 font-mono text-vp-sm leading-relaxed whitespace-pre-wrap break-words text-ink"
            >
              {safeText(view.text)}
            </pre>
            <TextFooter view={view} />
          </div>
        )
      }
    }
  }

  return createPortal(
    <div
      className="vp-backdrop fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
      onClick={onClose}
      data-testid="preview-backdrop"
    >
      <div
        ref={boxRef}
        role="dialog"
        aria-modal="true"
        aria-label={t('preview.title')}
        tabIndex={-1}
        onClick={(e) => e.stopPropagation()}
        data-testid="file-preview"
        className="vp-panel-in flex max-h-[85vh] w-full max-w-3xl flex-col overflow-hidden rounded-vp border border-hairline bg-surface shadow-xl outline-none"
      >
        <div className="flex shrink-0 items-center gap-2 border-b border-hairline px-3 py-2.5">
          <span
            className="min-w-0 flex-1 truncate font-mono text-vp-base text-ink"
            title={safeText(entry.path)}
          >
            {/* A filename is whatever an agent wrote to disk, and this one is
                the largest text on screen. See safeText. */}
            {safeText(entry.name)}
          </span>
          <span className="tabular shrink-0 text-vp-xs text-ink-2">{formatBytes(entry.size)}</span>
          {/* The way back to the bytes, in the header rather than under the
              document. A rendered page can draw anything, a header it does not
              control included, so the control that says "show me the file
              instead" has to sit outside the frame and be visible without
              scrolling or hovering. */}
          {view.kind === 'text' &&
            (canRender(view.markup) || isMarkdown(entry.name) || shouldHighlight(entry.name)) && (
            // `.vp-segmented` and `.vp-tab`, the same track the side panel's
            // tabs sit in. This is a choice between two views of one thing,
            // which is what that vocabulary is for, and a hand-written copy of
            // it here is the drift the vocabulary was extracted to stop.
            //
            // `role="tablist"` without a `tabpanel`: the two buttons control
            // what fills the dialog body, and the body is not a sibling with
            // an id to point at. The pressed state carries the meaning either
            // way, and both are named in words at every width — there are two
            // of them, so nothing is competing for the room.
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
                    title={label}
                    className="vp-tab text-vp-xs"
                  >
                    <Icon size={11} className="shrink-0" />
                    {label}
                  </button>
                )
              })}
            </div>
          )}
          {download}
          <button
            type="button"
            onClick={onClose}
            data-testid="preview-close"
            title={t('preview.close')}
            className="vp-control"
          >
            <X size={14} />
          </button>
        </div>
        {body()}
      </div>
    </div>,
    document.body,
  )
}

/**
 * The line count under a text preview, and whether it was cut short.
 *
 * Extracted when markdown and highlighted code became two more views of the
 * same text: three copies of one footer is three places for the truncation
 * notice to be forgotten in, and the notice is the one that matters.
 */
function TextFooter({ view }: { view: { text: string; truncated: boolean } }) {
  const lines = countLines(view.text)
  return (
    <div className="flex shrink-0 items-center gap-2 border-t border-hairline px-3 py-1.5 text-vp-xs text-ink-2">
      <span className="tabular shrink-0">
        {lines === 0
          ? t('preview.empty')
          : lines === 1
            ? t('preview.oneLine')
            : t('preview.lines', { n: lines.toLocaleString() })}
      </span>
      {view.truncated && (
        <span data-testid="preview-truncated" className="min-w-0 flex-1">
          {t('preview.truncated', { n: lines.toLocaleString() })}
        </span>
      )}
    </div>
  )
}
