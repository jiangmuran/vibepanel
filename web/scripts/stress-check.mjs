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
import { spawn, execSync } from 'node:child_process'
import { mkdtempSync, mkdirSync, rmSync } from 'node:fs'
import { createServer } from 'node:net'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { assertFreshBuild } from './lib/fresh.mjs'
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
const rows = (page) =>
  page.$$eval('.xterm-rows > div', (els) =>
    els.map((el) => (el.textContent ?? '').replace(/\u00a0/g, ' ')))

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
  const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
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
  const cols = Number(grid.split('x')[0])
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
    await page.locator('.xterm-screen').click()
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
    const rows = [...document.querySelectorAll('.xterm-rows > div')]
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
  await page.locator('.xterm-screen').click()
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

  const box = await page.locator('.xterm-screen').boundingBox()
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
        const wrap = document.querySelector('.xterm')?.parentElement
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

  // ── a flood ──────────────────────────────────────────────────────────────
  // A build or a test run produces tens of thousands of lines in a few
  // seconds. The pump must not stall, the browser must not die, and the tail
  // must be right.
  await mk(['sh', '-c', 'exec sh'], 'flood')
  await sleep(2500)
  await select('flood')
  await page.locator('.xterm-screen').click()
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
  const termBox = await page.locator('.xterm-screen').first().boundingBox()
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
