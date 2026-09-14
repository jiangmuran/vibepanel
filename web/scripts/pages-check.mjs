// Share pages, in a real browser: the sandbox, the editing loop, the templates.
//
// Three halves, and the first is the one this exists for.
//
//   sandbox    A page is HTML the panel did not write, served on the panel's
//              own origin, opened in a browser that is signed in to the panel.
//              From inside such a page this tries everything that would turn it
//              into the owner: read the API with the cookie, open the terminal
//              socket, read the cookie, reach storage, reach any other host,
//              navigate the panel, open a window. Each must fail, and the check
//              asserts how it failed -- "nothing succeeded" is also what a page
//              that never ran its probes looks like.
//
//   workflow   What docs/share-pages.md §7 promises, driven through the UI: a
//              page made in settings becomes a project, a session and a first
//              line typed at its prompt; the Preview reloads once after a file
//              settles, shows the errors a page throws and writes them for the
//              agent; Pick types one line and no Enter; Publish; a wall reloads
//              into a new version; a trial ends; parameters redraw without a
//              reload; a revoked link stops polling.
//
//   templates  Every template the binary carries, on the screens it is for,
//              against every fixture: no errors, no refused requests, no NaN or
//              [object Object] on screen, no horizontal overflow, and the hostile
//              fixture's markup never becoming markup.
//
// Run: node scripts/pages-check.mjs
//      VP_PAGES_ONLY=sandbox,templates node scripts/pages-check.mjs

import { chromium } from 'playwright'
import { createServer as createNetServer } from 'node:net'
import { createServer as createHttpServer } from 'node:http'
import { spawn, execFileSync } from 'node:child_process'
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, existsSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { assertFreshBuild } from './lib/fresh.mjs'
import { sweepStaleSockets } from './lib/stale.mjs'

const ROOT = new URL('../../', import.meta.url).pathname
const BIN = join(ROOT, 'vibepanel')
// A check that measures yesterday's binary looks exactly like a pass.
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
const SOCKET = `vppages-${process.pid}`
const BASE = `http://127.0.0.1:${PORT}`
const USERNAME = 'pages'
const PASSWORD = 'pages-check-password-1'

const findings = []
const note = (sev, where, msg) => {
  findings.push({ sev, where, msg })
  console.log(`[${sev}] ${where}: ${msg}`)
}
const pass = (where, msg) => console.log(`[ok]   ${where}: ${msg}`)
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const only = (process.env.VP_PAGES_ONLY ?? 'sandbox,workflow,templates').split(',')

const work = mkdtempSync(join(tmpdir(), 'vppages-'))
const SHOTS = join(work, 'shots')
mkdirSync(SHOTS, { recursive: true })

// ─── a listener standing in for the rest of the internet ─────────────────
//
// Anything a page manages to send leaves a hit here. CSP refusals are reported
// by the browser too, but a refusal nobody listened for and a request that
// was never made are indistinguishable from the page's side; this is the side
// that cannot be fooled.
const outsidePort = await freePort()
const outsideHits = []
const outside = createHttpServer((req, res) => {
  outsideHits.push(req.url)
  res.setHeader('Access-Control-Allow-Origin', '*')
  res.end('outside')
})
await new Promise((r) => outside.listen(outsidePort, '127.0.0.1', r))
const OUTSIDE = `http://127.0.0.1:${outsidePort}`

let server
let browser
// A Ctrl-C or a `timeout` must not leave a panel and a tmux server behind.
// SIGKILL cannot be caught; sweepStaleSockets above is for that.
for (const sig of ['SIGINT', 'SIGTERM', 'SIGHUP']) {
  process.on(sig, () => {
    server?.kill('SIGTERM')
    try {
      execFileSync('tmux', ['-L', SOCKET, 'kill-server'], { stdio: 'ignore' })
    } catch {
      /* nothing to kill */
    }
    process.exit(130)
  })
}
try {
  const busy = await fetch(`${BASE}/api/health`).then(() => true).catch(() => false)
  if (busy) throw new Error(`something is already answering on ${BASE}`)

  server = spawn(BIN, ['serve', '-addr', `127.0.0.1:${PORT}`, '-data-dir', join(work, 'data'),
    '-tmux-socket', SOCKET], { stdio: ['ignore', 'pipe', 'pipe'], env: { ...process.env, HOME: work } })
  let log = ''
  server.stdout.on('data', (d) => { log += d })
  server.stderr.on('data', (d) => { log += d })
  let setupToken = ''
  for (let i = 0; i < 50 && !setupToken; i++) {
    await sleep(300)
    setupToken = (/^ {6}([A-Za-z0-9_-]{30,})$/m.exec(log) ?? [])[1] ?? ''
  }
  if (!setupToken) throw new Error(`no setup token in:\n${log}`)

  browser = await chromium.launch({ headless: true })
  // The owner's browser. Every probe below runs in *this* context, so the
  // session cookie is right there for a page to try to use.
  const owner = await browser.newContext({ viewport: { width: 1600, height: 1000 } })
  const setup = await owner.request.post(`${BASE}/api/auth/setup`, {
    data: { token: setupToken, username: USERNAME, password: PASSWORD },
  })
  if (setup.status() !== 201) throw new Error(`setup: ${setup.status()} ${await setup.text()}`)
  // The first-run tour is a modal over the whole panel; mark it seen, as the
  // other checks do, so the clicks below land on the panel.
  await owner.request.post(`${BASE}/api/settings/tour`, { headers: { Origin: BASE } })
  const api = async (method, path, data) => {
    const res = await owner.request.fetch(`${BASE}${path}`, {
      method,
      data,
      headers: { Origin: BASE, 'Content-Type': 'application/json' },
    })
    const text = await res.text()
    let body = null
    try {
      body = text ? JSON.parse(text) : null
    } catch {
      body = text
    }
    return { status: res.status(), body }
  }
  const must = async (method, path, data) => {
    const r = await api(method, path, data)
    if (r.status >= 300) throw new Error(`${method} ${path}: ${r.status} ${JSON.stringify(r.body)}`)
    return r.body
  }

  /** A page directory, adopted and published, with a link drawing it. */
  const publishedPage = async (name, files, manifest) => {
    const dir = join(work, 'pages', name)
    mkdirSync(dir, { recursive: true })
    writeFileSync(join(dir, 'vibepanel.json'), JSON.stringify(manifest ?? { sdk: 1, name, sections: ['sessions'] }))
    for (const [p, body] of Object.entries(files)) {
      mkdirSync(join(dir, p, '..'), { recursive: true })
      writeFileSync(join(dir, p), body)
    }
    const page = await must('POST', '/api/settings/pages', { name, template: '', sourceDir: dir })
    await must('POST', `/api/settings/pages/${page.id}/publish`, { note: 'check' })
    const link = await must('POST', '/api/settings/shares', {
      name, detail: 'names', expiresIn: 3600, pageId: page.id, scope: '', scopeId: '', remark: '',
      locked: false,
    })
    return { dir, page, link }
  }

  // ─── sandbox ─────────────────────────────────────────────────────────────
  if (only.includes('sandbox')) {
    const probe = `<!doctype html><meta charset="utf-8"><title>probe</title>
<form id="f" action="${OUTSIDE}/form" method="post"><input name="x" value="1"></form>
<script>
const results = {}
const done = (k, v) => { results[k] = v; document.title = 'probe ' + Object.keys(results).length }
window.__results = results
const policy = []
document.addEventListener('securitypolicyviolation', (e) => policy.push(e.effectiveDirective + ' ' + e.blockedURI))
window.__policy = policy
try { done('cookie', 'read:' + document.cookie) } catch (e) { done('cookie', 'threw:' + e.name) }
try { localStorage.setItem('x', '1'); done('storage', 'wrote') } catch (e) { done('storage', 'threw:' + e.name) }
try { done('origin', String(self.origin)) } catch (e) { done('origin', 'threw') }
fetch('/api/state', { credentials: 'include' }).then(async (r) => done('state', 'read:' + r.status + ':' + (await r.text()).slice(0, 40)), (e) => done('state', 'refused:' + e.name))
fetch('/api/settings/audit', { credentials: 'include' }).then((r) => done('audit', 'read:' + r.status), (e) => done('audit', 'refused:' + e.name))
fetch('/api/sessions', { method: 'POST', credentials: 'include', headers: { 'Content-Type': 'application/json' }, body: '{}' }).then((r) => done('write', 'status:' + r.status), (e) => done('write', 'refused:' + e.name))
fetch('${OUTSIDE}/fetch').then(() => done('outside', 'reached'), (e) => done('outside', 'refused:' + e.name))
try {
  const ws = new WebSocket(location.origin.replace('http', 'ws') + '/ws')
  ws.onopen = () => done('socket', 'opened')
  ws.onerror = () => done('socket', 'refused')
} catch (e) { done('socket', 'threw:' + e.name) }
const img = new Image(); img.onload = () => done('image', 'loaded'); img.onerror = () => done('image', 'refused'); img.src = '${OUTSIDE}/img.png'
try { const w = window.open('${OUTSIDE}/popup'); done('popup', w ? 'opened' : 'blocked') } catch (e) { done('popup', 'threw:' + e.name) }
try { document.getElementById('f').submit(); done('form', 'submitted') } catch (e) { done('form', 'threw:' + e.name) }
const frame = document.createElement('iframe'); frame.src = '/'; document.body.appendChild(frame)
setTimeout(() => { try { done('frame', frame.contentDocument ? 'readable' : 'opaque') } catch (e) { done('frame', 'threw:' + e.name) } }, 800)
</script><script src="vibepanel.js"></script>`
    const { link } = await publishedPage('probe', { 'index.html': probe })

    const tab = await owner.newPage()
    // 'commit' and not 'load': a page that escaped the sandbox may never
    // finish loading (a popup, a navigation), and the finding is what its
    // probes managed, not that the wait timed out.
    await tab.goto(`${BASE}/share/${link.token}/`, { waitUntil: 'commit' }).catch(() => {})
    await sleep(4000)
    const results = await tab.evaluate(() => window.__results ?? {}).catch(() => ({}))
    const policy = await tab.evaluate(() => window.__policy ?? []).catch(() => [])
    await tab.screenshot({ path: join(SHOTS, 'sandbox-probe.png') })

    const expect = (key, ok, why) => {
      const v = results[key]
      if (v === undefined) note('FAIL', `sandbox/${key}`, `the probe never reported (${why}); the page did not run`)
      else if (ok(v)) pass(`sandbox/${key}`, v)
      else note('FAIL', `sandbox/${key}`, `${v}: ${why}`)
    }
    expect('cookie', (v) => v.startsWith('threw:'), 'a page must not be able to read the session cookie')
    expect('storage', (v) => v.startsWith('threw:'), 'a page must have no origin to store under')
    expect('origin', (v) => v === 'null', 'the page must be in an opaque origin')
    expect('state', (v) => v.startsWith('refused:'), 'the panel API must not be readable from a page, cookie or not')
    expect('audit', (v) => v.startsWith('refused:'), 'the audit log must not be readable from a page')
    // A write may reach the server without being readable; it must not be
    // accepted. 401 (no cookie sent) or 403 (Origin refused) are both refusals.
    expect('write', (v) => v.startsWith('refused:') || v === 'status:0', 'a page must not be able to POST as the owner')
    expect('outside', (v) => v.startsWith('refused:'), 'connect-src must not reach another host')
    expect('socket', (v) => v !== 'opened', 'the terminal socket must not open from a page')
    expect('image', (v) => v === 'refused', 'img-src must not reach another host')
    expect('popup', (v) => v !== 'opened', 'a page must not open windows')
    expect('frame', (v) => v !== 'readable', 'a page must not frame the panel and read it')
    await sleep(500)
    if (outsideHits.length > 0) note('FAIL', 'sandbox/outside', `the outside listener was reached: ${outsideHits.join(', ')}`)
    else pass('sandbox/outside', 'no request reached another host')
    if (policy.length === 0) note('WARN', 'sandbox/policy', 'no policy violation was reported; were the probes refused for another reason?')

    // The write probe, from the server's side: nothing was created.
    const state = await must('GET', '/api/state')
    if ((state.sessions ?? []).length > 0) note('FAIL', 'sandbox/write', 'a session exists that the probe may have created')

    // Framed by the panel is the Preview's case; framed by anything else must not load.
    const framer = await owner.newPage()
    await framer.setContent(`<iframe id="x" src="${BASE}/share/${link.token}/"></iframe>`)
    await sleep(1500)
    const framed = framer.frames().find((f) => f.url().includes('/share/'))
    const framedTitle = framed ? await framed.title().catch(() => '') : ''
    if (framedTitle.startsWith('probe')) note('FAIL', 'sandbox/framing', 'a handed-out page loaded inside another site’s frame')
    else pass('sandbox/framing', 'a handed-out page refuses to be framed')
    await framer.close()
    await tab.close()
  }

  // ─── workflow ────────────────────────────────────────────────────────────
  if (only.includes('workflow')) {
    const ui = await owner.newPage()
    const consoleErrors = []
    ui.on('pageerror', (e) => consoleErrors.push(e.message))
    await ui.goto(BASE, { waitUntil: 'networkidle' })
    await ui.waitForSelector('[data-testid="sidebar"], [data-testid="sidebar-rail"]', { timeout: 15000 })

    // Made in settings, with an agent: here the Shell profile stands in for
    // one, because what is being checked is the hand-over, not Claude.
    await ui.locator('[data-testid="settings-open"]').click()
    await ui.locator('[data-testid="settings-group-sharing"]').click()
    await ui.waitForSelector('[data-testid="sharing"]', { timeout: 10000 })
    // No directory given: it goes under the data directory as page-<slug>,
    // not into somebody's home.
    const pageDir = join(work, 'data', 'pages', 'page-lobby')
    await ui.locator('[data-testid="page-new"]').click()
    await ui.locator('[data-testid="page-new-name"]').fill('Lobby')
    await ui.locator('[data-testid="page-new-template"]').selectOption('blank')
    await ui.screenshot({ path: join(SHOTS, 'workflow-settings.png') })
    await ui.locator('[data-testid="page-create"]').click()
    await ui.waitForSelector('[data-testid="launch-picker"]', { timeout: 10000 })
      .catch(() => note('FAIL', 'workflow/create', 'making a page with an agent did not offer the launch picker'))
    await ui.locator('[data-testid="launch-option"]', { hasText: 'Shell' }).first().click()
    await sleep(4500)

    if (!existsSync(join(pageDir, 'AGENTS.md')) || !existsSync(join(pageDir, 'fixtures', 'hostile.json'))) {
      note('FAIL', 'workflow/create', 'the page directory was not scaffolded')
    } else pass('workflow/create', 'scaffolded with AGENTS.md and fixtures')

    const state = await must('GET', '/api/state')
    const project = state.projects.find((p) => p.path === pageDir)
    const session = state.sessions.find((s) => project && s.projectId === project.id && !s.scratch)
    if (project && project.name === 'page-lobby') pass('workflow/create', 'the project is called page-lobby')
    else note('FAIL', 'workflow/create', `the page's project is called ${project?.name}`)
    if (!project || !session) {
      note('FAIL', 'workflow/create', 'no project and session were made for the page')
      throw new Error('cannot continue the workflow without a session')
    }
    const pane = () => execFileSync('tmux', ['-L', SOCKET, 'capture-pane', '-p', '-t', `=${session.tmuxName}:`]).toString()
    const firstPrompt = pane()
    if (/AGENTS\.md/.test(firstPrompt)) pass('workflow/prompt', 'the first line is at the prompt')
    else note('FAIL', 'workflow/prompt', `the first line did not reach the session:\n${firstPrompt.slice(-300)}`)
    if (/command not found|No such file/.test(firstPrompt)) {
      note('FAIL', 'workflow/prompt', 'the first line was sent, not typed: the shell ran it')
    }
    // Clear the typed line so later pastes are readable on their own.
    execFileSync('tmux', ['-L', SOCKET, 'send-keys', '-t', `=${session.tmuxName}:`, 'C-u'])

    // The Preview pane.
    const pages = await must('GET', '/api/settings/pages')
    const lobby = pages.find((p) => p.sourceDir === pageDir)
    // Opening a page from the settings opens its Preview by itself; the page
    // line above the file list is the way back to it otherwise.
    if (await ui.locator('[data-testid="page-frame"]').first().waitFor({ state: 'visible', timeout: 8000 }).then(() => true, () => false)) {
      pass('workflow/preview', 'the Preview opened beside the new page by itself')
    } else {
      await ui.locator('[data-testid="page-line"]').click({ timeout: 15000 })
        .catch(() => note('FAIL', 'workflow/preview', 'the Preview did not open and no page line appeared above the file list'))
    }
    const frameEl = ui.locator('[data-testid="page-frame"]').first()
    await frameEl.waitFor({ timeout: 15000 }).catch(() => note('FAIL', 'workflow/preview', 'no preview frame'))
    await sleep(3000)
    const frame = () => ui.frames().find((f) => f.url().includes('/share/'))
    const badge = await frame()?.locator('#status').getAttribute('data-vp-status').catch(() => null)
    if (badge === 'live') pass('workflow/preview', 'the draft is live in the pane')
    else note('FAIL', 'workflow/preview', `the preview's badge says ${badge}`)

    // Settle, then reload once: three writes in quick succession.
    let loads = 0
    ui.on('framenavigated', (f) => {
      if (f.url().includes('/share/')) loads++
    })
    const index = readFileSync(join(pageDir, 'index.html'), 'utf8')
    for (let i = 0; i < 3; i++) {
      writeFileSync(join(pageDir, 'index.html'), index.replace('<h1 id="title">vibepanel</h1>', `<h1 id="title">vibepanel</h1><p id="edit">${i}</p>`))
      await sleep(150)
    }
    await sleep(3000)
    if (loads === 1) pass('workflow/settle', 'three quick writes reloaded the frame once')
    else note('FAIL', 'workflow/settle', `three quick writes reloaded the frame ${loads} times`)

    // An error the page throws is on screen and in the directory.
    writeFileSync(join(pageDir, 'index.html'), index.replace('const vp = VibePanel.connect()', 'const vp = VibePanel.connect(); window.definitelyNotAFunction()'))
    await sleep(3500)
    const errorsText = await ui.locator('[data-testid="page-errors"]').innerText().catch(() => '')
    if (/definitelyNotAFunction/.test(errorsText)) pass('workflow/errors', 'the thrown error is listed under the frame')
    else note('FAIL', 'workflow/errors', `no error listed: ${JSON.stringify(errorsText)}`)
    await sleep(1500)
    const errorsFile = join(pageDir, '.vibepanel', 'errors.json')
    if (existsSync(errorsFile) && /definitelyNotAFunction/.test(readFileSync(errorsFile, 'utf8'))) {
      pass('workflow/errors', 'errors.json has it for the agent')
    } else note('FAIL', 'workflow/errors', 'errors.json does not have the error')

    // Pick: one line at the prompt, no Enter, whatever the element's text holds.
    writeFileSync(join(pageDir, 'index.html'), index.replace('<h1 id="title">vibepanel</h1>', '<h1 id="title">vibepanel</h1><p id="pickme">line one<br>rm -rf nowhere</p>'))
    await sleep(3500)
    // The pane pastes into the selected session; select it.
    await ui.locator('[data-testid="session-row"]', { hasText: /./ }).first().click().catch(() => {})
    await ui.locator('[data-testid="page-pick"]').click()
    await sleep(400)
    const target = frame()?.locator('#pickme')
    if (target) {
      await target.hover().catch(() => {})
      await target.click().catch(() => {})
    }
    await sleep(1500)
    const afterPick = pane()
    if (/\[index\.html · \d+×\d+\] .*#?pickme|p#pickme/.test(afterPick)) pass('workflow/pick', 'the picked element is typed at the prompt')
    else note('FAIL', 'workflow/pick', `nothing picked reached the prompt:\n${afterPick.slice(-300)}`)
    if (/nowhere: (command )?not found|No such file/.test(afterPick)) note('FAIL', 'workflow/pick', 'the picked text ran as a command')
    execFileSync('tmux', ['-L', SOCKET, 'send-keys', '-t', `=${session.tmuxName}:`, 'C-u'])
    writeFileSync(join(pageDir, 'index.html'), index)
    await sleep(3000)

    // Publish from the pane.
    await ui.locator('[data-testid="page-publish-open"]').click()
    await ui.locator('[data-testid="page-publish-note"]').fill('first')
    await ui.locator('[data-testid="page-publish-confirm"]').click()
    await sleep(1500)
    const afterPublish = await must('GET', `/api/settings/pages/${lobby.id}`)
    if (afterPublish.page.publishedVersion === 1) pass('workflow/publish', 'published v1 from the pane')
    else note('FAIL', 'workflow/publish', `published version is ${afterPublish.page.publishedVersion}`)

    // A wall on a link, with a parameter.
    const link = await must('POST', '/api/settings/shares', {
      name: 'hall', detail: 'counts', expiresIn: 3600, pageId: lobby.id, params: { title: 'Hall' },
      scope: '', scopeId: '', remark: '', locked: false,
    })
    const stranger = await browser.newContext({ viewport: { width: 1920, height: 1080 } })
    const wall = await stranger.newPage()
    let wallLoads = 0
    wall.on('load', () => wallLoads++)
    await wall.goto(`${BASE}/share/${link.token}`, { waitUntil: 'load' })
    await sleep(2500)
    const title = await wall.locator('#title').innerText().catch(() => '')
    if (title === 'Hall') pass('workflow/params', 'the link’s title parameter is drawn')
    else note('FAIL', 'workflow/params', `title is ${JSON.stringify(title)}`)

    // A parameter changes without a reload.
    const loadsBefore = wallLoads
    await must('PUT', `/api/settings/shares/${link.id}/page`, { pageId: lobby.id, pinVersion: 0, params: { title: 'Kitchen' } })
    await sleep(4000)
    const title2 = await wall.locator('#title').innerText().catch(() => '')
    if (title2 === 'Kitchen' && wallLoads === loadsBefore) pass('workflow/params', 'a new title arrived without a reload')
    else note('FAIL', 'workflow/params', `title ${JSON.stringify(title2)} after ${wallLoads - loadsBefore} reloads`)

    // A publish reaches the wall on its own.
    writeFileSync(join(pageDir, 'index.html'), index.replace('<h1 id="title">vibepanel</h1>', '<h1 id="title">vibepanel</h1><p id="v2">v2</p>'))
    await must('POST', `/api/settings/pages/${lobby.id}/publish`, { note: 'second' })
    await sleep(10000)
    if (await wall.locator('#v2').count()) pass('workflow/reload', 'the wall reloaded into v2 by itself')
    else note('FAIL', 'workflow/reload', 'the wall did not pick up the new version')

    // A trial goes on the wall and comes off it.
    writeFileSync(join(pageDir, 'index.html'), index.replace('<h1 id="title">vibepanel</h1>', '<h1 id="title">vibepanel</h1><p id="trial">trial</p>'))
    await sleep(1500)
    await ui.locator('[data-testid="page-trial-open"]').click({ timeout: 8000 }).catch(() => note('FAIL', 'workflow/trial', 'no trial control'))
    await ui.locator('[data-testid="page-trial-start"]').click({ timeout: 8000 }).catch(() => {})
    await sleep(10000)
    if (await wall.locator('#trial').count()) pass('workflow/trial', 'the wall shows the trial')
    else note('FAIL', 'workflow/trial', 'the wall did not reload into the trial')
    await ui.locator('[data-testid="page-trial-end"]').click({ timeout: 8000 }).catch(() => note('FAIL', 'workflow/trial', 'no revert control'))
    await sleep(10000)
    if ((await wall.locator('#trial').count()) === 0 && (await wall.locator('#v2').count())) pass('workflow/trial', 'reverting put v2 back')
    else note('FAIL', 'workflow/trial', 'the wall is not back on the published version')

    // Revoked: says so, and stops asking.
    await must('DELETE', `/api/settings/shares/${link.id}`)
    await sleep(5000)
    const gone = await wall.locator('#status').getAttribute('data-vp-status').catch(() => null)
    if (gone === 'revoked') pass('workflow/revoke', 'the wall says the link is gone')
    else note('FAIL', 'workflow/revoke', `the wall's status is ${gone}`)
    let polls = 0
    wall.on('request', (r) => {
      if (r.url().includes('/v1/snapshot')) polls++
    })
    await sleep(6000)
    if (polls === 0) pass('workflow/revoke', 'no polling after revocation')
    else note('FAIL', 'workflow/revoke', `${polls} polls after the link was revoked`)

    // The revoked address answers with a page of its own, not the panel.
    const dead = await stranger.newPage()
    const deadRes = await dead.goto(`${BASE}/share/${link.token}/`, { waitUntil: 'load' })
    const deadText = await dead.locator('body').innerText().catch(() => '')
    const spa = await dead.locator('#root').count()
    if (deadRes?.status() === 404 && /no longer works/.test(deadText) && spa === 0) {
      pass('workflow/gone', 'a dead link says so, without the panel\'s bundle')
    } else note('FAIL', 'workflow/gone', `status ${deadRes?.status()}, #root ${spa}: ${deadText.slice(0, 120)}`)

    // Links are listed under the page they show, and View opens a copy of one.
    const kept = await must('POST', '/api/settings/shares', {
      name: 'kitchen', detail: 'counts', expiresIn: 3600, pageId: lobby.id, params: { title: 'Kitchen' },
      scope: '', scopeId: '', remark: '', locked: false,
    })
    await ui.locator('[data-testid="settings-open"]').click()
    await ui.locator('[data-testid="settings-group-sharing"]').click()
    const row = ui.locator(`[data-testid="page-row"][data-page="${lobby.id}"] [data-testid="share-row"]`, { hasText: 'kitchen' })
    await row.waitFor({ timeout: 10000 }).catch(() => {})
    if (await row.count()) pass('workflow/settings', 'the link is listed under its page')
    else note('FAIL', 'workflow/settings', 'no row for the link under its page')
    // Nobody chose a pages directory, so none is stored and the default is said.
    const source = await ui.locator('[data-testid="pages-root-source"]').getAttribute('data-source').catch(() => null)
    const rootDir = await ui.locator('[data-testid="pages-root-dir"]').innerText().catch(() => '')
    if (source === 'default' && rootDir === join(work, 'data', 'pages')) pass('workflow/root', 'the pages directory is the default, and says so')
    else note('FAIL', 'workflow/root', `pages directory ${JSON.stringify(rootDir)} from ${source}`)

    // Out as a zip, back in as a new unpublished page.
    const zipRes = await owner.request.get(`${BASE}/api/settings/pages/${lobby.id}/export`)
    const zipBody = await zipRes.body()
    const imported = await owner.request.post(`${BASE}/api/settings/pages/import?name=Lobby%20copy`, {
      headers: { Origin: BASE, 'Content-Type': 'application/zip' }, data: zipBody,
    })
    const importedBody = await imported.json().catch(() => ({}))
    if (zipRes.status() === 200 && imported.status() === 201 && importedBody.page?.publishedVersion === 0 &&
      existsSync(join(importedBody.page.sourceDir, 'index.html'))) {
      pass('workflow/import', `exported ${zipBody.length} bytes and imported as ${importedBody.page.name}`)
    } else note('FAIL', 'workflow/import', `export ${zipRes.status()}, import ${imported.status()}: ${JSON.stringify(importedBody).slice(0, 200)}`)
    await ui.screenshot({ path: join(SHOTS, 'workflow-sharing.png') })
    const [peekTab] = await Promise.all([
      ui.context().waitForEvent('page', { timeout: 10000 }).catch(() => null),
      row.locator('[data-testid="share-view"]').click().catch(() => {}),
    ])
    if (peekTab) {
      await peekTab.waitForLoadState('load').catch(() => {})
      await sleep(2500)
      const peekTitle = await peekTab.locator('#title').innerText().catch(() => '')
      if (peekTitle === 'Kitchen') pass('workflow/view', 'View shows what the link shows')
      else note('FAIL', 'workflow/view', `the viewed page's title is ${JSON.stringify(peekTitle)}`)
      await peekTab.close()
    } else note('FAIL', 'workflow/view', 'View opened no tab')
    await ui.locator('[data-testid="settings-close"]').click().catch(() => {})
    await must('DELETE', `/api/settings/shares/${kept.id}`)

    // The error the check planted in the page is expected; anything else is not.
    const unexpected = consoleErrors.filter((m) => !m.includes('definitelyNotAFunction'))
    if (unexpected.length) note('WARN', 'workflow/console', unexpected.slice(0, 5).join(' | '))
    await ui.screenshot({ path: join(SHOTS, 'workflow-panel.png') })
    // The Preview with the window to itself, and its screens side by side.
    await ui.locator('[data-testid="detail-full-page"]').click().catch(() => {})
    await sleep(800)
    await ui.locator('[data-testid="page-screens"] button', { hasText: 'phone' }).click().catch(() => {})
    await sleep(2500)
    const frames = await ui.locator('[data-testid="page-frame"]').count()
    if (frames >= 2) pass('workflow/full', `${frames} screens side by side`)
    else note('FAIL', 'workflow/full', `the full Preview shows ${frames} screen(s) after adding one`)
    await ui.screenshot({ path: join(SHOTS, 'workflow-full.png') })

    // A page whose directory is gone comes back from its published version.
    rmSync(pageDir, { recursive: true, force: true })
    const reopened = await must('POST', `/api/settings/pages/${lobby.id}/open`, {})
    if (reopened.restored > 0 && existsSync(join(pageDir, 'index.html')) && reopened.projectId === project.id) {
      pass('workflow/restore', `v${reopened.restored} written back into the same directory and project`)
    } else note('FAIL', 'workflow/restore', JSON.stringify(reopened))
    await stranger.close()
    await ui.close()
  }

  // ─── templates ───────────────────────────────────────────────────────────
  if (only.includes('templates')) {
    const cat = await must('GET', '/api/settings/pages/catalogue')
    const SCREENS = { 'tv-1080': [1920, 1080], laptop: [1440, 900], phone: [390, 844] }
    for (const tpl of cat.templates) {
      const dir = join(work, 'templates', tpl.id)
      const page = await must('POST', '/api/settings/pages', { name: `tpl ${tpl.id}`, template: tpl.id, sourceDir: dir })
      const preview = await must('POST', `/api/settings/pages/${page.id}/preview`, { detail: 'names' })
      for (const [screen, [width, height]] of Object.entries(SCREENS)) {
        for (const fixture of ['busy', 'counts', 'hostile', 'empty']) {
          const where = `templates/${tpl.id}/${screen}/${fixture}`
          const ctx = await browser.newContext({ viewport: { width, height } })
          const tab = await ctx.newPage()
          const errors = []
          tab.on('pageerror', (e) => errors.push(e.message))
          tab.on('console', (m) => {
            if (m.type() === 'error') errors.push(m.text())
          })
          await tab.goto(`${BASE}/share/${preview.token}/?fixture=${fixture}`, { waitUntil: 'load' })
          await sleep(1500)
          const report = await tab.evaluate(() => {
            const bad = []
            const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT)
            let n
            while ((n = walker.nextNode())) {
              const p = n.parentNode
              if (p && (p.nodeName === 'SCRIPT' || p.nodeName === 'STYLE')) continue
              const text = (n.nodeValue ?? '').trim()
              if (text === 'null' || /\bNaN\b|\bundefined\b|\[object Object\]/.test(text)) bad.push(text.slice(0, 40))
            }
            // Built from code points so this script holds no invisible characters.
            const bidi = new RegExp(`[${String.fromCharCode(0x202a)}-${String.fromCharCode(0x202e)}${String.fromCharCode(0x2066)}-${String.fromCharCode(0x2069)}]`)
            return {
              bad,
              reordered: bidi.test(document.body.innerText),
              overflowX: document.documentElement.scrollWidth > innerWidth + 1,
              injected: document.querySelectorAll('img[onerror], h1[style]').length,
              badge: document.querySelector('[data-vp-status]')?.getAttribute('data-vp-status') ?? null,
            }
          })
          await tab.screenshot({ path: join(SHOTS, `${tpl.id}-${screen}-${fixture}.png`) })
          if (errors.length) note('FAIL', where, `errors: ${errors.slice(0, 3).join(' | ')}`)
          if (report.bad.length) note('FAIL', where, `on screen: ${report.bad.join(' | ')}`)
          if (report.overflowX) note('FAIL', where, 'wider than the screen')
          if (report.injected) note('FAIL', where, 'the hostile fixture’s markup became elements')
          if (report.reordered) note('FAIL', where, 'a bidi override from a name reached the screen')
          if (!report.badge) note('FAIL', where, 'no connection badge on screen')
          if (!errors.length && !report.bad.length && !report.overflowX && !report.injected && !report.reordered &&
            report.badge) pass(where, 'clean')
          await ctx.close()
        }
      }
    }
    if (outsideHits.length > 0) note('FAIL', 'templates/outside', `a template reached another host: ${outsideHits.join(', ')}`)
  }
} catch (e) {
  note('FAIL', 'pages-check', e.stack ?? String(e))
} finally {
  await browser?.close().catch(() => {})
  server?.kill('SIGTERM')
  outside.close()
  try {
    execFileSync('tmux', ['-L', SOCKET, 'kill-server'], { stdio: 'ignore' })
  } catch {
    /* no server left to kill */
  }
}

const fails = findings.filter((f) => f.sev === 'FAIL').length
const warns = findings.filter((f) => f.sev === 'WARN').length
console.log(`\nscreenshots: ${SHOTS}`)
console.log(`=== pages check: ${fails} FAIL, ${warns} WARN ===`)
process.exit(fails > 0 ? 1 : 0)
