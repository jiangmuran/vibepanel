/**
 * Returns every tappable element smaller than a thumb needs.
 *
 * The five controls in a session row were given a 44px box because somebody
 * looked at them. Measuring all of them instead found: every key on the soft
 * keyboard at 32px tall, the header controls at 27 square, the compose box's
 * buttons at 32, the settings dialog's close button at 23, and the state dot —
 * which is a button — at 18.
 *
 * Height only. 44x44 is the guideline, but eighteen keys at 44 wide do not fit
 * a phone, and a target 32 wide and 44 tall between two other targets is a
 * different proposition from one 27 square in the corner of a dialog. The rule
 * the panel actually holds itself to is the one written here, not the one in
 * the guideline, and it is written down so the gap is a decision.
 *
 * Text fields are exempt: they are wide, they are hit by their label as much as
 * their box, and the 16px font rule already grew them from 32 to 38.
 */
export function findSmallTargets(target, minHeight = 40) {
  return target.evaluate((min) => {
    const out = []
    // Counted so the caller can assert the scan looked at something: a button
    // refactored into a <div onClick> stops matching the selector below, and a
    // scan that matches nothing reports "no problems" in the same words as a
    // page whose controls are all the right size.
    let examined = 0
    for (const el of document.querySelectorAll('button, a[href], [role="button"]')) {
      if (el.closest('.xterm')) continue
      if (el.offsetParent === null) continue
      const st = getComputedStyle(el)
      if (st.display === 'none' || st.visibility === 'hidden') continue
      const r = el.getBoundingClientRect()
      if (r.width < 1 || r.height < 1) continue
      examined++
      if (r.height >= min) continue
      const id = el.getAttribute('data-testid') ??
        `${el.tagName.toLowerCase()}:${(el.textContent ?? '').trim().slice(0, 12) || el.getAttribute('title') || '?'}`
      out.push(`${id} is ${Math.round(r.height)}px tall`)
    }
    return { examined, small: out.slice(0, 10) }
  }, minHeight)
}
