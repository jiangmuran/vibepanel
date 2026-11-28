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
  return 'var(--usage-grok)'
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
