// Reading what a terminal is showing, from outside the page.
//
// This used to be `document.querySelectorAll('.xterm-rows > div')` in nine
// places across four check scripts. That works only while xterm is on its DOM
// renderer, one span per cell; the moment the WebGL renderer was loaded the
// rows went empty and thirteen assertions failed at once, every one of them
// reporting an empty terminal about a terminal that was full.
//
// The buffer is the better source regardless. The DOM was a rendering of it,
// and a check that reads the rendering can be fooled by the renderer -- which
// is exactly what happened. `window.vibepanelScreen` is declared in
// Terminal.tsx and reads the xterm buffer; the DOM is kept as a fallback for a
// page that predates it.

/**
 * The rows on screen. Pass `{ all: true }` to reach the scrollback as well.
 *
 * With nothing named it reads the *focused* terminal, because a check that has
 * just typed into one means that one -- the panel has a main terminal and a row
 * of scratch terminals under it, and picking whichever was created first read
 * the wrong screen and reported that typing produced no output.
 */
export async function rows(page, opts) {
  return page.evaluate((o) => {
    const read = window.vibepanelScreen
    if (read) return read(o) ?? []
    return [...document.querySelectorAll('.xterm-rows > div')].map((d) => d.textContent ?? '')
  }, opts ?? {})
}

/** The screen as one string, for `includes` checks. */
export async function text(page, opts) {
  return (await rows(page, opts)).join('\n')
}
