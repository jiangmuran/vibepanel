import { useEffect, useRef, useState } from 'react'

import { api } from '../protocol/api'
import type { ShareBoard, ShareDashboard } from '../protocol/wire'
import { t } from '../i18n'
import { Widget } from './board/render'
import { forViewport } from './board/viewer'

/**
 * What that screen is showing, drawn at the shape of that screen.
 *
 * The problem this exists for is the whole problem: the owner is composing a
 * television they cannot see, from a laptop, and the two obvious ways to help
 * them are both worse than this one. Invented sample data composes a layout
 * against numbers that will not be the real ones. A second reduction of the
 * panel's state written on this side diverges from the real redaction on the
 * first field either side gains, and it diverges in the direction "the preview
 * shows something the real screen does not".
 *
 * So this fetches `GET /api/settings/shares/{id}/preview`, which is the *same
 * builder* the dashboard endpoint uses, and renders it through the *same*
 * widget switch. What is on this box and what is on the wall come from one
 * function and one renderer.
 *
 * The box is the reported viewport's aspect ratio when a screen has actually
 * opened the link, and 16:9 when none has. That distinction is said out loud
 * rather than defaulted quietly: "nothing has opened this yet" is exactly what
 * somebody about to hang a screen needs to know, and a confident 16:9 preview
 * of a link nothing is showing is a lie about the state of the room.
 */

/** How wide the inner board is drawn before it is scaled down.
 *
 *  A fixed width rather than the container's, so the preview collapses at the
 *  band the *target* screen is in rather than the band the editor's panel is
 *  in. A wall board previewed inside a 400px column would otherwise collapse to
 *  one column and show a phone. */
const INNER_WIDTH = 1600

/** How often the preview re-asks while the editor is open.
 *
 *  Slower than the dashboard's two seconds: this is a thumbnail beside an
 *  editor, not a wall. Fast enough that the machine line moves, which is what
 *  tells the owner the preview is live rather than a screenshot. */
const PREVIEW_MS = 5000

export function BoardPreview({
  linkID,
  board,
  viewportWidth,
  viewportHeight,
}: {
  linkID: string
  /** The board being edited, so the preview follows the editor rather than the
   *  last thing that was saved. The data behind it is the server's. */
  board: ShareBoard
  viewportWidth: number
  viewportHeight: number
}) {
  const [data, setData] = useState<ShareDashboard | null>(null)
  const [failed, setFailed] = useState(false)
  const [now, setNow] = useState(() => Math.floor(Date.now() / 1000))
  const boxRef = useRef<HTMLDivElement>(null)
  const [scale, setScale] = useState(0.25)

  useEffect(() => {
    let cancelled = false
    let timer = 0
    const tick = async () => {
      try {
        const next = await api.sharePreview(linkID)
        if (!cancelled) {
          setData(next)
          setFailed(false)
        }
      } catch {
        // A preview that cannot be fetched leaves the last one on screen and
        // says so once. It is a thumbnail: failing it must not take the editor
        // down with it.
        if (!cancelled) setFailed(true)
      }
      if (!cancelled) timer = window.setTimeout(() => void tick(), PREVIEW_MS)
    }
    void tick()
    return () => {
      cancelled = true
      clearTimeout(timer)
    }
  }, [linkID])

  useEffect(() => {
    const timer = window.setInterval(() => setNow(Math.floor(Date.now() / 1000)), 1000)
    return () => clearInterval(timer)
  }, [])

  // The scale is measured rather than assumed, because the editor's column
  // width depends on the browser window and on whether the settings modal is
  // wide. A stale scale is a preview with a white margin or one that overflows.
  useEffect(() => {
    const box = boxRef.current
    if (!box) return
    const observer = new ResizeObserver(() => {
      setScale(box.clientWidth / INNER_WIDTH)
    })
    observer.observe(box)
    setScale(box.clientWidth / INNER_WIDTH)
    return () => observer.disconnect()
  }, [])

  const known = viewportWidth > 0 && viewportHeight > 0
  const ratio = known ? viewportWidth / viewportHeight : 16 / 9
  const innerHeight = INNER_WIDTH / ratio
  // The first page only. A rotating board's later pages drawn into the same
  // grid would overlap into an arrangement that is on no screen anywhere, which
  // is worse than showing one page and letting the widget list below say the
  // rest.
  const widgets = board.widgets
    .filter((w) => (w.page ?? 0) === 0)
    .map((w) => forViewport(w, INNER_WIDTH))

  return (
    <div data-testid="board-preview">
      <div className="mb-2 flex flex-wrap items-baseline gap-x-3 text-vp-sm text-ink-3">
        <span>{t('share.preview')}</span>
        {known ? (
          <span className="tabular" data-testid="preview-viewport">
            {t('share.viewport', { w: viewportWidth, h: viewportHeight })}
          </span>
        ) : (
          <span data-testid="preview-cold">{t('share.previewCold')}</span>
        )}
        {failed && <span data-testid="preview-failed">{t('share.previewGone')}</span>}
      </div>
      <div
        ref={boxRef}
        className="relative overflow-hidden rounded-vp border border-hairline bg-bg"
        style={{ aspectRatio: `${Math.round(ratio * 100)} / 100` }}
      >
        {data && (
          <div
            className="absolute left-0 top-0 origin-top-left p-6"
            style={{ width: INNER_WIDTH, height: innerHeight, transform: `scale(${scale})` }}
            // Not interactive: it is a picture of a screen, and a click that
            // did something here would be a control on a preview of a wall
            // nobody can click.
            aria-hidden="true"
          >
            <div
              className="vp-board h-full"
              data-fill={board.fill ? 'true' : 'false'}
              data-testid="preview-board"
            >
              {widgets.map((w, i) => (
                <Widget key={`${w.kind}-${i}`} w={w} data={data} now={now} />
              ))}
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
