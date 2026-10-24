// Fidelity and endurance checks for the terminal transport.
//
// Separate from render-check.mjs, which asks whether the interface works.
// This asks whether the terminal underneath it is faithful: wide characters,
// full-screen programs, a flood of output, and a connection that drops.
//
// These are the areas where a terminal quietly corrupts rather than failing,
// which is why they are worth driving with a browser rather than reasoning
// about.
//
//   npm run build && (cd .. && go build -o vibepanel ./cmd/vibepanel)
//   npm run check:stress
import { chromium } from 'playwright'
import { rows as screenRows } from './lib/screen.mjs'
import { spawn, execSync } from 'node:child_process'
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:net'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { assertFreshBuild } from './lib/fresh.mjs'

/**
 * The main terminal on screen.
 *
 * The most recently viewed sessions stay mounted and hidden, so `.xterm-screen`
 * on its own matches one of those as soon as a check has switched session once:
 * a strict locator refuses, and `.first()` clicks or measures a terminal nobody
 * can see. The change that kept terminals mounted failed render-check on its
 * first click for exactly that reason.
 */
const MAIN_SCREEN = '[data-testid="main-terminal"]:visible .xterm-screen'
// Inside page.evaluate, where `:visible` does not exist, the same thing is
// written `[data-testid="main-terminal"]:not(.hidden)`.

const BIN = process.argv[2] ?? new URL('../../vibepanel', import.meta.url).pathname
// Measuring a build that does not contain the change is the one failure that
// looks exactly like a pass. See lib/fresh.mjs.
assertFreshBuild(BIN, new URL('../../', import.meta.url).pathname)
const SHOTS = process.argv[3] ?? join(tmpdir(), 'vpstress-shots')
mkdirSync(SHOTS, { recursive: true })

const PORT = await new Promise((resolve, reject) => {
  const probe = createServer()
  probe.once('error', reject)
  probe.listen(0, '127.0.0.1', () => {
    const { port } = probe.address()
    probe.close(() => resolve(port))
  })
})
const SOCKET = `vpstress-${process.pid}`
const DATA = mkdtempSync(join(tmpdir(), 'vpstress-'))
const FAKE_HOME = mkdtempSync(join(tmpdir(), 'vpstress-home-'))

const findings = []
const note = (sev, area, msg) => findings.push({ sev, area, msg })
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

let cleanedUp = false
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
let serverLog = ''
server.stdout.on('data', (d) => (serverLog += d))
server.stderr.on('data', (d) => (serverLog += d))

let browser
async function cleanup() {
  if (cleanedUp) return
  cleanedUp = true
  try { await browser?.close() } catch { /* already gone */ }
  server.kill('SIGTERM')
  await sleep(400)
  try { execSync(`tmux -L ${SOCKET} kill-server`, { stdio: 'ignore' }) } catch { /* none */ }
  try { rmSync(join('/tmp', `tmux-${process.getuid()}`, SOCKET), { force: true }) } catch { /* best effort */ }
  for (const dir of [DATA, FAKE_HOME]) {
    try { rmSync(dir, { recursive: true, force: true }) } catch { /* best effort */ }
  }
}
for (const sig of ['SIGINT', 'SIGTERM', 'SIGHUP']) {
  process.on(sig, () => void cleanup().finally(() => process.exit(130)))
}

const BASE = `http://localhost:${PORT}`
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
  // 400 is the same failure wearing a different number, and it took longer to
  // find. `decode` calls DisallowUnknownFields, so a request naming a field the
  // server has renamed is refused outright -- and these scripts read the
  // response only when they want a value out of it, so the refusal went
  // nowhere. `parentSessionId` became `scratch` and three scripts kept sending
  // the old name for weeks: the sessions were never created, the fixtures were
  // not the state being checked, and everything downstream of them passed
  // against nothing. Only scale-check noticed, and only because it counted the
  // tabs it had asked for. Nothing here sends a bad body on purpose.
  if (res.status === 400) {
    throw new Error(
      `${init.method ?? 'GET'} ${path} -> 400 ${await res.text()}; the server refused ` +
      'this request, so the state this was building does not exist and nothing ' +
      'measured after it means anything',
    )
  }
  return res
}

/** Reads the visible rows straight out of the DOM renderer. */
// Non-breaking spaces are normalised, and it is not cosmetic.
//
// xterm splits a row into one <span> per run of styling, and the text between
// two spans comes back as U+00A0 rather than U+0020. How many runs a row has
// depends on how many escape sequences produced it -- so a line printed with
// one colour on and one off reads back with ordinary spaces, and the same line
// printed with eight sequences reads back with NBSP, and every `includes(...)`
// in this file silently stops matching.
//
// Measured, on a passing tree, after making the flood emit more sequences:
//
//   the tail is missing the last line: ["line 19999 of noise", ...]
//   codepoints: ["6c 69 6e 65 a0 31 39 39 39 39 a0 6f 66 a0 ..."]
//
// An assertion printing the very text that satisfies it, because the space in
// the middle was not the space it was looking for. Any check that greps
// rendered terminal text has this hazard; normalising once here is cheaper than
// remembering it at thirty call sites.
const rows = async (page) =>
  (await screenRows(page)).map((r) => r.replace(/\u00a0/g, ' '))

try {
  for (let i = 0; i < 120; i++) {
    try { if ((await fetch(BASE + '/api/health')).ok) break } catch { /* not up */ }
    await sleep(150)
  }

  const token = /one-time setup token:\s*\n\s*\n\s*(\S+)/.exec(serverLog)?.[1]
  if (!token) throw new Error(`no setup token:\n${serverLog}`)
  const setupRes = await authed('/api/auth/setup', {
    method: 'POST',
    body: JSON.stringify({ token, username: 'stress', password: 'a sufficiently long password' }),
  })
  cookie = (setupRes.headers.getSetCookie?.() ?? []).map((c) => c.split(';')[0]).join('; ')
  // The first-run tour would sit over everything this check clicks. See the
  // note in render-check.mjs; dismissed through the API for the same reason.
  await authed('/api/settings/tour', { method: 'POST' })


  const proj = await (await authed('/api/projects', {
    method: 'POST',
    body: JSON.stringify({ path: DATA, name: 'stress' }),
  })).json()
  const mk = (command, title) =>
    authed('/api/sessions', {
      method: 'POST',
      body: JSON.stringify({ projectId: proj.id, command, title, cols: 80, rows: 24 }),
    }).then((r) => r.json())

  browser = await chromium.launch({ headless: true })
  // Pinned to xterm's DOM renderer.
  //
  // These checks measure cell geometry -- that a CJK character advances exactly
  // two Latin cells -- and geometry only exists in the DOM renderer's output.
  // The GPU renderer draws to a canvas and `.xterm-rows` is empty under it, so
  // the measurement has nothing to measure.
  //
  // Acceptable here because what this file is about is tmux and the byte
  // stream, not the renderer: wide characters, full-screen programs, floods and
  // dropped sockets behave the same either way. render-check covers the shipped
  // renderer separately.
  const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
  await ctx.addInitScript(() => localStorage.setItem('vibepanel.renderer', 'dom'))
  const page = await ctx.newPage()
  const pageErrors = []
  page.on('pageerror', (e) => pageErrors.push(String(e)))

  await page.goto(BASE, { waitUntil: 'networkidle' })
  await page.locator('[data-testid="auth-username"]').fill('stress')
  await page.locator('[data-testid="auth-password"]').fill('a sufficiently long password')
  await page.locator('[data-testid="auth-submit"]').click()
  await page.waitForSelector('[data-testid="sidebar"], [data-testid="sidebar-rail"]', { timeout: 15000 })
  await sleep(1500)

  const select = async (title) => {
    const row = page.locator('[data-testid="session-row"]', { hasText: title }).first()
    await row.click()
    await sleep(2000)
  }
  const waitForRow = async (predicate, timeoutMs = 20000) => {
    const end = Date.now() + timeoutMs
    while (Date.now() < end) {
      const r = await rows(page)
      const hit = r.findIndex(predicate)
      if (hit >= 0) return { index: hit, rows: r }
      await sleep(300)
    }
    return { index: -1, rows: await rows(page) }
  }

  // ── wide characters ──────────────────────────────────────────────────────
  // Agents write Chinese, and a terminal that measures those characters as one
  // column instead of two drifts by one cell per character — every table,
  // every progress bar and every box drawing goes crooked, and it looks like
  // the program's fault rather than the terminal's.
  await mk(['sh', '-c', 'exec sh'], 'wide')
  await sleep(2500)
  await select('wide')

  const grid = await page.locator('[data-testid="grid-size"]').innerText().catch(() => '')
  // Split on anything that is not a digit. The separator is a multiplication
  // sign now and was an ASCII "x" when this was written, and `split('x')` on
  // "79\u00d7133" gives NaN -- a check reporting that it could not read the
  // grid, about a change to the typography of the chip that displays it.
  const cols = Number(grid.split(/\D+/).filter(Boolean)[0])
  // Focus the terminal here rather than inside the branch below. It used to be
  // in the else, so a failure to read the grid also silently disabled the two
  // measurements after it: they typed into a page with no focus in the
  // terminal and reported "output never arrived".
  await page.locator(MAIN_SCREEN).click()
  if (!Number.isFinite(cols) || cols < 20) {
    note('FAIL', 'wide', `could not read the grid size: ${JSON.stringify(grid)}`)
  } else {
    // Print more wide characters than fit, so the terminal has to wrap. Where
    // it wraps is the measurement: at two columns each it breaks after cols/2
    // characters, at one column each it would take twice as many. A terminal
    // that is merely wide enough to hold the line proves nothing.
    const perRow = Math.floor(cols / 2)
    const count = perRow + 10
    const marker = 'WIDEEND'
    // Literal characters, not escapes: the shell's printf does not understand
    // \u or \U, and a test that types those measures nothing but its own
    // payload arriving as text.
    //
    // The marker is split so the echoed command line does not contain it, and
    // the search finds the output rather than what was typed.
    const payload = '\u4e2d'.repeat(count)
    await page.keyboard.type(`printf '%s\\n%s\\n' '${payload}' "WIDE""END"`)
    await page.keyboard.press('Enter')

    const found = await waitForRow((r) => r.trim() === marker)
    if (found.index < 0) {
      note('FAIL', 'wide',
        `the wide-character test never produced output: ${JSON.stringify(found.rows.filter(Boolean).slice(-4))}`)
    } else {
      // The two rows above the marker are the wrapped wide text.
      const firstWrapped = found.rows[found.index - 2] ?? ''
      const cjkCount = [...firstWrapped].filter((ch) => ch === '\u4e2d').length
      if (cjkCount !== perRow) {
        note('FAIL', 'wide',
          `wide text wrapped after ${cjkCount} characters on a ${cols}-column grid, want ${perRow} — ` +
          'they are being measured as one column each')
      }
      const secondWrapped = found.rows[found.index - 1] ?? ''
      const rest = [...secondWrapped].filter((ch) => ch === '\u4e2d').length
      if (cjkCount + rest !== count) {
        note('FAIL', 'wide',
          `${cjkCount + rest} of ${count} wide characters survived the round trip`)
      }
    }
  }

  // Emoji are two columns as well, and a terminal that gets them wrong
  // scrambles anything an agent prints with a status icon in it.
  await page.keyboard.type(`printf '%s ROCKET''OK\\n' '\u{1F680}\u{1F525}'`)
  await page.keyboard.press('Enter')
  const emoji = await waitForRow((r) => r.trim().endsWith('ROCKETOK'))
  if (emoji.index < 0) {
    note('WARN', 'wide', 'emoji output never arrived')
  } else if (!/\u{1F680}/u.test(emoji.rows[emoji.index])) {
    note('FAIL', 'wide', `emoji were lost: ${JSON.stringify(emoji.rows[emoji.index].slice(0, 60))}`)
  }
  // The grid only stays aligned if a wide character advances exactly two
  // cells, and the Latin and CJK glyphs come from different fonts whose
  // natural advances do not match. xterm compensates with letter-spacing, so
  // what matters is the rendered result — measured here from the DOM it
  // actually produces, not from canvas metrics it does not use.
  //
  // Not document.fonts.check: it returns true for font families that are not
  // installed at all, so it cannot tell a real glyph from a missing one.
  await page.keyboard.type(`printf '%s\\n' 'MMMMMMMMMMMMMMMMMMMM|' '\u4f60\u597d\u4e16\u754c\u6d4b\u8bd5\u6c49\u5b57\u6e32\u67d3|'`)
  await page.keyboard.press('Enter')
  await sleep(2000)
  const cells = await page.evaluate(() => {
    const rows = [...document.querySelectorAll('[data-testid="main-terminal"]:not(.hidden) .xterm-rows > div')]
    const latin = rows.find((r) => (r.textContent ?? '').startsWith('MMMMMMMMMMMMMMMMMMMM|'))
    const cjk = rows.find((r) => (r.textContent ?? '').startsWith('\u4f60\u597d'))
    if (!latin || !cjk) return null
    const width = (row) =>
      [...row.children].reduce((sum, el) => sum + el.getBoundingClientRect().width, 0)
    return { latin: width(latin), cjk: width(cjk), latinChars: 21, cjkChars: 10 }
  })
  if (!cells) {
    note('WARN', 'font', 'could not find both measurement rows')
  } else {
    const cell = cells.latin / cells.latinChars
    // Ten wide characters plus a pipe: twenty-one cells, the same as the
    // Latin row. If the two rows differ, mixed text does not line up.
    const perWide = (cells.cjk - cell) / cells.cjkChars / cell
    if (Math.abs(perWide - 2) > 0.05) {
      note('FAIL', 'font',
        `a wide character occupies ${perWide.toFixed(3)} cells, want 2 — rows of mixed text will not line up`)
    }
  }

  await page.screenshot({ path: join(SHOTS, 'wide.png') })

  // ── the alternate screen ─────────────────────────────────────────────────
  // Every agent TUI lives on it. Leaving it has to restore what was underneath,
  // or a session looks destroyed every time an editor closes.
  await mk(['sh', '-c', 'echo BEFORE_VIM; exec sh'], 'altscreen')
  await sleep(2500)
  await select('altscreen')
  await page.locator(MAIN_SCREEN).click()
  await page.keyboard.type('vim -u NONE -c "startinsert" /tmp/vpstress-alt.txt')
  await page.keyboard.press('Enter')
  await sleep(2500)
  await page.keyboard.type('INSIDE_VIM')
  await sleep(800)
  const inVim = await waitForRow((r) => r.includes('INSIDE_VIM'), 8000)
  if (inVim.index < 0) {
    note('FAIL', 'altscreen', 'vim never drew anything')
  } else {
    await page.screenshot({ path: join(SHOTS, 'altscreen.png') })
    // Leaving must put the shell back, scrollback and all.
    await page.keyboard.press('Escape')
    await sleep(400)
    await page.keyboard.type(':q!')
    await page.keyboard.press('Enter')
    const restored = await waitForRow((r) => r.includes('BEFORE_VIM'), 10000)
    if (restored.index < 0) {
      note('FAIL', 'altscreen',
        `leaving the alternate screen did not restore what was underneath: ${JSON.stringify(
          restored.rows.filter(Boolean).slice(0, 4),
        )}`)
    }
    const stillVim = (await rows(page)).some((r) => r.includes('INSIDE_VIM'))
    if (stillVim) note('WARN', 'altscreen', 'the editor is still on screen after quitting')
  }

  // ── scrollback ───────────────────────────────────────────────────────────
  // tmux holds 20,000 lines per session and, until this was measured, not one
  // of them could be reached from the panel on any device.
  //
  // Two things in the embedded tmux config are why. A tmux client's first
  // write to its terminal is ESC[?1049h — the alternate screen, which has no
  // scrollback at all — and tmux scrolls with CSI Ps S, which discards what
  // goes off the top rather than feeding it to the terminal. Measured with
  // xterm's own buffer: 0 lines of scrollback before, 269 after.
  //
  // This checks the property a person has, not the config: scroll up, and
  // earlier output must come back.
  // exec sleep, not exec sh: a shell prints a prompt after the burst, and output
  // arriving while the view is scrolled up pulls it back to the bottom. That is
  // correct behaviour and it made the first version of this check pass one run
  // in three.
  await mk(['sh', '-c', 'i=1; while [ $i -le 400 ]; do echo SCROLL_$i; i=$((i+1)); done; exec sleep 300'], 'scrollback')
  await sleep(3000)
  await select('scrollback')

  const topRow = async () => (await rows(page)).find((r) => r.trim()) ?? ''
  const lineNo = (r) => Number((/SCROLL_(\d+)/.exec(r) ?? [])[1] ?? NaN)

  // Wait for the whole burst to have arrived before measuring anything.
  //
  // The first version scrolled 1500ms after selecting and flapped: one run in
  // three with the configuration unchanged, and always all-or-nothing. A replay
  // still in flight moves the top row by itself, and the terminal follows new
  // output to the bottom, so both the before and the after were being read out
  // of a moving picture.
  //
  // Waiting for the last line of the burst is the precondition that makes this
  // a measurement rather than a race: until SCROLL_400 is on screen there is
  // nothing to be scrolled back from.
  const sawLast = await waitForRow((r) => r.includes('SCROLL_400'), 20000)
  if (sawLast.index < 0) {
    note('WARN', 'scrollback', 'the burst never finished arriving; not measuring a moving picture')
  }
  let settled = await topRow()
  for (let i = 0; i < 20; i++) {
    await sleep(500)
    const now = await topRow()
    if (now === settled && Number.isFinite(lineNo(now))) break
    settled = now
  }

  const box = await page.locator(MAIN_SCREEN).boundingBox()
  if (!box || !Number.isFinite(lineNo(settled))) {
    note('WARN', 'scrollback', `nothing to scroll: ${JSON.stringify(settled.slice(0, 40))}`)
  } else {
    // Scroll in batches and keep looking, the way a person keeps scrolling.
    // Failure still means every one of them reached nothing.
    let reached = settled
    for (let batch = 0; batch < 12; batch++) {
      await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2)
      for (let i = 0; i < 10; i++) await page.mouse.wheel(0, -240)
      await sleep(400)
      reached = await topRow()
      if (Number.isFinite(lineNo(reached)) && lineNo(reached) < lineNo(settled)) break
    }
    if (!Number.isFinite(lineNo(reached)) || lineNo(reached) >= lineNo(settled)) {
      // What the wheel actually landed on. A check that fails has to say what
      // it saw, or the next person re-derives it from nothing.
      const under = await page.evaluate(({ x, y }) => {
        const el = document.elementFromPoint(x, y)
        const wrap = document.querySelector('[data-testid="main-terminal"]:not(.hidden) .xterm')?.parentElement
        return {
          hit: el ? `${el.tagName.toLowerCase()}.${el.className}`.slice(0, 80) : null,
          stack: document.elementsFromPoint(x, y).slice(0, 4)
            .map((e) => `${e.tagName.toLowerCase()}.${String(e.className).slice(0, 30)}`),
          wrapOverflow: wrap ? getComputedStyle(wrap).overflow : null,
          wrapScrollTop: wrap?.scrollTop ?? null,
          wrapScrollHeight: wrap?.scrollHeight ?? null,
          wrapClientHeight: wrap?.clientHeight ?? null,
        }
      }, { x: box.x + box.width / 2, y: box.y + box.height / 2 })
      note('FAIL', 'scrollback',
        `120 wheel notches went nowhere: top line was SCROLL_${lineNo(settled)}, ` +
        `now ${JSON.stringify(reached.slice(0, 40))}. Under the pointer: ${JSON.stringify(under)}`)
    } else {
      note('PASS', 'scrollback',
        `scrolled back from SCROLL_${lineNo(settled)} to SCROLL_${lineNo(reached)}`)
    }
  }

  // ── the wheel in a full-screen TUI that never asked for the mouse ────────
  // Issue #19: scrolling a pane that was drawing a full-screen program
  // forwarded nothing. The program is the only thing that knows what is above
  // the live screen, and the panel's own buffer under one is redraw fragments
  // rather than a transcript -- scrolling that was the torn
  // half-one-frame-half-another picture in the report. A full-screen program
  // that did not ask for the mouse is told the scroll as arrow keys
  // (alternate scroll), and whether it moved is what this measures: POS is a
  // counter the fixture moves for every Up and Down it receives, so a changed
  // number is the gesture having arrived, read straight off the fixture's own
  // accounting. The pane is what tells the panel a full-screen program is
  // drawing (tmux's alternate_on, polled, so the check waits for the
  // data-fullscreen flag before touching the wheel), and it never sends a
  // mouse report, so nothing here can pass by accident.
  const altscrollScript = join(DATA, 'altscroll.py')
  writeFileSync(
    altscrollScript,
    [
      'import os, sys, termios, tty',
      'out = sys.stdout',
      'pos = 0',
      'def draw():',
      "    out.write('\\x1b[?1049h\\x1b[H\\x1b[2J')",
      "    out.write('\\x1b[1;1H ALTSCROLL_READY (full screen, no mouse)')",
      '    for r in range(4, 22):',
      "        out.write(f'\\x1b[{r};1H FILLER_ROW_{r:02d}   ')",
      "    out.write(f'\\x1b[23;1H POS={pos}   ')",
      "    out.write('\\x1b[24;1H status: waiting   ')",
      '    out.flush()',
      'draw()',
      'fd = sys.stdin.fileno()',
      'old = termios.tcgetattr(fd)',
      'tty.setraw(fd)',
      'try:',
      '    while True:',
      '        b = os.read(fd, 64)',
      '        if not b:',
      '            break',
      "        pos += b.count(b'\\x1b[A') + b.count(b'\\x1bOA')",
      "        pos -= b.count(b'\\x1b[B') + b.count(b'\\x1bOB')",
      '        draw()',
      'except KeyboardInterrupt:',
      '    pass',
      'finally:',
      '    termios.tcsetattr(fd, termios.TCSADRAIN, old)',
      "    out.write('\\x1b[?1049l')",
      '    out.flush()',
    ].join('\n') + '\n',
  )
  await mk(['python3', '-u', altscrollScript], 'altscroll')
  await sleep(2500)
  await select('altscroll')
  const posRow = async () => (await rows(page)).find((r) => r.includes('POS=')) ?? ''
  const pos = (r) => Number((/POS=(-?\d+)/.exec(r) ?? [])[1] ?? NaN)
  // The poller, not the byte stream, is what says a full-screen program is
  // drawing -- the smcup@ line in vibepanel.conf keeps it out of the stream --
  // and the poll runs every couple of seconds. Scrolling before it lands
  // measures the old behaviour by construction.
  const fullscreenShown = await page
    .waitForSelector('[data-testid="main-terminal"] [data-fullscreen="true"]', { timeout: 10000 })
    .then(() => true)
    .catch(() => false)
  if (!fullscreenShown) {
    note('WARN', 'altscroll', 'the pane never reported fullscreen; not measuring without it')
  } else {
    const before = await posRow()
    const box = await page.locator(MAIN_SCREEN).boundingBox()
    if (!box || !Number.isFinite(pos(before))) {
      note('WARN', 'altscroll', `nothing to scroll: ${JSON.stringify(before)}`)
    } else {
      await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2)
      for (let i = 0; i < 8; i++) await page.mouse.wheel(0, -240)
      await sleep(800)
      let afterUp = await posRow()
      let ok = Number.isFinite(pos(afterUp)) && pos(afterUp) > pos(before)
      for (let i = 0; i < 5 && !ok; i++) {
        await page.mouse.wheel(0, -240)
        await sleep(600)
        afterUp = await posRow()
        ok = Number.isFinite(pos(afterUp)) && pos(afterUp) > pos(before)
      }
      if (!ok) {
        note('FAIL', 'altscroll',
          `the wheel reached a full-screen TUI as nothing: ${JSON.stringify(before)} -> ${JSON.stringify(afterUp)}`)
      } else {
        for (let i = 0; i < 6; i++) await page.mouse.wheel(0, 240)
        await sleep(800)
        const afterDown = await posRow()
        if (!Number.isFinite(pos(afterDown)) || pos(afterDown) >= pos(afterUp)) {
          note('FAIL', 'altscroll',
            `wheel-up moved the TUI (POS ${pos(before)} -> ${pos(afterUp)}) but wheel-down did not: ${JSON.stringify(afterDown)}`)
        } else {
          note('PASS', 'altscroll',
            `a full-screen TUI without mouse reporting was scrolled by the wheel: POS ${pos(before)} -> ${pos(afterUp)} -> ${pos(afterDown)}`)
        }
      }
      await page.screenshot({ path: join(SHOTS, 'altscroll.png') })
    }
  }

  // ── a flood ──────────────────────────────────────────────────────────────
  // A build or a test run produces tens of thousands of lines in a few
  // seconds. The pump must not stall, the browser must not die, and the tail
  // must be right.
  await mk(['sh', '-c', 'exec sh'], 'flood')
  await sleep(2500)
  await select('flood')
  await page.locator(MAIN_SCREEN).click()
  const floodStart = Date.now()
  // Coloured, which it was not.
  //
  // The escape-fragment check below exists for the truncated-start path -- the
  // one that used to print the tail of an escape sequence as literal text --
  // and this loop emitted `echo "line $i of noise"`, pure text with no escape
  // sequence anywhere in it. So the ring buffer had nothing that could be cut
  // in half, and that check could not fire whatever its regex said. It was
  // aimed past the defect *and* pointed at a fixture that cannot produce one.
  //
  // printf rather than echo, because echo's handling of backslash escapes is
  // not portable across shells and this pane runs whatever tmux started.
  //
  // Eight sequences per line rather than two, which is not decoration. Where
  // the ring buffer's start lands is arbitrary, so what matters is the odds
  // that it lands *inside* a sequence: with one colour on and one off, escape
  // bytes are about a third of the line and a run with trimming entirely
  // disabled still passed. Measured. At eight they are most of it.
  await page.keyboard.type(
    'i=0; while [ $i -lt 20000 ]; do printf ' +
    '"\\033[31m\\033[1m\\033[4m\\033[7mline %d of noise\\033[0m\\033[27m\\033[24m\\033[22m\\n" $i; ' +
    'i=$((i+1)); done; echo FLOOD_DONE')
  await page.keyboard.press('Enter')
  const flood = await waitForRow((r) => r.includes('FLOOD_DONE'), 90000)
  const floodMs = Date.now() - floodStart
  if (flood.index < 0) {
    note('FAIL', 'flood', `twenty thousand lines never finished arriving (waited ${floodMs}ms)`)
  } else {
    note('INFO', 'flood', `twenty thousand lines in ${(floodMs / 1000).toFixed(1)}s`)
    // The page has to still be a page.
    const alive = await page.evaluate(() => document.querySelectorAll('[data-testid="session-row"]').length)
    if (!alive) note('FAIL', 'flood', 'the interface stopped responding after the flood')
    // And the very last lines must be the right ones, in order.
    const tail = (await rows(page)).filter(Boolean)
    if (!tail.some((r) => r.includes('line 19999'))) {
      note('FAIL', 'flood', `the tail is missing the last line: ${JSON.stringify(tail.slice(-4))}`)
    }
  }

  // ── replay after the buffer wrapped ──────────────────────────────────────
  // Twenty thousand lines is well past the replay buffer, so reconnecting now
  // exercises the truncated-start path — the one that used to print the tail
  // of an escape sequence as literal text.
  await page.reload({ waitUntil: 'networkidle' })
  await sleep(3500)
  const afterReload = (await rows(page)).join('\n')
  if (!afterReload.includes('line 19') && !afterReload.includes('FLOOD_DONE')) {
    note('FAIL', 'replay', `nothing recognisable came back after a reload: ${JSON.stringify(afterReload.slice(0, 200))}`)
  }
  if (/\[\?\d+;\d+c|\[>\d+;\d+;\d+c/.test(afterReload)) {
    note('FAIL', 'replay', 'the replay injected terminal responses into the session')
  }
  // A stray escape fragment at the top would show as literal parameter bytes.
  //
  // The old pattern was `^\d+;?\d*[a-zA-Z]\s*$` with `length < 8`, anchored at
  // both ends: it matched a row that is *only* "31m", and a regression in
  // trimPartialEscape produces "31mline 4021 of noise" -- parameter bytes with
  // the rest of the line behind them. So it could not match the shape it was
  // written for. Anchored at the start only now.
  //
  // Deliberately not matching a leading "[". The buffer can cut between the ESC
  // and the "[", and trimPartialEscape leaves that case alone on purpose:
  // skipping a leading bracket also eats bash's job-control prefix, so
  // "[1]+  Done  sleep 5" becomes "+  Done  sleep 5". That trade is measured in
  // TestTrimPartialEscapeKnownFalseNegative and a visible "[31m" is the
  // accepted side of it. Flagging it here would report a decision as a defect.
  //
  // DO NOT read this as covering trimPartialEscape. It does not, and that was
  // measured four ways rather than assumed. With the function replaced by
  // `return b`, every one of these passed 0 FAIL:
  //
  //   the original flood, `echo "line $i of noise"` -- no escape sequence
  //     anywhere in it, so the buffer had nothing that could be cut in half
  //   two sequences a line, escape bytes about a third of it
  //   eight sequences a line, escape bytes most of it
  //   the same, after scrolling to the top of the scrollback
  //
  // Something between the ring buffer and the rendered page is not carrying
  // the fragment through, and what it is has not been identified. The
  // deterministic coverage is TestTrimPartialEscape* in internal/session, which
  // fails immediately for the same mutation.
  //
  // Kept because a literal escape tail at the top of a replay is a real defect
  // whatever produces it, and a FAIL here would be true. Not kept as evidence
  // that the trimming works.
  // Scrolled to the top first, which is the whole reason this check never
  // fired. `rows()` reads the visible grid, and after a reload the viewport
  // sits at the *bottom* -- on line 19999 -- while the truncated start of the
  // replay is thousands of rows up in the scrollback. The check has been
  // looking at the opposite end of the screen from the thing it is named for.
  //
  // Measured before this line existed: with trimPartialEscape replaced by
  // `return b`, three different floods -- plain text, two sequences a line,
  // eight sequences a line -- all passed. A check that cannot fail when the
  // function it guards is deleted is a decoration.
  const termBox = await page.locator(MAIN_SCREEN).boundingBox()
  if (termBox) {
    await page.mouse.move(termBox.x + termBox.width / 2, termBox.y + termBox.height / 2)
    await page.mouse.wheel(0, -400000)
    await sleep(1200)
  }
  const firstRow = (await rows(page)).find((r) => r.trim()) ?? ''
  if (/^\d{1,4}(?:;\d{1,4})*[a-zA-Z]/.test(firstRow.trim())) {
    note('FAIL', 'replay',
      `the first restored row opens with the tail of an escape sequence: ${JSON.stringify(firstRow)}`)
  }

  // ── losing the connection ────────────────────────────────────────────────
  // Phones sleep, laptops close, networks drop. Coming back has to be
  // automatic, because a terminal that needs a manual reload after every
  // hiccup is one nobody trusts to leave open.
  await sleep(1000)
  const beforeDrop = await page.locator('[data-testid="connection"]').getAttribute('data-status')
  if (beforeDrop !== 'open') {
    note('FAIL', 'reconnect', `not connected before the test: ${beforeDrop}`)
  } else {
    await page.evaluate(() => {
      // Reach through the page's own socket rather than cutting the network,
      // so this tests the client's recovery and not the browser's.
      const ws = performance
        .getEntriesByType('resource')
        .filter((e) => e.name.startsWith('ws'))
      void ws
      // The panel keeps one socket; closing it from here is what a dropped
      // network looks like to the client.
      const sockets = window.__vpSockets ?? []
      for (const s of sockets) s.close()
    })
    // No hook exposed, so fall back to what a user would actually experience:
    // put the tab to sleep and wake it.
    await page.evaluate(() => window.dispatchEvent(new Event('offline')))
    await sleep(500)
    await page.evaluate(() => window.dispatchEvent(new Event('online')))
    await sleep(2000)
    const after = await page.locator('[data-testid="connection"]').getAttribute('data-status')
    if (after !== 'open') {
      note('WARN', 'reconnect', `status is ${after} after an offline/online cycle`)
    }
  }


  // ── the GPU renderer's glyph atlas under thousands of distinct CJK glyphs ──
  // A screen of Chinese drew as fragments under WebGL -- some characters
  // right, some two overlapped -- while the buffer was right. xterm's WebGL
  // renderer keeps every glyph it has drawn in texture pages and merges them
  // when it runs out of texture units, and the fixes for bugs in that merge
  // (xterm.js #5883, #6042, #6043, #6055) were not in addon-webgl 0.19.0.
  //
  // What this measures: the screen after the flood, against the same screen
  // in a page whose atlas holds only what is on it. Anything the flooded
  // atlas had wrong shows up as a difference. Measured red: with addon-webgl
  // patched never to re-upload a page it had bound once, this step saw the
  // fragments and failed on a quarter of the pixels.
  //
  // It is a guard, and it has never been seen red. On 0.19.0 in headless
  // Chromium it passes too, in every shape tried (docs/build-log.md, "The
  // glyph atlas, measured"): SwiftShader reports 32 texture units where real
  // Intel and Apple GPUs report 16, and 0.19.0 never resets its clear-model
  // flag after the first merge, which hides #5883's second bug. The units are
  // made 16 here and the page is at DPR 2, which is what makes merges happen
  // at all with this many glyphs; that much is measured.
  {
    const script = join(DATA, 'atlas.mjs')
    writeFileSync(script, `
      const chars = []
      for (let c = 0x4e00; c <= 0x9fff; c++) chars.push(c)
      for (let c = 0x3400; c <= 0x4dbf; c++) chars.push(c)
      let seed = 7
      const rnd = () => ((seed = (seed * 1103515245 + 12345) & 0x7fffffff) / 0x7fffffff)
      for (let i = chars.length - 1; i > 0; i--) { const j = Math.floor(rnd() * (i + 1)); [chars[i], chars[j]] = [chars[j], chars[i]] }
      const cols = process.stdout.columns || 80, rows = process.stdout.rows || 24
      const per = Math.floor(cols / 2)
      const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
      const out = (s) => new Promise((r) => process.stdout.write(s, r))
      const screen = (from) => {
        let s = '\\x1b[2J\\x1b[H'
        for (let r = 0; r < rows - 1; r++) s += String.fromCodePoint(...chars.slice(from + r * per, from + (r + 1) * per)) + (r < rows - 2 ? '\\r\\n' : '')
        return s
      }
      const size = per * (rows - 1)
      // Waits for Enter, so every screen is drawn by the browser's renderer
      // rather than arriving all at once as a replay.
      await out('ATLAS' + 'WAIT')
      await new Promise((r) => process.stdin.once('data', r))
      await out('\\x1b[?25l')
      // The primary screen, then an agent's full-screen UI on the alternate
      // one -- the shape of xterm's own regression test -- then back.
      let at = 0
      for (; at + size <= chars.length / 2; at += size) { await out(screen(at)); await sleep(80) }
      const keep = screen(0)
      await out(keep)
      await out('\\x1b[?1049h')
      for (; at + size <= chars.length; at += size) { await out(screen(at)); await sleep(80) }
      await out('\\x1b[?1049l')
      await out('ATLAS' + 'READY')
      await sleep(1e9)
    `)
    await mk([process.execPath, script], 'atlas')
    await sleep(1500)
    const gpu = await browser.newContext({ viewport: { width: 1200, height: 800 }, deviceScaleFactor: 2 })
    await gpu.addInitScript(() => {
      for (const C of [WebGLRenderingContext, WebGL2RenderingContext]) {
        const get = C.prototype.getParameter
        C.prototype.getParameter = function (p) {
          // MAX_TEXTURE_IMAGE_UNITS, as an Intel or Apple GPU reports it.
          return p === 0x8872 ? 16 : get.call(this, p)
        }
      }
      // The widest atlas page made. Pages are square canvases that start at
      // 512, and only a merge makes a wider one, so this is how the check
      // knows it exercised a merge rather than assuming it did. Read from
      // the canvas, not from texture uploads: a renderer that fails to
      // rebind a merged page never uploads it, which is the bug.
      const h = Object.getOwnPropertyDescriptor(HTMLCanvasElement.prototype, 'height')
      Object.defineProperty(HTMLCanvasElement.prototype, 'height', {
        ...h,
        set(v) {
          if (v === this.width && v >= 512 && (v & (v - 1)) === 0) window.__vpAtlasWidest = Math.max(window.__vpAtlasWidest ?? 0, v)
          h.set.call(this, v)
        },
      })
    })
    const g = await gpu.newPage()
    const gErrors = []
    g.on('pageerror', (e) => gErrors.push(String(e)))
    await g.goto(BASE, { waitUntil: 'networkidle' })
    await g.locator('[data-testid="auth-username"]').fill('stress')
    await g.locator('[data-testid="auth-password"]').fill('a sufficiently long password')
    await g.locator('[data-testid="auth-submit"]').click()
    await g.waitForSelector('[data-testid="sidebar"], [data-testid="sidebar-rail"]', { timeout: 15000 })
    await sleep(1500)
    await g.locator('[data-testid="session-row"]', { hasText: 'atlas' }).first().click()
    await sleep(1500)
    await g.locator('[data-testid="main-terminal"]:visible .xterm-screen').click()
    await g.keyboard.press('Enter')
    const end = Date.now() + 90000
    let ready = false
    while (Date.now() < end && !ready) {
      ready = (await rows(g)).join('').includes('ATLASREADY')
      if (!ready) await sleep(500)
    }
    const onGpu = await g.evaluate(() => !!document.querySelector('[data-testid="main-terminal"]:not(.hidden) .xterm-screen canvas') &&
      !document.querySelector('[data-testid="main-terminal"]:not(.hidden) .xterm-rows')?.children.length)
    if (!ready) {
      await g.screenshot({ path: join(SHOTS, 'atlas-stuck.png') })
      note('FAIL', 'atlas', `the glyph flood never finished: ${JSON.stringify((await rows(g)).filter(Boolean).slice(-3)).slice(0, 300)}`)
    } else if (!onGpu) {
      note('WARN', 'atlas', 'no WebGL in this browser; the atlas was not exercised')
    } else if (((await g.evaluate(() => window.__vpAtlasWidest ?? 0)) ?? 0) < 1024) {
      note('FAIL', 'atlas', `the flood never merged an atlas page (widest ${await g.evaluate(() => window.__vpAtlasWidest)}); the step measured nothing`)
    } else {
      await g.mouse.move(0, 0)
      await sleep(1500)
      const screen = g.locator('[data-testid="main-terminal"]:visible .xterm-screen')
      const before = await screen.screenshot()
      // The reference is the same session opened in a fresh page: same
      // size, same DPR, same units, and an atlas holding only what is on the
      // screen. Changing the DPR there and back was tried first, and with a
      // renderer deliberately broken it drew the same fragments twice: the
      // atlas for the original ratio came back from xterm's cache.
      const g2 = await gpu.newPage()
      g2.on('pageerror', (e) => gErrors.push(String(e)))
      await g2.goto(BASE, { waitUntil: 'networkidle' })
      await g2.waitForSelector('[data-testid="sidebar"], [data-testid="sidebar-rail"]', { timeout: 15000 })
      await g2.locator('[data-testid="session-row"]', { hasText: 'atlas' }).first().click()
      await sleep(2500)
      await g2.mouse.move(0, 0)
      await sleep(1000)
      const fresh = g2.locator('[data-testid="main-terminal"]:visible .xterm-screen')
      const after = await fresh.screenshot()
      const diff = await g.evaluate(async ([a, b]) => {
        const dec = async (b64) => {
          const bm = await createImageBitmap(await (await fetch('data:image/png;base64,' + b64)).blob())
          const c = new OffscreenCanvas(bm.width, bm.height)
          const x = c.getContext('2d')
          x.drawImage(bm, 0, 0)
          return x.getImageData(0, 0, bm.width, bm.height)
        }
        const A = await dec(a)
        const B = await dec(b)
        if (A.width !== B.width || A.height !== B.height) return { n: -1, total: 0 }
        let n = 0
        for (let i = 0; i < A.data.length; i += 4) {
          const d = Math.abs(A.data[i] - B.data[i]) + Math.abs(A.data[i + 1] - B.data[i + 1]) + Math.abs(A.data[i + 2] - B.data[i + 2])
          if (d > 48) n++
        }
        return { n, total: A.width * A.height }
      }, [before.toString('base64'), after.toString('base64')])
      writeFileSync(join(SHOTS, 'atlas-before.png'), before)
      writeFileSync(join(SHOTS, 'atlas-after.png'), after)
      // A few hundred pixels of slack: the cursor is hidden, but a blink
      // or the selection layer is not worth failing a run over. A broken
      // page is thousands -- every glyph on it.
      if (diff.n < 0 || diff.n > 300) {
        note('FAIL', 'atlas', `after the flood the screen differs from a fresh page: ${diff.n} of ${diff.total} pixels (shots: atlas-before/after.png)`)
      } else {
        note('INFO', 'atlas', `matches a fresh page: ${diff.n} of ${diff.total} pixels differ, widest page ${await g.evaluate(() => window.__vpAtlasWidest)}`)
      }
    }
    for (const e of gErrors) note('FAIL', 'atlas', `uncaught: ${e}`)
    await gpu.close()
  }

  for (const e of pageErrors) note('FAIL', 'js', `uncaught: ${e}`)
  await page.screenshot({ path: join(SHOTS, 'after-flood.png') })
} catch (e) {
  note('FAIL', 'harness', String(e))
} finally {
  await cleanup()
}

const order = { FAIL: 0, WARN: 1, INFO: 2 }
findings.sort((a, b) => order[a.sev] - order[b.sev])
const fails = findings.filter((f) => f.sev === 'FAIL').length
console.log(`\n=== stress check: ${fails} FAIL, ${findings.filter((f) => f.sev === 'WARN').length} WARN ===`)
for (const f of findings) console.log(`[${f.sev}] ${f.area}: ${f.msg}`)
console.log(`\nscreenshots: ${SHOTS}`)
process.exit(fails > 0 ? 1 : 0)
