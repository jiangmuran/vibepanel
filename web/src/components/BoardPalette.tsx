import type { ShareCatalogue, SharePreset, ShareWidgetSpec } from '../protocol/wire'
import { t } from '../i18n'
import { kindLabel, presetLabel, presetWhy, screenLabel } from './board/labels'
import { Sketch } from './board/sketch'

/**
 * The widget library, and the template gallery beside it.
 *
 * Both exist because of the same sentence: 「我希望可以有一个小组件库 然后我简单
 * 拖拽布局排版就能生成 也有很多排好的模版」. There were already thirty-seven
 * widget kinds and two dozen templates; the complaint was not that there were
 * too few, it was that both were `<select>` elements. A dropdown of thirty-seven
 * names is a catalogue nobody reads. The same thirty-seven as small pictures is
 * a thing you shop from.
 *
 * **The pictures are schematic, not live.** A palette of thirty-seven live
 * widgets is thirty-seven renders of the whole payload on every poll, and a
 * gallery of two dozen live boards is worse. What a picture has to carry here
 * is *shape*: how wide the thing is by default, and whether it is a figure, a
 * chart, a list or a rule. That is exactly what a wireframe says and it costs
 * nothing. The real thing is one drag away on the canvas, drawn from the real
 * payload.
 *
 * The grouping is the catalogue's own — by what a widget tells you rather than
 * by which table it came from — because "which of thirty-seven do I want" is a
 * question nobody can answer and "what am I trying to see" is one everybody can.
 */

/**
 * The widget library. One press starts a drag onto the canvas; releasing over
 * the canvas drops it where the marker is.
 */
export function BoardPalette({
  catalogue,
  full,
  onGrab,
}: {
  catalogue: ShareCatalogue
  /** The board is at MaxWidgets. The entries stay visible and stop responding,
   *  because a palette that empties itself is a palette somebody thinks has
   *  broken. */
  full: boolean
  onGrab: (e: React.PointerEvent, spec: ShareWidgetSpec) => void
}) {
  return (
    <div data-testid="board-palette">
      <div className="mb-2 flex items-baseline gap-3">
        <span className="text-vp-md text-ink">{t('board.palette')}</span>
        {full && (
          <span className="text-vp-sm text-ink-3">
            {t('board.full', { n: catalogue.maxWidgets })}
          </span>
        )}
      </div>
      {/* A box with a border, and it scrolls inside it.

          The scroller is not new; the border is. Thirty-seven entries do not
          fit anywhere sensible, so the list has always been cut off — but cut
          off against the background it read as a row of tiles chopped in half
          by a bug. Inside a frame, the same cut says "there is more here".
          `@sm` rather than `sm`: this is a 20rem column inside a modal, and
          the browser being 640px wide says nothing about it. The column count
          goes back *down* at `@3xl` because that is where the editor splits
          in two and this stops being the full width of it. */}
      <div className="grid max-h-64 grid-cols-2 gap-1 overflow-y-auto rounded-vp border border-hairline p-1 @sm:grid-cols-3 @xl:grid-cols-4 @3xl:grid-cols-3">
        {catalogue.widgets.map((spec) => (
          <button
            key={spec.kind}
            type="button"
            disabled={full}
            data-testid="palette-item"
            data-kind={spec.kind}
            title={kindLabel(spec.kind, spec.kind)}
            onPointerDown={(e) => {
              if (full) return
              onGrab(e, spec)
            }}
            className="vp-press flex cursor-grab flex-col gap-1 rounded-vp border border-hairline bg-surface-2 p-2 text-left disabled:cursor-default disabled:opacity-50"
          >
            <Sketch kind={spec.kind} />
            {/* Two lines, not one. Three columns of these at the width the
                settings panel gives them is about eleven characters a line,
                and single-line truncation turned the library into "One big
                nu...", "Busiest proj...", "Code, over t...", "Heaviest
                ses..." -- a list to choose from where the names cannot be
                read. Grid rows stretch to their tallest cell, so the second
                line costs nothing on rows that do not need it. */}
            <span className="line-clamp-2 text-vp-sm leading-tight text-ink">
              {kindLabel(spec.kind, spec.kind)}
            </span>
          </button>
        ))}
      </div>
    </div>
  )
}

/**
 * The template gallery: every preset as a thumbnail of its actual arrangement.
 *
 * Drawn from the preset's own spans and heights, so what is on the card is the
 * composition — which is the only thing that makes having two dozen of them
 * worth anything. Choosing a template out of a `<select>` by name is what the
 * complaint was about.
 */
export function BoardGallery({
  catalogue,
  current,
  onPick,
}: {
  catalogue: ShareCatalogue
  current: string
  onPick: (preset: SharePreset) => void
}) {
  return (
    <div data-testid="board-gallery">
      <div className="mb-2 text-vp-md text-ink">{t('board.templates')}</div>
      {catalogue.screens.map((screen) => {
        const presets = catalogue.presets.filter((p) => p.screen === screen)
        if (presets.length === 0) return null
        return (
          <div key={screen} className="mb-3">
            <div className="mb-1 text-vp-sm text-ink-3">{screenLabel(screen)}</div>
            {/* Six-up where there is room. The gallery spans the whole editor
                now rather than sitting in the 20rem column, and two dozen
                cards three-up in that column was a strip long enough to bury
                everything else on the page. */}
            <div className="grid grid-cols-2 gap-2 @md:grid-cols-3 @2xl:grid-cols-4 @4xl:grid-cols-6">
              {presets.map((p) => (
                <button
                  key={p.id}
                  type="button"
                  onClick={() => onPick(p)}
                  data-testid="gallery-item"
                  data-preset={p.id}
                  aria-pressed={current === p.id}
                  title={presetWhy(p.id)}
                  className="vp-press flex flex-col gap-1 rounded-vp border border-hairline bg-surface-2 p-2 text-left aria-pressed:border-accent"
                >
                  <Thumbnail preset={p} maxSpan={catalogue.maxSpan} />
                  <span className="line-clamp-2 text-vp-sm leading-tight text-ink">
                    {presetLabel(p.id)}
                  </span>
                  <span className="line-clamp-2 text-vp-xs leading-tight text-ink-3">
                    {presetWhy(p.id)}
                  </span>
                </button>
              ))}
            </div>
          </div>
        )
      })}
    </div>
  )
}

/**
 * One preset, as the rectangles it is.
 *
 * Page 0 only. A rotating board's later pages flowed into the same grid would
 * overlap into an arrangement that is on no screen anywhere, which is worse
 * than showing the page somebody would actually see first.
 */
function Thumbnail({ preset, maxSpan }: { preset: SharePreset; maxSpan: number }) {
  const widgets = preset.widgets.filter((w) => (w.page ?? 0) === 0)
  return (
    <div
      className="vp-thumb"
      style={{ gridTemplateColumns: `repeat(${maxSpan}, minmax(0, 1fr))` }}
      aria-hidden="true"
      data-testid="gallery-thumb"
      data-widgets={widgets.length}
    >
      {widgets.map((w, i) => (
        <span
          key={`${w.kind}-${i}`}
          className="rounded-md"
          style={{
            gridColumn: `span ${Math.max(1, Math.min(w.span, maxSpan))}`,
            gridRow: `span ${Math.max(1, Math.min(w.height ?? 1, 4))}`,
            // Opacity carries the tier -- a hero is the solid block, the
            // texture behind it is fainter -- so the shape of the composition
            // reads at 90 pixels wide without any of it being legible.
            background: 'var(--vp-accent)',
            opacity: 0.25 + Math.min(0.6, ((w.height ?? 1) * w.span) / 24),
          }}
        />
      ))}
    </div>
  )
}
