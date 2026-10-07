/**
 * How a frame asks the panel to open something, and how the panel hears it.
 *
 * A frame is deep in the tree -- a pane, a settings section, a page root --
 * and the thing that can select a session or open the settings dialog is
 * App. Rather than thread a callback through every slot, the request goes
 * through one subscription App holds. What travels is already checked by
 * host.ts: handles, never ids, and the plugin that asked.
 */

export interface OpenRequest {
  plugin: string
  session?: string
  project?: string
  settings?: string
}

type Listener = (req: OpenRequest) => void

const listeners = new Set<Listener>()

export function requestOpen(req: OpenRequest) {
  for (const fn of listeners) fn(req)
}

export function onOpen(fn: Listener): () => void {
  listeners.add(fn)
  return () => {
    listeners.delete(fn)
  }
}
