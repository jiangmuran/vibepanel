import type { ShareDashboard, ShareWidget } from '../../protocol/wire'
import { t } from '../../i18n'
import { formatBytes, meterText, meterWidth } from '../panels/meter'
import { Empty, Tile } from './Tile'
import { duration, ratio } from './format'
import { metricLabel } from './labels'

/**
 * The machine, at the size a wall needs.
 *
 * Every figure here has a ceiling except the load averages, which is why the
 * gauge takes four metrics and not five: an arc drawn against a maximum nobody
 * agreed on is a picture that means whatever the person reading it assumes.
 */

/** One pressure, with the number that a bar alone cannot carry at a distance. */
export function BigMeter({
  label,
  value,
  detail,
}: {
  label: string
  value: number | null
  detail: string
}) {
  const pct = meterWidth(value)
  const tone = toneFor(pct, value)
  return (
    <div className="min-w-0" data-testid="dash-meter">
      <div className="mb-2 flex items-baseline justify-between gap-3">
        <span className="truncate text-vp-xl text-ink-2">{label}</span>
        <span className="tabular shrink-0 text-vp-2xl font-semibold text-ink">
          {meterText(value)}
        </span>
      </div>
      <div className="h-3 overflow-hidden rounded-full" style={{ background: 'var(--vp-surface-2)' }}>
        <div
          className="h-full rounded-full transition-[width] duration-500 ease-vp"
          style={{ width: `${pct}%`, background: tone }}
        />
      </div>
      <div className="tabular mt-2 truncate text-vp-xl text-ink-2">{detail}</div>
    </div>
  )
}

function toneFor(pct: number, value: number | null): string {
  if (value === null) return 'var(--vp-surface-2)'
  if (pct >= 90) return 'var(--vp-state-crashed)'
  if (pct >= 75) return 'var(--vp-state-waiting)'
  return 'var(--vp-accent)'
}

/** What one machine metric reads, and what to say underneath it. */
function machineMetric(
  metric: string,
  data: ShareDashboard,
): { value: number | null; detail: string } {
  const m = data.machine
  switch (metric) {
    case 'cpu':
      return {
        value: m.cpuReadable ? m.cpuPercent : null,
        detail: t('monitor.cores', { n: m.cores }),
      }
    case 'memory':
      return {
        value: ratio(m.memTotal - m.memAvailable, m.memTotal),
        detail: t('monitor.of', {
          used: formatBytes(m.memTotal - m.memAvailable),
          total: formatBytes(m.memTotal),
        }),
      }
    case 'disk':
      return {
        value: ratio(m.diskTotal - m.diskFree, m.diskTotal),
        detail: t('monitor.free', { size: formatBytes(m.diskFree) }),
      }
    case 'swap':
      return {
        value: ratio(m.swapTotal - m.swapFree, m.swapTotal),
        detail:
          m.swapTotal > 0
            ? t('monitor.of', {
                used: formatBytes(m.swapTotal - m.swapFree),
                total: formatBytes(m.swapTotal),
              })
            : t('dash.noSwap'),
      }
    case 'todoPercent': {
      const todos = data.todos
      if (!todos) return { value: null, detail: t('dash.unknown') }
      const total = todos.open + todos.done
      return {
        value: ratio(todos.done, total),
        detail: `${todos.done} / ${total}`,
      }
    }
    default:
      return { value: null, detail: t('dash.unknown') }
  }
}

/** The four meters together. */
export function Machine({ w, data }: { w: ShareWidget; data: ShareDashboard }) {
  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-machine" label={t('board.kind.machine')}>
      <div className="grid gap-x-8 gap-y-5" style={{ gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))' }}>
        {(['cpu', 'memory', 'disk', 'swap'] as const).map((metric) => {
          const { value, detail } = machineMetric(metric, data)
          return (
            <BigMeter key={metric} label={metricLabel(metric, metric)} value={value} detail={detail} />
          )
        })}
      </div>
    </Tile>
  )
}

/**
 * One pressure as an arc.
 *
 * An arc rather than a bar because this is the widget somebody puts four of on
 * a screen and reads from the far side of a room: a ring's fill is legible as a
 * shape at a distance where a 3px bar is a line. The figure is inside it, at
 * the largest size on the tile — the arc is the glance and the number is the
 * answer, and a colour-blind reader has both.
 */
export function Gauge({ w, data }: { w: ShareWidget; data: ShareDashboard }) {
  const metric = w.metric ?? 'cpu'
  const { value, detail } = machineMetric(metric, data)
  const pct = meterWidth(value)
  const tone = toneFor(pct, value)
  // A 270-degree arc, which leaves a gap at the bottom so that "empty" and
  // "full" are different shapes rather than the same closed ring.
  const r = 42
  const circumference = 2 * Math.PI * r
  const sweep = 0.75
  const dash = circumference * sweep

  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-gauge" label={metricLabel(metric, metric)}>
      <div className="flex flex-col items-center">
        <div className="relative w-full" style={{ maxWidth: '13rem' }}>
          <svg viewBox="0 0 100 100" className="w-full" role="img" aria-label={metricLabel(metric, metric)}>
            <circle
              cx="50"
              cy="50"
              r={r}
              fill="none"
              stroke="var(--vp-surface-2)"
              strokeWidth="10"
              strokeLinecap="round"
              strokeDasharray={`${dash} ${circumference}`}
              transform="rotate(135 50 50)"
            />
            <circle
              cx="50"
              cy="50"
              r={r}
              fill="none"
              stroke={tone}
              strokeWidth="10"
              strokeLinecap="round"
              strokeDasharray={`${(dash * pct) / 100} ${circumference}`}
              transform="rotate(135 50 50)"
              style={{ transition: 'stroke-dasharray 500ms var(--vp-ease)' }}
            />
          </svg>
          <span
            className="tabular absolute inset-0 flex items-center justify-center text-vp-2xl font-semibold text-ink"
            data-testid="gauge-value"
          >
            {meterText(value)}
          </span>
        </div>
        <span className="tabular mt-1 truncate text-vp-xl text-ink-2">{detail}</span>
      </div>
    </Tile>
  )
}

/** How long it has been up, and how hard it is being pushed. */
export function Uptime({ w, data }: { w: ShareWidget; data: ShareDashboard }) {
  const m = data.machine
  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-uptime" label={t('dash.uptimeLabel')}>
      <div className="tabular text-vp-2xl font-semibold text-ink">{duration(m.uptime)}</div>
      <div className="mt-3 text-vp-xl text-ink-3">{t('dash.load')}</div>
      <div className="tabular text-vp-2xl font-semibold text-ink">
        {m.load1.toFixed(2)} · {m.load5.toFixed(2)} · {m.load15.toFixed(2)}
      </div>
      {m.cores > 0 && (
        <div className="tabular mt-1 text-vp-xl text-ink-2">{t('monitor.cores', { n: m.cores })}</div>
      )}
    </Tile>
  )
}

/** Nothing to draw, said in as few words as the tile allows. */
export function Unknown({ w }: { w: ShareWidget }) {
  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-unknown">
      <Empty text={t('dash.emptyWidget')} />
    </Tile>
  )
}
