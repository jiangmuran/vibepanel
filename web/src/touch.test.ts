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
    // Only comments that own their line.
    //
    // The previous version was `/\*[\s\S]*?\*\//g`, which finds `/*` inside a
    // string literal just as happily as at the start of a comment --
    // `accept="image/*,application/pdf,text/*"` opens one that never closes, and
    // it ate TouchBar.tsx down to a fifth of itself. Every assertion in that
    // block was then running against a string with the code missing from it,
    // and passing or failing for reasons unrelated to the source.
    //
    // A comment in this project always starts its own line, so requiring that
    // is enough, and a `/*` inside an attribute never does.
  read(rel)
    .replace(/^[ \t]*\{?\/\*[\s\S]*?\*\/\}?[ \t]*$/gm, ' ')
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

describe('a touchscreen that is not a phone', () => {
  /*
   * A tablet is 820 css pixels, so it is not `narrow`, so it gets the desktop
   * layout -- which assumes a keyboard and a mouse. It has neither.
   * 「iPad 端没法摁 ESC，没法上传图片」 and 「没有办法滚动底下的小终端」: three
   * separate things, one cause, and none of them reachable by the phone checks
   * because a phone is `narrow` and gets all three already.
   *
   * Source scans, for the same reason as the block above: these are wire-ups
   * whose absence shows only on hardware this suite does not have.
   */
  it('gives a tablet the keys and the attach button', () => {
    const app = read('App.tsx')
    // Gated on the pointer rather than on a width, which is the one question
    // the device can actually answer, and not on `narrow` -- that is the
    // phone, which has its own bar already.
    expect(app).toMatch(/!narrow && coarsePointer && \(\s*<TouchControls/)
    expect(app).toMatch(/!narrow && coarsePointer && touchKeys && \(/)
  })

  it('puts them in the row that already exists', () => {
    // 「你没有必要单开一个横杠吧... 单开一条有点浪费空间」. A full-width row for
    // two buttons costs a line of terminal on the device with the least of it,
    // so the toggle and the attach button join the header's control cluster
    // and only the keys themselves take space, and only while open.
    const app = code('App.tsx')
    const header = app.slice(app.indexOf('data-testid="right-show"'))
    expect(header.slice(0, header.indexOf('sign-out'))).toContain('<TouchControls')
  })

  it('leaves the keys off until asked, and attaching one press', () => {
    // Closed by default: a tablet often has a real keyboard attached, and
    // eighteen soft keys permanently across the bottom of a screen that does
    // not need them is worse than the missing Escape was.
    expect(code('App.tsx')).toMatch(/const \[touchKeys, setTouchKeys\] = useState\(false\)/)
    const bar = code('components/mobile/TouchControls.tsx')
    // Attaching does not go behind the toggle, or one hidden thing has been
    // traded for another.
    expect(bar).toMatch(/data-testid="touch-attach"/)
    expect(bar).toMatch(/type="file"/)
  })

  it('lets a finger scroll the bottom terminal too', () => {
    // The main pane got the gesture and the strip did not, so the small
    // terminal could not be scrolled at all on a touchscreen.
    // Both call sites, counted rather than matched as a pair: they are sixty
    // lines apart and a window wide enough to span them would also match one
    // occurrence twice.
    const app = read('App.tsx')
    const wired = app.match(/touchSelect=\{narrow \|\| coarsePointer\}/g) ?? []
    expect(wired.length, 'the main pane and the bottom strip both need it').toBe(2)
    expect(code('components/BottomTerminals.tsx')).toMatch(/touchSelect=\{props\.touchSelect\}/)
  })
})
