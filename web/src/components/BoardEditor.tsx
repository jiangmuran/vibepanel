import { ArrowDown, ArrowUp, Plus, Trash2 } from 'lucide-react'

import type { ShareBoard, ShareCatalogue, SharePreset, ShareWidget } from '../protocol/wire'
import { t } from '../i18n'
import {
  byLabel,
  filterLabel,
  groupLabel,
  kindLabel,
  metricLabel,
  orderLabel,
  presetLabel,
  presetWhy,
  screenLabel,
} from './board/labels'

/**
 * Where a board is made.
 *
 * A preset is a starting point rather than a mode: choosing one drops its
 * widgets in, and from that moment every one of them can be moved, resized,
 * pointed at a different number, split by a different dimension or thrown away.
 * That is the whole difference between "a template" and "a fixed picture with a
 * name", and it is why the preset select does not stay in charge of anything
 * after it is used.
 *
 * Every control here is built from the server's own catalogue — the kinds, the
 * metrics, the dimensions, the bounds. Not from a copy in this file, because a
 * copy is how a settings page comes to offer a widget the server refuses, and
 * the person who finds out is the one who pressed the button.
 */

/**
 * The widths worth offering, from the server's own catalogue.
 *
 * Not all twelve, and not a list in this file: a select with twelve entries is
 * a control nobody can aim at, and a second copy of which fractions are worth
 * offering is how an editor comes to offer a width no preset uses and miss one
 * every preset does.
 */
function spans(catalogue: ShareCatalogue): number[] {
  return catalogue.steps.filter((n) => n >= 1 && n <= catalogue.maxSpan)
}

/** How many grid rows tall. Heights are what make a hero a hero. */
function heights(max: number): number[] {
  return Array.from({ length: Math.max(1, max) }, (_, i) => i + 1)
}

/** Rotation intervals worth offering. 0 is a board that does not move. */
const ROTATIONS = [0, 10, 20, 30, 60]

/** Day ranges a bar chart is worth drawing over. */
const DAY_RANGES = [7, 14, 30, 90, 365]

const SELECT =
  'shrink-0 rounded-vp border border-hairline bg-surface-2 px-2 py-1 text-vp-sm text-ink outline-none focus:border-accent'

export function BoardEditor({
  board,
  catalogue,
  onChange,
  onPickPreset,
}: {
  board: ShareBoard
  catalogue: ShareCatalogue
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
  const onPreset = (preset: SharePreset) => {
    onChange({
      grid: catalogue.maxSpan,
      preset: preset.id,
      rotate: preset.rotate,
      fill: preset.fill,
      widgets: [...preset.widgets],
    })
    onPickPreset?.(preset)
  }
  const specOf = (kind: string) => catalogue.widgets.find((s) => s.kind === kind)

  const setWidget = (index: number, next: ShareWidget) => {
    const widgets = board.widgets.map((w, i) => (i === index ? next : w))
    onChange({ ...board, widgets })
  }
  const move = (index: number, by: number) => {
    const to = index + by
    if (to < 0 || to >= board.widgets.length) return
    const widgets = [...board.widgets]
    const [row] = widgets.splice(index, 1)
    widgets.splice(to, 0, row)
    onChange({ ...board, widgets })
  }
  const remove = (index: number) => {
    onChange({ ...board, widgets: board.widgets.filter((_, i) => i !== index) })
  }
  const add = (kind: string) => {
    if (!kind) return
    const spec = specOf(kind)
    if (!spec || board.widgets.length >= catalogue.maxWidgets) return
    // The first allowed value for each setting, so a widget added from the
    // palette is one the server already accepts. A metric left empty is
    // refused, and refusing on save something the editor offered is the
    // failure this whole file is arranged to avoid.
    const w: ShareWidget = { kind, span: spec.span }
    if (spec.metrics && spec.metrics.length > 0) w.metric = spec.metrics[0]
    if (spec.bys && spec.bys.length > 0) w.by = spec.bys[0]
    onChange({ ...board, widgets: [...board.widgets, w] })
  }

  const pages = board.widgets.reduce((most, w) => Math.max(most, (w.page ?? 0) + 1), 1)
  const full = board.widgets.length >= catalogue.maxWidgets

  return (
    <div data-testid="board-editor">
      <div className="mb-2 flex flex-wrap items-center gap-2">
        {/* Grouped by the screen it was composed for rather than by audience.
            "Which of twenty-four do I want" is a question nobody can answer;
            "what am I putting this on" is one everybody can. The audience is
            still in the sentence under it. */}
        <select
          value={board.preset}
          onChange={(e) => {
            const preset = catalogue.presets.find((p) => p.id === e.target.value)
            if (!preset) return
            onPreset(preset)
          }}
          title={t('board.preset')}
          data-testid="board-preset"
          className={SELECT}
        >
          {catalogue.screens.map((screen) => (
            <optgroup key={screen} label={screenLabel(screen)}>
              {catalogue.presets
                .filter((p) => p.screen === screen)
                .map((p) => (
                  <option key={p.id} value={p.id}>
                    {presetLabel(p.id)}
                  </option>
                ))}
            </optgroup>
          ))}
        </select>
        <button
          type="button"
          onClick={() => onChange({ ...board, fill: !board.fill })}
          aria-pressed={board.fill}
          title={t('board.fill')}
          data-testid="board-fill"
          className="vp-control px-2"
        >
          {t('board.fill')}
        </button>
        <select
          value={board.rotate}
          onChange={(e) => onChange({ ...board, rotate: Number(e.target.value) })}
          title={t('board.rotate')}
          data-testid="board-rotate"
          className={SELECT}
        >
          {ROTATIONS.map((s) => (
            <option key={s} value={s}>
              {s === 0 ? t('board.rotateNever') : t('board.rotateEvery', { n: s })}
            </option>
          ))}
        </select>
        <span className="text-vp-sm text-ink-3">{t('board.widgets', { n: board.widgets.length })}</span>
      </div>

      {board.preset && (
        <p className="mb-2 text-vp-sm leading-relaxed text-ink-3" data-testid="board-preset-why">
          {presetWhy(board.preset)}
        </p>
      )}

      {board.widgets.map((w, i) => {
        const spec = specOf(w.kind)
        return (
          <div
            key={`${w.kind}-${i}`}
            data-testid="board-widget"
            data-kind={w.kind}
            className="mb-1 flex flex-wrap items-center gap-1 rounded-vp border border-hairline bg-surface-2 px-2 py-1.5"
          >
            <span className="min-w-0 flex-1 truncate text-vp-base text-ink">
              {kindLabel(w.kind, w.kind)}
            </span>

            {spec?.metrics && (
              <select
                value={w.metric ?? spec.metrics[0]}
                onChange={(e) => setWidget(i, { ...w, metric: e.target.value })}
                title={t('board.metric')}
                data-testid="widget-metric"
                className={SELECT}
              >
                {spec.metrics.map((m) => (
                  <option key={m} value={m}>
                    {metricLabel(m, m)}
                  </option>
                ))}
              </select>
            )}
            {spec?.bys && (
              <select
                value={w.by ?? spec.bys[0]}
                onChange={(e) => setWidget(i, { ...w, by: e.target.value })}
                title={t('board.by')}
                data-testid="widget-by"
                className={SELECT}
              >
                {spec.bys.map((b) => (
                  <option key={b} value={b}>
                    {byLabel(b, b)}
                  </option>
                ))}
              </select>
            )}
            {spec?.filters && (
              <select
                value={w.filter ?? spec.filters[0]}
                onChange={(e) => setWidget(i, { ...w, filter: e.target.value })}
                title={t('board.filter')}
                data-testid="widget-filter"
                className={SELECT}
              >
                {spec.filters.map((f) => (
                  <option key={f} value={f}>
                    {filterLabel(f, f)}
                  </option>
                ))}
              </select>
            )}
            {spec?.orders && (
              <select
                value={w.order ?? spec.orders[0]}
                onChange={(e) => setWidget(i, { ...w, order: e.target.value })}
                title={t('board.order')}
                data-testid="widget-order"
                className={SELECT}
              >
                {spec.orders.map((o) => (
                  <option key={o} value={o}>
                    {orderLabel(o, o)}
                  </option>
                ))}
              </select>
            )}
            {spec?.groups && (
              <select
                value={w.group ?? spec.groups[0]}
                onChange={(e) => setWidget(i, { ...w, group: e.target.value })}
                title={t('board.group')}
                data-testid="widget-group"
                className={SELECT}
              >
                {spec.groups.map((g) => (
                  <option key={g} value={g}>
                    {groupLabel(g, g)}
                  </option>
                ))}
              </select>
            )}
            {spec?.days && (
              <select
                value={w.days ?? 30}
                onChange={(e) => setWidget(i, { ...w, days: Number(e.target.value) })}
                title={t('board.days')}
                data-testid="widget-days"
                className={SELECT}
              >
                {DAY_RANGES.filter((d) => d <= catalogue.maxDays).map((d) => (
                  <option key={d} value={d}>
                    {t('dash.lastDays', { n: d })}
                  </option>
                ))}
              </select>
            )}
            {spec?.text && (
              <input
                value={w.text ?? ''}
                maxLength={catalogue.maxCaption}
                onChange={(e) => setWidget(i, { ...w, text: e.target.value })}
                placeholder={t('board.caption')}
                data-testid="widget-text"
                className="min-w-0 flex-1 rounded-vp border border-hairline bg-surface px-2 py-1 text-vp-base text-ink outline-none focus:border-accent"
              />
            )}
            {spec?.rotate && (
              <select
                value={w.rotate ?? 0}
                onChange={(e) => setWidget(i, { ...w, rotate: Number(e.target.value) })}
                title={t('board.rotateList')}
                data-testid="widget-rotate"
                className={SELECT}
              >
                {ROTATIONS.map((s) => (
                  <option key={s} value={s}>
                    {s === 0 ? t('board.rotateNever') : t('board.rotateEvery', { n: s })}
                  </option>
                ))}
              </select>
            )}

            <select
              value={w.span}
              onChange={(e) => setWidget(i, { ...w, span: Number(e.target.value) })}
              title={t('board.width')}
              data-testid="widget-span"
              className={SELECT}
            >
              {spans(catalogue).map((n) => (
                <option key={n} value={n}>
                  {t('board.widthOf', { n })}
                </option>
              ))}
            </select>
            {/* The other half of the hero/texture ratio. A screen where every
                tile is the same size is a dashboard, not a display, and a span
                alone cannot make one thing four times the area of the rest. */}
            <select
              value={w.height ?? 1}
              onChange={(e) => setWidget(i, { ...w, height: Number(e.target.value) })}
              title={t('board.height')}
              data-testid="widget-height"
              className={SELECT}
            >
              {heights(catalogue.maxRows).map((n) => (
                <option key={n} value={n}>
                  {t('board.heightOf', { n })}
                </option>
              ))}
            </select>
            {/* The page selector only appears once a board actually rotates.
                A control for a feature that is switched off is one somebody
                sets and then waits for. */}
            {board.rotate > 0 && (
              <select
                value={w.page ?? 0}
                onChange={(e) => setWidget(i, { ...w, page: Number(e.target.value) })}
                title={t('board.page')}
                data-testid="widget-page"
                className={SELECT}
              >
                {Array.from({ length: Math.min(pages + 1, 12) }, (_, n) => (
                  <option key={n} value={n}>
                    {t('board.pageOf', { n: n + 1 })}
                  </option>
                ))}
              </select>
            )}

            <button
              type="button"
              onClick={() => move(i, -1)}
              title={t('board.up')}
              data-testid="widget-up"
              className="vp-control"
            >
              <ArrowUp size={13} />
            </button>
            <button
              type="button"
              onClick={() => move(i, 1)}
              title={t('board.down')}
              data-testid="widget-down"
              className="vp-control"
            >
              <ArrowDown size={13} />
            </button>
            <button
              type="button"
              onClick={() => remove(i)}
              title={t('board.remove')}
              data-testid="widget-remove"
              className="vp-control"
            >
              <Trash2 size={13} />
            </button>
          </div>
        )
      })}

      <div className="mt-2 flex flex-wrap items-center gap-2">
        <Plus size={13} className="shrink-0 text-ink-2" />
        <select
          value=""
          disabled={full}
          onChange={(e) => add(e.target.value)}
          title={t('board.add')}
          data-testid="board-add"
          className={SELECT}
        >
          <option value="">{t('board.add')}</option>
          {catalogue.widgets.map((s) => (
            <option key={s.kind} value={s.kind}>
              {kindLabel(s.kind, s.kind)}
            </option>
          ))}
        </select>
        {full && (
          <span className="text-vp-sm text-ink-3">
            {t('board.full', { n: catalogue.maxWidgets })}
          </span>
        )}
        {board.widgets.length === 0 && (
          <span className="text-vp-sm" style={{ color: 'var(--vp-state-waiting)' }}>
            {t('board.empty')}
          </span>
        )}
      </div>
    </div>
  )
}
