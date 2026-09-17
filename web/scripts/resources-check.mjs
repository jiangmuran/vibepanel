// The Resources tab and the memory question, driven in a real browser against
// the real binary, with a session that really runs out of memory.
//
// What it covers: the panel runs as a transient user service, so it moves its
// sessions into a scope of their own the way an installed one does; the tab
// says so; choosing a mode and saving custom numbers stick; a session that
// fills its budget and stalls raises the bar across the top of the console,
// naming the process; pressing End on the bar ends that process and the bar
// goes away; with auto allowed, a countdown shows and the panel ends the
// process itself; the panel answers quickly the whole time; and the tab and the
// bar fit at phone, tablet and desktop widths in both themes and languages.
//
// The pool is made small on purpose (VIBEPANEL_DEBUG_SCOPE_MEMORY_MAX), so the
// session fills two gigabytes rather than the machine.
//
// Without a user manager (most CI runners) the panel cannot be a service, the
// sessions are not isolated, and everything that needs a pool is a WARN; the
// layout half still runs.

import { chromium } from 'playwright'
import { createServer as createNetServer } from 'node:net'
import { spawn, execFileSync } from 'node:child_process'
import { mkdtempSync, mkdirSync, writeFileSync, chmodSync, existsSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { assertFreshBuild } from './lib/fresh.mjs'
import { sweepStaleSockets } from './lib/stale.mjs'
import { findUnreachable } from './lib/overflow.mjs'
import { findUnnamedControls } from './lib/names.mjs'
import { findFadedControls } from './lib/faded.mjs'

const ROOT = new URL('../../', import.meta.url).pathname
const BIN = join(ROOT, 'vibepanel')
assertFreshBuild(BIN, ROOT)
sweepStaleSockets((msg) => console.log(`==> ${msg}`))

const freePort = () =>
  new Promise((resolve, reject) => {
    const probe = createNetServer()
    probe.once('error', reject)
    probe.listen(0, '127.0.0.1', () => {
      const { port } = probe.address()
      probe.close(() => resolve(port))
    })
  })

const PORT = await freePort()
const SOCKET = `vpres-${process.pid}`
const UNIT = `vp-resources-check-${process.pid}`
const SCOPE = `vibepanel-sessions-${SOCKET}.scope` // a user manager's name: no uid
const BASE = `http://127.0.0.1:${PORT}`
const USERNAME = 'res'
const PASSWORD = 'resources-check-password-1'

const findings = []
const note = (sev, where, msg) => {
  findings.push({ sev, where, msg })
  console.log(`[${sev}] ${where}: ${msg}`)
}
const pass = (where, msg) => console.log(`[ok]   ${where}: ${msg}`)
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
async function until(pred, ms = 8000, step = 200) {
  const deadline = Date.now() + ms
  while (Date.now() < deadline) {
    if (await pred()) return true
    await sleep(step)
  }
  return false
}

const work = mkdtempSync(join(tmpdir(), 'vpres-'))
const SHOTS = join(work, 'shots')
mkdirSync(SHOTS, { recursive: true })

const uid = process.getuid()
const runtime = process.env.XDG_RUNTIME_DIR || `/run/user/${uid}`
const env = { ...process.env, XDG_RUNTIME_DIR: runtime }
const quiet = (cmd, args) => {
  try {
    return execFileSync(cmd, args, { env, stdio: ['ignore', 'pipe', 'ignore'] }).toString()
  } catch {
    return null
  }
}
const asService = existsSync(runtime) && quiet('systemctl', ['--user', 'show', '-p', 'Version']) !== null

let server
let browser
function cleanup() {
  if (asService) {
    quiet('systemctl', ['--user', 'stop', UNIT])
    quiet('systemctl', ['--user', 'stop', SCOPE])
  } else {
    server?.kill('SIGTERM')
  }
  quiet('tmux', ['-L', SOCKET, 'kill-server'])
}
for (const sig of ['SIGINT', 'SIGTERM', 'SIGHUP']) {
  process.on(sig, () => {
    cleanup()
    process.exit(130)
  })
}

// A session that fills its budget with memory it holds, and then keeps six
// workers reading files at random that no longer fit beside it.
const hog = join(work, 'hog.sh')
writeFileSync(join(work, 'hold.py'), 'import sys,time\na=[]\nwhile len(a)*64<int(sys.argv[1]): a.append(bytearray(b"x"*(64<<20)))\ntime.sleep(900)\n')
writeFileSync(join(work, 'touch.py'), 'import mmap,random,sys,time\nf=open(sys.argv[1],"rb");m=mmap.mmap(f.fileno(),0,prot=mmap.PROT_READ);n=len(m)\nwhile True:\n  for _ in range(200): m[random.randrange(n)]\n  time.sleep(0.02)\n')
writeFileSync(hog, `#!/bin/bash
cd "$(dirname "$0")"
for i in 1 2 3 4 5 6; do [ -f w$i.bin ] || head -c 300M /dev/urandom > w$i.bin; done
exec -a vp-hold python3 hold.py 1400 &
sleep 3
for i in 1 2 3 4 5 6; do python3 touch.py w$i.bin & done
wait
`)
chmodSync(hog, 0o755)

try {
  const busy = await fetch(`${BASE}/api/health`).then(() => true).catch(() => false)
  if (busy) throw new Error(`something is already answering on ${BASE}`)
  const panelEnv = {
    HOME: work,
    VIBEPANEL_DATA_DIR: join(work, 'data'),
    VIBEPANEL_TMUX_SOCKET: SOCKET,
    VIBEPANEL_ADDR: `127.0.0.1:${PORT}`,
    VIBEPANEL_DEBUG_SCOPE_MEMORY_MAX: String(2 * 1024 ** 3),
  }
  let readLog
  if (asService) {
    execFileSync('systemd-run', ['--user', '--unit', UNIT, '--collect', '-p', 'KillMode=process',
      ...Object.entries(panelEnv).map(([k, v]) => `--setenv=${k}=${v}`), BIN, 'serve'], { env, stdio: 'ignore' })
    readLog = () => quiet('journalctl', ['--user', '-u', UNIT, '--no-pager', '-o', 'cat']) ?? ''
  } else {
    note('WARN', 'service', 'no user manager: the panel is not a service here, so nothing is isolated')
    let log = ''
    server = spawn(BIN, ['serve'], { stdio: ['ignore', 'pipe', 'pipe'], env: { ...process.env, ...panelEnv } })
    server.stdout.on('data', (d) => { log += d })
    server.stderr.on('data', (d) => { log += d })
    readLog = () => log
  }
  let setupToken = ''
  for (let i = 0; i < 60 && !setupToken; i++) {
    await sleep(300)
    setupToken = (/^ {6}([A-Za-z0-9_-]{30,})$/m.exec(readLog()) ?? [])[1] ?? ''
  }
  if (!setupToken) throw new Error(`no setup token in:\n${readLog()}`)

  browser = await chromium.launch({ headless: true })
  const owner = await browser.newContext({ viewport: { width: 1400, height: 950 } })
  const setup = await owner.request.post(`${BASE}/api/auth/setup`, {
    data: { token: setupToken, username: USERNAME, password: PASSWORD },
  })
  if (setup.status() !== 201) throw new Error(`setup: ${setup.status()} ${await setup.text()}`)
  await owner.request.post(`${BASE}/api/settings/tour`, { headers: { Origin: BASE } })
  const api = async (method, path, data) => {
    const res = await owner.request.fetch(`${BASE}${path}`, {
      method, data, headers: { Origin: BASE, 'Content-Type': 'application/json' },
    })
    const text = await res.text()
    let body = null
    try { body = text ? JSON.parse(text) : null } catch { body = text }
    return { status: res.status(), body }
  }
  const must = async (method, path, data) => {
    const r = await api(method, path, data)
    if (r.status >= 300) throw new Error(`${method} ${path}: ${r.status} ${JSON.stringify(r.body)}`)
    return r.body
  }

  const project = await must('POST', '/api/projects', { path: work, name: 'resproj' })
  await must('POST', '/api/sessions', { projectId: project.id, command: ['sleep', '3600'], title: 'quiet' })

  const page = await owner.newPage()
  const errors = []
  page.on('pageerror', (e) => errors.push(String(e)))
  await page.goto(`${BASE}/`, { waitUntil: 'networkidle' })
  await page.evaluate(() => localStorage.setItem('vibepanel.lang', 'en'))
  await page.reload({ waitUntil: 'networkidle' })

  const openResources = async (tab) => {
    await tab.locator('[data-testid="settings-open"]').click()
    await tab.getByTestId('settings-group-resources').click()
    return until(() => tab.getByTestId('resources').count().then((n) => n > 0), 6000)
  }

  // ── the tab, and whether the sessions are apart from the panel ──────────
  if (!(await openResources(page))) throw new Error('the resources tab did not render')
  const isolated = await until(
    () => page.getByTestId('resources-isolation').getAttribute('data-state').then((s) => s === 'isolated'),
    asService ? 10000 : 3000,
  )
  if (asService && !isolated) {
    const reason = await page.getByTestId('resources-isolation').getAttribute('data-reason')
    note('FAIL', 'isolation', `a user service's sessions were not isolated: ${reason}`)
  } else if (asService) {
    pass('isolation', 'the tab says the sessions run apart from the panel')
  }

  // ── choosing a mode, and custom numbers ─────────────────────────────────
  await page.getByTestId('resources-mode-conservative').click()
  const conservative = await until(async () => (await api('GET', '/api/resources')).body.policy.mode === 'conservative')
  const isPressed = () => page.getByTestId('resources-mode-conservative').getAttribute('aria-pressed').then((v) => v === 'true')
  const shown = await until(isPressed, 5000, 100)
  // And still chosen a poll later: a poll that was in flight when the mode was
  // saved used to answer with the old mode and put it back.
  await sleep(2500)
  const kept = await isPressed()
  if (!conservative || !shown || !kept) note('FAIL', 'mode', `conservative did not stick (stored ${conservative}, shown ${shown}, still shown ${kept})`)
  else pass('mode', 'a preset is stored, shown as chosen, and still chosen after the next poll')

  await page.getByTestId('resources-mode-custom').click()
  if (!(await until(() => page.getByTestId('resources-custom').count().then((n) => n > 0)))) {
    note('FAIL', 'custom', 'choosing Custom did not open its fields')
  } else {
    await page.getByTestId('resources-custom-ask').fill('80')
    const auto = page.getByTestId('resources-custom-auto')
    if (await auto.isChecked()) await auto.uncheck()
    await page.getByTestId('resources-custom-save').click()
    const stored = await until(async () => {
      const p = (await api('GET', '/api/resources')).body.policy
      return p.mode === 'custom' && p.askPercent === 80 && p.autoAct === false
    })
    if (!stored) note('FAIL', 'custom', `custom numbers were not stored: ${JSON.stringify((await api('GET', '/api/resources')).body.policy)}`)
    else pass('custom', 'custom numbers are stored')
  }
  const bad = await api('PUT', '/api/resources/policy', { mode: 'custom', poolPercent: 20, askPercent: 80, autoAct: false, graceSeconds: 30 })
  if (bad.status !== 400) note('FAIL', 'custom', `an out-of-range pool was answered ${bad.status}`)
  else pass('custom', 'an out-of-range number is refused rather than clamped')
  await page.locator('[data-testid="settings-close"]').click()

  // ── a session that runs out ─────────────────────────────────────────────
  const latencies = []
  const measure = async () => {
    const t0 = Date.now()
    const r = await owner.request.get(`${BASE}/api/state`)
    latencies.push(Date.now() - t0)
    return r.status()
  }
  const endProc = async (sessionId) => {
    const v = (await api('GET', '/api/resources')).body
    return v.sessions.find((s) => s.id === sessionId)?.top?.find((p) => p.name === 'python3' && p.rss > 1024 ** 3)
  }

  if (!isolated) {
    note('WARN', 'pressure', 'skipped: without a pool there is nothing to fill safely')
  } else {
    const hogSession = await must('POST', '/api/sessions', { projectId: project.id, command: [hog], title: 'hog' })
    // Asked, not acted on: the policy above has auto off.
    const asked = await until(async () => {
      await measure()
      return page.getByTestId('resource-alert').count().then((n) => n > 0)
    }, 90000, 1000)
    if (!asked) {
      note('FAIL', 'ask', `no question after 90s: ${JSON.stringify((await api('GET', '/api/resources')).body.pool)}`)
    } else {
      const text = await page.getByTestId('resource-alert').innerText()
      pass('ask', `the bar is up: ${text.replace(/\s+/g, ' ').slice(0, 120)}`)
      await page.screenshot({ path: join(SHOTS, 'alert-1400.png') })
      if (await page.getByTestId('resource-alert-countdown').count()) {
        note('FAIL', 'ask', 'a countdown with auto turned off')
      }
      // The phone, while the bar is up.
      const phone = await browser.newContext({ viewport: { width: 400, height: 800 } })
      await phone.request.post(`${BASE}/api/auth/login`, { data: { username: USERNAME, password: PASSWORD }, headers: { Origin: BASE } })
      const small = await phone.newPage()
      await small.goto(`${BASE}/`, { waitUntil: 'networkidle' })
      if (await until(() => small.getByTestId('resource-alert').count().then((n) => n > 0), 8000)) {
        await small.screenshot({ path: join(SHOTS, 'alert-400.png') })
        const overflowX = await small.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1)
        // Every answer on screen, not only the bar's box: the first version
        // of this passed with Details past the right edge.
        const outside = await small.getByTestId('resource-alert').locator('button').evaluateAll((els) =>
          els.filter((el) => { const r = el.getBoundingClientRect(); return r.right > innerWidth || r.left < 0 }).map((el) => el.textContent))
        if (overflowX || outside.length) note('FAIL', 'ask/400', `the bar does not fit a phone: ${outside.join(', ')}`)
        else pass('ask/400', 'every answer on the bar is on a phone screen')
      } else {
        note('FAIL', 'ask/400', 'a page opened while the question stands does not show it')
      }
      await phone.close()

      // Answered from the bar.
      const hold = await until(() => endProc(hogSession.id).then(Boolean), 20000, 1000)
      if (!hold) note('WARN', 'end', 'the 1.4 GiB process was not listed yet')
      const alertBefore = (await api('GET', '/api/resources')).body.alert
      const named = alertBefore?.proc?.name
      await page.getByTestId('resource-alert-end').click()
      const gone = await until(async () => {
        await measure()
        return (await page.getByTestId('resource-alert').count()) === 0
      }, 20000, 500)
      if (!gone) note('FAIL', 'end', 'the bar stayed after End')
      const acts = (await api('GET', '/api/resources')).body.actions
      if (!acts.some((a) => a.kind === 'kill' && !a.auto && a.name === named)) {
        note('FAIL', 'end', `no manual kill of ${named} recorded: ${JSON.stringify(acts)}`)
      } else {
        pass('end', `End on the bar ended ${named}, and it is in the recent list`)
      }
    }

    // Allowed to act: a countdown, then the panel does it.
    await must('PUT', '/api/resources/policy', { mode: 'custom', poolPercent: 88, askPercent: 80, autoAct: true, graceSeconds: 10 })
    await must('POST', '/api/sessions', { projectId: project.id, command: [hog], title: 'hog2' })
    const countdown = await until(async () => {
      await measure()
      return page.getByTestId('resource-alert-countdown').count().then((n) => n > 0)
    }, 120000, 1000)
    if (!countdown) {
      const v = (await api('GET', '/api/resources')).body
      note('FAIL', 'auto', `no countdown after 120s: level ${v.level}, pool ${JSON.stringify(v.pool)}, alert ${JSON.stringify(v.alert)}`)
    } else {
      pass('auto', 'a stall with auto allowed shows a countdown')
      const acted = await until(async () => {
        await measure()
        return (await api('GET', '/api/resources')).body.actions.some((a) => a.kind === 'kill' && a.auto)
      }, 60000, 1000)
      if (!acted) note('FAIL', 'auto', 'the countdown ran out and nothing was ended')
      else pass('auto', 'the panel ended the process itself')
    }

    const sorted = [...latencies].sort((a, b) => a - b)
    const p95 = sorted[Math.floor(sorted.length * 0.95)] ?? 0
    const worst = sorted[sorted.length - 1] ?? 0
    if (sorted.length < 10) note('FAIL', 'answering', `only ${sorted.length} measurements`)
    else if (p95 > 500 || worst > 3000) note('FAIL', 'answering', `the panel slowed with the sessions: p95 ${p95}ms, worst ${worst}ms over ${sorted.length}`)
    else pass('answering', `the panel kept answering: p95 ${p95}ms, worst ${worst}ms over ${sorted.length} requests`)
  }

  // ── layout at every size, theme and language ────────────────────────────
  for (const lang of ['zh', 'en']) {
    for (const theme of ['light', 'dark']) {
      for (const [w, h] of [[400, 800], [820, 1000], [1400, 950]]) {
        const where = `layout/${lang}/${theme}/${w}`
        const ctx = await browser.newContext({ viewport: { width: w, height: h } })
        await ctx.request.post(`${BASE}/api/auth/login`, { data: { username: USERNAME, password: PASSWORD }, headers: { Origin: BASE } })
        const tab = await ctx.newPage()
        const tabErrors = []
        tab.on('pageerror', (e) => tabErrors.push(String(e)))
        await tab.goto(`${BASE}/`, { waitUntil: 'commit' })
        await tab.evaluate(([l, t]) => {
          localStorage.setItem('vibepanel.lang', l)
          localStorage.setItem('vibepanel.theme', t)
        }, [lang, theme])
        await tab.goto(`${BASE}/`, { waitUntil: 'networkidle' })
        if (!(await openResources(tab))) {
          note('FAIL', where, 'the tab did not render')
          await ctx.close()
          continue
        }
        // Open the first session's processes, so the widest row is measured.
        const row = tab.getByTestId('resources-session').first()
        if (await row.count()) await row.locator('button').first().click()
        await sleep(600)
        await tab.screenshot({ path: join(SHOTS, `resources-${lang}-${theme}-${w}.png`) })
        const body = tab.getByTestId('settings-body')
        const overflowX = await body.evaluate((el) => el.scrollWidth > el.clientWidth + 1)
        if (overflowX) note('FAIL', where, 'the tab is wider than the dialog body')
        const { examined, found } = await findUnreachable(tab, sleep)
        if (examined < 20) note('FAIL', where, `the overflow scan measured ${examined} elements`)
        else if (found.length) note('FAIL', where, `unreachable content: ${found.slice(0, 5).join(' | ')}`)
        const unnamed = await findUnnamedControls(tab)
        if (unnamed.length) note('FAIL', where, `controls with no name: ${unnamed.slice(0, 5).join(', ')}`)
        const faded = await findFadedControls(tab)
        if (faded.length) note('FAIL', where, `faded controls: ${faded.slice(0, 5).join(', ')}`)
        if (tabErrors.length) note('FAIL', where, `errors: ${tabErrors.slice(0, 3).join(' | ')}`)
        if (!overflowX && !found.length && !unnamed.length && !faded.length && !tabErrors.length) pass(where, 'clean')
        await ctx.close()
      }
    }
  }

  if (errors.length) note('FAIL', 'console', errors.slice(0, 5).join(' | '))
  else pass('console', 'no page errors on the main tab')
} catch (e) {
  note('FAIL', 'resources-check', e.stack ?? String(e))
} finally {
  await browser?.close().catch(() => {})
  cleanup()
  // The hog's six 300 MB files, not the screenshots.
  for (let i = 1; i <= 6; i++) rmSync(join(work, `w${i}.bin`), { force: true })
}

const fails = findings.filter((f) => f.sev === 'FAIL').length
const warns = findings.filter((f) => f.sev === 'WARN').length
console.log(`\nscreenshots: ${SHOTS}`)
console.log(`=== resources check: ${fails} FAIL, ${warns} WARN ===`)
process.exit(fails > 0 ? 1 : 0)
