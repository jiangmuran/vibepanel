// What the panel is like with a couple of dozen sessions open.
//
//   npm run build && (cd .. && go build -o vibepanel ./cmd/vibepanel)
//   npm run check:scale
//
// The panel attaches to every session, not only the one being watched, so that
// state detection works for the ones you are not looking at. That decision has
// a cost per session — a tmux client, a PTY pump, a 2 MiB replay buffer — and
// it was reasoned about rather than measured. This measures it, at the load the
// panel is actually used at: the setup that prompted the project runs 17 agents.
//
// Failures here are about degradation rather than correctness: a snapshot that
// grew to hundreds of kilobytes, a sidebar that scrolls the selection out of
// reach, a poller that stops keeping up.
import { chromium } from 'playwright'
import { spawn, execSync } from 'node:child_process'
import { mkdtempSync, mkdirSync, rmSync, readFileSync } from 'node:fs'
import { createServer } from 'node:net'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { sweepStaleSockets } from './lib/stale.mjs'
import { findUnreachable } from './lib/overflow.mjs'
import { findFadedControls } from './lib/faded.mjs'
import { assertFreshBuild } from './lib/fresh.mjs'

const BIN = process.argv[2] ?? new URL('../../vibepanel', import.meta.url).pathname
// Measuring a build that does not contain the change is the one failure that
// looks exactly like a pass. See lib/fresh.mjs.
assertFreshBuild(BIN, new URL('../../', import.meta.url).pathname)
const SHOTS = process.argv[3] ?? join(tmpdir(), 'vpscale-shots')
const COUNT = Number(process.argv[4] ?? 24)
mkdirSync(SHOTS, { recursive: true })

const PORT = await new Promise((resolve, reject) => {
  const probe = createServer()
  probe.once('error', reject)
  probe.listen(0, '127.0.0.1', () => {
    const { port } = probe.address()
    probe.close(() => resolve(port))
  })
})
const SOCKET = `vpscale-${process.pid}`

// Before anything else: a run killed with SIGKILL cannot clean up after
// itself, and what it leaves behind is a tmux server holding live sessions.
sweepStaleSockets((msg) => console.log(`==> ${msg}`))
const DATA = mkdtempSync(join(tmpdir(), 'vpscale-'))
const FAKE_HOME = mkdtempSync(join(tmpdir(), 'vpscale-home-'))

const findings = []

// Collected at module scope and reported in the finally below.
//
// These lived inside the try and were reported near the end of it, so any
// earlier failure — a click that timed out because the page was blank — threw
// past the one piece of evidence that explained the blank page. One of them
// collected these and never reported them at all. An uncaught error in the
// page is the most informative thing a run can produce; it has to survive the
// run failing.
const pageErrors = []
const note = (sev, area, msg) => findings.push({ sev, area, msg })
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

let serverLog = ''
const server = spawn(BIN, ['serve'], {
  env: {
    ...process.env,
    HOME: FAKE_HOME,
    VIBEPANEL_DATA_DIR: DATA,
    VIBEPANEL_TMUX_SOCKET: SOCKET,
    VIBEPANEL_ADDR: `127.0.0.1:${PORT}`,
    VIBEPANEL_DOMAIN: 'localhost',
  },
  stdio: ['ignore', 'pipe', 'pipe'],
})
server.stdout.on('data', (d) => (serverLog += d))
server.stderr.on('data', (d) => (serverLog += d))

let browser
let cleanedUp = false
async function cleanup() {
  if (cleanedUp) return
  cleanedUp = true
  try { await browser?.close() } catch { /* already gone */ }
  server.kill('SIGTERM')
  await sleep(600)
  try { execSync(`tmux -L ${SOCKET} kill-server`, { stdio: 'ignore' }) } catch { /* none */ }
  for (const dir of [DATA, FAKE_HOME]) {
    try { rmSync(dir, { recursive: true, force: true }) } catch { /* best effort */ }
  }
}
for (const sig of ['SIGINT', 'SIGTERM', 'SIGHUP']) {
  process.on(sig, () => void cleanup().finally(() => process.exit(130)))
}

const BASE = `http://localhost:${PORT}`
const USERNAME = 'scale'
const PASSWORD = 'a sufficiently long password'
let cookie = ''
// Fetch carrying the session cookie, for seeding through the API.
//
// 404 and 405 throw rather than being returned, because a check that asks for
// a route the server does not have gets a perfectly ordinary Response and goes
// on to draw conclusions from its body. That happened: a probe polled
// `GET /api/sessions`, which exists only for POST, with `.catch(() => [])` on
// the parse — so every refusal became an empty list, an empty list contained
// no session, and "no session" was the success condition. It reported a
// healthy result in three milliseconds and would have done so against a server
// that was switched off.
//
// 405 as well as 404, and that is not a detail: chi answers a known path with
// an unregistered method with 405, so the first version of this guard checked
// only for 404 and did not catch the exact bug it was written for. Injecting
// the bogus call and watching the run stay green is the only reason that was
// noticed.
//
// Nothing here expects either. If something ever does, it can call fetch.
const authed = async (path, init = {}) => {
  const res = await fetch(BASE + path, {
    ...init,
    headers: {
      'Content-Type': 'application/json',
      ...(cookie ? { Cookie: cookie } : {}),
      ...(init.headers ?? {}),
    },
  })
  if (res.status === 404 || res.status === 405) {
    throw new Error(
      `${init.method ?? 'GET'} ${path} -> ${res.status}; this server has no such ` +
      'route and method, so whatever this check concluded from the answer was meaningless',
    )
  }
  return res
}

/** Resident memory of the server, in MiB, straight from /proc. */
const rssMiB = () => {
  try {
    const status = readFileSync(`/proc/${server.pid}/status`, 'utf8')
    const kb = Number(/VmRSS:\s+(\d+)/.exec(status)?.[1] ?? 0)
    return kb / 1024
  } catch {
    return NaN
  }
}
const tmuxClients = () => {
  try {
    return execSync(`tmux -L ${SOCKET} list-clients -F '#{client_name}' 2>/dev/null | wc -l`, {
      encoding: 'utf8',
    }).trim()
  } catch {
    return '?'
  }
}

try {
  for (let i = 0; i < 120; i++) {
    try { if ((await fetch(BASE + '/api/health')).ok) break } catch { /* not up */ }
    await sleep(150)
  }
  const token = /one-time setup token:\s*\n\s*\n\s*(\S+)/.exec(serverLog)?.[1]
  if (!token) throw new Error(`no setup token:\n${serverLog}`)
  const setupRes = await authed('/api/auth/setup', {
    method: 'POST',
    body: JSON.stringify({ token, username: USERNAME, password: PASSWORD }),
  })
  cookie = (setupRes.headers.getSetCookie?.() ?? []).map((c) => c.split(';')[0]).join('; ')

  const baseline = rssMiB()

  // Three projects, so the sidebar has to group as well as scroll.
  const projects = []
  for (let p = 0; p < 3; p++) {
    const dir = join(DATA, `project-${p}`)
    mkdirSync(dir, { recursive: true })
    projects.push(await (await authed('/api/projects', {
      method: 'POST',
      body: JSON.stringify({ path: dir, name: `project-${p}` }),
    })).json())
  }

  const createStart = Date.now()
  for (let i = 0; i < COUNT; i++) {
    const res = await authed('/api/sessions', {
      method: 'POST',
      body: JSON.stringify({
        projectId: projects[i % projects.length].id,
        title: `task-${String(i).padStart(2, '0')}`,
        command: ['sleep', '600'],
      }),
    })
    if (!res.ok) {
      note('FAIL', 'scale', `creating session ${i} of ${COUNT} failed with ${res.status}`)
      break
    }
  }
  const createMs = Date.now() - createStart
  console.log(`created ${COUNT} sessions in ${createMs} ms (${Math.round(createMs / COUNT)} ms each)`)

  // The poller attaches to everything; give it a few cycles to settle.
  await sleep(8000)

  // A snapshot goes to every viewer on every state change. If it is large, an
  // idle panel with a phone connected is spending real bandwidth on nothing.
  const t0 = Date.now()
  const snapRes = await authed('/api/state')
  const snapBody = await snapRes.text()
  const snapMs = Date.now() - t0
  const snapKiB = Buffer.byteLength(snapBody) / 1024
  const snapshot = JSON.parse(snapBody)
  console.log(
    `snapshot ${snapKiB.toFixed(1)} KiB in ${snapMs} ms for ${snapshot.sessions.length} sessions`,
  )
  if (snapshot.sessions.length !== COUNT) {
    note('FAIL', 'scale',
      `the snapshot lists ${snapshot.sessions.length} sessions, expected ${COUNT}`)
  }
  if (snapMs > 1000) {
    note('FAIL', 'scale', `/api/state took ${snapMs} ms with ${COUNT} sessions`)
  }
  if (snapKiB > 128) {
    note('WARN', 'scale',
      `the snapshot is ${snapKiB.toFixed(0)} KiB and is pushed on every state change`)
  }

  // How often that snapshot is actually pushed, with agents producing output.
  //
  // The poller compares each snapshot against the previous one and broadcasts
  // only when they differ — "a tick that broadcasts regardless is polling
  // again, just with the cost moved onto every connected viewer". last_output_at
  // was in the payload and moves for any session that is printing, so one busy
  // agent made every tick a broadcast: measured at six sessions, ten ticks out
  // of ten and 85 KiB/min per viewer, which at this many sessions is about
  // 20 MB an hour onto a phone.
  {
    const busy = snapshot.sessions.slice(0, 6)
    for (const sess of busy) {
      execSync(`tmux -L ${SOCKET} respawn-pane -k -t '=${sess.tmuxName}:' ` +
        `"sh -c 'i=0; while :; do i=\$((i+1)); echo line \$i of build output; sleep 0.1; done'"`)
    }
    await sleep(6000)
    // Prove they are printing before measuring: a respawn that silently did
    // nothing reports the idle figure and looks like a pass.
    const printing = busy.filter((sess) => {
      const pane = execSync(`tmux -L ${SOCKET} capture-pane -p -t '=${sess.tmuxName}:'`, { encoding: 'utf8' })
      return pane.includes('of build output')
    }).length
    if (printing < busy.length) {
      note('WARN', 'scale', `only ${printing} of ${busy.length} sessions are producing output; not measuring`)
    } else {
      let prev = null
      let changed = 0
      const TICKS = 8
      for (let i = 0; i < TICKS; i++) {
        const body = await (await authed('/api/state')).text()
        if (prev !== null && body !== prev) changed++
        prev = body
        await sleep(2000)
      }
      const perMin = (changed / ((TICKS - 1) * 2 / 60)) * snapKiB
      console.log(`snapshot changed on ${changed} of ${TICKS - 1} ticks with ${busy.length} sessions printing ` +
        `(${perMin.toFixed(0)} KiB/min per viewer)`)
      if (changed > (TICKS - 1) / 2) {
        note('FAIL', 'scale',
          `${changed} of ${TICKS - 1} ticks broadcast a full snapshot while sessions were merely ` +
          `printing — ${perMin.toFixed(0)} KiB/min to every viewer. Something that changes with ` +
          'output is in the payload again.')
      } else {
        note('PASS', 'scale',
          `output alone broadcasts on ${changed} of ${TICKS - 1} ticks (${perMin.toFixed(0)} KiB/min per viewer)`)
      }
    }
  }

  const attached = tmuxClients()
  console.log(`tmux clients: ${attached} (one per session is the design)`)
  // Number('?') is NaN and NaN < COUNT is false, so a failed count used to
  // read as "everything is attached". Assert the measurement before the
  // threshold, or the check quietly stops being one.
  const attachedN = Number(attached)
  if (!Number.isFinite(attachedN)) {
    note('FAIL', 'scale',
      `could not count tmux clients (got ${JSON.stringify(attached)}), so the attach ` +
      'assertion did not run')
  } else if (attachedN < COUNT) {
    note('FAIL', 'scale',
      `only ${attached} of ${COUNT} sessions are attached; the ones that are not have no ` +
      'state detection, which is the reason for attaching them all')
  }

  const loaded = rssMiB()
  console.log(
    `server RSS ${baseline.toFixed(0)} MiB → ${loaded.toFixed(0)} MiB ` +
    `(${((loaded - baseline) / COUNT).toFixed(1)} MiB per session)`,
  )
  // The replay buffer is 2 MiB per session, but it is allocated as it fills, so
  // idle sessions should cost far less than that.
  //
  // The validity check comes first for the same reason as above: rssMiB returns
  // NaN when /proc is unavailable, and every comparison against NaN is false,
  // which would have turned an unmeasurable run into a passing one.
  if (!Number.isFinite(baseline) || !Number.isFinite(loaded)) {
    note('WARN', 'scale',
      'could not read the server process memory, so the per-session cost was not checked')
  } else if (loaded - baseline > COUNT * 3) {
    note('FAIL', 'scale',
      `${COUNT} idle sessions cost ${(loaded - baseline).toFixed(0)} MiB, more than the ` +
      '2 MiB replay buffer each; something is retaining more than it should')
  }

  browser = await chromium.launch({ headless: true })
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const page = await ctx.newPage()
  page.on('pageerror', (e) => pageErrors.push(String(e)))
  // Console as well as uncaught errors. A React tree that fails to render can
  // report it through console.error alone, and a check that only listens for
  // pageerror then sees a blank page with nothing wrong with it.
  const consoleLines = []
  page.on('console', (m) => {
    if (m.type() === 'error' || m.type() === 'warning') {
      consoleLines.push(`${m.type()}: ${m.text()}`.slice(0, 300))
    }
  })
  page.on('crash', () => consoleLines.push('crash: the renderer process died'))
  page.on('requestfailed', (r) =>
    consoleLines.push(`requestfailed: ${r.url().slice(-60)} ${r.failure()?.errorText ?? ''}`))

  const loadStart = Date.now()
  await page.goto(BASE, { waitUntil: 'networkidle' })
  await page.locator('[data-testid="auth-username"]').fill(USERNAME)
  await page.locator('[data-testid="auth-password"]').fill(PASSWORD)
  await page.locator('[data-testid="auth-submit"]').click()
  await page.waitForSelector('[data-testid="sidebar"]', { timeout: 30000 })
  let rows = 0
  for (let i = 0; i < 60; i++) {
    rows = await page.locator('[data-testid="session-row"]').count()
    if (rows >= COUNT) break
    await sleep(500)
  }
  const loadMs = Date.now() - loadStart
  console.log(`sidebar showed ${rows}/${COUNT} rows ${loadMs} ms after opening the page`)
  if (rows < COUNT) {
    // Carry the state that was judged. "0 of 24" on its own sent two rounds
    // looking for a crash that had not happened.
    const shape = await page.evaluate(() => ({
      body: document.body.innerHTML.length,
      html: document.body.innerHTML.slice(0, 200),
      url: location.href,
      title: document.title,
      root: document.getElementById('root')?.childElementCount ?? -1,
      sidebar: (document.querySelector('[data-testid="sidebar"]')?.innerText ?? '').slice(0, 300),
      rails: document.querySelectorAll('[data-testid="sidebar-rail"]').length,
      rows: document.querySelectorAll('[data-testid="session-row"]').length,
    }))
    const st = await (await authed('/api/state')).json()
    // Printed as well as noted: findings only appear when the run ends, and a
    // failing run spends several minutes timing out before it gets there.
    console.log(`diagnostic: ${JSON.stringify(shape)}`)
    console.log(`console: ${JSON.stringify(consoleLines.slice(-12))}`)
    note('FAIL', 'ui',
      `the sidebar shows ${rows} of ${COUNT} sessions. The API has ${st.projects?.length ?? '?'} ` +
      `projects and ${st.sessions?.length ?? '?'} sessions; the page has ${shape.body} bytes of ` +
      `body, ${shape.rails} collapsed rails, ${shape.rows} rows, and the sidebar reads ` +
      `${JSON.stringify(shape.sidebar)}. It is at ${shape.url} titled ` +
      `${JSON.stringify(shape.title)} with ${shape.root} children under #root; the body starts ` +
      `${JSON.stringify(shape.html)}`)
  }

  // Every row is reachable: a list that grows past the viewport must scroll,
  // and the page itself must not.
  const overflow = await page.evaluate(() => ({
    page: document.documentElement.scrollWidth - document.documentElement.clientWidth,
    sidebar: (() => {
      const el = document.querySelector('[data-testid="sidebar"]')
      if (!el) return null
      const scroller = [...el.querySelectorAll('*')].find(
        (n) => n.scrollHeight > n.clientHeight + 4,
      )
      return scroller ? scroller.scrollHeight - scroller.clientHeight : 0
    })(),
  }))
  if (overflow.page > 1) {
    note('FAIL', 'ui', `the page scrolls horizontally by ${overflow.page}px with ${COUNT} sessions`)
  }
  if (overflow.sidebar === 0) {
    note('WARN', 'ui',
      `nothing in the sidebar scrolls with ${COUNT} sessions; rows below the fold may be unreachable`)
  }
  // Inside its own scroller after scrolling, not merely "scrollIntoViewIfNeeded
  // did not throw".
  //
  // That helper resolves as long as it can scroll *something*, so it reported
  // success for an element that had overflowed a container with
  // `overflow: visible` and was being painted outside it — which is exactly the
  // failure this is meant to catch. Measured on the terminal strip, where the
  // helper said "reachable" about a tab drawn 350px past the edge of its row.
  await page.locator('[data-testid="session-row"]').last().scrollIntoViewIfNeeded().catch(() => {})
  const lastReachable = await page.evaluate(() => {
    const rows = document.querySelectorAll('[data-testid="session-row"]')
    const last = rows[rows.length - 1]
    if (!last) return null
    // The nearest ancestor that actually scrolls.
    let box = last.parentElement
    while (box && box.scrollHeight <= box.clientHeight + 4) box = box.parentElement
    const container = (box ?? document.documentElement).getBoundingClientRect()
    const r = last.getBoundingClientRect()
    return r.top >= container.top - 1 && r.bottom <= container.bottom + 1
  })
  if (lastReachable === null) {
    note('FAIL', 'ui', 'there are no session rows to reach')
  } else if (!lastReachable) {
    note('FAIL', 'ui',
      'the last session row cannot be brought inside the thing that is supposed to scroll it; ' +
      'rows below the fold are painted outside their container and cannot be clicked')
  }
  await page.screenshot({ path: join(SHOTS, 'many-sessions.png') })

  // Rows of things that can outgrow their box: the terminal strip and the
  // collapsed project rail.
  //
  // Both had no overflow handling at all. Eight scratch terminals in an 820px
  // window put four tabs past the right edge of the strip — and `overflow:
  // visible` does not clip, it paints them over the panel next door, with no
  // way to scroll to them. The rail reached the bottom of a 520px window at
  // fourteen projects and would have gone on drawing past it.
  //
  // Same shape as the key bar on a phone, which is why it is worth a check
  // rather than a fix: three instances of one mistake means a fourth is
  // coming.
  const reachInside = async (sel, axis) =>
    page.evaluate(
      ({ sel, axis }) => {
        const items = document.querySelectorAll(sel)
        const last = items[items.length - 1]
        if (!last) return null
        let box = last.parentElement
        const grew = (el) =>
          axis === 'x' ? el.scrollWidth > el.clientWidth + 4 : el.scrollHeight > el.clientHeight + 4
        while (box && !grew(box)) box = box.parentElement
        if (!box) return true // nothing overflows; nothing to reach
        box.scrollTo(axis === 'x' ? { left: box.scrollWidth } : { top: box.scrollHeight })
        const c = box.getBoundingClientRect()
        const r = last.getBoundingClientRect()
        return axis === 'x'
          ? r.left >= c.left - 1 && r.right <= c.right + 1
          : r.top >= c.top - 1 && r.bottom <= c.bottom + 1
      },
      { sel, axis },
    )

  // The point of the whole thing: a session that needs a human must reach the
  // top of the list, and it must do so while two dozen others are attached.
  const victim = snapshot.sessions.find((s) => s.title === `task-${String(COUNT - 1).padStart(2, '0')}`)
  if (!victim) {
    note('WARN', 'scale', 'could not find a session to mark as waiting')
  } else {
    const markedAt = Date.now()
    await authed(`/api/sessions/${victim.id}`, {
      method: 'PATCH',
      body: JSON.stringify({ state: 'waiting' }),
    })
    let promotedMs = -1
    for (let i = 0; i < 40; i++) {
      const first = await page
        .locator('[data-testid="session-row"]')
        .first()
        .innerText()
        .catch(() => '')
      if (first.includes(victim.title)) {
        promotedMs = Date.now() - markedAt
        break
      }
      await sleep(300)
    }
    if (promotedMs < 0) {
      note('FAIL', 'scale',
        `a session marked as waiting never reached the top of the list with ${COUNT} sessions open`)
    } else {
      console.log(`waiting session reached the top in ${promotedMs} ms`)
      if (promotedMs > 5000) {
        note('WARN', 'scale', `promotion took ${promotedMs} ms; the push path may be backing up`)
      }
    }
  }

  // Enough projects and enough scratch terminals to overflow, in a window
  // small enough to matter. Last, so nothing above is measured against it.
  for (let i = 0; i < 18; i++) {
    const dir = join(DATA, `extra-${i}`)
    mkdirSync(dir, { recursive: true })
    await authed('/api/projects', {
      method: 'POST',
      body: JSON.stringify({ path: dir, name: `extra-project-${i}` }),
    })
  }
  const first = snapshot.sessions.find((x) => !x.parentSessionId)
  for (let i = 0; i < 8; i++) {
    await authed('/api/sessions', {
      method: 'POST',
      body: JSON.stringify({
        projectId: first.projectId,
        parentSessionId: first.id,
        command: ['sh', '-c', 'exec sh'],
      }),
    })
  }
  await page.setViewportSize({ width: 900, height: 560 })
  // By name, not by position. The sidebar sorts by urgency, and a session was
  // marked waiting a few lines above — so the first row is that one, whose
  // terminal strip is empty. This passed until the day it did not, and then
  // failed as "expected eight terminal tabs, saw 0", which is the guard doing
  // its job: a fixture that did not build the state is not a check.
  await page
    .locator('[data-testid="session-row"]', { hasText: first.title })
    .first()
    .click()
  await sleep(4000)

  const tabCount = await page.locator('[data-testid="bottom-tab"]').count()
  if (tabCount < 8) {
    note('FAIL', 'ui', `expected eight terminal tabs to test the strip with, saw ${tabCount}`)
  } else {
    const stripOk = await reachInside('[data-testid="bottom-tab"]', 'x')
    if (stripOk === false) {
      note('FAIL', 'ui',
        'the last terminal tab cannot be scrolled into the strip; tabs past the edge are ' +
        'painted over the panel beside them and cannot be clicked')
    }
  }

  // Collapse the sidebar into the rail and check the same thing vertically.
  await page.locator('[data-testid="sidebar"] header button').first().click()
  await sleep(1200)
  const badges = await page.locator('[data-testid="rail-project"]').count()
  if (badges < 10) {
    note('FAIL', 'ui', `expected a full collapsed rail to test with, saw ${badges} badges`)
  } else {
    const railOk = await reachInside('[data-testid="rail-project"]', 'y')
    if (railOk === false) {
      note('FAIL', 'ui', 'the last project in the collapsed rail cannot be scrolled into it')
    }
    // And they must still be the size they were designed to be.
    //
    // This is the failure that actually happened, and reachability alone did
    // not see it: flex children compress before they overflow, so twenty
    // projects did not spill out of the rail — every badge was squeezed from
    // 36px to 17, which is neither readable nor tappable, and the scroller
    // added to catch spilling never fired because nothing ever spilled.
    const squashed = await page.$$eval('[data-testid="rail-project"]', (els) =>
      els.map((el) => Math.round(el.getBoundingClientRect().height)).filter((h) => h < 28),
    )
    if (squashed.length > 0) {
      note('FAIL', 'ui',
        `${squashed.length} project badges in the rail are squashed to ${squashed.join(', ')}px; ` +
        'a crowded rail compresses its contents instead of scrolling')
    }
  }
  // And the generic form of the same question, in the state where crowding
  // actually happens. The render check runs this too, but at 1440 with two
  // terminal tabs — where nothing has ever been close to overflowing.
  // Crowding is when a control is most likely to be faded out by a layout rule
  // that only meant to tidy something.
  const fadedCrowded = await findFadedControls(page)
  if (fadedCrowded.length > 0) {
    note('FAIL', 'ui',
      'with everything crowded, controls are on screen as far as a script can tell and ' +
      `invisible to a person: ${fadedCrowded.join(', ')}`)
  }
  const { found: spilling } = await findUnreachable(page, sleep)
  if (spilling.length > 0) {
    note('FAIL', 'ui',
      'with everything crowded, content is painted outside its container with no way to ' +
      `scroll to it: ${spilling.join('; ')}`)
  }

  await page.screenshot({ path: join(SHOTS, 'overflowing-rows.png') })

  // ── the same crowd, on a phone ───────────────────────────────────────────
  // This check visited 900 and 1440 and never a phone, so the intersection of
  // the two things the panel is for — a lot of sessions, and reading them from
  // a phone — had never been rendered.
  //
  // overflow.mjs says why that matters, about a different control: "Run once
  // on the desktop page it says nothing about the key bar, which only exists
  // on a phone — which is exactly how the first version of this passed while
  // the key row was hiding two keys." The drawer is the same shape of thing:
  // it does not exist above the breakpoint, so nothing above the breakpoint
  // can check it.
  await page.setViewportSize({ width: 390, height: 844 })
  await sleep(1200)
  const drawerBtn = page.locator('header button').first()
  if (!(await drawerBtn.isVisible().catch(() => false))) {
    note('FAIL', 'ui', 'no way to open the project list at phone width')
  } else {
    await drawerBtn.click()
    await sleep(1200)
    const phoneRows = await page.locator('[data-testid="session-row"]').count()
    if (phoneRows < COUNT) {
      note('FAIL', 'ui',
        `the drawer lists ${phoneRows} of ${COUNT} sessions; the rest cannot be chosen at all`)
    }
    // The last row is the one a scroll container gets wrong.
    const last = page.locator('[data-testid="session-row"]').last()
    const scrolled = await last.scrollIntoViewIfNeeded({ timeout: 5000 }).then(() => true).catch(() => false)
    const box = scrolled ? await last.boundingBox() : null
    if (!box || box.y < 0 || box.y + box.height > 844 + 1) {
      note('FAIL', 'ui',
        `the last session in the drawer cannot be scrolled into view on a phone: ` +
        `${box ? `y=${Math.round(box.y)} h=${Math.round(box.height)}` : 'no box'}`)
    }
    const phoneFaded = await findFadedControls(page)
    if (phoneFaded.length > 0) {
      note('FAIL', 'ui',
        'with the drawer open on a phone, controls are on screen as far as a script can tell ' +
        `and invisible to a person: ${phoneFaded.join(', ')}`)
    }
    const { found: phoneSpill } = await findUnreachable(page, sleep)
    if (phoneSpill.length > 0) {
      note('FAIL', 'ui',
        'with the drawer open on a phone, content is painted outside its container with no ' +
        `way to scroll to it: ${phoneSpill.join('; ')}`)
    }
    const wide = await page.evaluate(
      () => document.documentElement.scrollWidth - document.documentElement.clientWidth)
    if (wide > 0) {
      note('FAIL', 'ui', `the page scrolls sideways by ${wide}px with the drawer open on a phone`)
    }
    await page.screenshot({ path: join(SHOTS, 'phone-drawer-crowded.png') })
    await page.keyboard.press('Escape')
    await sleep(600)
  }

  await page.setViewportSize({ width: 900, height: 560 })
  await sleep(800)
  const rail = page.locator('[data-testid="sidebar-rail"] button').first()
  if (await rail.isVisible().catch(() => false)) await rail.click()
  await page.setViewportSize({ width: 1440, height: 900 })
  await sleep(1500)

  // Switching between sessions is the most common interaction, and it mounts a
  // terminal each time. Measure a few switches rather than one.
  const switchTimes = []
  for (let i = 0; i < 5; i++) {
    const row = page.locator('[data-testid="session-row"]').nth(i)
    const t = Date.now()
    await row.click()
    // Through the buffer, not the DOM: under the GPU renderer `.xterm-rows` is
    // empty however full the terminal is, so this waited fifteen seconds and
    // gave up on every switch.
    await page.waitForFunction(
      () => (window.vibepanelScreen?.()?.some((r) => r.trim()) ?? false),
      null,
      { timeout: 15000 },
    ).catch(() => {})
    switchTimes.push(Date.now() - t)
  }
  const worst = Math.max(...switchTimes)
  console.log(`session switches: ${switchTimes.join(', ')} ms`)
  if (worst > 4000) {
    note('FAIL', 'ui', `switching sessions took up to ${worst} ms with ${COUNT} open`)
  }


  // Nothing should have fallen over during any of that.
  if (server.exitCode !== null) {
    note('FAIL', 'scale', `the server exited with ${server.exitCode} under ${COUNT} sessions`)
  }
  if (/panic:|fatal error:/.test(serverLog)) {
    note('FAIL', 'scale', `the server log contains a panic: ${serverLog.slice(-300)}`)
  }
} catch (err) {
  note('FAIL', 'harness', String(err?.stack ?? err))
} finally {
  for (const e of [...new Set(pageErrors)]) note('FAIL', 'js', `uncaught: ${e}`)
  await cleanup()
}

for (const f of findings) console.log(`[${f.sev}] ${f.area}: ${f.msg}`)
// Counting WARN rather than everything-that-is-not-FAIL. The
// subtraction was right only while FAIL and WARN were the only
// severities this file used: the first PASS recorded here was
// reported as a warning, and a summary that invents warnings is one
// people stop reading.
const fails = findings.filter((f) => f.sev === 'FAIL').length
console.log(`=== scale check: ${fails} FAIL, ${findings.filter((f) => f.sev === 'WARN').length} WARN ===`)
console.log(`screenshots: ${SHOTS}`)
// Flush before exiting, and then exit deliberately.
//
// Node's stdout is asynchronous when it is a pipe — which it is whenever this
// runs under make, CI, or anything capturing the output — and process.exit()
// abandons whatever has not been flushed. The findings and the verdict are the
// last thing printed and therefore the first thing lost: three runs of the
// scale check in a row produced a different amount of output each time, one of
// them stopping mid-way with no verdict at all, and the missing lines were
// read as the run having crashed.
//
// Setting only process.exitCode would flush, but it also waits for the event
// loop to drain, and one stray handle from a browser or a child process would
// hang the check instead of ending it. Flush, then exit.
await new Promise((resolve) => process.stdout.write('', resolve))
process.exit(fails ? 1 : 0)
