// The side panel's pane layout, in a browser, without a server.
//
//   npm run check:panes
//
// Splitting a tab into a pane is a gesture: press, move, watch where it would
// land, release. None of that is assertable in vitest, which runs in node with
// no layout at all — and the reducer that decides what a drop *means* is
// pinned there already, in components/panes.test.ts. What is left is the
// wiring, and the wiring is where both defects in this feature were: the tab
// strip sat inside the "new pane above" band, so a clumsy sideways click split
// the panel in two; and the click that a released drag still fires re-selected
// the tab that had just been moved somewhere else.
//
// It needs no Go binary, no tmux and no database, because the panel under test
// is rendered on its own through panes-harness.html. That is what makes it a
// check worth running while working rather than only before a release: about
// twenty seconds, against the working tree.
//
// The console carries 502s from the token panel, which reaches for /api with
// nothing behind it. Everything else on screen is chrome.
import { chromium } from 'playwright'
import { spawn } from 'node:child_process'
import { createServer } from 'node:net'
import { readFile } from 'node:fs/promises'

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

const PORT = await new Promise((resolve, reject) => {
  const probe = createServer()
  probe.once('error', reject)
  probe.listen(0, '127.0.0.1', () => {
    const { port } = probe.address()
    probe.close(() => resolve(port))
  })
})
const BASE = `http://127.0.0.1:${PORT}/panes-harness.html`

const failures = []
const note = (ok, what) => {
  console.log(`${ok ? '  ok  ' : ' FAIL '} ${what}`)
  if (!ok) failures.push(what)
}

// The working tree through vite, not a built bundle. A check that measures
// `dist` measures whatever was built last, which is the failure lib/fresh.mjs
// exists for in the harnesses that do need a binary.
const vite = spawn('npx', ['vite', '--port', String(PORT), '--strictPort'], {
  cwd: new URL('..', import.meta.url).pathname,
  stdio: ['ignore', 'pipe', 'pipe'],
})
const viteLog = []
vite.stdout.on('data', (b) => viteLog.push(String(b)))
vite.stderr.on('data', (b) => viteLog.push(String(b)))

const stop = () => {
  try {
    vite.kill('SIGTERM')
  } catch {
    /* already gone */
  }
}
process.on('exit', stop)

let up = false
for (let i = 0; i < 100; i++) {
  try {
    if ((await fetch(BASE)).ok) {
      up = true
      break
    }
  } catch {
    /* not listening yet */
  }
  await sleep(200)
}
if (!up) {
  console.error(`vite never served ${BASE}:\n${viteLog.join('')}`)
  stop()
  process.exit(1)
}

const browser = await chromium.launch()
const page = await browser.newPage({ viewport: { width: 1200, height: 900 } })
const pageErrors = []
page.on('pageerror', (e) => pageErrors.push(String(e)))
await page.goto(BASE, { waitUntil: 'networkidle' })
await sleep(800)

// The tab list, from the source rather than from a copy of it in this file.
// A literal here is a selector that silently matches nothing the day a tab is
// renamed, which the script reports as a missing element rather than as the
// rename it is. chrome.test.ts pins the order; this only needs the names.
const TABS = (await readFile(new URL('../src/components/chrome.ts', import.meta.url), 'utf8'))
  .match(/export const PANEL_TABS = \[([^\]]*)\]/)[1]
  .match(/'([a-z]+)'/g)
  .map((q) => q.slice(1, -1))
note(TABS.length >= 2, `read the tab list from chrome.ts (${TABS.join(', ')})`)

// One reading of the ledger, shaped like the real one. `today` is fixed so the
// window arithmetic has something to be right about; the two projects exist so
// "this project" can be a figure rather than an em dash.
const SPEND = {
  scannedAt: 1_700_000_000, scanning: false, passMs: 9, passError: '',
  sources: [{ tool: 'claude', root: '/x', found: true, problem: '', files: 12, bytes: 1, skipped: 0 }],
  today: '2026-03-10', from: '2026-02-09', to: '2026-03-10', days: 30,
  total: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, requests: 0 },
  byDay: ['2026-03-04', '2026-03-08', '2026-03-09', '2026-03-10'].map((day) => ({
    day, input: 12000, output: 3400, cacheRead: 220000, cacheWrite: 8000, requests: 41,
  })),
  heatmap: [], byMonth: [],
  byTool: [
    { tool: 'claude', input: 30000, output: 9000, cacheRead: 500000, cacheWrite: 20000, requests: 90, files: 12, skipped: 0, problems: 0, problem: '' },
    { tool: 'codex', input: 9000, output: 1200, cacheRead: 60000, cacheWrite: 3000, requests: 22, files: 4, skipped: 0, problems: 0, problem: '' },
  ],
  projects: [
    { id: 'harness', name: 'harness', path: '', input: 8000, output: 2000, cacheRead: 90000, cacheWrite: 4000, requests: 30 },
  ],
  sessions: [], sessionCount: 7, sessionLimit: 100,
}

const panel = page.locator('[data-testid="right-panel"]')
note(await panel.isVisible(), 'the panel renders')
note((await page.locator('[data-testid="panel-header"]').count()) === 1, 'one panel header')
note(TABS.length === 2, `two tabs and no more (${TABS.join(', ')})`)
for (const id of TABS) {
  note((await page.locator(`[data-testid="panel-tab-${id}"]`).count()) === 1, `one ${id} tab`)
}
note((await page.locator('[data-testid="panel-collapse"]').count()) === 1, 'the collapse control is there')
note((await page.locator('[data-testid="pane-menu-0"]').count()) === 1, 'the pane menu is there')

// The preset that put notes and todo in two panes is gone, because notes and
// todo are one tab now. A control that presses and changes nothing is worse
// than no control.
note((await page.locator('[data-testid="panel-split"]').count()) === 0,
  'the notes/todo preset is gone rather than inert')

// The strip is icons: 「用量的icon 不显示汉字」. The name has to survive
// somewhere a person can find it, so every tab carries both a title and an
// aria-label -- a tooltip is not something a finger can ask for, and an
// aria-label is announced and never seen.
for (const id of TABS) {
  const tab = page.locator(`[data-testid="panel-tab-${id}"]`)
  const text = (await tab.innerText()).trim()
  note(text === '', `the ${id} tab draws no words (${JSON.stringify(text)})`)
  const title = await tab.getAttribute('title')
  const aria = await tab.getAttribute('aria-label')
  note(!!title && !!aria && title === aria,
    `the ${id} tab is named for a pointer and for a reader (${title} / ${aria})`)
  note((await tab.locator('svg').count()) === 1, `the ${id} tab draws one glyph`)
}

// The control set does not change with the tab.
for (const id of TABS) {
  await page.locator(`[data-testid="panel-tab-${id}"]`).click()
  await sleep(250)
  const n = await page.locator('[data-testid="panel-collapse"], [data-testid="pane-menu-0"]').count()
  note(n === 2, `two header controls on the ${id} tab`)
  const header = await page.locator('[data-testid="panel-header"]').boundingBox()
  const collapse = await page.locator('[data-testid="panel-collapse"]').boundingBox()
  note(collapse.x + collapse.width <= header.x + header.width + 1, `collapse fits on the ${id} tab`)
}

// The marker moves.
await page.locator('[data-testid="panel-tab-files"]').click()
await sleep(400)
const markerAt = async () => page.evaluate(() => {
  const m = document.querySelector('.vp-marker')
  return m ? m.getBoundingClientRect().x : null
})
const m1 = await markerAt()
await page.locator(`[data-testid="panel-tab-${TABS[TABS.length - 1]}"]`).click()
await sleep(500)
const m2 = await markerAt()
note(m1 !== null && m2 !== null && Math.abs(m2 - m1) > 20, `the marker travels (${m1} -> ${m2})`)

// And travels rather than teleporting: the whole complaint was that switching
// tabs happened between two frames with nothing to follow.
//
// This matters more than it did. The tabs used to be unequal — the selected one
// grew to hold its name — so the marker grew as it travelled and the eye read
// the growth as much as the movement. The strip is icons now, the tabs are
// equal, and the travel is all there is: a marker that teleports has nothing
// left to make the change legible as a movement.
{
  const narrow = await browser.newPage({ viewport: { width: 900, height: 800 } })
  await narrow.goto(`${BASE}?w=220`, { waitUntil: 'networkidle' })
  await sleep(600)
  const at = async () => narrow.evaluate(() => {
    const m = document.querySelector('.vp-marker')
    return m ? m.getBoundingClientRect().x : null
  })
  const declared = await narrow.evaluate(() => {
    const m = document.querySelector('.vp-marker')
    return m ? getComputedStyle(m).transitionProperty : ''
  })
  note(/transform/.test(declared), `the marker declares a transition (${declared})`)
  await narrow.locator(`[data-testid="panel-tab-${TABS[0]}"]`).click()
  await sleep(500)
  const from = await at()
  await narrow.locator(`[data-testid="panel-tab-${TABS[TABS.length - 1]}"]`).click()
  await sleep(60)
  const mid = await at()
  await sleep(600)
  const end = await at()
  const moving = mid !== null && end !== null && from !== null &&
    Math.abs(end - mid) > 6 && Math.abs(mid - from) > 2
  note(moving, `the marker is mid-flight a frame later (${from} -> ${mid} -> ${end})`)
  // The glyph on the selected tab lifts, which is what replaced the label
  // folding open. Not the only thing saying which tab is selected -- the
  // marker is under it and aria-selected is on it -- but the strip is four
  // identical-sized icons now and one of them has to look chosen.
  const scale = await narrow.evaluate(() => {
    const on = document.querySelector('[role="tab"][aria-selected="true"] .vp-tab-icon')
    const off = document.querySelector('[role="tab"][aria-selected="false"] .vp-tab-icon')
    if (!on || !off) return null
    const read = (el) => {
      const cs = getComputedStyle(el)
      return { t: cs.transform, o: Number(cs.opacity) }
    }
    return { on: read(on), off: read(off) }
  })
  note(scale !== null && scale.on.t !== scale.off.t && scale.on.o > scale.off.o,
    `the selected glyph is lifted (${JSON.stringify(scale)})`)

  await narrow.close()
}

// The panel lays its figures out in two columns once it is wide enough, and
// the extra width has to buy something -- a panel dragged to 640 that only
// stretches is 360 pixels of whitespace.
{
  const wide = await browser.newPage({ viewport: { width: 1400, height: 900 } })
  await wide.goto(`${BASE}?w=500`, { waitUntil: 'networkidle' })
  await sleep(600)
  note((await wide.locator('[data-testid="right-panel"]').getAttribute('data-density')) === 'wide',
    'a wide panel reports itself wide')
  await wide.goto(`${BASE}?w=240`, { waitUntil: 'networkidle' })
  await sleep(600)
  note((await wide.locator('[data-testid="right-panel"]').getAttribute('data-density')) === 'narrow',
    'and a narrow one narrow')
  await wide.close()
}

// The body enters from the side the strip moved towards.
await page.locator(`[data-testid="panel-tab-${TABS[0]}"]`).click()
await sleep(400)
await page.locator(`[data-testid="panel-tab-${TABS[TABS.length - 1]}"]`).click()
await sleep(150)
note((await page.locator('.vp-swap').first().getAttribute('data-dir')) === 'forward',
  'moving right brings the body in from the right')
await page.locator(`[data-testid="panel-tab-${TABS[0]}"]`).click()
await sleep(150)
note((await page.locator('.vp-swap').first().getAttribute('data-dir')) === 'back',
  'and moving left brings it in from the left')

// The divider inside a tab: the repository under the file tree, the checklist
// under the note. 「放在文件tab的下半段 可以上下拖动」.
//
// On its own page with ?project, because the *top* half only has something to
// show when there is one. The panels inside fetch and fail against no server,
// which is fine: the stack is the tab's structure and does not depend on either
// half having anything to say.
{
  const stacked = await browser.newPage({ viewport: { width: 1200, height: 900 } })
  // A payload rather than a server. The six figures are the part of this
  // redesign that was asked for by name — 「几个数字 有布局 … 好看一点」 — and
  // their layout is a thing you measure rather than read, so the harness gets
  // a reading to lay out. Everything else here still runs against nothing.
  await stacked.route('**/api/token-usage*', async (route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(SPEND),
    })
  })
  await stacked.goto(`${BASE}?project=1&w=420`, { waitUntil: 'networkidle' })
  await sleep(800)

  for (const id of TABS) {
    await stacked.locator(`[data-testid="panel-tab-${id}"]`).click()
    await sleep(400)
    note((await stacked.locator(`[data-testid="stack-${id}"]`).count()) === 1,
      `the ${id} tab is two panels`)
    note((await stacked.locator(`[data-testid="stack-${id}-divider"]`).count()) === 1,
      `and has a divider between them`)
    // The same dock under both tabs, in the same order. That is the whole
    // point of the restructure: 「都在侧边栏显示」 — the two things you glance
    // at are never somewhere you navigate to.
    note((await stacked.locator('[data-testid="panel-dock"]').count()) === 1,
      `the ${id} tab has the dock below it`)
    for (const block of ['tokens', 'monitor']) {
      note((await stacked.locator(`[data-testid="dock-${block}"]`).count()) === 1,
        `and the ${block} block is in it on ${id}`)
    }
  }

  // The blocks are in the order the dock declares, top to bottom, on both
  // tabs. A dock that reorders itself between tabs is two docks.
  {
    const y = async (block) =>
      (await stacked.locator(`[data-testid="dock-${block}"]`).boundingBox()).y
    note((await y('tokens')) < (await y('monitor')),
      'token spend sits above the monitor, which is where it was asked for')
  }

  // Six figures, three ranks. The instruction was 「有布局」 and 「好看一点」,
  // and the mistake it is warning against is six identical cards — so this
  // measures the hierarchy rather than the presence of the numbers: today is
  // the largest, its two pieces of context are the same size as each other and
  // smaller than it, and there is no third size hiding in between.
  await stacked.locator(`[data-testid="panel-tab-${TABS[0]}"]`).click()
  await sleep(600)
  {
    const block = stacked.locator('[data-testid="token-block"]')
    note((await block.count()) === 1, 'the token block is in the dock')
    const ranks = await block.evaluate((el) =>
      [...el.querySelectorAll('[data-testid="spend-figure"]')].map((v) => ({
        rank: v.getAttribute('data-rank'),
        value: (v.textContent ?? '').trim(),
        size: parseFloat(getComputedStyle(v).fontSize),
      })))
    note(ranks.length === 3, `three figures in the block (saw ${ranks.length})`)
    if (ranks.length === 3) {
      note(ranks[0].rank === 'hero' && ranks[0].size > ranks[1].size,
        `today is first and largest (${JSON.stringify(ranks.map((r) => [r.rank, r.size]))})`)
      note(ranks[1].size === ranks[2].size,
        'and its two pieces of context are one rank')
      note(new Set(ranks.map((r) => r.size)).size === 2,
        'two sizes and no third hiding between them')
      note(ranks.every((r) => r.value !== '—'),
        `every figure has a number (${ranks.map((r) => r.value).join(', ')})`)
    }
    // 分应用消耗 as a divided bar rather than three numbers, and readable
    // without colour: every segment is named in the legend with its share.
    const segs = await stacked.locator('[data-testid="token-tool-seg"]').count()
    const keys = await stacked.locator('[data-testid="token-tool-key"]').count()
    note(segs === 2 && keys === 2, `the bar has a segment and a legend entry per tool (${segs}/${keys})`)
    const legend = await stacked.locator('[data-testid="token-tools"]').innerText()
    note(/%/.test(legend) && /claude/.test(legend),
      `the legend names each tool and its share (${JSON.stringify(legend.replace(/\s+/g, ' '))})`)
    // 时间 and 字数, as a footer line rather than two more cards.
    const footer = await stacked.locator('[data-testid="token-block-footer"]').innerText()
    note(/30/.test(footer), `the footer says what period the figures cover (${footer})`)
  }

  // The press target is the header row and only the header row. A block that
  // is both a button and a container full of numbers is how somebody expands a
  // panel while trying to read one.
  for (const block of ['tokens', 'monitor']) {
    const header = stacked.locator(`[data-testid="dock-open-${block}"]`)
    note((await header.count()) === 1, `the ${block} block has one way in`)
    const label = await header.getAttribute('aria-label')
    note(!!label, `and it is named for a reader (${label})`)
    const inside = await stacked
      .locator(`[data-testid="dock-${block}"] button, [data-testid="dock-${block}"] a[href]`)
      .count()
    note(inside === 1, `and nothing else in the ${block} block is pressable (saw ${inside})`)
  }

  // Three states, and each replaces the one below it.
  for (const block of ['tokens', 'monitor']) {
    await stacked.locator(`[data-testid="dock-open-${block}"]`).click()
    await sleep(400)
    note((await stacked.locator(`[data-testid="detail-${block}"]`).count()) === 1,
      `pressing the ${block} header opens it in the panel`)
    // Instead of the stack, never beside it: there is no arrangement where a
    // block is half-open behind a tab strip.
    note((await stacked.locator('[data-testid="panel-dock"]').count()) === 0,
      `and the dock is gone while ${block} is open`)
    note((await stacked.locator('[role="tablist"]').count()) === 0,
      `and so is the tab strip`)

    if (block === 'monitor') {
      await stacked.locator(`[data-testid="detail-full-${block}"]`).click()
      await sleep(400)
      note((await stacked.locator(`[data-testid="detail-full-view-${block}"]`).count()) === 1,
        `pressing again gives ${block} the window`)
      // Escape backs out one level, not to the top.
      await stacked.keyboard.press('Escape')
      await sleep(400)
      note((await stacked.locator(`[data-testid="detail-full-view-${block}"]`).count()) === 0 &&
        (await stacked.locator(`[data-testid="detail-${block}"]`).count()) === 1,
        'Escape leaves the window and lands back in the panel')
    }

    await stacked.keyboard.press('Escape')
    await sleep(400)
    note((await stacked.locator(`[data-testid="detail-${block}"]`).count()) === 0,
      `and again closes ${block} back to the stack`)
    note((await stacked.locator('[data-testid="panel-dock"]').count()) === 1,
      'which brings the dock back')
  }

  // Drag it. The gesture is the one the pane dividers use, and it has to move
  // the boundary rather than the whole tab.
  await stacked.locator('[data-testid="panel-tab-files"]').click()
  await sleep(400)
  const before = await stacked.locator('[data-testid="stack-files-top"]').boundingBox()
  const grip = await stacked.locator('[data-testid="stack-files-divider"]').boundingBox()
  const halfTransition = () => stacked.evaluate(() =>
    getComputedStyle(document.querySelector('[data-testid="stack-files-top"]')).transitionProperty)
  const eased = await halfTransition()
  note(/flex-grow/.test(eased), `the halves ease while nothing is dragging them (${eased})`)
  await stacked.mouse.move(grip.x + grip.width / 2, grip.y + grip.height / 2)
  await stacked.mouse.down()
  await stacked.mouse.move(grip.x + grip.width / 2, grip.y - 140, { steps: 10 })
  await sleep(200)
  // And stop easing while one is. A transition on the thing under the pointer
  // means the divider arrives where the pointer was two frames ago, which
  // reads as the panel lagging rather than as a movement.
  const dragging = await halfTransition()
  note(!/flex-grow/.test(dragging), `and stop while it is being dragged (${dragging})`)
  await stacked.mouse.up()
  await sleep(400)
  const after = await stacked.locator('[data-testid="stack-files-top"]').boundingBox()
  note(before.height - after.height > 80,
    `dragging the divider up shrinks the top half (${Math.round(before.height)} -> ${Math.round(after.height)})`)

  // Neither half may be dragged away. The grip above the lower half is the
  // only way back to it, so a half dragged to nothing is a half with no way
  // back at all.
  const low = await stacked.locator('[data-testid="stack-files-divider"]').boundingBox()
  await stacked.mouse.move(low.x + low.width / 2, low.y + low.height / 2)
  await stacked.mouse.down()
  await stacked.mouse.move(low.x + low.width / 2, low.y - 4000, { steps: 12 })
  await sleep(200)
  await stacked.mouse.up()
  await sleep(400)
  const floored = await stacked.locator('[data-testid="stack-files-top"]').boundingBox()
  note(floored.height > 40, `a half cannot be dragged away (${Math.round(floored.height)}px left)`)

  // And it is remembered. A divider you have to place again on every reload is
  // one nobody moves twice.
  const placed = (await stacked.locator('[data-testid="stack-files-top"]').boundingBox()).height
  await stacked.reload({ waitUntil: 'networkidle' })
  await sleep(900)
  await stacked.locator('[data-testid="panel-tab-files"]').click()
  await sleep(500)
  const reloaded = (await stacked.locator('[data-testid="stack-files-top"]').boundingBox()).height
  note(Math.abs(reloaded - placed) < 12,
    `the divider is where it was left after a reload (${Math.round(placed)} -> ${Math.round(reloaded)})`)

  // The keyboard reaches it, because dragging is a mouse gesture and the panel
  // has to be usable without one. Down first, because the drag above left the
  // divider on its floor and up from the floor is correctly a no-op.
  //
  // The sign is the pane divider's: the arrow moves the *boundary*, so down
  // grows the half above it. A stack divider that answered the other way would
  // be two boundaries in one column moving opposite ways to the same key.
  await stacked.locator('[data-testid="stack-files-divider"]').focus()
  await stacked.keyboard.press('ArrowDown')
  await stacked.keyboard.press('ArrowDown')
  await sleep(400)
  const stepped = (await stacked.locator('[data-testid="stack-files-top"]').boundingBox()).height
  note(stepped - reloaded > 8,
    `ArrowDown moves the boundary down, growing the half above it (${Math.round(reloaded)} -> ${Math.round(stepped)})`)
  await stacked.keyboard.press('ArrowUp')
  await sleep(400)
  const back = (await stacked.locator('[data-testid="stack-files-top"]').boundingBox()).height
  note(back < stepped, `and ArrowUp takes it back (${Math.round(stepped)} -> ${Math.round(back)})`)

  await stacked.close()
}

// The pane menu.
await page.locator('[data-testid="pane-menu-0"]').click()
await sleep(300)
note(await page.locator('[data-testid="pane-menu-open-0"]').isVisible(), 'the pane menu opens')
const items = await page.locator('[data-testid="pane-menu-open-0"] [role="menuitem"]').count()
note(items === 5, `the menu has its five moves (saw ${items})`)
await page.keyboard.press('Escape')
await sleep(300)
note((await page.locator('[data-testid="pane-menu-open-0"]').count()) === 0, 'Escape closes it')

// Split without a pointer, through the menu.
await page.locator(`[data-testid="panel-tab-${TABS[1]}"]`).click()
await sleep(300)
await page.locator('[data-testid="pane-menu-0"]').click()
await sleep(250)
await page.locator('[data-testid="pane-menu-open-0"] [role="menuitem"]').nth(1).click()
await sleep(500)
note(Number(await panel.getAttribute('data-panes')) === 2, 'the menu splits a tab into a new pane')
note((await page.locator('[data-testid="pane-resize-0"]').count()) === 1, 'and a divider appears between them')

// Every pane draws its landing places, not only the one under the pointer.
// Two panes here, and the pointer is in the first: the second has to be
// showing where a drop would go as well, or finding out means going there.
{
  const tab = await page.locator('[data-testid="panel-tab-files"]').boundingBox()
  const first = await page.locator('[data-testid="pane-0"]').boundingBox()
  await page.mouse.move(tab.x + tab.width / 2, tab.y + tab.height / 2)
  await page.mouse.down()
  await page.mouse.move(first.x + first.width / 2, first.y + first.height * 0.6, { steps: 6 })
  await sleep(250)
  const near = await page.locator('[data-testid="pane-drops-0"] > *').count()
  const far = await page.locator('[data-testid="pane-drops-1"] > *').count()
  note(near === 3, `the pane under the pointer draws its three bands (saw ${near})`)
  note(far === 3, `every landing place is drawn while dragging, not only the near one (saw ${far})`)
  await page.keyboard.press('Escape')
  await page.mouse.up()
  await sleep(400)
}

// Restore.
await page.locator('[data-testid="pane-menu-1"]').click()
await sleep(250)
await page.locator('[data-testid="pane-menu-open-1"] [role="menuitem"]').last().click()
await sleep(400)
note(Number(await panel.getAttribute('data-panes')) === 1, 'restore puts it all back in one pane')

// Drag a tab into a new pane below.
{
  const tab = await page.locator('[data-testid="panel-tab-notes"]').boundingBox()
  const pane = await page.locator('[data-testid="pane-0"]').boundingBox()
  await page.mouse.move(tab.x + tab.width / 2, tab.y + tab.height / 2)
  await page.mouse.down()
  await page.mouse.move(tab.x + tab.width / 2, tab.y + 40, { steps: 4 })
  await sleep(200)
  const zones = await page.locator('[data-testid="pane-drops-0"] > *').count()
  note(zones === 3, `a drag paints the bands as soon as it starts (saw ${zones})`)
  await page.mouse.move(pane.x + pane.width / 2, pane.y + pane.height * 0.9, { steps: 8 })
  await sleep(250)
  const lit = await page.locator('[data-drop][data-over="true"]').getAttribute('data-drop')
  note(lit === 'after', `the band under the pointer lights up (${lit})`)
  await page.mouse.up()
  await sleep(500)
  note(Number(await panel.getAttribute('data-panes')) === 2, 'dropping made a second pane')
  const second = await page.locator('[data-testid="pane-1"]').getAttribute('data-pane-tabs')
  note(second === 'notes', `and it holds the tab that was dragged (${second})`)
}

// Drag it back onto the other pane's tabs.
{
  const tab = await page.locator('[data-testid="panel-tab-notes"]').boundingBox()
  const pane = await page.locator('[data-testid="pane-0"]').boundingBox()
  await page.mouse.move(tab.x + tab.width / 2, tab.y + tab.height / 2)
  await page.mouse.down()
  await page.mouse.move(pane.x + pane.width / 2, pane.y + pane.height / 2, { steps: 8 })
  await sleep(250)
  await page.mouse.up()
  await sleep(500)
  note(Number(await panel.getAttribute('data-panes')) === 1, 'dragging it back collapses the pane')
}

// Escape cancels a drag.
{
  const tab = await page.locator(`[data-testid="panel-tab-${TABS[TABS.length - 1]}"]`).boundingBox()
  const pane = await page.locator('[data-testid="pane-0"]').boundingBox()
  await page.mouse.move(tab.x + tab.width / 2, tab.y + tab.height / 2)
  await page.mouse.down()
  await page.mouse.move(pane.x + pane.width / 2, pane.y + pane.height * 0.9, { steps: 8 })
  await sleep(200)
  await page.keyboard.press('Escape')
  await sleep(200)
  note((await page.locator('[data-testid="pane-drops-0"]').count()) === 0, 'Escape clears the drop zones')
  await page.mouse.up()
  await sleep(400)
  note(Number(await panel.getAttribute('data-panes')) === 1, 'and the tab stays where it was')
}

// A plain click is still a plain click.
await page.locator(`[data-testid="panel-tab-${TABS[TABS.length - 1]}"]`).click()
await sleep(300)
note((await panel.getAttribute('data-tab')) === TABS[TABS.length - 1], 'a click still selects a tab')

// And a drag that went nowhere is not also one. The release still fires a
// click at the tab it started on; without that being swallowed, a cancelled
// drag quietly selects whatever it picked up.
await page.locator('[data-testid="panel-tab-files"]').click()
await sleep(300)
{
  const tab = await page.locator('[data-testid="panel-tab-notes"]').boundingBox()
  await page.mouse.move(tab.x + tab.width / 2, tab.y + tab.height / 2)
  await page.mouse.down()
  await page.mouse.move(tab.x + tab.width / 2 + 14, tab.y + tab.height / 2 + 2, { steps: 4 })
  await sleep(150)
  await page.mouse.up()
  await sleep(400)
  const on = await panel.getAttribute('data-tab')
  note(on === 'files', `a cancelled drag leaves the selection alone (on ${on})`)
}

// Keyboard: arrows move between tabs, Alt+arrow moves the tab.
await page.locator('[data-testid="panel-tab-files"]').click()
await sleep(200)
await page.locator('[data-testid="panel-tab-files"]').focus()
await page.keyboard.press('ArrowRight')
await sleep(300)
note((await panel.getAttribute('data-tab')) === TABS[1], 'ArrowRight moves to the next tab')
await page.keyboard.press('Alt+ArrowDown')
await sleep(400)
note(Number(await panel.getAttribute('data-panes')) === 2, 'Alt+ArrowDown moves the tab into a pane of its own')

// A layout written by a build that had six tabs.
//
// Every browser that has opened this panel before today has `git` and `todos`
// in that key, most of them in panes of their own. parseLayout drops a tab it
// does not know and drops the pane that empties, and panes.test.ts pins the
// arithmetic — this is the half that test cannot reach: that the panel comes
// back on screen at all, with a strip, rather than empty or thrown.
{
  const old = await browser.newPage({ viewport: { width: 1200, height: 900 } })
  await old.addInitScript(() => {
    // Every key the panel might read for this viewport band, because the band
    // is computed from the window and this script cannot know which one it
    // lands in.
    const layout = JSON.stringify({
      version: 1,
      groups: [
        { tabs: ['files', 'git'], active: 'git', size: 0.4 },
        { tabs: ['todos'], active: 'todos', size: 0.2 },
        { tabs: ['monitor'], active: 'monitor', size: 0.2 },
        { tabs: ['notes', 'tokens'], active: 'tokens', size: 0.2 },
      ],
    })
    for (const w of [0, 640, 900, 1200, 1440, 1800, 2400, 3200]) {
      for (const h of [0, 600, 800, 1000, 1300, 1700, 2200]) {
        localStorage.setItem(`vibepanel.panes.${w}x${h}`, layout)
      }
    }
  })
  await old.goto(BASE, { waitUntil: 'networkidle' })
  await sleep(900)
  const panes = Number(await old.locator('[data-testid="right-panel"]').getAttribute('data-panes'))
  note(panes >= 1, `yesterday's layout opens as a panel (${panes} panes)`)
  const tabs = await old.locator('[role="tab"]').count()
  note(tabs === TABS.length, `and every tab is reachable (saw ${tabs} of ${TABS.length})`)
  for (const gone of ['git', 'todos', 'monitor', 'tokens']) {
    note((await old.locator(`[data-testid="panel-tab-${gone}"]`).count()) === 0,
      `the retired ${gone} tab is not resurrected`)
  }
  await old.close()
}

// A too-short window merges panes rather than stacking empty strips.
await page.setViewportSize({ width: 1200, height: 240 })
await sleep(700)
note(Number(await panel.getAttribute('data-panes')) === 1, 'a short window folds the panes back together')


// A component that threw and was caught by its boundary still renders
// something, so every assertion above can pass over a panel that is on fire.
note(pageErrors.length === 0, `nothing threw (${pageErrors.slice(0, 2).join('; ') || 'clean'})`)

await browser.close()
stop()

if (failures.length > 0) {
  console.error('')
  for (const f of failures) console.error(`  - ${f}`)
}
// The line `make verify` collects. Every check in this tree ends with one, so
// a run that skipped a section cannot end in "all checks passed".
console.log(`\n=== panes check: ${failures.length} FAIL, 0 WARN ===`)
process.exit(failures.length > 0 ? 1 : 0)
