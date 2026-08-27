/**
 * Which renderer to use. 'auto' means the GPU one when the browser will give it.
 *
 * An escape hatch, not a preference anybody should have to think about. A GPU
 * path can be wrong in ways that are not a crash -- a driver that draws nothing,
 * a compositor that tears, a remote desktop that software-emulates WebGL at two
 * frames a second -- and none of those are things this panel can detect. When
 * the terminal looks wrong the answer has to be reachable from the settings
 * page rather than from a bug report.
 *
 * It is also what lets two of the browser checks measure cell geometry, which
 * only exists in the DOM renderer's output. That is a consequence, not the
 * reason: a switch that exists only for a test is a switch somebody finds later
 * and cannot explain.
 */
export const RENDERER_KEY = 'vibepanel.renderer'

export function rendererPreference(): 'auto' | 'dom' {
  try {
    return localStorage.getItem(RENDERER_KEY) === 'dom' ? 'dom' : 'auto'
  } catch {
    // Private mode. The GPU path is the better default for someone who cannot
    // record a preference either way.
    return 'auto'
  }
}
