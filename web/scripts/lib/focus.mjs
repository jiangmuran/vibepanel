/**
 * Returns every keyboard tab stop that looks identical when focused.
 *
 * Somebody navigating with a keyboard has to be able to see where they are.
 * The panel does that in two different ways — buttons keep the browser's
 * outline, inputs remove it (`outline-none`) and turn their border accent-blue
 * instead — so the check cannot look for a particular property.
 *
 * It compares the element against itself: read the computed style while
 * focused, blur, read again, restore focus. Anything different is a visible
 * indicator. A first version looked for an outline or a box-shadow and would
 * have reported every text field in the panel as invisible, which is the
 * opposite of true.
 *
 * Driven with real Tab presses rather than `.focus()`, because `:focus-visible`
 * is what the styles hang off and it distinguishes the two.
 */
export async function findInvisibleFocus(page, stops = 14) {
  const seen = []
  for (let i = 0; i < stops; i++) {
    await page.keyboard.press('Tab')
    const f = await page.evaluate(() => {
      const el = document.activeElement
      if (!el || el === document.body) return null
      const read = (n) => {
        const st = getComputedStyle(n)
        return [st.outlineStyle, st.outlineWidth, st.outlineColor, st.boxShadow,
                st.borderColor, st.borderWidth, st.backgroundColor, st.color].join('|')
      }
      const id = el.getAttribute('data-testid') ??
        `${el.tagName.toLowerCase()}:${(el.textContent ?? '').trim().slice(0, 14) ||
          el.getAttribute('title') || '?'}`
      const focused = read(el)
      el.blur()
      const blurred = read(el)
      el.focus()
      return { id, changed: focused !== blurred }
    })
    if (f && !f.changed && !seen.includes(f.id)) seen.push(f.id)
  }
  return seen
}
