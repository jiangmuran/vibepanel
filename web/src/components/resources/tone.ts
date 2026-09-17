import type { ResourcesView } from '../../protocol/wire'

type Level = ResourcesView['level']

/** The colour of a memory level; LevelIcon in level.tsx is its shape. */
export function levelTone(level: Level): string {
  if (level === 'critical') return 'var(--vp-state-crashed)'
  if (level === 'warn') return 'var(--vp-state-waiting)'
  return 'var(--vp-state-done)'
}

/** A share of a scale, clamped to 0–100, and 0 for an unknown scale. */
export function pct(n: number, of: number): number {
  if (!of) return 0
  return Math.max(0, Math.min(100, (n / of) * 100))
}
