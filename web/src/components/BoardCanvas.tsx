import { useEffect, useRef, useState } from 'react'

import type { ShareBoard, ShareDashboard, ShareWidget } from '../protocol/wire'
import { t } from '../i18n'
import { Widget } from './board/render'
import { Sketch } from './board/sketch'
import { Tile } from './board/Tile'
import { forViewport } from './board/viewer'
import { DensityProvider } from './board/density'
import { pageWidgets } from './board/edit'
import { kindLabel } from './board/labels'

/**
 * The board, drawn at the shape of the screen it is for, and edited by hand.
 *
 * The preview *is* the editing surface. That is the whole change: a board used
 * to be built by choosing a preset in a form and then adjusting twenty-four
 * rows of dropdowns beside a picture of it — 「整个ui就是一团乱麻」 — and the
 * thing anybody actually wants is to pick a tile up and put it where it goes.
 *
 * Three properties are load-bearing and each cost a rewrite somewhere else in
 * this product to learn:
 *
 *   - **It draws the real board from the real payload.** `GET
 *     /api/settings/shares/{id}/preview` runs the *same builder* the dashboard
 *     endpoint runs, and this renders it through the *same* widget switch.
 *     Sample data would compose a layout against numbers that will not be the
 *     real ones, and a second reduction written on this side would diverge in
 *     the direction "the preview shows something the wall does not".
 *
 *   - **Pointer Events, a five-pixel threshold, Escape from anywhere.** The
 *     same idiom the side panel's tab drag uses, deliberately: HTML5 drag and
 *     drop never fires on touch, capturing on pointerdown paints every landing
 *     place for the length of a mouse press, and a drag you can only escape by
 *     finding a neutral place to release is one people abandon by dropping the
 *     tile somewhere they did not want it. Two different drag feels in one
 *     product is the "assembled" complaint in another form.
 *
 *   - **Every landing place is drawn for the whole gesture, with the one under
 *     the pointer filled.** A drop target that lights up only under the pointer
 *     is a guessing game.
 *
 * Geometry is read off the DOM at the moment of the move — `data-slot-index` is
 * on the element that draws the tile, so the rectangle and the index cannot
 * disagree — rather than out of a registry the tiles keep up to date. The tiles
 * are the thing being rearranged, so a registry of them is one more thing that
 * can be stale exactly when it is being used.
 */

/** How wide the inner board is drawn before it is scaled down.
 *
 *  Fixed rather than the container's, so the board collapses at the band the
 *  *target* screen is in rather than the band the editor's column is in. A wall
 *  board previewed inside a 400px panel would otherwise show a phone. */
const INNER_WIDTH = 1600

export type CanvasGesture = 'move' | 'span' | 'height'

export function BoardCanvas({
  board,
  data,
  page,
  selected,
  dropGap,
  dragging,
  viewportWidth,
  viewportHeight,
  onSelect,
  onGrab,
}: {
  board: ShareBoard
  /** Null until the first preview has landed. The frame is drawn either way:
   *  an empty box while it loads is indistinguishable from a board with
   *  nothing on it. */
  data: ShareDashboard | null
  page: number
  selected: number | null
  /** Which gap the pointer is aiming at, or null when nothing is being
   *  dragged. */
  dropGap: number | null
  dragging: boolean
  viewportWidth: number
  viewportHeight: number
  onSelect: (index: number | null) => void
  onGrab: (e: React.PointerEvent, index: number, gesture: CanvasGesture) => void
}) {
  const boxRef = useRef<HTMLDivElement>(null)
  const [scale, setScale] = useState(0.25)
  const [now, setNow] = useState(() => Math.floor(Date.now() / 1000))

  useEffect(() => {
    const timer = window.setInterval(() => setNow(Math.floor(Date.now() / 1000)), 1000)
    return () => clearInterval(timer)
  }, [])

  // Measured rather than assumed: the editor's column depends on the browser
  // window and on whether the settings modal is wide, and a stale scale is a
  // preview with a white margin or one that overflows its box.
  useEffect(() => {
    const box = boxRef.current
    if (!box) return
    const observer = new ResizeObserver(() => setScale(box.clientWidth / INNER_WIDTH))
    observer.observe(box)
    setScale(box.clientWidth / INNER_WIDTH)
    return () => observer.disconnect()
  }, [])

  const known = viewportWidth > 0 && viewportHeight > 0
  const ratio = known ? viewportWidth / viewportHeight : 16 / 9
  const innerHeight = INNER_WIDTH / ratio
  const entries = pageWidgets(board, page)

  return (
    <div
      ref={boxRef}
      data-testid="board-canvas"
      data-dragging={dragging ? 'true' : 'false'}
      className="relative overflow-hidden rounded-vp border border-hairline bg-bg"
      style={{ aspectRatio: `${Math.round(ratio * 100)} / 100` }}
      // A press on the background clears the selection, which is what makes
      // the inspector's "nothing selected" state reachable without a button.
      onPointerDown={() => onSelect(null)}
    >
      <div
        className="absolute left-0 top-0 origin-top-left p-6"
        style={{ width: INNER_WIDTH, height: innerHeight, transform: `scale(${scale})` }}
      >
        {/* The board is drawn at the target screen's scale and then shrunk, so
            what the owner is looking at is the composition the wall gets —
            including the type sizes, which is the half a schematic wireframe
            cannot show. `vp-wall` is the same class the dashboard's root
            carries; see styles.css for why scale is CSS and density is not. */}
        <DensityProvider value={board.density}>
          <div
            className="vp-wall vp-board h-full"
            data-fill={board.fill ? 'true' : 'false'}
            data-testid="canvas-board"
            // The base unit, pinned to the box being drawn rather than to the
            // window. `.vp-wall` computes it from `100vw`, and inside the
            // canvas that is the *editor's* window -- which would scale the
            // picture of the wall to the laptop looking at it. The same
            // arithmetic the rule uses, against INNER_WIDTH.
            style={{ ['--vp-wall' as string]: `${(INNER_WIDTH / 120).toFixed(2)}px` }}
          >
            {entries.length === 0 && (
              <p className="col-span-12 text-vp-2xl text-ink-3" data-testid="canvas-empty">
                {t('board.canvasEmpty')}
              </p>
            )}
            {entries.map(({ w, index }, position) => (
              <div
                key={`${w.kind}-${index}`}
                data-slot-index={index}
                data-slot-position={position}
                data-selected={selected === index ? 'true' : 'false'}
                className="relative min-w-0"
                style={{
                  gridColumn: `span ${forViewport(w, INNER_WIDTH).span}`,
                  gridRow: `span ${forViewport(w, INNER_WIDTH).height ?? 1}`,
                }}
              >
                {/* The tile itself, drawn from the real payload. It is inert:
                    every pointer event belongs to the gesture, not to whatever
                    the widget would have done with it.

                    The wrapper is the grid item, so the tile's own `gridColumn`
                    and `gridRow` land on a block element and do nothing — which
                    is what lets one component serve both the wall, where it
                    places itself, and the canvas, where the slot places it. */}
                <div className="pointer-events-none h-full [&>section]:h-full">
                  {data ? (
                    <Widget w={forViewport(w, INNER_WIDTH)} data={data} now={now} />
                  ) : (
                    <Ghost w={forViewport(w, INNER_WIDTH)} />
                  )}
                </div>

                {/* The grab surface. A button rather than a div so it is
                    reachable and announced without a second hidden control,
                    and so the keyboard handling in the editor has somewhere to
                    land. */}
                <button
                  type="button"
                  data-testid="canvas-grab"
                  data-index={index}
                  aria-label={t('board.grab', { name: kindLabel(w.kind, w.kind) })}
                  className="absolute inset-0 cursor-grab rounded-vp-lg border-2 border-transparent focus-visible:border-accent data-[on=true]:border-accent"
                  data-on={selected === index ? 'true' : 'false'}
                  onPointerDown={(e) => {
                    e.stopPropagation()
                    onGrab(e, index, 'move')
                  }}
                />
                {/* Width, by the right edge. Height, by the bottom. Two
                    separate handles rather than one corner: a wall board is
                    adjusted in one dimension at a time far more often than in
                    both, and a corner makes the common case the fiddly one. */}
                <span
                  role="separator"
                  aria-orientation="vertical"
                  aria-label={t('board.width')}
                  data-testid="canvas-span-handle"
                  className="absolute right-0 top-0 h-full w-4 cursor-col-resize"
                  onPointerDown={(e) => {
                    e.stopPropagation()
                    onGrab(e, index, 'span')
                  }}
                />
                <span
                  role="separator"
                  aria-orientation="horizontal"
                  aria-label={t('board.height')}
                  data-testid="canvas-height-handle"
                  className="absolute bottom-0 left-0 h-4 w-full cursor-row-resize"
                  onPointerDown={(e) => {
                    e.stopPropagation()
                    onGrab(e, index, 'height')
                  }}
                />

                {/* Every landing place, for the whole gesture. Drawn on both
                    sides of every tile rather than only under the pointer,
                    because a target that appears where the pointer already is
                    is a target nobody can aim at. */}
                {dragging && (
                  <>
                    <Marker side="left" on={dropGap === index} />
                    <Marker side="right" on={dropGap === index + 1} />
                  </>
                )}
              </div>
            ))}
          </div>
        </DensityProvider>
      </div>
    </div>
  )
}

/**
 * A tile with nothing to draw yet.
 *
 * Two comments in this repository said "the canvas still draws the
 * arrangement, because the rectangles are the board" — and it did not. A board
 * being *created* has no link, so it has no preview, so `data` was null and
 * every tile rendered nothing at all: six widgets on screen as an empty black
 * rectangle with invisible drag handles over it. The one place where the whole
 * point is to compose a layout before anything exists was the one place that
 * showed no layout.
 *
 * The same wireframe the palette entry was dragged from, at the size the tile
 * actually is, so what you are moving looks like what you picked up. Not
 * invented sample data: `board/preview.ts` says why numbers that will not be
 * the real ones are worse than no numbers.
 */
function Ghost({ w }: { w: ShareWidget }) {
  return (
    // Through `Tile`, not a div that looks like one. The frame, the hairline,
    // the padding and the heading size are what make a wall of tiles line up,
    // and a ghost drawn with its own approximation of them composes a layout
    // that is not the one being made. The `span`/`height` it sets on itself
    // are inert here -- the slot wrapper is the grid item -- which is the same
    // thing that lets a real widget serve both surfaces.
    <Tile
      label={kindLabel(w.kind, w.kind)}
      span={w.span}
      height={w.height}
      testid="canvas-ghost"
      kind={w.kind}
    >
      <Sketch kind={w.kind} className="h-full w-full opacity-60" />
    </Tile>
  )
}

/**
 * One landing place.
 *
 * Red line 4: the one under the pointer is filled and three times as wide, so
 * which target is live is carried by size as well as by hue. A photograph of
 * this screen at an angle still says where the tile is going.
 */
function Marker({ side, on }: { side: 'left' | 'right'; on: boolean }) {
  return (
    <span
      aria-hidden="true"
      data-testid="canvas-marker"
      data-on={on ? 'true' : 'false'}
      className={`pointer-events-none absolute top-0 h-full rounded-full ${
        side === 'left' ? '-left-1' : '-right-1'
      }`}
      style={{
        width: on ? 12 : 4,
        background: on ? 'var(--vp-accent)' : 'var(--vp-surface-2)',
        opacity: on ? 1 : 0.6,
      }}
    />
  )
}
