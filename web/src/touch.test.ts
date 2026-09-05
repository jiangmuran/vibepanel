import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

/**
 * A tablet is the desktop layout in somebody's hands.
 *
 * `NARROW_QUERY` is `(max-width: 767px)`, so an iPad — 820 css pixels in
 * portrait — gets the full three-column layout. That is right: it has the room.
 * What comes with it is a finger, and three separate things were built for a
 * mouse and never asked which they had:
 *
 *   the sidebar rows   44px tall from the coarse-pointer floor, with type
 *                      sized for a mouse at arm's length
 *   the panel tabs     a five-pixel drag threshold, which a fingertip clears
 *                      on an ordinary tap
 *   attaching a file   a chooser that lives in the side panel, which the
 *                      narrow layout does not render at all
 *
 * All three were reported in one message from an iPad. None of them is a
 * "phone versus desktop" question, which is why none of them was caught by the
 * phone checks: the answer is `(pointer: coarse)`, which is the one thing the
 * device can actually be asked.
 *
 * Source scans, because each of these is a value or a wire-up that can be
 * reverted without any behaviour test noticing — and the behaviour only shows
 * up on hardware this suite does not have.
 */

const ROOT = new URL('.', import.meta.url).pathname
const read = (rel: string) => readFileSync(ROOT + rel, 'utf8')

/**
 * Source with its comments removed.
 *
 * Needed because these comments quote the very things they forbid -- the second
 * time in this suite that a scan failed on its own explanation.
 */
const code = (rel: string) =>
  read(rel)
    .replace(/\/\*[\s\S]*?\*\//g, ' ')
    .replace(/^\s*\/\/.*$/gm, ' ')

describe('a finger on the desktop layout', () => {
  it('sizes the sidebar type for the pointer, not the layout', () => {
    const sidebar = read('components/Sidebar.tsx')
    // The rank table keys off `big`, which is the drawer *or* a coarse pointer.
    expect(sidebar).toMatch(/const big = overlay \|\| touch/)
    // `rank` is the type; `pack` is padding and legitimately still keys off
    // the layout, because the coarse-pointer floor already sets the height.
    const rank = sidebar.slice(sidebar.indexOf('const rank = {'))
    expect(rank.slice(0, rank.indexOf('}'))).not.toMatch(/overlay \?/)
    // And App has to actually pass it, which is the half a rename would drop.
    expect(read('App.tsx')).toMatch(/touch=\{coarsePointer\}/)
  })

  it('asks a finger to move further than a mouse before a tab drags', () => {
    const panel = read('components/RightPanel.tsx')
    expect(panel).toMatch(/TOUCH_DRAG_THRESHOLD = 10/)
    // Read off the event, not guessed from the viewport: a tablet with a mouse
    // attached is a fine pointer on a wide screen, and gets the mouse value.
    expect(panel).toMatch(/dragThreshold\(e\.pointerType\)/)
  })

  it('gives a narrow layout a way to attach a file', () => {
    const compose = read('components/mobile/ComposeInput.tsx')
    // A real file input. iOS answers one with Photo Library, Take Photo and
    // Files; there is no other road, because a phone cannot drop and iOS does
    // not hand a pasted image to a page that is not an editable field.
    expect(compose).toMatch(/type="file"/)
    expect(compose).toMatch(/onFiles\(/)
    // `capture` would force the camera and hide the photo library, and what is
    // being attached is nearly always a screenshot that already exists.
    expect(code('components/mobile/ComposeInput.tsx')).not.toMatch(/\bcapture\b/)
    // Wired to the same upload the desktop paste and drop paths use, so a file
    // lands in the same place however it arrived.
    expect(read('App.tsx')).toMatch(/onFiles=\{\(files\) => void uploadInto\(files\)\}/)
  })
})

describe('the installed app', () => {
  /*
   * No orientation preference, which is not the same as "any".
   *
   * The manifest said `"orientation": "any"`, and that is a declaration the app
   * makes about itself rather than a neutral default -- it tells the platform
   * every orientation is acceptable, which is exactly what somebody who has
   * locked their tablet to landscape did not ask for. Omitting the key leaves
   * the decision where it belongs, with the device.
   *
   * Reported from an iPad: 「我锁定了横屏，但是它仍然可以被翻转为竖屏」. iOS
   * ignores manifest orientation outright, so this cannot be the whole story
   * there -- but declaring a preference the panel does not have is wrong
   * regardless of which platform reads it.
   */
  it('states no orientation preference', () => {
    const m = JSON.parse(readFileSync(ROOT + '../public/manifest.webmanifest', 'utf8'))
    expect(Object.keys(m)).not.toContain('orientation')
  })
})
