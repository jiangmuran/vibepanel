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

const panel = page.locator('[data-testid="right-panel"]')
note(await panel.isVisible(), 'the panel renders')
note((await page.locator('[data-testid="panel-header"]').count()) === 1, 'one panel header')
for (const id of ['files', 'monitor', 'notes', 'todos', 'tokens']) {
  note((await page.locator(`[data-testid="panel-tab-${id}"]`).count()) === 1, `one ${id} tab`)
}
note((await page.locator('[data-testid="panel-split"]').count()) === 1, 'the split control is there')
note((await page.locator('[data-testid="panel-collapse"]').count()) === 1, 'the collapse control is there')
note((await page.locator('[data-testid="pane-menu-0"]').count()) === 1, 'the pane menu is there')

// The control set does not change with the tab.
for (const id of ['files', 'monitor', 'notes', 'todos', 'tokens']) {
  await page.locator(`[data-testid="panel-tab-${id}"]`).click()
  await sleep(250)
  const n = await page.locator('[data-testid="panel-split"], [data-testid="panel-collapse"], [data-testid="pane-menu-0"]').count()
  note(n === 3, `three header controls on the ${id} tab`)
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
await page.locator('[data-testid="panel-tab-tokens"]').click()
await sleep(500)
const m2 = await markerAt()
note(m1 !== null && m2 !== null && Math.abs(m2 - m1) > 20, `the marker travels (${m1} -> ${m2})`)

// And travels rather than teleporting: the whole complaint was that switching
// tabs happened between two frames with nothing to follow.
//
// Measured on a narrow panel, where the tabs are icons and no label folds open
// as the selection moves. On a labelled strip the tab under the marker grows
// over 260ms and the marker follows it, so the marker appears to travel even
// with no transition of its own — a check that passes for a reason other than
// the one it names.
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
  await narrow.locator('[data-testid="panel-tab-files"]').click()
  await sleep(500)
  const from = await at()
  await narrow.locator('[data-testid="panel-tab-tokens"]').click()
  await sleep(60)
  const mid = await at()
  await sleep(600)
  const end = await at()
  const moving = mid !== null && end !== null && from !== null &&
    Math.abs(end - mid) > 6 && Math.abs(mid - from) > 2
  note(moving, `the marker is mid-flight a frame later (${from} -> ${mid} -> ${end})`)
  await narrow.close()
}

// The body enters from the side the strip moved towards.
await page.locator('[data-testid="panel-tab-files"]').click()
await sleep(400)
await page.locator('[data-testid="panel-tab-tokens"]').click()
await sleep(150)
note((await page.locator('.vp-swap').first().getAttribute('data-dir')) === 'forward',
  'moving right brings the body in from the right')
await page.locator('[data-testid="panel-tab-files"]').click()
await sleep(150)
note((await page.locator('.vp-swap').first().getAttribute('data-dir')) === 'back',
  'and moving left brings it in from the left')

// The split preset.
await page.locator('[data-testid="panel-split"]').click()
await sleep(500)
note((await panel.getAttribute('data-split')) === 'true', 'the split preset builds the pair')
note(Number(await panel.getAttribute('data-panes')) >= 2, 'the preset made more than one pane')
await page.locator('[data-testid="panel-split"]').click()
await sleep(400)
note((await panel.getAttribute('data-split')) === 'false', 'and takes it apart again')

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
await page.locator('[data-testid="panel-tab-monitor"]').click()
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
  const tab = await page.locator('[data-testid="panel-tab-todos"]').boundingBox()
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
await page.locator('[data-testid="panel-tab-todos"]').click()
await sleep(300)
note((await panel.getAttribute('data-tab')) === 'todos', 'a click still selects a tab')

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
note((await panel.getAttribute('data-tab')) === 'monitor', 'ArrowRight moves to the next tab')
await page.keyboard.press('Alt+ArrowDown')
await sleep(400)
note(Number(await panel.getAttribute('data-panes')) === 2, 'Alt+ArrowDown moves the tab into a pane of its own')

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
