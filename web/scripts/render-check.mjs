// Headless render check for vibepanel.
//
// Boots the real binary against a throwaway tmux socket and data dir, drives it
// with a real browser, and reports anything that looks wrong: console errors,
// failed requests, unreadable colour pairs, a terminal that never connects, a
// palette that does not follow the theme.
//
//   npm run build && (cd .. && go build -o vibepanel ./cmd/vibepanel)
//   npm run check:render
//
// It exists because the bugs that matter most here are not the ones a unit
// test sees. Every finding it has produced so far — a replay injecting terminal
// responses into the shell, a viewer claiming the grid by loading the page, the
// terminal palette lagging one theme change behind — needed a real browser
// talking to a real tmux to show up at all.
//
// Exits non-zero on any FAIL. Screenshots land in the directory given as the
// second argument.
import { chromium } from 'playwright'
import { spawn, execSync } from 'node:child_process'
import { createServer } from 'node:net'
import { mkdtempSync, rmSync, mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const BIN = process.argv[2] ?? new URL('../../vibepanel', import.meta.url).pathname
// A free port picked by the kernel, not a guess. A hard-coded one silently
// connects to whatever is already listening — including an orphaned server
// from a previous run, which then serves a stale build and produces an hour of
// confusing results.
const PORT = await new Promise((resolve, reject) => {
  const probe = createServer()
  probe.once('error', reject)
  probe.listen(0, '127.0.0.1', () => {
    const { port } = probe.address()
    probe.close(() => resolve(port))
  })
})
const SOCKET = `vprender-${process.pid}`
const DATA = mkdtempSync(join(tmpdir(), 'vprender-'))
const SHOTS = process.argv[3] ?? join(DATA, 'shots')
mkdirSync(SHOTS, { recursive: true })

const findings = []
const note = (sev, area, msg) => findings.push({ sev, area, msg })

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

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
function parseColor(c) {
  const m = c.match(/rgba?\(([^)]+)\)/)
  if (!m) return null
  const parts = m[1].split(/[\s,/]+/).filter(Boolean).map(Number)
  return { r: parts[0], g: parts[1], b: parts[2], a: parts.length > 3 ? parts[3] : 1 }
}
function lum({ r, g, b }) {
  const f = (v) => {
    v /= 255
    return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4
  }
  return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b)
}
function contrast(fg, bg) {
  // Flatten any alpha onto the background before comparing; ignoring it makes
  // translucent secondary text look far more readable than it is.
  const a = fg.a ?? 1
  const flat = { r: fg.r * a + bg.r * (1 - a), g: fg.g * a + bg.g * (1 - a), b: fg.b * a + bg.b * (1 - a) }
  const l1 = lum(flat), l2 = lum(bg)
  const [hi, lo] = l1 > l2 ? [l1, l2] : [l2, l1]
  return (hi + 0.05) / (lo + 0.05)
}

let cleanedUp = false
const server = spawn(BIN, ['serve'], {
  env: {
    ...process.env,
    VIBEPANEL_DATA_DIR: DATA,
    VIBEPANEL_TMUX_SOCKET: SOCKET,
    VIBEPANEL_ADDR: `127.0.0.1:${PORT}`,
  },
  stdio: ['ignore', 'pipe', 'pipe'],
})
let serverLog = ''
server.stdout.on('data', (d) => (serverLog += d))
server.stderr.on('data', (d) => (serverLog += d))

const BASE = `http://127.0.0.1:${PORT}`
let browser

async function cleanup() {
  if (cleanedUp) return
  cleanedUp = true
  try { await browser?.close() } catch { /* already gone */ }
  server.kill('SIGTERM')
  await sleep(400)
  try { execSync(`tmux -L ${SOCKET} kill-server`, { stdio: 'ignore' }) } catch { /* none */ }
  // kill-server leaves the socket file behind; a few hundred of those pile up
  // fast when this runs in a loop.
  try { rmSync(join(process.env.TMUX_TMPDIR || '/tmp', `tmux-${process.getuid()}`, SOCKET), { force: true }) } catch { /* best effort */ }
  try { rmSync(DATA, { recursive: true, force: true }) } catch { /* best effort */ }
}

// The server is a child process, and a child does not die because its parent
// was killed. Without these handlers, every `timeout`-terminated run leaves a
// panel listening on a port and a tmux server holding sessions.
for (const sig of ['SIGINT', 'SIGTERM', 'SIGHUP']) {
  process.on(sig, () => {
    void cleanup().finally(() => process.exit(130))
  })
}

try {
  if (!(await waitHealth(BASE))) {
    note('FAIL', 'server', `did not become healthy on ${BASE}\n${serverLog}`)
    throw new Error('server not healthy')
  }

  // Seed content through the API, the same way the UI would.
  const proj = await (await fetch(`${BASE}/api/projects`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path: process.cwd(), name: 'render-check' }),
  })).json()

  const mkSession = (cmd) =>
    fetch(`${BASE}/api/sessions`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ projectId: proj.id, command: cmd }),
    }).then((r) => r.json())

  await mkSession(['sh', '-c', 'echo RENDER_CHECK_MARKER; exec sh'])
  await mkSession(['htop'])

  // A session that rings the terminal bell: without hooks this is the only
  // signal that an agent has stopped and wants a person, and surfacing it is
  // the panel's whole reason to exist.
  await mkSession(['sh', '-c', "sleep 1; printf 'needs you\\a'; exec sleep 300"])

  // A second project, so ordering can be exercised.
  await fetch(`${BASE}/api/projects`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path: DATA, name: 'zzz-second' }),
  })
  await sleep(2500) // let the poller derive titles

  browser = await chromium.launch({ headless: true })
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const page = await ctx.newPage()

  const consoleErrors = []
  const pageErrors = []
  const failedReqs = []
  page.on('console', (m) => {
    if (m.type() === 'error') consoleErrors.push(m.text())
  })
  page.on('pageerror', (e) => pageErrors.push(String(e)))
  page.on('requestfailed', (r) => failedReqs.push(`${r.url()} — ${r.failure()?.errorText}`))

  await page.goto(BASE, { waitUntil: 'networkidle' })
  await sleep(1200)

  for (const e of pageErrors) note('FAIL', 'js', `uncaught: ${e}`)
  for (const e of consoleErrors) note('WARN', 'console', e)
  for (const f of failedReqs) note('FAIL', 'net', f)

  // ── structure ────────────────────────────────────────────────────────────
  const sidebarText = await page.locator('[data-testid="sidebar"]').innerText().catch(() => '')
  if (!sidebarText.toLowerCase().includes('render-check')) {
    note('FAIL', 'ui', `sidebar does not list the project; saw: ${JSON.stringify(sidebarText)}`)
  }
  const sessionRows = await page.locator('[data-testid="session-row"]').count()
  if (sessionRows < 2) note('FAIL', 'ui', `expected 2 session rows, found ${sessionRows}`)

  // ── websocket status ─────────────────────────────────────────────────────
  const status = await page
    .locator('[data-testid="connection"]')
    .getAttribute('data-status')
    .catch(() => null)
  if (status !== 'open') note('FAIL', 'ws', `socket status is ${JSON.stringify(status)}, want "open"`)

  // ── terminal actually painted something ──────────────────────────────────
  await page.waitForSelector('.xterm-screen', { timeout: 8000 }).catch(() =>
    note('FAIL', 'term', 'xterm never mounted'),
  )
  await sleep(1500)
  const termText = await page.locator('.xterm-screen').innerText().catch(() => '')
  if (!termText.trim()) note('FAIL', 'term', 'terminal rendered but is empty')

  // ── typing round trip ────────────────────────────────────────────────────
  // Pick the shell explicitly. Sessions sort by urgency, so the one that rang
  // the bell is at the top and gets auto-selected — typing into it would be
  // typing into a sleep.
  const shellRow = page.locator('[data-testid="session-row"]', { hasText: 'scratchpad' }).first()
  if (await shellRow.isVisible().catch(() => false)) {
    await shellRow.click()
    await sleep(1200)
  } else {
    note('WARN', 'ui', 'could not find the shell session row to select')
  }
  await page.locator('.xterm-screen').click()
  await page.keyboard.type('echo BROWSER_TYPED_OK')
  await page.keyboard.press('Enter')
  let typed = false
  for (let i = 0; i < 40; i++) {
    const t = await page.locator('.xterm-screen').innerText().catch(() => '')
    if ((t.match(/BROWSER_TYPED_OK/g) ?? []).length >= 2) { typed = true; break }
    await sleep(250)
  }
  if (!typed) note('FAIL', 'term', 'typing into the terminal produced no output')

  // ── contrast, both themes ────────────────────────────────────────────────
  const probeContrast = async (label) => {
    const results = await page.evaluate(() => {
      const out = []
      const seen = new Set()
      const SKIP = new Set(['STYLE', 'SCRIPT', 'TEMPLATE', 'NOSCRIPT', 'TITLE'])
      const walk = (el) => {
        if (SKIP.has(el.tagName)) return
        const cs = getComputedStyle(el)
        // xterm keeps offscreen measurement nodes in the DOM; they are not
        // shown to anyone and their colours mean nothing.
        if (cs.visibility === 'hidden' || cs.display === 'none' || cs.opacity === '0') return
        const box = el.getBoundingClientRect()
        if (box.width < 1 || box.height < 1) return
        if (box.bottom < 0 || box.right < 0) return
        const text = (el.textContent ?? '').trim()
        const own = Array.from(el.childNodes).some(
          (n) => n.nodeType === 3 && n.textContent.trim(),
        )
        if (own && text) {
          // Walk up for the nearest painted background.
          let bg = 'rgba(0, 0, 0, 0)'
          let p = el
          while (p) {
            const c = getComputedStyle(p).backgroundColor
            if (c && !/rgba\(0, 0, 0, 0\)|transparent/.test(c)) { bg = c; break }
            p = p.parentElement
          }
          const key = `${cs.color}|${bg}|${cs.fontSize}`
          if (!seen.has(key)) {
            seen.add(key)
            out.push({
              color: cs.color, bg, fontSize: parseFloat(cs.fontSize),
              weight: cs.fontWeight, sample: text.slice(0, 40),
            })
          }
        }
        for (const c of el.children) walk(c)
      }
      walk(document.body)
      return out
    })
    for (const r of results) {
      const fg = parseColor(r.color), bg = parseColor(r.bg)
      if (!fg || !bg) continue
      const ratio = contrast(fg, bg)
      const large = r.fontSize >= 18.66 || (r.fontSize >= 14 && Number(r.weight) >= 700)
      const min = large ? 3 : 4.5
      if (ratio < min) {
        note(ratio < 2 ? 'FAIL' : 'WARN', `contrast/${label}`,
          `${ratio.toFixed(2)}:1 (need ${min}) — "${r.sample}" ${r.color} on ${r.bg}`)
      }
    }
  }

  // Drive the real control rather than setting the attribute: the attribute
  // alone changes the CSS but not React's state, so xterm keeps its old
  // palette and the check silently measures a combination no user can produce.
  const themeButton = page.locator('[data-testid="theme-toggle"]')
  const setTheme = async (want) => {
    for (let i = 0; i < 4; i++) {
      const cur = await page.evaluate(() => document.documentElement.dataset.theme ?? 'system')
      if (cur === want) return true
      await themeButton.click()
      await sleep(300)
    }
    return false
  }
  if (!(await setTheme('light'))) note('WARN', 'theme', 'could not reach the light theme via the toggle')
  await sleep(500)
  await probeContrast('light')
  await page.screenshot({ path: join(SHOTS, 'light.png'), fullPage: false })

  if (!(await setTheme('dark'))) note('WARN', 'theme', 'could not reach the dark theme via the toggle')
  await sleep(500)
  await probeContrast('dark')
  await page.screenshot({ path: join(SHOTS, 'dark.png'), fullPage: false })

  // The terminal is the largest surface on the page. xterm holds its own
  // palette, so if it is not rebuilt when the theme changes it stays bright
  // white in dark mode — and React runs child effects before parent ones,
  // which is exactly how that regressed once already.
  // Checks the viewport specifically, not just the nearest painted ancestor of
  // the rows: the viewport is a *sibling* of .xterm-screen, so walking up from
  // the rows skips it entirely — which is how a hard-coded black viewport
  // survived an earlier theme check unnoticed.
  const termLuminance = async () => page.evaluate(() => {
    const el = document.querySelector('.xterm-viewport') ?? document.querySelector('.xterm')
    if (!el) return null
    let p = el
    while (p) {
      const c = getComputedStyle(p).backgroundColor
      if (c && !/rgba\(0, 0, 0, 0\)|transparent/.test(c)) {
        const m = c.match(/rgba?\(([^)]+)\)/)
        if (!m) return null
        const [r, g, b] = m[1].split(/[\s,/]+/).filter(Boolean).map(Number)
        const f = (v) => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4 }
        return { css: c, l: 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b) }
      }
      p = p.parentElement
    }
    return null
  })
  const darkTerm = await termLuminance()
  if (!darkTerm || darkTerm.l > 0.2) {
    note('FAIL', 'theme',
      `terminal background is ${darkTerm?.css ?? 'unknown'} in dark mode (luminance ${darkTerm?.l.toFixed(3)}); xterm did not follow the theme`)
  }
  if (!(await setTheme('light'))) note('WARN', 'theme', 'could not return to the light theme')
  await sleep(600)
  const lightTerm = await termLuminance()
  if (!lightTerm || lightTerm.l < 0.6) {
    note('FAIL', 'theme',
      `terminal background is ${lightTerm?.css ?? 'unknown'} in light mode (luminance ${lightTerm?.l.toFixed(3)})`)
  }
  if (!(await setTheme('dark'))) note('WARN', 'theme', 'could not return to the dark theme')
  await sleep(400)

  // ── two viewers: sync and size arbitration ───────────────────────────────
  const page2 = await ctx.newPage()
  const p2Errors = []
  page2.on('pageerror', (e) => p2Errors.push(String(e)))
  await page2.setViewportSize({ width: 520, height: 700 })
  await page2.goto(BASE, { waitUntil: 'networkidle' })
  await sleep(2500)
  for (const e of p2Errors) note('FAIL', 'js/viewer2', `uncaught: ${e}`)

  // Wait rather than sampling: the affordance appears only once the subscribe
  // round trip has told this viewer it is passive.
  const takeControlVisible = await page2
    .locator('[data-testid="take-control"]')
    .waitFor({ state: 'visible', timeout: 8000 })
    .then(() => true)
    .catch(() => false)
  if (!takeControlVisible) {
    note('WARN', 'arbitration',
      'the small second viewer shows no "take control" affordance; it may have silently taken the grid')
  }

  await page.bringToFront()
  await page.locator('.xterm-screen').click()
  await page.keyboard.type('echo SYNC_TO_SECOND_VIEWER')
  await page.keyboard.press('Enter')
  let synced = false
  for (let i = 0; i < 40; i++) {
    const t = await page2.locator('.xterm-screen').innerText().catch(() => '')
    if (t.includes('SYNC_TO_SECOND_VIEWER')) { synced = true; break }
    await sleep(250)
  }
  if (!synced) note('FAIL', 'sync', 'the second viewer never saw output typed in the first')
  await page2.screenshot({ path: join(SHOTS, 'viewer2-narrow.png') })

  // A passive viewer resizing its own window must not move the shared grid.
  const gridBefore = await (await fetch(`${BASE}/api/state`)).json()
  await page2.setViewportSize({ width: 380, height: 600 })
  await sleep(1800)
  const gridAfter = await (await fetch(`${BASE}/api/state`)).json()
  const before = gridBefore.sessions.map((s) => `${s.id}:${s.cols}x${s.rows}`).join(',')
  const after = gridAfter.sessions.map((s) => `${s.id}:${s.cols}x${s.rows}`).join(',')
  if (before !== after) {
    note('FAIL', 'arbitration',
      `a passive viewer resizing its window moved the shared grid: ${before} -> ${after}`)
  }

  // ── reload keeps content ─────────────────────────────────────────────────
  // A background tab is throttled and stops painting, so xterm's DOM renderer
  // has nothing to read. Bring it forward before asserting on what it shows.
  await page.bringToFront()
  await sleep(500)
  const titleBefore = await page.locator('[data-testid="session-title"]').innerText().catch(() => '')
  await page.reload({ waitUntil: 'networkidle' })
  await sleep(3000)
  const titleAfter = await page.locator('[data-testid="session-title"]').innerText().catch(() => '')
  if (titleBefore && titleAfter && titleBefore !== titleAfter) {
    note('FAIL', 'selection',
      `a reload changed the selected session: ${JSON.stringify(titleBefore)} -> ${JSON.stringify(titleAfter)}`)
  }
  const afterReload = await page.locator('.xterm-screen').innerText().catch(() => '')
  if (!afterReload.includes('BROWSER_TYPED_OK')) {
    note('FAIL', 'replay',
      `scrollback did not come back after a reload; terminal shows ${JSON.stringify(
        afterReload.replace(/\s+/g, ' ').trim().slice(0, 200),
      )}`)
  }
  // The replay must not make the fresh terminal re-answer capability queries
  // it finds in the buffer; those answers land at the shell prompt.
  if (/\[\?\d+;\d+c|\[>\d+;\d+;\d+c/.test(afterReload)) {
    note('FAIL', 'replay',
      `terminal responses were injected into the session by the replay: ${JSON.stringify(
        afterReload.replace(/\s+/g, ' ').trim().slice(-120),
      )}`)
  }

  // ── bottom terminals ─────────────────────────────────────────────────────
  // They belong to the session above them and follow it. A terminal opened
  // while working on one thing must not still be sitting there, pointing at
  // the wrong directory, after switching to another.
  const showBottom = page.locator('[data-testid="bottom-show"]')
  if (await showBottom.isVisible().catch(() => false)) {
    await showBottom.click()
    await sleep(600)
  }
  const bottom = page.locator('[data-testid="bottom-terminals"]')
  if (!(await bottom.isVisible().catch(() => false))) {
    note('FAIL', 'bottom', 'the bottom terminal panel never appeared')
  } else {
    await page.locator('[data-testid="bottom-new"]').click()
    let gotTab = false
    for (let i = 0; i < 30; i++) {
      if ((await page.locator('[data-testid="bottom-tab"]').count()) > 0) { gotTab = true; break }
      await sleep(400)
    }
    if (!gotTab) {
      note('FAIL', 'bottom', 'creating a bottom terminal produced no tab')
    } else {
      // It must be a working terminal, not just a tab.
      const bottomScreen = bottom.locator('.xterm-screen')
      await bottomScreen.click()
      await page.keyboard.type('echo BOTTOM_TERMINAL_OK')
      await page.keyboard.press('Enter')
      let echoed = false
      for (let i = 0; i < 40; i++) {
        const txt = await bottomScreen.innerText().catch(() => '')
        if ((txt.match(/BOTTOM_TERMINAL_OK/g) ?? []).length >= 2) { echoed = true; break }
        await sleep(300)
      }
      if (!echoed) note('FAIL', 'bottom', 'typing into a bottom terminal produced no output')

      // A second terminal, to check the tabs are told apart. They all live in
      // the same directory as the session above them, so a naming rule based
      // on the directory would produce a row of identical tabs.
      await page.locator('[data-testid="bottom-new"]').click()
      await sleep(2500)
      const labels = await page.$$eval('[data-testid="bottom-tab"]', (els) =>
        els.map((el) => el.textContent?.trim() ?? ''),
      )
      if (labels.length < 2) {
        note('WARN', 'bottom', `expected two terminal tabs, saw ${JSON.stringify(labels)}`)
      } else if (new Set(labels).size !== labels.length) {
        note('FAIL', 'bottom', `terminal tabs are indistinguishable: ${JSON.stringify(labels)}`)
      }

      // Switching the main session must swap the strip, not carry it across.
      const otherRow = page.locator('[data-testid="session-row"]', { hasText: 'htop' }).first()
      if (await otherRow.isVisible().catch(() => false)) {
        await otherRow.click()
        await sleep(1500)
        const carried = await page.locator('[data-testid="bottom-tab"]').count()
        if (carried !== 0) {
          note('FAIL', 'bottom',
            `switching sessions carried ${carried} terminal tab(s) from the previous one`)
        }
        // And switching back brings it back.
        await page.locator('[data-testid="session-row"]', { hasText: 'scratchpad' }).first().click()
        await sleep(1500)
        if ((await page.locator('[data-testid="bottom-tab"]').count()) === 0) {
          note('FAIL', 'bottom', 'the terminal did not come back when switching back')
        }
      }
    }
    await page.screenshot({ path: join(SHOTS, 'bottom.png') })
  }

  // ── session state ────────────────────────────────────────────────────────
  // The bell has to reach the panel through tmux, the PTY and the detector.
  // tmux's bell-action swallowed it entirely once, silently, so this is worth
  // asserting end to end rather than trusting the unit tests.
  let sawWaiting = false
  for (let i = 0; i < 40; i++) {
    const states = await page.$$eval('[data-testid="state-dot"]', (els) =>
      els.map((el) => el.getAttribute('data-state')),
    )
    if (states.includes('waiting')) { sawWaiting = true; break }
    await sleep(500)
  }
  if (!sawWaiting) {
    note('FAIL', 'state', 'a session that rang the bell never showed as waiting')
  } else {
    // And it must be visible from another tab, which is where the user
    // actually is.
    const title = await page.title()
    if (!/^\(\d+\)/.test(title)) {
      note('FAIL', 'state', `tab title is ${JSON.stringify(title)}; a waiting session should show a count`)
    }

    // Clicking the indicator is how you say "I have dealt with this".
    const waitingDot = page.locator('[data-testid="state-dot"][data-state="waiting"]').first()
    await waitingDot.click()
    let cleared = false
    for (let i = 0; i < 20; i++) {
      const still = await page.locator('[data-testid="state-dot"][data-state="waiting"]').count()
      if (still === 0) { cleared = true; break }
      await sleep(400)
    }
    if (!cleared) {
      note('FAIL', 'state', 'clicking the indicator did not clear the waiting state')
    } else {
      // And the override must survive the poller, or the control is useless.
      await sleep(5000)
      const back = await page.locator('[data-testid="state-dot"][data-state="waiting"]').count()
      if (back > 0) note('FAIL', 'state', 'the manual override was undone by the poller')
    }
  }

  // ── dragging a project reorders it ───────────────────────────────────────
  // Built on Pointer Events precisely so it works on touch as well as mouse,
  // which HTML5 drag-and-drop does not, so it is worth proving it works at all.
  await page.setViewportSize({ width: 1440, height: 900 })
  await sleep(600)
  const projectNames = async () =>
    page.$$eval('[data-testid="project-group"]', (els) =>
      els.map((el) => el.querySelector('span')?.textContent?.trim() ?? ''),
    )
  const beforeDrag = await projectNames()
  if (beforeDrag.length < 2) {
    note('WARN', 'reorder', `expected two projects to drag, saw ${JSON.stringify(beforeDrag)}`)
  } else {
    const groups = page.locator('[data-testid="project-group"]')
    const second = groups.nth(1)
    await second.hover()
    const grip = second.locator('[data-testid="project-grip"]')
    const gripBox = await grip.boundingBox()
    const firstBox = await groups.nth(0).boundingBox()
    if (!gripBox || !firstBox) {
      note('WARN', 'reorder', 'could not locate the drag handle')
    } else {
      await page.mouse.move(gripBox.x + gripBox.width / 2, gripBox.y + gripBox.height / 2)
      await page.mouse.down()
      // Above the first project's midpoint, which is where the gap moves.
      await page.mouse.move(firstBox.x + 40, firstBox.y + firstBox.height / 2 - 8, { steps: 12 })
      await page.mouse.up()
      await sleep(1200)
      const afterDrag = await projectNames()
      if (afterDrag[0] !== beforeDrag[1]) {
        note('FAIL', 'reorder',
          `dragging the second project to the top did nothing: ${JSON.stringify(beforeDrag)} -> ${JSON.stringify(afterDrag)}`)
      }
      // And the panel should now offer a way back to activity ordering.
      const backToAuto = await page
        .locator('button[title="Sort by recent activity again"]')
        .isVisible()
        .catch(() => false)
      if (!backToAuto) {
        note('FAIL', 'reorder',
          'after a manual reorder there is no control to return to automatic ordering')
      }
    }
  }

  // ── the narrow-screen drawer must be opaque ──────────────────────────────
  // It floats over the terminal. A translucent one leaves session output
  // showing through the project list, and backdrop-filter cannot be relied on
  // to hide it — headless browsers and prefers-reduced-transparency both skip
  // it entirely.
  await page.setViewportSize({ width: 390, height: 844 })
  await sleep(700)
  const menu = page.locator('header button[title="Projects"]')
  if (await menu.isVisible().catch(() => false)) {
    await menu.click()
    await sleep(600)
    const drawer = await page.evaluate(() => {
      const el = document.querySelector('[data-testid="sidebar"][data-overlay="true"]')
      if (!el) return null
      const cs = getComputedStyle(el)
      const m = cs.backgroundColor.match(/rgba?\(([^)]+)\)/)
      const parts = m ? m[1].split(/[\s,/]+/).filter(Boolean).map(Number) : []
      return { bg: cs.backgroundColor, alpha: parts.length > 3 ? parts[3] : 1 }
    })
    if (!drawer) {
      note('WARN', 'layout/drawer', 'the narrow-screen drawer did not open')
    } else if (drawer.alpha < 0.98) {
      note('FAIL', 'layout/drawer',
        `the drawer is translucent (${drawer.bg}); terminal output shows through the project list`)
    }
    // Close through the drawer's own control; the full-screen backdrop sits
    // underneath it and cannot be clicked where the drawer covers it.
    await page
      .locator('[data-testid="sidebar"][data-overlay="true"] header button')
      .first()
      .click({ timeout: 3000 })
      .catch(() => {})
    await sleep(400)
  } else {
    note('WARN', 'layout/drawer', 'no menu button at phone width; the sidebar may be taking the screen')
  }

  // ── horizontal overflow, desktop and phone ───────────────────────────────
  for (const [label, vp] of [['desktop', { width: 1440, height: 900 }], ['phone', { width: 390, height: 844 }]]) {
    await page.setViewportSize(vp)
    await sleep(600)
    const over = await page.evaluate(() =>
      document.documentElement.scrollWidth - document.documentElement.clientWidth)
    if (over > 1) note('WARN', `layout/${label}`, `page scrolls horizontally by ${over}px`)
    await page.screenshot({ path: join(SHOTS, `${label}.png`) })
  }
} catch (e) {
  note('FAIL', 'harness', String(e))
} finally {
  await cleanup()
}

const order = { FAIL: 0, WARN: 1, INFO: 2 }
findings.sort((a, b) => order[a.sev] - order[b.sev])
const fails = findings.filter((f) => f.sev === 'FAIL').length
console.log(`\n=== render check: ${fails} FAIL, ${findings.filter(f => f.sev === 'WARN').length} WARN ===`)
for (const f of findings) console.log(`[${f.sev}] ${f.area}: ${f.msg}`)
console.log(`\nscreenshots: ${SHOTS}`)
process.exit(fails > 0 ? 1 : 0)
