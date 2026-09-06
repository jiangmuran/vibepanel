import { describe, expect, it } from 'vitest'

import { fullscreenSupported } from './Fullscreen'

/**
 * Offer the control only where it does something.
 *
 * A board is opened on a television, a wall panel and a tablet on a desk, and
 * on all of them the browser's own furniture is the only thing on screen that
 * is not the board. 「没有全屏按钮」.
 *
 * Detected rather than assumed, because the one place it is *not* available is
 * a phone running iOS -- there is no element fullscreen there at all -- and a
 * button that does nothing is worse than no button: the reader cannot tell a
 * missing feature from a broken one. Safari also still spells it
 * `webkitRequestFullscreen`, so a check for the standard name alone hides the
 * control on an iPad, which is exactly the device this was reported from.
 *
 * A pure function against a fake document, because vitest runs on `node` here
 * and there is no real one -- and because the four combinations below are the
 * whole of the decision.
 */

type Fake = {
  fullscreenEnabled?: boolean
  webkitExitFullscreen?: () => void
  documentElement: {
    requestFullscreen?: () => Promise<void>
    webkitRequestFullscreen?: () => void
  }
}

const doc = (f: Fake) => f as unknown as Document

describe('offering full screen', () => {
  it('is offered where the standard API exists', () => {
    expect(
      fullscreenSupported(doc({ fullscreenEnabled: true, documentElement: { requestFullscreen: async () => {} } })),
    ).toBe(true)
  })

  it('is offered on Safari, which still uses the prefix', () => {
    // The iPad case. Checking only the standard spelling hides the button on
    // the device the request came from.
    expect(
      fullscreenSupported(
        doc({ webkitExitFullscreen: () => {}, documentElement: { webkitRequestFullscreen: () => {} } }),
      ),
    ).toBe(true)
  })

  it('is not offered where nothing implements it', () => {
    // A phone running iOS. The control is absent rather than inert.
    expect(fullscreenSupported(doc({ documentElement: {} }))).toBe(false)
  })

  it('is not offered when the document forbids it', () => {
    // An iframe without `allow="fullscreen"`: the method is there and the
    // request is refused, which is a button that appears to do nothing.
    expect(
      fullscreenSupported(
        doc({ fullscreenEnabled: false, documentElement: { requestFullscreen: async () => {} } }),
      ),
    ).toBe(false)
  })
})
