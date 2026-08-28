import { useCallback, useEffect, useState } from 'react'

import { api } from '../../protocol/api'
import type { TokenUsage } from '../../protocol/wire'

/** How often the reading refreshes while any of it is on screen. */
const POLL_MS = 20000

/**
 * The window every figure in the dock is measured over.
 *
 * Thirty days: long enough that "this project" has usually seen something and
 * short enough that a sparkline of it is one bar per column at 280 pixels. The
 * footer states it, because five figures over an unnamed period are five
 * figures nobody can compare with anything.
 */
export const SPEND_SPAN = 30

export interface Spend {
  data: TokenUsage | null
  error: string | null
  /** One clock, moved on by the poll. See GitPanel's Ago for why. */
  now: number
  busy: boolean
  refresh: () => void
}

/**
 * One reading of the token ledger, shared by every surface that shows it.
 *
 * It lives above the compact block and the opened one on purpose. They are the
 * same subject at two sizes, and a fetch per surface would mean expanding a
 * block restarts its poll — so the figures you were reading blank for a moment
 * at exactly the press that was meant to show you more of them. It would also
 * mean the compact block and the detail could disagree, which reads as a bug in
 * the arithmetic rather than as two requests.
 *
 * No project filter and no tool filter. The payload already carries the
 * per-project and per-tool splits, so one pass answers all six figures; three
 * scoped requests would be three transcript passes to draw one block, and the
 * three answers would be from three different moments.
 */
export function useSpend(): Spend {
  const [data, setData] = useState<TokenUsage | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [now, setNow] = useState(() => Math.floor(Date.now() / 1000))

  useEffect(() => {
    // Self-scheduling, and the next tick is booked after the last one landed:
    // this starts a disk pass on the server, and a fixed interval against a
    // slow pass queues reads faster than they can be answered.
    let cancelled = false
    let timer = 0
    const tick = async () => {
      try {
        const next = await api.tokenUsage({ days: SPEND_SPAN })
        if (!cancelled) {
          setData(next)
          setNow(Math.floor(Date.now() / 1000))
          setError(null)
        }
      } catch (e) {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e))
      }
      if (!cancelled) timer = window.setTimeout(() => void tick(), POLL_MS)
    }
    void tick()
    return () => {
      cancelled = true
      window.clearTimeout(timer)
    }
  }, [])

  const refresh = useCallback(() => {
    setBusy(true)
    void api
      .refreshTokenUsage()
      .then(() => api.tokenUsage({ days: SPEND_SPAN }))
      .then((next) => {
        setData(next)
        setNow(Math.floor(Date.now() / 1000))
        setError(null)
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setBusy(false))
  }, [])

  return { data, error, now, busy, refresh }
}
