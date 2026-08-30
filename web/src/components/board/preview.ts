import { useEffect, useState } from 'react'

import { api } from '../../protocol/api'
import type { ShareDashboard } from '../../protocol/wire'

/**
 * The payload the editor's canvas draws.
 *
 * `GET /api/settings/shares/{id}/preview` runs the *same builder* the dashboard
 * endpoint runs, so what is on the owner's canvas and what is on the wall come
 * from one function. The two obvious alternatives are both worse: invented
 * sample data composes a layout against numbers that will not be the real ones,
 * and a second reduction of the panel's state written on this side diverges on
 * the first field either half gains — in the direction "the editor shows
 * something the real screen does not".
 *
 * A hook rather than a component, because the canvas is now the editing surface
 * and the editor owns it. The fetch stays out of the editor's own file so that
 * file has no network in it at all.
 */

/** How often the canvas re-asks while the editor is open.
 *
 *  Slower than the dashboard's two seconds: this is a canvas beside a palette,
 *  not a wall. Fast enough that the machine line moves, which is what tells the
 *  owner they are looking at something live rather than a screenshot. */
const PREVIEW_MS = 5000

export interface Preview {
  data: ShareDashboard | null
  /** The fetch failed and the last payload is still on screen. Said once; a
   *  canvas that cannot be refreshed must not take the editor down with it. */
  failed: boolean
}

export function useBoardPreview(linkID: string): Preview {
  const [data, setData] = useState<ShareDashboard | null>(null)
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    // A link that does not exist yet has nothing to preview. Nothing is set
    // here -- the state is already null on the first render, and a setState in
    // an effect body is the cascading render the lint refuses. The canvas still
    // draws the arrangement: `BoardCanvas`'s `Ghost` puts the widget's own
    // wireframe in every tile when there is no payload. It did not, for as
    // long as this comment has been here, and the surface where a board is
    // *composed* was a black rectangle with invisible handles on it.
    if (linkID === '') return
    let cancelled = false
    let timer = 0
    const tick = async () => {
      try {
        const next = await api.sharePreview(linkID)
        if (!cancelled) {
          setData(next)
          setFailed(false)
        }
      } catch {
        if (!cancelled) setFailed(true)
      }
      if (!cancelled) timer = window.setTimeout(() => void tick(), PREVIEW_MS)
    }
    void tick()
    return () => {
      cancelled = true
      clearTimeout(timer)
    }
  }, [linkID])

  return { data, failed }
}
