import { describe, expect, it } from 'vitest'
import { dailyModels, modelFills, modelTone } from './modelUsage'
import type { UsageModelDay } from '../../protocol/wire'

const bucket = (day: string, model: string, output: number, cacheRead = 0): UsageModelDay => ({
  day, model, output, cacheRead, input: 2, cacheWrite: 1, requests: 1,
})

describe('the model colours', () => {
  it('keeps the requested family mapping for transcript names and tool names', () => {
    for (const name of ['claude', 'claude-opus-4-6', 'claude-sonnet-4-5', 'opus', 'haiku']) {
      expect(modelTone(name)).toBe('var(--usage-claude)')
    }
    for (const name of ['codex', 'gpt-5.2-codex', 'gpt-5.1', 'o3']) {
      expect(modelTone(name)).toBe('var(--usage-codex)')
    }
    expect(modelTone('grok-4')).toBe('var(--usage-grok)')
    expect(modelTone('some-new-model')).not.toBe(modelTone('grok-4'))
  })

  it('tells two models of one family apart, the same way whatever the order', () => {
    const a = modelFills(['claude-sonnet-4-5', 'gpt-5', 'claude-opus-4-6'])
    const b = modelFills(['claude-opus-4-6', 'claude-sonnet-4-5', 'gpt-5'])
    expect(a.get('claude-opus-4-6')).toEqual(b.get('claude-opus-4-6'))
    expect(a.get('claude-opus-4-6')).not.toEqual(a.get('claude-sonnet-4-5'))
    expect(a.get('gpt-5')!.opacity).toBe(1)
  })
})

describe('daily model stacks', () => {
  const rows = [bucket('2026-11-27', 'opus', 4, 100), bucket('2026-11-28', 'opus', 7, 200),
    bucket('2026-11-28', 'gpt-5-codex', 13, 300), bucket('2026-11-28', 'opus', 3, 50)]

  it('keeps each day distinct and adds repeated model buckets', () => {
    const days = dailyModels(rows, 'output')
    expect([...days.get('2026-11-27')!]).toEqual([['opus', 4]])
    expect([...days.get('2026-11-28')!]).toEqual([['opus', 10], ['gpt-5-codex', 13]])
  })

  it('includes cache and input only in the total metric', () => {
    const days = dailyModels(rows, 'total')
    expect(days.get('2026-11-28')?.get('opus')).toBe(266)
    expect(days.get('2026-11-28')?.get('gpt-5-codex')).toBe(316)
    expect(dailyModels([], 'total').size).toBe(0)
  })
})
