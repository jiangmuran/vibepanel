import { tKey } from '../../i18n'

/**
 * Names for the strings the server owns.
 *
 * Widget kinds, presets, metrics, filters, orders and dimensions are decided in
 * `internal/store/board.go`, so this side cannot type-check them: a board
 * written by a newer build can name a widget this one has never heard of. Every
 * lookup therefore has a fallback, and the fallback is never the identifier —
 * a wall reading "board.kind.spendsplit" has put an internal name on a screen
 * behind somebody's desk.
 *
 * A Go test walks the registry and fails if any id here has no dictionary
 * entry, so the fallback is for a *future* server rather than for a translation
 * somebody forgot.
 */
function look(prefix: string, id: string, fallback: string): string {
  if (!id) return fallback
  return tKey(`${prefix}.${id}`) ?? fallback
}

export function kindLabel(kind: string, fallback: string): string {
  return look('board.kind', kind, fallback)
}

export function presetLabel(id: string): string {
  return look('board.preset', id, id)
}

export function presetWhy(id: string): string {
  return tKey(`board.presetWhy.${id}`) ?? ''
}

export function audienceLabel(id: string): string {
  return look('board.audience', id, id)
}

/** Which screen a preset was composed for: phone, laptop, wall, bigwall. */
export function screenLabel(id: string): string {
  return look('board.screen', id, id)
}

export function metricLabel(metric: string, fallback: string): string {
  return look('board.metric', metric, fallback)
}

export function filterLabel(filter: string, fallback: string): string {
  return look('board.filter', filter, fallback)
}

export function orderLabel(order: string, fallback: string): string {
  return look('board.order', order, fallback)
}

export function groupLabel(group: string, fallback: string): string {
  return look('board.group', group, fallback)
}

export function byLabel(by: string, fallback: string): string {
  return look('board.by', by, fallback)
}
