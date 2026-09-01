import { useCallback, useEffect, useRef, useState } from 'react'
import { Trash2 } from 'lucide-react'

import type {
  ShareBoard,
  ShareCatalogue,
  ShareDashboard,
  SharePreset,
  ShareWidget,
  ShareWidgetSpec,
} from '../protocol/wire'
import { t } from '../i18n'
import { BoardCanvas, type CanvasGesture } from './BoardCanvas'
import { BoardGallery, BoardPalette } from './BoardPalette'
import {
  byLabel,
  filterLabel,
  groupLabel,
  kindLabel,
  metricLabel,
  orderLabel,
} from './board/labels'
import {
  DRAG_THRESHOLD,
  gapAt,
  insertWidget,
  moveWidget,
  removeWidget,
  snapHeight,
  snapSpan,
  widgetFrom,
  type Slot,
} from './board/edit'

/**
 * Where a board is made, by dragging it.
 *
 * This replaced a form. The form was a preset in a `<select>`, a list of every
 * widget as a row of eight dropdowns, and a picture of the result beside it —
 * 「整个ui就是一团乱麻 … 然后也没法修改」. What is here instead is the three
 * things that were asked for: a library you can see, a canvas you drag onto,
 * and templates you pick by looking at them.
 *
 * The gesture lives here rather than in the canvas or the palette, because one
 * gesture spans both: a press on a palette entry and a release over the canvas
 * is one drag, and the element that received the pointerdown is the one that
 * keeps the capture. So both children report `onGrab` upward and this file owns
 * what happens next — including the geometry, which is read off the DOM at the
 * moment of the move rather than out of a registry the tiles maintain.
 *
 * Everything it offers comes from the server's catalogue: the kinds, the
 * metrics, the widths, the bounds. A copy in this file is how a settings page
 * comes to offer a widget the server refuses, and the person who finds out is
 * the one who pressed the button.
 */

/** Rotation intervals worth offering. 0 is a board that does not move. */
const ROTATIONS = [0, 10, 20, 30, 60]

/** Day ranges a series is worth drawing over. */
const DAY_RANGES = [7, 14, 30, 90, 365]

const SELECT =
  'shrink-0 rounded-vp border border-hairline bg-surface-2 px-2 py-1 text-vp-sm text-ink outline-none focus:border-accent'

/** A select with its name beside it, wrapping as one piece. */
const FIELD = 'flex shrink-0 items-center gap-1.5 text-vp-sm text-ink-3'

/** What the press recorded, so the drag that follows can be arithmetic. */
type Press = {
  x: number
  y: number
  index: number
  span: number
  /** The tile's height in rows when the press landed. */
  height: number
  /** One row of the board in pixels, measured when the press landed. */
  row: number
}

/** What is being dragged, and where it would land. */
type Drag =
  | { kind: 'move'; index: number; gap: number | null }
  | { kind: 'add'; spec: ShareWidgetSpec; gap: number | null }
  | { kind: 'span'; index: number }
  | { kind: 'height'; index: number }

export function BoardEditor({
  board,
  catalogue,
  preview,
  linkID,
  viewportWidth,
  viewportHeight,
  onChange,
  onPickPreset,
}: {
  board: ShareBoard
  catalogue: ShareCatalogue
  /** The live payload the canvas draws, or null before the first one lands.
   *  Fetched by the parent so one poll serves the canvas and anything else on
   *  the page, and so this file has no fetch in it. */
  preview: ShareDashboard | null
  linkID: string
  viewportWidth: number
  viewportHeight: number
  onChange: (next: ShareBoard) => void
  /** Told which preset was chosen, not only what it expanded to.
   *
   *  A preset can carry more than widgets: one of them is only correct scoped
   *  to a single project in `counts` mode, because the failure there is a
   *  customer reading another customer's project name off the screen they were
   *  sat in front of. The form above applies those; this editor only knows
   *  about boards. */
  onPickPreset?: (preset: SharePreset) => void
}) {
  const [selected, setSelected] = useState<number | null>(null)
  const [page, setPage] = useState(0)
  const [drag, setDragState] = useState<Drag | null>(null)
  // The drag twice: once as state so it can be drawn, once in a ref so the
  // release handler can read it. A pointerup can arrive before React has
  // flushed the pointermove just before it -- a flick is exactly when those two
  // land together -- and committing the target from one move ago drops the tile
  // in the wrong place. The same reason the panel's tab drag keeps a ref.
  const live = useRef<Drag | null>(null)
  // The pressed widget's index lives in the ref beside the start point, not in
  // `selected`. A pointermove can arrive before React has flushed the
  // setSelected from the pointerdown just before it -- a flick is exactly when
  // those two land together -- and reading the state there loses the first
  // frames of the drag.
  const press = useRef<Press | null>(null)
  const rootRef = useRef<HTMLDivElement>(null)

  const setDrag = useCallback((next: Drag | null) => {
    live.current = next
    setDragState(next)
  }, [])

  // Escape cancels, from anywhere. A drag you can only get out of by finding a
  // neutral place to release is a drag people abandon by dropping the tile
  // somewhere they did not want it.
  useEffect(() => {
    if (!drag) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setDrag(null)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [drag, setDrag])

  /**
   * The tiles' rectangles, read off the DOM now.
   *
   * `data-slot-index` is on the element that draws the tile, so the rectangle
   * and the index cannot disagree. A map kept up to date by the tiles would be
   * one more thing that can be stale exactly while they are being rearranged.
   */
  const slots = useCallback((): Slot[] => {
    const root = rootRef.current
    if (!root) return []
    const out: Slot[] = []
    for (const el of root.querySelectorAll<HTMLElement>('[data-slot-index]')) {
      const r = el.getBoundingClientRect()
      if (r.width <= 0 || r.height <= 0) continue
      out.push({
        index: Number(el.dataset.slotIndex),
        left: r.left,
        top: Math.round(r.top),
        right: r.right,
        bottom: r.bottom,
      })
    }
    return out
  }, [])

  const setWidget = (index: number, next: ShareWidget) => {
    onChange({ ...board, widgets: board.widgets.map((w, i) => (i === index ? next : w)) })
  }

  const onGrabWidget = (e: React.PointerEvent, index: number, gesture: CanvasGesture) => {
    if (e.button !== 0) return
    e.currentTarget.setPointerCapture(e.pointerId)
    const w = board.widgets[index]
    const tile = tileOf(rootRef.current, index)?.getBoundingClientRect() ?? null
    press.current = {
      x: e.clientX,
      y: e.clientY,
      index,
      span: w?.span ?? 1,
      height: w?.height ?? 1,
      row: tile ? tile.height / Math.max(1, w?.height ?? 1) : 0,
    }
    setSelected(index)
    if (gesture === 'move') {
      // Not a drag yet. A threshold, so a plain press selects and only a real
      // movement paints every landing place on the board.
      setDrag(null)
    } else {
      setDrag({ kind: gesture, index })
    }
  }

  const onGrabSpec = (e: React.PointerEvent, spec: ShareWidgetSpec) => {
    if (e.button !== 0) return
    e.currentTarget.setPointerCapture(e.pointerId)
    // No row unit: a height drag starts on a tile's own handle, never on the
    // palette, so nothing here ever reads it.
    press.current = { x: e.clientX, y: e.clientY, index: -1, span: spec.span, height: 1, row: 0 }
    setDrag({ kind: 'add', spec, gap: null })
  }

  const onMove = (e: React.PointerEvent) => {
    const from = press.current
    if (!from) return
    const moved =
      Math.abs(e.clientX - from.x) > DRAG_THRESHOLD || Math.abs(e.clientY - from.y) > DRAG_THRESHOLD
    const current = live.current

    if (current?.kind === 'span') {
      const el = tileOf(rootRef.current, current.index)
      if (!el) return
      const r = el.getBoundingClientRect()
      const grid = gridOf(rootRef.current)
      if (!grid) return
      // Against the grid's own box rather than the tile's, so the width that
      // comes out is the fraction of the *board* the tile covers -- which is
      // what a span is. Measuring against the tile makes every drag relative to
      // wherever it started, and a tile can then never be made narrower than it
      // already is.
      const fraction = (e.clientX - r.left) / grid.width
      const next = snapSpan(fraction, catalogue.steps, catalogue.maxSpan)
      const w = board.widgets[current.index]
      if (w && w.span !== next) setWidget(current.index, { ...w, span: next })
      return
    }
    if (current?.kind === 'height') {
      // Nothing measured at the press means no unit to snap to, and snapHeight
      // answers that with one row -- so the tile would collapse on the first
      // move rather than stay where it is.
      if (from.row <= 0) return
      const el = tileOf(rootRef.current, current.index)
      if (!el) return
      const r = el.getBoundingClientRect()
      const next = heightDrag(e.clientY, r, from, catalogue.maxRows)
      const w = board.widgets[current.index]
      if (w && (w.height ?? 1) !== next) setWidget(current.index, { ...w, height: next })
      return
    }
    if (current?.kind === 'add') {
      setDrag({ ...current, gap: gapAt(slots(), e.clientX, e.clientY) })
      return
    }
    if (!moved && !current) return
    if (from.index < 0) return
    setDrag({ kind: 'move', index: from.index, gap: gapAt(slots(), e.clientX, e.clientY) })
  }

  const onUp = (e: React.PointerEvent) => {
    if (e.currentTarget.hasPointerCapture(e.pointerId)) {
      e.currentTarget.releasePointerCapture(e.pointerId)
    }
    press.current = null
    const current = live.current
    setDrag(null)
    if (!current) return
    if (current.kind === 'move' && current.gap !== null) {
      const next = moveWidget(board.widgets, current.index, current.gap)
      // moveWidget returns the same array when the drop changes nothing, which
      // is what stops a drag that went nowhere from rewriting the board and
      // re-saving it two seconds later.
      if (next !== board.widgets) onChange({ ...board, widgets: next })
    }
    if (current.kind === 'add') {
      if (board.widgets.length >= catalogue.maxWidgets) return
      const made = widgetFrom(current.spec)
      // Onto the page being edited, not always page 0: dropping a tile while
      // looking at page three and finding it on page one is a control that did
      // something other than what it looked like.
      if (page > 0) made.page = page
      const at = current.gap ?? board.widgets.length
      onChange({ ...board, widgets: insertWidget(board.widgets, made, at) })
      setSelected(at)
    }
  }

  /**
   * The keyboard equivalent of the drag.
   *
   * Not an afterthought: the canvas is the only way to arrange a board now, so
   * a pointer-only canvas would have removed the feature from anybody who
   * cannot use one. Arrows move the selected tile in the order; with shift they
   * resize it.
   */
  const onKeyDown = (e: React.KeyboardEvent) => {
    if (fromFormControl(e.target)) return
    if (selected === null) return
    const w = board.widgets[selected]
    if (!w) return
    const steps = catalogue.steps
    if (e.key === 'Delete' || e.key === 'Backspace') {
      e.preventDefault()
      onChange({ ...board, widgets: removeWidget(board.widgets, selected) })
      setSelected(null)
      return
    }
    if (e.shiftKey && (e.key === 'ArrowLeft' || e.key === 'ArrowRight')) {
      e.preventDefault()
      const i = steps.indexOf(w.span)
      const at = i < 0 ? 0 : i
      const to = Math.max(0, Math.min(steps.length - 1, at + (e.key === 'ArrowRight' ? 1 : -1)))
      setWidget(selected, { ...w, span: steps[to] ?? w.span })
      return
    }
    if (e.shiftKey && (e.key === 'ArrowUp' || e.key === 'ArrowDown')) {
      e.preventDefault()
      const next = Math.max(
        1,
        Math.min(catalogue.maxRows, (w.height ?? 1) + (e.key === 'ArrowDown' ? 1 : -1)),
      )
      setWidget(selected, { ...w, height: next })
      return
    }
    if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') {
      e.preventDefault()
      const next = moveWidget(board.widgets, selected, selected - 1)
      if (next !== board.widgets) {
        onChange({ ...board, widgets: next })
        setSelected(selected - 1)
      }
      return
    }
    if (e.key === 'ArrowRight' || e.key === 'ArrowDown') {
      e.preventDefault()
      const next = moveWidget(board.widgets, selected, selected + 2)
      if (next !== board.widgets) {
        onChange({ ...board, widgets: next })
        setSelected(selected + 1)
      }
    }
  }

  const onPreset = (preset: SharePreset) => {
    onChange({
      grid: catalogue.maxSpan,
      preset: preset.id,
      rotate: preset.rotate,
      fill: preset.fill,
      density: preset.density,
      widgets: [...preset.widgets],
    })
    setSelected(null)
    setPage(0)
    onPickPreset?.(preset)
  }

  const pages = board.widgets.reduce((most, w) => Math.max(most, (w.page ?? 0) + 1), 1)
  const full = board.widgets.length >= catalogue.maxWidgets
  const current = selected === null ? undefined : board.widgets[selected]
  const spec = current ? catalogue.widgets.find((s) => s.kind === current.kind) : undefined

  return (
    // `@container` on the root, so every `@` variant below asks how wide this
    // editor is rather than how wide the browser is.
    //
    // `lg:` asks about the window, and this editor is inside a modal whose
    // body is a fraction of it: on a 1600px desktop the old two-column rule
    // fired against 540px of actual space, which put a 208px canvas next to a
    // 320px palette -- the thing being arranged smaller than the list of
    // things to drag onto it. The dialog is wider for this group now
    // (settings/groups.ts), but the split still has to follow the space this
    // component *has*: it is also rendered inside each link's edit panel,
    // which is narrower again, and inside a browser window somebody has made
    // narrow, which the dialog's own max-width says nothing about.
    <div
      ref={rootRef}
      data-testid="board-editor"
      className="@container"
      onPointerMove={onMove}
      onPointerUp={onUp}
      onPointerCancel={() => {
        press.current = null
        setDrag(null)
      }}
      onKeyDown={onKeyDown}
    >
      <div className="grid gap-4 @3xl:grid-cols-[minmax(0,1fr)_20rem]">
        <div className="min-w-0">
          <BoardCanvas
            board={board}
            data={preview}
            page={page}
            selected={selected}
            dropGap={drag && 'gap' in drag ? drag.gap : null}
            dragging={drag !== null && 'gap' in drag}
            viewportWidth={viewportWidth}
            viewportHeight={viewportHeight}
            onSelect={setSelected}
            onGrab={onGrabWidget}
          />
          <p className="mt-1 text-vp-sm text-ink-3" data-testid="canvas-hint">
            {viewportWidth > 0
              ? t('share.viewport', { w: viewportWidth, h: viewportHeight })
              : t('share.previewCold')}
            {' · '}
            {t('board.widgets', { n: board.widgets.length })}
            {linkID === '' && ` · ${t('board.newLink')}`}
          </p>

          {/* `gap-x-4`, not `gap-2`. Each of these is a label with its control
              1.5 units away, so at a uniform 2 the space inside a field and
              the space between two fields were the same, and the row read as
              "Fill the screen Density | Normal | Rotate pages | Never". */}
          <div className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-2">
            {/* A checkbox, because this is a setting and not an action.
                It was a `.vp-control` with `aria-pressed`, which is transparent
                with no border: off, it was text sitting above two fields and
                read as their heading, and on, the only difference was that the
                text turned accent-coloured. Red line 4 -- the note on
                `[aria-pressed='true']` in styles.css says the split toggle is
                the only control allowed to lean on that colour, because two
                panes appearing below is what actually carries its state. This
                one had nothing else carrying it. A tick is a shape. */}
            <label className={FIELD}>
              <input
                type="checkbox"
                checked={board.fill}
                onChange={(e) => onChange({ ...board, fill: e.target.checked })}
                data-testid="board-fill"
              />
              {t('board.fill')}
            </label>
            {/* Density, and it is not a size control. See board/density.ts: how
                large everything is drawn follows the viewport and is settled in
                CSS; this is how much each tile says, so the same wall can be a
                headline from the door and a working dashboard from the chair. */}
            {/* Labelled, and visibly. These read "Normal" and "Never" on
                their own, and the row wraps to one control per line in the
                settings panel, so what arrived under the fill button was a
                heading and two dropdowns saying nothing about what they set.
                `title` does not fix that: it needs a pointer to appear, which
                a phone does not have, and a screen reader announces it only
                when there is no label. */}
            <label className={FIELD}>
              {t('board.density')}
              <select
                value={board.density}
                onChange={(e) => onChange({ ...board, density: Number(e.target.value) })}
                data-testid="board-density"
                className={SELECT}
              >
                {Array.from({ length: Math.max(1, catalogue.maxDensity) }, (_, i) => i + 1).map(
                  (n) => (
                    <option key={n} value={n}>
                      {t(densityKey(n, catalogue.maxDensity))}
                    </option>
                  ),
                )}
              </select>
            </label>
            <label className={FIELD}>
              {t('board.rotate')}
              <select
                value={board.rotate}
                onChange={(e) => onChange({ ...board, rotate: Number(e.target.value) })}
                data-testid="board-rotate"
                className={SELECT}
              >
                {ROTATIONS.map((s) => (
                  <option key={s} value={s}>
                    {s === 0 ? t('board.rotateNever') : t('board.rotateEvery', { n: s })}
                  </option>
                ))}
              </select>
            </label>
            {/* Which page is on the canvas. Only once the board rotates: a
                control for a feature that is switched off is one somebody sets
                and then waits for. */}
            {board.rotate > 0 && (
              <label className={FIELD}>
                {t('board.page')}
                <select
                  value={page}
                  onChange={(e) => setPage(Number(e.target.value))}
                  data-testid="canvas-page"
                  className={SELECT}
                >
                  {Array.from({ length: Math.min(pages + 1, 12) }, (_, n) => (
                    <option key={n} value={n}>
                      {t('board.pageOf', { n: n + 1 })}
                    </option>
                  ))}
                </select>
              </label>
            )}
          </div>
        </div>

        <div className="min-w-0">
          <BoardPalette catalogue={catalogue} full={full} onGrab={onGrabSpec} />

          {/* The inspector: one widget's settings, not twenty-four rows of
              them. What made the old editor unreadable was that every widget's
              controls were on screen at once; here the thing being adjusted is
              the thing that is selected on the canvas. */}
          <div className="mt-3" data-testid="widget-inspector">
            {!current || !spec ? (
              <p className="text-vp-sm text-ink-3" data-testid="inspector-empty">
                {t('board.pickOne')}
              </p>
            ) : (
              <div className="rounded-vp border border-hairline bg-surface-2 p-2">
                <div className="mb-2 flex items-center gap-2">
                  <span className="min-w-0 flex-1 truncate text-vp-base text-ink">
                    {kindLabel(current.kind, current.kind)}
                  </span>
                  <button
                    type="button"
                    onClick={() => {
                      onChange({ ...board, widgets: removeWidget(board.widgets, selected!) })
                      setSelected(null)
                    }}
                    title={t('board.remove')}
                    data-testid="widget-remove"
                    className="vp-control"
                  >
                    <Trash2 size={13} />
                  </button>
                </div>
                {/* Every one of these is labelled, and the gap is wide enough
                    that a label belongs to the select after it rather than to
                    the one before. This was a row of up to seven bare
                    dropdowns reading "Tokens per hour", "By project", "7/12",
                    "2 rows" -- values with nothing saying what they set, which
                    is the shape the whole editor was replaced for. `title` is
                    not a label: it needs a pointer a phone does not have. */}
                <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
                  {spec.metrics && (
                    <Choice
                      testid="widget-metric"
                      title={t('board.metric')}
                      value={current.metric ?? spec.metrics[0]}
                      options={spec.metrics}
                      label={metricLabel}
                      onPick={(v) => setWidget(selected!, { ...current, metric: v })}
                    />
                  )}
                  {spec.bys && (
                    <Choice
                      testid="widget-by"
                      title={t('board.by')}
                      value={current.by ?? spec.bys[0]}
                      options={spec.bys}
                      label={byLabel}
                      onPick={(v) => setWidget(selected!, { ...current, by: v })}
                    />
                  )}
                  {spec.filters && (
                    <Choice
                      testid="widget-filter"
                      title={t('board.filter')}
                      value={current.filter ?? spec.filters[0]}
                      options={spec.filters}
                      label={filterLabel}
                      onPick={(v) => setWidget(selected!, { ...current, filter: v })}
                    />
                  )}
                  {spec.orders && (
                    <Choice
                      testid="widget-order"
                      title={t('board.order')}
                      value={current.order ?? spec.orders[0]}
                      options={spec.orders}
                      label={orderLabel}
                      onPick={(v) => setWidget(selected!, { ...current, order: v })}
                    />
                  )}
                  {spec.groups && (
                    <Choice
                      testid="widget-group"
                      title={t('board.group')}
                      value={current.group ?? spec.groups[0]}
                      options={spec.groups}
                      label={groupLabel}
                      onPick={(v) => setWidget(selected!, { ...current, group: v })}
                    />
                  )}
                  {spec.days && (
                    <label className={FIELD}>
                      {t('board.days')}
                      <select
                        value={current.days ?? 30}
                        onChange={(e) =>
                          setWidget(selected!, { ...current, days: Number(e.target.value) })
                        }
                        data-testid="widget-days"
                        className={SELECT}
                      >
                        {DAY_RANGES.filter((d) => d <= catalogue.maxDays).map((d) => (
                          <option key={d} value={d}>
                            {t('dash.lastDays', { n: d })}
                          </option>
                        ))}
                      </select>
                    </label>
                  )}
                  {spec.rotate && (
                    <label className={FIELD}>
                      {t('board.rotateList')}
                      <select
                        value={current.rotate ?? 0}
                        onChange={(e) =>
                          setWidget(selected!, { ...current, rotate: Number(e.target.value) })
                        }
                        data-testid="widget-rotate"
                        className={SELECT}
                      >
                        {ROTATIONS.map((s) => (
                          <option key={s} value={s}>
                            {s === 0 ? t('board.rotateNever') : t('board.rotateEvery', { n: s })}
                          </option>
                        ))}
                      </select>
                    </label>
                  )}
                  {spec.text && (
                    <label className={`${FIELD} w-full`}>
                      {t('board.caption')}
                      <input
                        value={current.text ?? ''}
                        maxLength={catalogue.maxCaption}
                        onChange={(e) => setWidget(selected!, { ...current, text: e.target.value })}
                        placeholder={t('board.caption')}
                        data-testid="widget-text"
                        className="min-w-0 flex-1 rounded-vp border border-hairline bg-surface px-2 py-1 text-vp-base text-ink outline-none focus:border-accent"
                      />
                    </label>
                  )}
                  {/* Width and height are dragged on the canvas; the selects
                      stay because a drag is not reachable from a keyboard and
                      because an exact twelfth is easier to pick than to aim at. */}
                  <label className={FIELD}>
                    {t('board.width')}
                    <select
                      value={current.span}
                      onChange={(e) =>
                        setWidget(selected!, { ...current, span: Number(e.target.value) })
                      }
                      data-testid="widget-span"
                      className={SELECT}
                    >
                      {catalogue.steps
                        .filter((n) => n >= 1 && n <= catalogue.maxSpan)
                        .map((n) => (
                          <option key={n} value={n}>
                            {t('board.widthOf', { n })}
                          </option>
                        ))}
                    </select>
                  </label>
                  <label className={FIELD}>
                    {t('board.height')}
                    <select
                      value={current.height ?? 1}
                      onChange={(e) =>
                        setWidget(selected!, { ...current, height: Number(e.target.value) })
                      }
                      data-testid="widget-height"
                      className={SELECT}
                    >
                      {Array.from({ length: Math.max(1, catalogue.maxRows) }, (_, i) => i + 1).map(
                        (n) => (
                          <option key={n} value={n}>
                            {t('board.heightOf', { n })}
                          </option>
                        ),
                      )}
                    </select>
                  </label>
                  {board.rotate > 0 && (
                    <label className={FIELD}>
                      {t('board.page')}
                      <select
                        value={current.page ?? 0}
                        onChange={(e) =>
                          setWidget(selected!, { ...current, page: Number(e.target.value) })
                        }
                        data-testid="widget-page"
                        className={SELECT}
                      >
                        {Array.from({ length: Math.min(pages + 1, 12) }, (_, n) => (
                          <option key={n} value={n}>
                            {t('board.pageOf', { n: n + 1 })}
                          </option>
                        ))}
                      </select>
                    </label>
                  )}
                </div>
              </div>
            )}
          </div>

        </div>

        {/* The gallery, across both columns and below them.

            It used to be the third block in the 20rem column, under the
            palette and the inspector -- two dozen cards two-up in a narrow
            strip, which ran about 1400px long beside a canvas 400px tall and
            left the left half of this editor empty for the whole of it. Full
            width it is six-up and four rows, and it reads as what it is: the
            other way to start, under the thing you are arranging, rather than
            an appendix to the widget library. */}
        <div className="@3xl:col-span-2">
          <BoardGallery catalogue={catalogue} current={board.preset} onPick={onPreset} />
        </div>
      </div>
      {board.widgets.length === 0 && (
        <p className="mt-2 text-vp-sm" style={{ color: 'var(--vp-state-waiting)' }}>
          {t('board.empty')}
        </p>
      )}
    </div>
  )
}

function Choice({
  testid,
  title,
  value,
  options,
  label,
  onPick,
}: {
  testid: string
  title: string
  value: string
  options: string[]
  label: (id: string, fallback: string) => string
  onPick: (value: string) => void
}) {
  return (
    <label className={FIELD}>
      {title}
      <select
        value={value}
        onChange={(e) => onPick(e.target.value)}
        data-testid={testid}
        className={SELECT}
      >
        {options.map((o) => (
          <option key={o} value={o}>
            {label(o, o)}
          </option>
        ))}
      </select>
    </label>
  )
}

/** The word for one density step.
 *
 *  Keyed off the ends rather than off the number, so a server that grows a
 *  fourth step gets "in between" for it instead of an identifier on screen. */
function densityKey(n: number, max: number): 'board.densitySpare' | 'board.densityNormal' | 'board.densityDense' {
  if (n <= 1) return 'board.densitySpare'
  if (n >= max) return 'board.densityDense'
  return 'board.densityNormal'
}

/**
 * How many rows tall a drag of a tile's bottom edge has reached.
 *
 * The unit is `press.row`, measured once when the press landed, and that is the
 * whole reason this is a function rather than two lines in the move handler.
 * `tile` is re-read from the DOM on every move, so its height already contains
 * whatever this drag committed a moment ago; dividing it by the height the
 * press started at — `press.height`, which does not move — gives a unit wrong
 * by exactly that factor. The tile then flips between two row counts under a
 * pointer that is holding still (1, 2, 1, 2 at 165px down a one-row tile), and
 * releasing in the same place twice gives two different answers.
 */
export function heightDrag(
  y: number,
  tile: { top: number; height: number },
  press: Press,
  maxRows: number,
): number {
  return snapHeight(y - tile.top, press.row, maxRows)
}

/** The controls the board's own keys must keep their hands off. */
const FORM_CONTROLS = 'input, select, textarea, [contenteditable]'

/**
 * Whether a key was typed into a control rather than aimed at the canvas.
 *
 * The inspector's caption field and its seven selects are inside the element
 * the board's `onKeyDown` is on, and the inspector is only there while a widget
 * is selected — which is exactly when those keys are live. So without this,
 * Backspace to fix a typo in a caption deleted the widget instead, and
 * ArrowDown on the Height select reordered the board and left the value where
 * it was: the arrows `preventDefault` here, so the select never sees them.
 */
export function fromFormControl(target: unknown): boolean {
  return (target as Element | null)?.closest(FORM_CONTROLS) != null
}

function tileOf(root: HTMLElement | null, index: number): HTMLElement | null {
  return root?.querySelector<HTMLElement>(`[data-slot-index="${index}"]`) ?? null
}

function gridOf(root: HTMLElement | null): DOMRect | null {
  const el = root?.querySelector<HTMLElement>('[data-testid="canvas-board"]')
  return el ? el.getBoundingClientRect() : null
}
