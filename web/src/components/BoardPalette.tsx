import type { ShareCatalogue, SharePreset, ShareWidgetSpec } from '../protocol/wire'
import { t } from '../i18n'
import { kindLabel, presetLabel, presetWhy, screenLabel } from './board/labels'

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
 * What a kind's picture looks like, by what it says rather than by what it
 * reads.
 *
 * A `switch` with a default, like the renderer's, and for the same reason: the
 * catalogue is served by the server, so a build of this frontend can be handed
 * a kind it has never heard of. It gets the neutral picture rather than none,
 * because a palette entry with no picture reads as broken.
 */
type Shape = 'figure' | 'chart' | 'series' | 'list' | 'grid' | 'meter' | 'rule' | 'text'

function shapeOf(kind: string): Shape {
  switch (kind) {
    case 'bignumber':
    case 'attention':
    case 'output':
    case 'odometer':
    case 'spendtotals':
    case 'spendrate':
    case 'spendcompare':
    case 'kinds':
    case 'prs':
    case 'states':
    case 'clock':
    case 'datetime':
    case 'uptime':
    case 'health':
      return 'figure'
    case 'machinearea':
    case 'tokenburn':
    case 'sparkline':
      return 'chart'
    case 'spendbars':
    case 'spendstack':
    case 'codechurn':
    case 'spentmade':
    case 'flow':
    case 'waits':
      return 'series'
    case 'sessionlist':
    case 'cputop':
    case 'exits':
    case 'busiest':
    case 'todos':
    case 'projects':
    case 'timeline':
    case 'spendsplit':
    case 'repoprojects':
    case 'feed':
      return 'list'
    case 'sessiongrid':
    case 'spendheatmap':
      return 'grid'
    case 'machine':
    case 'gauge':
    case 'statebar':
    case 'nowstrip':
      return 'meter'
    case 'rule':
    case 'spacer':
      return 'rule'
    case 'caption':
    case 'heading':
    case 'remark':
      return 'text'
    default:
      return 'figure'
  }
}

/** The wireframe for one shape, in a unit box the caller stretches. */
function Sketch({ shape }: { shape: Shape }) {
  const ink = 'var(--vp-ink-3)'
  const accent = 'var(--vp-accent)'
  return (
    <svg
      viewBox="0 0 60 28"
      preserveAspectRatio="none"
      className="h-7 w-full"
      aria-hidden="true"
      data-testid="palette-sketch"
      data-shape={shape}
    >
      {shape === 'figure' && (
        <>
          <rect x="4" y="5" width="26" height="12" rx="2" fill={accent} opacity="0.7" />
          <rect x="4" y="20" width="18" height="3" rx="1.5" fill={ink} opacity="0.5" />
        </>
      )}
      {shape === 'chart' && (
        <polyline
          points="4,22 14,14 24,17 34,8 44,12 56,5"
          fill="none"
          stroke={accent}
          strokeWidth="2.5"
          strokeLinejoin="round"
        />
      )}
      {shape === 'series' &&
        [6, 14, 22, 30, 38, 46].map((x, i) => (
          <rect
            key={x}
            x={x}
            y={24 - [8, 14, 6, 18, 11, 20][i]}
            width="6"
            height={[8, 14, 6, 18, 11, 20][i]}
            rx="1"
            fill={accent}
            opacity="0.75"
          />
        ))}
      {shape === 'list' &&
        [4, 11, 18].map((y, i) => (
          <g key={y}>
            <rect x="4" y={y} width="16" height="4" rx="2" fill={ink} opacity="0.5" />
            <rect
              x="24"
              y={y}
              width={[30, 20, 12][i]}
              height="4"
              rx="2"
              fill={accent}
              opacity="0.7"
            />
          </g>
        ))}
      {shape === 'grid' &&
        [0, 1, 2, 3].flatMap((c) =>
          [0, 1].map((r) => (
            <rect
              key={`${c}-${r}`}
              x={4 + c * 14}
              y={5 + r * 11}
              width="11"
              height="8"
              rx="1.5"
              fill={accent}
              opacity={0.35 + ((c + r) % 3) * 0.2}
            />
          )),
        )}
      {shape === 'meter' && (
        <>
          <rect x="4" y="8" width="52" height="5" rx="2.5" fill={ink} opacity="0.35" />
          <rect x="4" y="8" width="34" height="5" rx="2.5" fill={accent} />
          <rect x="4" y="18" width="52" height="5" rx="2.5" fill={ink} opacity="0.35" />
          <rect x="4" y="18" width="18" height="5" rx="2.5" fill={accent} opacity="0.7" />
        </>
      )}
      {shape === 'rule' && <rect x="4" y="13" width="52" height="2" rx="1" fill={ink} opacity="0.5" />}
      {shape === 'text' && (
        <>
          <rect x="4" y="7" width="40" height="5" rx="2.5" fill={ink} opacity="0.6" />
          <rect x="4" y="16" width="26" height="4" rx="2" fill={ink} opacity="0.35" />
        </>
      )}
    </svg>
  )
}

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
      <div className="grid max-h-64 grid-cols-2 gap-1 overflow-y-auto pr-1 sm:grid-cols-3">
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
            <Sketch shape={shapeOf(spec.kind)} />
            <span className="truncate text-vp-sm text-ink">{kindLabel(spec.kind, spec.kind)}</span>
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
            <div className="grid grid-cols-2 gap-2 sm:grid-cols-3">
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
                  <span className="truncate text-vp-sm text-ink">{presetLabel(p.id)}</span>
                  <span className="truncate text-vp-xs text-ink-3">{presetWhy(p.id)}</span>
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
