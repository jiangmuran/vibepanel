import type { UsageModelDay } from '../../protocol/wire'
import { totalOf } from './tokens'
import type { Metric } from './spend'

/** Family colours stay stable across filters, ranking order and themes. */
export function modelTone(name: string): string {
  const key = name.toLowerCase()
  if (/claude|opus|sonnet|haiku/.test(key)) return 'var(--usage-claude)'
  if (/codex|gpt|^o[134](?:-|$)/.test(key)) return 'var(--usage-codex)'
  if (/grok/.test(key)) return 'var(--usage-grok)'
  if (/gemini/.test(key)) return 'var(--usage-gemini)'
  if (/deepseek/.test(key)) return 'var(--usage-deepseek)'
  if (/kimi|moonshot/.test(key)) return 'var(--usage-kimi)'
  // Not grok's grey: a model nobody recognised drawn in grok's colour read as
  // grok.
  return 'var(--vp-ink-3)'
}

const STEPS = [1, 0.7, 0.5, 0.35]

/**
 * A model's fill among a set of models: its family's hue, and a step of
 * opacity for each further model of the same family. Opus and Sonnet in one
 * stacked bar were the same orange, two segments told apart by nothing. The
 * names are sorted first, so every place that draws the same set -- the bars,
 * the legend, the ranking -- gives a model the same fill whatever order it
 * was handed them in.
 */
export function modelFills(names: readonly string[]): Map<string, { background: string; opacity: number }> {
  const seen = new Map<string, number>()
  const out = new Map<string, { background: string; opacity: number }>()
  for (const name of [...new Set(names)].sort()) {
    const tone = modelTone(name)
    const k = seen.get(tone) ?? 0
    seen.set(tone, k + 1)
    out.set(name, { background: tone, opacity: STEPS[Math.min(k, STEPS.length - 1)] })
  }
  return out
}

/** Preserve actual daily amounts, including days when a model was not used. */
export function dailyModels(rows: UsageModelDay[], metric: Metric): Map<string, Map<string, number>> {
  const days = new Map<string, Map<string, number>>()
  for (const row of rows) {
    let models = days.get(row.day)
    if (!models) {
      models = new Map()
      days.set(row.day, models)
    }
    models.set(row.model, (models.get(row.model) ?? 0) + (metric === 'output' ? row.output : totalOf(row)))
  }
  return days
}
