/**
 * Returns every box whose content does not fit and cannot be scrolled to.
 *
 * Four separate places in this panel have had a row that could outgrow its
 * container with nothing deciding what happens next — the key bar, the terminal
 * tab strip, the collapsed rail, the wrapped key row. Rather than wait for the
 * fifth, this asks the question generically, everywhere, at every size the
 * check visits.
 *
 * Two things it deliberately does not report, both learned by getting them
 * wrong. An element inside a scroller: its own box overflowing is how a
 * scroller works, and the first version flagged every row of the activity log.
 * And — the opposite mistake — an element merely clipped by a distant
 * `overflow: hidden`: that is not containment but the content being invisible
 * and unreachable, and since the app root is overflow-hidden, treating it as
 * fine silenced every real finding.
 *
 * It has to run wherever the thing being checked exists. Run once on the
 * desktop page it says nothing about the key bar, which only exists on a
 * phone — which is exactly how the first version of this passed while the key
 * row was hiding two keys.
 */
export async function findUnreachable(target, sleep) {
  // Twice, and only what survives both.
  //
  // xterm re-fits its grid asynchronously after anything changes the space it
  // has, so a scan that lands mid-resize sees the terminal host briefly taller
  // than its box. That produced a failure that vanished on the next run — and
  // a check that fires intermittently is worse than no check, because the
  // lesson people take from it is to run it again.
  const first = await scanOnce(target)
  if (first.length === 0) return []
  await sleep(600)
  const second = await scanOnce(target)
  const found = first.filter((f) => second.includes(f))
  return found
}

function scanOnce(target) {
  return target.evaluate(() => {
    const scrolls = (node, axis) => {
      for (let a = node.parentElement; a; a = a.parentElement) {
        const mode = axis === 'x' ? getComputedStyle(a).overflowX : getComputedStyle(a).overflowY
        if (mode === 'auto' || mode === 'scroll') return true
      }
      return false
    }
    // Does anything actually end up drawn past the bottom edge?
    //
    // `scrollHeight` is layout, and a CSS transform is not: a passive viewer
    // renders the owner's grid at its natural size and scales it down, so the
    // box reports hundreds of pixels of overflow while every glyph is on
    // screen. Asking the children where they were painted tells the two apart
    // — the scaled one fits, a list that has simply run out of room does not.
    const paintsOutside = (el) => {
      const box = el.getBoundingClientRect()
      let worst = null
      for (const child of el.children) {
        const r = child.getBoundingClientRect()
        // Above as well as below. A list scrolled to its end and then made
        // unscrollable hides its head, not its tail — which is how the first
        // version of this stopped catching the mutation it was written for,
        // one refinement after it started catching it.
        const over = Math.max(r.bottom - box.bottom, box.top - r.top)
        if (over > 2 && (!worst || over > worst.over)) worst = { over, child }
      }
      if (!worst) return null
      const c = worst.child
      const cid = c.getAttribute('data-testid')
      const ccls = (c.className || '').toString().split(/\s+/).filter(Boolean).slice(0, 3).join('.')
      // Naming the child, not only the container. "a div clips 77px" is not
      // something anyone can act on; "its .xterm-screen paints 77px past the
      // bottom" is.
      return `${cid ? `[${cid}]` : c.tagName.toLowerCase() + (ccls ? '.' + ccls : '')}` +
        ` by ${Math.round(worst.over)}px`
    }
    const out = []
    for (const el of document.querySelectorAll('*')) {
      if (el.closest('.xterm')) continue // the terminal scrolls itself
      // And the element xterm is mounted into. It re-fits its grid
      // asynchronously after anything changes the space it has, so this host
      // is briefly taller than its box whenever the layout moves — reported
      // once, gone on the next run, and still intermittent after sampling
      // twice. Nothing the panel can fix and nothing that persists on screen;
      // an intermittent failure is how a check stops being read.
      if (el.querySelector(':scope > .xterm')) continue
      const st = getComputedStyle(el)
      if (st.display === 'none' || el.offsetParent === null) continue
      // A tag name alone names nothing. The first finding this produced was
      // "div is 41px too tall", which is every div.
      const testid = el.getAttribute('data-testid')
      const cls = (el.className || '').toString().split(/\s+/).filter(Boolean).slice(0, 4).join('.')
      const name = testid
        ? `[${testid}]`
        : `${el.tagName.toLowerCase()}${cls ? '.' + cls : ''}`
      const wide = el.scrollWidth - el.clientWidth
      const tall = el.scrollHeight - el.clientHeight
      if (st.overflowX === 'visible' && wide > 2 && !scrolls(el, 'x')) {
        out.push(`${name} is ${wide}px too wide`)
      }
      if (st.overflowY === 'visible' && tall > 2 && !scrolls(el, 'y')) {
        out.push(`${name} is ${tall}px too tall`)
      }
      // A scroller turned into a clipper.
      //
      // `overflow-y: hidden` on a box holding more than it can show is content
      // nobody can reach — and it is invisible to the rule above, which only
      // looks at boxes whose overflow is `visible`. Changing one `auto` to
      // `hidden` is a one-word diff that hides the tail of a list, and this
      // scan was blind to exactly that.
      //
      // Found by mutation: with the session list's `overflow-y-auto` turned to
      // `hidden`, twenty-four sessions on a phone still reported clean —
      // because `scrollIntoViewIfNeeded` and `scrollTop` both keep working on
      // a hidden box. `overflow: hidden` stops *people* scrolling, not
      // scripts, so every script-based measurement says the content is fine.
      //
      // Vertical only. `overflow: hidden` with wider content is how
      // `text-overflow: ellipsis` works, and every truncated label in the
      // panel would be a finding.
      // Not the box the terminal is mounted in, one level above the host that
      // is already excluded above for the same reason.
      //
      // Measured while adding this rule: in the harness's touch page, after the
      // long-press tests, that wrapper reports 77px of layout overflow and its
      // host paints 8px past the bottom — persistently enough to survive the
      // double scan, invisible on the screenshot, and not reproducible in
      // isolation across a dozen attempts at three device pixel ratios. Eight
      // pixels is less than one row of a scaled grid.
      //
      // Excluded rather than explained, and that is the honest description. A
      // new check whose first finding is a sub-pixel-row overhang nobody can
      // see is a check people turn off, and the thing it exists for — a
      // scroller changed to a clipper — is caught regardless, which the
      // mutation confirms.
      const hostsTerminal = !!el.querySelector(':scope > div > .xterm')
      const spilledChild =
        st.overflowY === 'hidden' && tall > 2 && !hostsTerminal ? paintsOutside(el) : null
      if (spilledChild) {
        out.push(`${name} clips ${tall}px vertically and cannot be scrolled: ${spilledChild}`)
      }
    }
    return out.slice(0, 8)
  })
}



async function waitHealth(url, ms = 15000) {
  const end = Date.now() + ms
  while (Date.now() < end) {
    try {
      const r = await fetch(url + '/api/health')
      if (r.ok) return true
    } catch { /* not up yet */ }
    await sleep(150)
  }
  return false
}

// ── relative luminance / contrast, per WCAG ────────────────────────────────
