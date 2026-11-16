// The file viewer, driven in a real browser against a real panel.
//
// docs/file-preview.md is the design and the batches. This check is born with
// batch 1, the shell: every fixture in scripts/fixtures/preview is opened at
// three widths in both themes and must land in the viewer with the kind it
// is, inside the window, with nothing thrown; the keys are pressed and each
// one's effect read back from the page; a 50,000 line file is opened and its
// first paint and scroll rate measured; a copy out of the body must be the
// file's lines and not its line numbers.
//
// Later batches add to this file rather than beside it: each one's drawing is
// a type in the same loop.
import { chromium } from 'playwright'
import { spawn, execSync } from 'node:child_process'
import { cpSync, mkdtempSync, mkdirSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:net'
import { tmpdir } from 'node:os'
import { join, relative } from 'node:path'

import { assertFreshBuild } from './lib/fresh.mjs'

const BIN = process.argv[2] ?? new URL('../../vibepanel', import.meta.url).pathname
assertFreshBuild(BIN, new URL('../../', import.meta.url).pathname)
const SHOTS = process.argv[3] ?? join(tmpdir(), 'vppreview-shots')
mkdirSync(SHOTS, { recursive: true })
const FIXTURES = new URL('./fixtures/preview/files/', import.meta.url).pathname

const PORT = await new Promise((resolve, reject) => {
  const probe = createServer()
  probe.once('error', reject)
  probe.listen(0, '127.0.0.1', () => {
    const { port } = probe.address()
    probe.close(() => resolve(port))
  })
})
const SOCKET = `vppreview-${process.pid}`
const DATA = mkdtempSync(join(tmpdir(), 'vppreview-'))
const FAKE_HOME = mkdtempSync(join(tmpdir(), 'vppreview-home-'))
const PROJ = join(DATA, 'fixtures')

const findings = []
const note = (sev, area, msg) => findings.push({ sev, area, msg })
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

// The large files, made here: committing megabytes of generated text buys
// nothing. Both bounds the server serves are reached -- 50,000 lines, and a
// file past 2 MB that is cut -- and one past the 8 MB preview limit.
cpSync(FIXTURES, PROJ, { recursive: true })
mkdirSync(join(PROJ, 'large'), { recursive: true })
const FIFTY = Array.from({ length: 50000 }, (_, i) => `line ${i + 1} of fifty thousand`)
writeFileSync(join(PROJ, 'large/fifty-thousand.log'), FIFTY.join('\n') + '\n')
writeFileSync(join(PROJ, 'large/three-megabytes.log'),
  Array.from({ length: 40000 }, (_, i) => `${i}: ${'x'.repeat(70)}`).join('\n') + '\n')
writeFileSync(join(PROJ, 'large/nine-megabytes.bin'), Buffer.alloc(9 * 1024 * 1024, 7))

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
const authed = async (path, init = {}) => {
  const res = await fetch(BASE + path, {
    ...init,
    headers: { 'Content-Type': 'application/json', ...(cookie ? { Cookie: cookie } : {}), ...(init.headers ?? {}) },
  })
  if (res.status === 404 || res.status === 405) {
    throw new Error(`${init.method ?? 'GET'} ${path} -> ${res.status}; no such route`)
  }
  return res
}

/**
 * What each fixture must open as. The kinds the viewer's `data-kind` says;
 * everything not named is text. Batch 1 has no video, audio, archive or hex
 * body yet, so those are the honest "cannot show" with the download, and the
 * line changes in the batch that draws them.
 */
const KIND = [
  // ICO and BMP are images the server does not sniff yet (batch 5).
  [/\.(bmp|ico)$/, 'none'],
  [/\.(png|jpe?g|gif|webp)$/, 'image'],
  [/^img\/icon\.svg$/, 'text'],
  [/\.pdf$/, 'pdf'],
  [/^large\/nine-megabytes\.bin$/, 'tooBig'],
  [/^(media|archives|bin)\//, 'none'],
  [/^encodings\/(latin1|utf16)\.txt$/, 'none'],
]
const kindOf = (f) => KIND.find(([re]) => re.test(f))?.[1] ?? 'text'

const VIEWER = '[role="dialog"][data-testid="file-viewer"]'

try {
  for (let i = 0; i < 120; i++) {
    try { if ((await fetch(BASE + '/api/health')).ok) break } catch { /* not up */ }
    await sleep(150)
  }
  const token = /one-time setup token:\s*\n\s*\n\s*(\S+)/.exec(serverLog)?.[1]
  if (!token) throw new Error(`no setup token:\n${serverLog}`)
  const setupRes = await authed('/api/auth/setup', {
    method: 'POST',
    body: JSON.stringify({ token, username: 'preview', password: 'a sufficiently long password' }),
  })
  cookie = (setupRes.headers.getSetCookie?.() ?? []).map((c) => c.split(';')[0]).join('; ')
  // The first-run tour sits over everything this check clicks.
  await authed('/api/settings/tour', { method: 'POST' })
  const project = await (await authed('/api/projects', {
    method: 'POST', body: JSON.stringify({ path: PROJ, name: 'fixtures' }),
  })).json()
  await authed('/api/sessions', { method: 'POST', body: JSON.stringify({ projectId: project.id, command: ['sh'] }) })

  const files = []
  const walk = (dir) => {
    for (const n of readdirSync(dir).sort()) {
      const p = join(dir, n)
      if (statSync(p).isDirectory()) walk(p)
      else files.push(relative(PROJ, p))
    }
  }
  walk(PROJ)

  browser = await chromium.launch()
  const contextFor = async ({ width, height, scheme, phone }) => {
    const ctx = await browser.newContext({
      viewport: { width, height }, colorScheme: scheme, isMobile: !!phone, hasTouch: !!phone,
      permissions: ['clipboard-read', 'clipboard-write'], acceptDownloads: true,
    })
    await ctx.addCookies(cookie.split('; ').map((c) => {
      const [name, ...v] = c.split('=')
      return { name, value: v.join('='), url: BASE }
    }))
    return ctx
  }

  /** Opens the Files tab at the root, on whatever the layout calls it. */
  const filesAtRoot = async (page) => {
    await page.goto(BASE, { waitUntil: 'networkidle' })
    // The side panel is a column on a desktop, a sheet behind `right-show` on
    // a tablet, and a sheet behind the menu on a phone.
    const tab = page.locator('[data-testid="panel-tab-files"]:visible')
    if (!(await tab.count())) {
      if (await page.locator('[data-testid="right-show"]:visible').count()) {
        await page.locator('[data-testid="right-show"]').click()
      } else {
        await page.locator('[data-testid="menu-button"]').click()
        await page.locator('[data-testid="open-panel-sheet"]').click()
      }
    }
    await tab.first().click()
    await page.locator('[data-testid="file-tree"]:visible').first().waitFor({ timeout: 10000 })
  }
  // A row's title is the path through safeText, so a bidi override in a name
  // is U+FFFD there; the selector has to say what the row says.
  const shown = (p) => p.replace(/[\u0000-\u001F\u007F-\u009F\u00AD\u202A-\u202E\u2066-\u2069\u200E\u200F\u200B\u2060\uFEFF\u061C]/g, '\uFFFD')
  const openFile = async (page, f) => {
    const parts = f.split('/')
    for (let i = 0; i < parts.length - 1; i++) {
      const dir = parts.slice(0, i + 1).join('/')
      await page.locator(`[data-testid="file-entry"][title="${shown(dir)}"] [data-testid="file-open"]`).click()
    }
    await page.locator(`[data-testid="file-entry"][title="${shown(f)}"] [data-testid="file-open"]`).click()
    const v = page.locator(VIEWER)
    await v.waitFor({ timeout: 10000 })
    await page.waitForFunction((sel) => {
      const k = document.querySelector(`${sel} [data-testid="viewer-content"]`)?.dataset.kind
      return k && k !== 'loading'
    }, VIEWER, { timeout: 15000 })
    return v
  }

  // ── every fixture, at three widths, in both themes ───────────────────────
  // PREVIEW_QUICK=1: one shape and the sample, for working on the sections
  // after this loop. Never what `make preview-check` runs.
  const QUICK = process.env.PREVIEW_QUICK === '1'
  const ALL_SHAPES = [
    { name: '1280-light', width: 1280, height: 800, scheme: 'light' },
    { name: '1280-dark', width: 1280, height: 800, scheme: 'dark' },
    { name: '768-light', width: 768, height: 1024, scheme: 'light' },
    { name: '768-dark', width: 768, height: 1024, scheme: 'dark' },
    { name: '320-light', width: 320, height: 640, scheme: 'light', phone: true },
    { name: '320-dark', width: 320, height: 640, scheme: 'dark', phone: true },
  ]
  const SHAPES = QUICK
    ? ALL_SHAPES.filter((x) => x.name === (process.env.PREVIEW_SHAPE ?? '1280-light'))
    : ALL_SHAPES
  // Every file at the widest; the narrower shapes take one of each kind and the
  // awkward names, which is where a layout breaks.
  const SAMPLE = files.filter((f) =>
    /README|src\/app\.ts|config\.json|table\.csv|table\.tsv|huge\.png|manual\.pdf|fifty-thousand|nine-mega|中文|very-long|minified|page\.html|bundle\.zip/.test(f))
  let opened = 0
  for (const shape of SHAPES) {
    const ctx = await contextFor(shape)
    const page = await ctx.newPage()
    const errors = []
    page.on('pageerror', (e) => errors.push(String(e)))
    page.on('console', (m) => {
      if (m.type() !== 'error') return
      const text = m.text()
      // The sandbox refusing a page's script is the sandbox working; the page
      // section below is what checks it.
      if (text.startsWith('Blocked script execution')) return
      // The server answers 415 for a file with no preview, and the browser
      // logs every 4xx. Batch 4 gives those files a hex body and a 200.
      if (text.includes('status of 415')) return
      errors.push(text)
    })
    const list = shape.name === '1280-light' && !QUICK ? files : SAMPLE
    for (const f of list) {
      errors.length = 0
      try {
        await filesAtRoot(page)
        const v = await openFile(page, f)
        const kind = await v.locator('[data-testid="viewer-content"]').getAttribute('data-kind')
        const want = kindOf(f)
        if (kind !== want) note('FAIL', 'kinds', `${shape.name} ${f}: opened as ${kind}, want ${want}`)
        // The plain body, where there is one, must be the file's lines. The
        // preview this replaced ran a one-line sanitiser over whole files and
        // drew every newline and tab as U+FFFD -- every log one run-on line.
        // Only a file that really holds a control character may show one.
        const body = v.locator('[data-testid="viewer-text"]')
        if (await body.count()) {
          // textContent, not innerText: innerText leaves out a block the
          // browser has not laid out, which is every block below the first
          // screen, and a check that reads only the rows on screen passed with
          // the body's sanitiser swapped for the one that eats tabs.
          const shown = await body.evaluate((el) => el.textContent)
          if (shown.includes('\uFFFD') && !/controls/.test(f)) {
            note('FAIL', 'text', `${shape.name} ${f}: drawn with U+FFFD in it: ${JSON.stringify(shown.slice(0, 80))}`)
          }
          const firstLine = readFileSync(join(PROJ, f), 'utf8').split(/\r\n|\n|\r/)[0]
          if (firstLine && !/controls|bom/.test(f) && !shown.includes(firstLine.slice(0, 40))) {
            note('FAIL', 'text', `${shape.name} ${f}: the first line ${JSON.stringify(firstLine.slice(0, 40))} is not on screen`)
          }
        }
        const geo = await page.evaluate((sel) => {
          const d = document.querySelector(sel).getBoundingClientRect()
          return {
            inside: d.left >= -1 && d.top >= -1 && d.right <= innerWidth + 1 && d.bottom <= innerHeight + 1,
            pageScrollsSideways: document.documentElement.scrollWidth > innerWidth + 1,
            headerOverflows: [...document.querySelectorAll(`${sel} header > div`)].some((r) => r.scrollWidth > r.clientWidth + 1),
            // Where there is room it floats, centred: a container query on the
            // backdrop's own classes never fired and it sat against the top.
            // Every segment of the trail that is shown is shown whole: two of
            // them on a phone once shrank to a letter each (`f/r/`).
            crumbsWhole: [...document.querySelectorAll(`${sel} [data-testid="viewer-crumb"]`)]
              .filter((b) => b.getClientRects().length)
              .every((b) => b.scrollWidth <= b.clientWidth + 1),
            // A phone keeps the trail's last two segments (the design): the
            // directory the file is in, and the name.
            phoneCrumbs: innerWidth >= 440 || [...document.querySelectorAll(`${sel} [data-testid="viewer-crumb"]`)]
              .filter((b) => b.getClientRects().length).length <= 1,
            // By layout, not by the drawn box: the dialog scales in, and a
            // file answered from its size alone (the 9 MB one) is measured
            // while the transform is still moving it, which flapped.
            centred: innerWidth < 700 || (() => {
              const el = document.querySelector(sel)
              return Math.abs(el.offsetTop + el.offsetHeight / 2 - innerHeight / 2) < 2
            })(),
            // A gutter number that does not fit runs into its line.
            gutterFits: [...document.querySelectorAll(`${sel} [data-line] > span[aria-hidden]`)].slice(0, 50)
              .every((g) => g.scrollWidth <= g.clientWidth && g.getBoundingClientRect().right <= g.nextElementSibling.getBoundingClientRect().left + 0.5),
          }
        }, VIEWER)
        if (!geo.inside) note('FAIL', 'layout', `${shape.name} ${f}: the viewer is not inside the window`)
        if (geo.pageScrollsSideways) note('FAIL', 'layout', `${shape.name} ${f}: the page scrolls sideways`)
        if (geo.headerOverflows) note('FAIL', 'layout', `${shape.name} ${f}: a header row is wider than the viewer`)
        if (!geo.crumbsWhole) note('FAIL', 'layout', `${shape.name} ${f}: a breadcrumb segment is cut short`)
        if (!geo.phoneCrumbs) note('FAIL', 'layout', `${shape.name} ${f}: more than the file's directory in the trail on a phone`)
        if (!geo.centred) note('FAIL', 'layout', `${shape.name} ${f}: the viewer is not centred in the window`)
        if (!geo.gutterFits) note('FAIL', 'layout', `${shape.name} ${f}: a line number runs into its line`)
        if (errors.length) note('FAIL', 'errors', `${shape.name} ${f}: ${errors[0].slice(0, 200)}`)
        if (shape.name === '1280-light' || SAMPLE.includes(f)) {
          await page.screenshot({ path: join(SHOTS, `${shape.name}--${f.replace(/[/ ]/g, '_')}.png`) })
        }
        await page.keyboard.press('Escape')
        await page.locator(VIEWER).waitFor({ state: 'detached', timeout: 3000 })
          .catch(() => note('FAIL', 'keys', `${shape.name} ${f}: Escape did not close the viewer`))
        opened++
      } catch (e) {
        note('FAIL', 'open', `${shape.name} ${f}: ${String(e).split('\n')[0]}`)
        await page.screenshot({ path: join(SHOTS, `${shape.name}--ERR--${f.replace(/[/ ]/g, '_')}.png`) }).catch(() => {})
      }
    }
    await ctx.close()
  }
  note('PASS', 'kinds', `${opened} opens across ${SHAPES.length} shapes, each in the kind it is`)

  // ── the keys ─────────────────────────────────────────────────────────────
  {
    const ctx = await contextFor({ width: 1280, height: 800, scheme: 'light' })
    const page = await ctx.newPage()
    await filesAtRoot(page)
    let v = await openFile(page, 'src/tool.py')
    // Focus starts in the viewer, so a key works without a click first.
    const focusInside = await page.evaluate((sel) => document.querySelector(sel).contains(document.activeElement), VIEWER)
    if (!focusInside) note('FAIL', 'keys', 'the focus is not in the viewer when it opens')

    // Code is the line body with colour; `s` is the same body without.
    await page.waitForFunction((sel) => document.querySelector(`${sel} [data-testid="viewer-text"]`)?.dataset.lang === 'python', VIEWER, { timeout: 10000 }).catch(() => {})
    await page.keyboard.press('s')
    if ((await v.locator('[data-testid="viewer-text"]').getAttribute('data-lang')) !== 'plain') {
      note('FAIL', 'keys', '`s` did not switch a .py file to its source')
    }

    const wrapBefore = await v.locator('[data-testid="viewer-text"]').getAttribute('data-wrap')
    await page.keyboard.press('w')
    const wrapAfter = await v.locator('[data-testid="viewer-text"]').getAttribute('data-wrap')
    if (wrapBefore === wrapAfter) note('FAIL', 'keys', '`w` did not toggle wrap')
    await page.keyboard.press('w')
    const numbersBefore = await v.locator('[data-testid="viewer-text"]').getAttribute('data-numbers')
    await page.keyboard.press('n')
    if ((await v.locator('[data-testid="viewer-text"]').getAttribute('data-numbers')) === numbersBefore) {
      note('FAIL', 'keys', '`n` did not toggle line numbers')
    }
    await page.keyboard.press('n')

    // Find: open, type, step, close, and Escape closes find before the viewer.
    await page.keyboard.press('Control+f')
    const box = v.locator('[data-testid="viewer-find"]')
    if (!(await box.isVisible())) note('FAIL', 'keys', 'Ctrl+F did not open find')
    await box.fill('a')
    await page.keyboard.press('Enter')
    const first = await v.locator('mark[data-current="true"]').getAttribute('data-match')
    await page.keyboard.press('Enter')
    const second = await v.locator('mark[data-current="true"]').getAttribute('data-match')
    await page.keyboard.press('Shift+Enter')
    const back = await v.locator('mark[data-current="true"]').getAttribute('data-match')
    if (first === null || second === first || back !== first) {
      note('FAIL', 'find', `Enter and Shift+Enter did not step: ${first} -> ${second} -> ${back}`)
    }
    const count = await v.locator('[data-testid="viewer-find-count"]').textContent()
    if (!/\d/.test(count ?? '')) note('FAIL', 'find', `no count: ${JSON.stringify(count)}`)
    await page.keyboard.press('Escape')
    if (!(await page.locator(VIEWER).isVisible())) note('FAIL', 'keys', 'Escape in find closed the viewer instead of find')
    if (await v.locator('[data-testid="viewer-findbar"]').count()) note('FAIL', 'keys', 'Escape did not close find')

    // Previous and next in the directory, in the tree's order.
    // tool.py is the last file in src/, so back first and then forward.
    await page.keyboard.press('[')
    const prevName = await page.waitForFunction((sel) => {
      const l = document.querySelector(sel)?.getAttribute('aria-label')
      return l && l !== 'tool.py' ? l : null
    }, VIEWER, { timeout: 3000 }).then((h) => h.jsonValue()).catch(() => null)
    if (prevName !== 'style.css') note('FAIL', 'keys', `\`[\` from tool.py opened ${JSON.stringify(prevName)}, want style.css`)
    await page.keyboard.press(']')
    await page.waitForFunction((sel) => document.querySelector(sel)?.getAttribute('aria-label') === 'tool.py', VIEWER, { timeout: 3000 })
      .catch(() => note('FAIL', 'keys', '`]` did not come back to tool.py'))
    // And at the end of the directory, `]` stays.
    await page.keyboard.press(']')
    await sleep(300)
    if ((await page.locator(VIEWER).getAttribute('aria-label')) !== 'tool.py') note('FAIL', 'keys', '`]` past the last file moved')
    // Moving between files keeps the focus in the viewer, and closing after
    // that still gives it back to the tree. The dialog is mounted once per
    // visit for this: remounted per file, the focus went to the tree mid-visit
    // and had nowhere to go back to at the end.
    const inViewer = await page.evaluate((sel) => document.querySelector(sel).contains(document.activeElement), VIEWER)
    if (!inViewer) note('FAIL', 'keys', 'after [ and ] the focus is not in the viewer')
    await page.keyboard.press('Escape')
    const toTree = await page.evaluate(() => !!document.activeElement?.closest('[data-testid="file-entry"]'))
    if (!toTree) note('FAIL', 'keys', 'closing after [ and ] left the focus nowhere in the tree')
    await filesAtRoot(page)
    v = await openFile(page, 'src/tool.py')

    // Copy the path, and download.
    await page.keyboard.press('c')
    const clip = await page.evaluate(() => navigator.clipboard.readText()).catch(() => '')
    if (clip !== 'src/tool.py') note('FAIL', 'keys', `\`c\` copied ${JSON.stringify(clip)}`)
    const [dl] = await Promise.all([page.waitForEvent('download', { timeout: 5000 }).catch(() => null), page.keyboard.press('d')])
    if (!dl || dl.suggestedFilename() !== 'tool.py') note('FAIL', 'keys', '`d` did not download the file')

    // The breadcrumb moves the tree, and closes the viewer.
    v = page.locator(VIEWER)
    await v.locator('[data-testid="viewer-crumb"][data-path=""]').click()
    await page.locator(VIEWER).waitFor({ state: 'detached', timeout: 3000 })
      .catch(() => note('FAIL', 'crumbs', 'the root segment did not close the viewer'))
    await page.locator('[data-testid="file-entry"][title="src"]').waitFor({ timeout: 5000 })
      .catch(() => note('FAIL', 'crumbs', 'the root segment did not move the tree to the root'))
    const rootName = await page.evaluate(() => [...document.querySelectorAll('[data-testid="viewer-crumb"]')].map((b) => b.textContent))
    if (rootName.length) note('FAIL', 'crumbs', 'crumbs left behind')

    // Escape gives the focus back to the row it came from.
    await openFile(page, 'README.md')
    await page.keyboard.press('Escape')
    const back2 = await page.evaluate(() => document.activeElement?.closest('[data-testid="file-entry"]')?.getAttribute('title'))
    if (back2 !== 'README.md') note('FAIL', 'keys', `focus went to ${JSON.stringify(back2)} after Escape, not the README row`)
    note('PASS', 'keys', 'Ctrl+F, Enter, Shift+Enter, Esc, [, ], w, n, s, c, d and the breadcrumb each did what the table says')
    await ctx.close()
  }

  // ── a project's own page, in the sandbox ─────────────────────────────────
  // The preview this replaced asked the highlighter first, which claims .html
  // and .svg, so the sandboxed frame was never reached and "Page" was the
  // source in colour. Scripts start off on every file and the switch is what
  // turns them on.
  {
    const ctx = await contextFor({ width: 1280, height: 800, scheme: 'light' })
    const page = await ctx.newPage()
    for (const f of ['page.html', 'img/icon.svg']) {
      await filesAtRoot(page)
      const v = await openFile(page, f)
      const frame = v.locator('[data-testid="preview-frame"]')
      if (!(await frame.count())) {
        note('FAIL', 'page', `${f} did not open in the sandboxed frame`)
        continue
      }
      if ((await frame.getAttribute('data-scripts')) !== 'off') note('FAIL', 'page', `${f} opened with scripts on`)
      if (f === 'page.html') {
        const text = async () => (await frame.contentFrame()).locator('#p').textContent().catch(() => null)
        await sleep(500)
        const off = await text()
        await v.locator('[data-testid="preview-scripts"]').click()
        await sleep(800)
        const on = await text()
        if (off !== 'static text' || on !== 'scripts ran') {
          note('FAIL', 'page', `scripts off read ${JSON.stringify(off)}, on read ${JSON.stringify(on)}`)
        }
      }
      await page.keyboard.press('Escape')
    }
    note('PASS', 'page', 'a project\'s HTML and SVG open in the sandbox, scripts off until switched on')
    await ctx.close()
  }

  // ── code and diffs ───────────────────────────────────────────────────────
  // Batch 2: code is the line body with a grammar, so find and go to line work
  // in it; Source is the same body without colour; a diff's rows are tinted
  // and keep their signs. Prism is asked for tokens and never for HTML: a
  // `.token` element anywhere would be its HTML output in the page.
  {
    const ctx = await contextFor({ width: 1280, height: 800, scheme: 'dark' })
    const page = await ctx.newPage()
    // Each grammar is its own chunk, fetched when a file in it is opened: a
    // .ts file fetches the TypeScript grammar and nothing else (its base,
    // JavaScript, is in Prism's core). A loader rewritten as a template string,
    // or a static import, would fetch many or all of them at once.
    const grammarChunks = []
    page.on('request', (r) => {
      const m = /\/assets\/(prism-[a-z0-9-]+?)-[A-Za-z0-9_-]+\.js$/.exec(r.url())
      if (m) grammarChunks.push(m[1])
    })
    await filesAtRoot(page)
    if (grammarChunks.length) note('FAIL', 'code', `grammars fetched before any file was opened: ${grammarChunks.join(', ')}`)
    let v = await openFile(page, 'src/app.ts')
    const body = v.locator('[data-testid="viewer-text"]')
    await page.waitForFunction((sel) => document.querySelector(`${sel} [data-testid="viewer-text"]`)?.dataset.lang === 'typescript', VIEWER, { timeout: 10000 })
      .catch(() => note('FAIL', 'code', 'app.ts never coloured as typescript'))
    const kinds = await body.evaluate((el) => [...new Set([...el.querySelectorAll('[data-tok]')].map((t) => t.dataset.tok))])
    if (grammarChunks.join(',') !== 'prism-typescript') {
      note('FAIL', 'code', `opening a .ts file fetched ${grammarChunks.length} grammar chunks: ${grammarChunks.join(', ')}`)
    }
    for (const want of ['keyword', 'string', 'comment']) {
      if (!kinds.includes(want)) note('FAIL', 'code', `app.ts has no ${want} token: ${kinds.join(',')}`)
    }
    // Colours are the theme's: a keyword is not the ink.
    const [kw, ink] = await body.evaluate((el) => [
      getComputedStyle(el.querySelector('[data-tok="keyword"]')).color,
      getComputedStyle(el).color,
    ])
    if (kw === ink) note('FAIL', 'code', `a keyword is drawn in the ink colour (${kw})`)
    // Find works in code, and a match inside a token keeps the token's colour.
    await page.keyboard.press('Control+f')
    await v.locator('[data-testid="viewer-find"]').fill('xport')
    await page.keyboard.press('Enter')
    const inside = await v.locator('mark[data-current="true"] [data-tok="keyword"]').count()
    if (!inside) note('FAIL', 'code', 'a match inside `export` lost the keyword colour')
    await page.keyboard.press('Escape')
    // Source is the same lines, no colour.
    await page.keyboard.press('s')
    const plainLang = await body.getAttribute('data-lang')
    const plainToks = await body.locator('[data-tok]').count()
    if (plainLang !== 'plain' || plainToks) note('FAIL', 'code', `Source is still coloured (${plainLang}, ${plainToks} tokens)`)
    await page.keyboard.press('Escape')

    // A diff.
    await filesAtRoot(page)
    v = await openFile(page, 'changes.diff')
    await page.waitForFunction((sel) => document.querySelector(`${sel} [data-testid="viewer-text"]`)?.dataset.lang === 'diff', VIEWER, { timeout: 10000 })
      .catch(() => note('FAIL', 'diff', 'changes.diff never coloured as a diff'))
    const rows = await v.evaluate((d) => [...d.querySelectorAll('[data-line]')].map((r) => ({
      text: r.textContent.replace(/^\d+/, ''),
      bg: getComputedStyle(r).backgroundColor,
    })))
    const added = rows.find((r) => r.text.startsWith('+const'))
    const context = rows.find((r) => r.text.startsWith(' import'))
    if (!added || !context || added.bg === context.bg) {
      note('FAIL', 'diff', `an added row is not tinted apart from context: ${JSON.stringify([added, context])}`)
    }
    if (added && !added.text.startsWith('+')) note('FAIL', 'diff', 'an added row lost its sign')
    await page.keyboard.press('Escape')

    // A fenced block in a document, through the same grammars.
    await filesAtRoot(page)
    v = await openFile(page, 'README.md')
    await page.waitForFunction((sel) => !!document.querySelector(`${sel} [data-testid="preview-code"] [class*="--vp-tok-keyword"]`), VIEWER, { timeout: 10000 })
      .catch(() => note('FAIL', 'code', 'the README\'s ```ts block is not coloured'))
    await page.keyboard.press('Escape')

    // Told not to highlight the page itself (viewer/prismSetup.ts). Read here
    // because in node Prism keeps its settings where no test can set them.
    const manual = await page.evaluate(() => window.Prism?.manual)
    if (manual !== true) note('FAIL', 'code', `Prism.manual is ${manual}: it will write its HTML into any language-* element`)
    const prismHtml = await page.evaluate(() => document.querySelectorAll('.token').length)
    if (prismHtml) note('FAIL', 'code', `${prismHtml} elements with Prism's own .token class: its HTML is in the page`)
    note('PASS', 'code', `code coloured by kind (${kinds.join(', ')}), find inside tokens, Source plain, a diff tinted with its signs, a fence coloured, no Prism HTML`)
    await ctx.close()
  }

  // ── 50,000 lines ─────────────────────────────────────────────────────────
  {
    const ctx = await contextFor({ width: 1280, height: 800, scheme: 'light' })
    const page = await ctx.newPage()
    await filesAtRoot(page)
    await page.locator('[data-testid="file-entry"][title="large"] [data-testid="file-open"]').click()
    const t0 = Date.now()
    await page.locator('[data-testid="file-entry"][title="large/fifty-thousand.log"] [data-testid="file-open"]').click()
    await page.locator(`${VIEWER} [data-line="0"]`).waitFor({ timeout: 15000 })
    const firstPaint = Date.now() - t0
    const v = page.locator(VIEWER)
    const shape = await v.evaluate((d) => ({
      nodes: d.querySelectorAll('*').length,
      lines: d.querySelector('[data-testid="viewer-lines"]')?.textContent,
    }))
    if (firstPaint > 2500) note('FAIL', 'large', `50,000 lines took ${firstPaint} ms to the first line`)
    if (shape.nodes > 5000) note('FAIL', 'large', `${shape.nodes} elements for a file nobody has scrolled: the blocks are not one node each`)
    if (!/50,000|50000/.test(shape.lines ?? '')) note('FAIL', 'large', `footer says ${JSON.stringify(shape.lines)}`)

    // Scrolling: frames painted while the body moves a screen per frame.
    const fps = await v.locator('[data-testid="viewer-text"]').evaluate(async (el) => {
      let frames = 0
      const start = performance.now()
      await new Promise((done) => {
        const tick = () => {
          frames++
          el.scrollTop += el.clientHeight
          if (performance.now() - start < 2000) requestAnimationFrame(tick)
          else done()
        }
        requestAnimationFrame(tick)
      })
      return frames / ((performance.now() - start) / 1000)
    })
    if (fps < 30) note('FAIL', 'large', `scrolling 50,000 lines ran at ${fps.toFixed(1)} frames a second`)

    // Go to line, across blocks nobody has seen.
    await page.keyboard.press('Control+g')
    await v.locator('[data-testid="viewer-goto"]').fill('40000')
    await page.keyboard.press('Enter')
    const landed = await page.waitForFunction(() => {
      const row = document.querySelector('[data-line="39999"]')
      if (!row) return false
      const r = row.getBoundingClientRect()
      return r.top >= 0 && r.bottom <= innerHeight
    }, null, { timeout: 5000 }).then(() => true).catch(() => false)
    if (!landed) note('FAIL', 'large', 'go to line 40000 did not put line 40000 on screen')

    // Find across all of it, including blocks never drawn as rows.
    await page.keyboard.press('Control+f')
    await v.locator('[data-testid="viewer-find"]').fill('line 49999 of')
    await page.keyboard.press('Enter')
    const found = await page.waitForFunction(() => {
      const m = document.querySelector('mark[data-current="true"]')
      return m && m.closest('[data-line]')?.getAttribute('data-line') === '49998'
    }, null, { timeout: 5000 }).then(() => true).catch(() => false)
    if (!found) note('FAIL', 'large', 'find did not reach line 49999')

    // A copy is the lines, not their numbers.
    const copied = await v.locator('[data-testid="viewer-text"]').evaluate((el) => {
      const row = el.querySelector('[data-line="39999"]')
      const sel = getSelection()
      sel.removeAllRanges()
      const r = document.createRange()
      r.selectNode(row)
      sel.addRange(r)
      return sel.toString()
    })
    if (copied.trim() !== FIFTY[39999]) note('FAIL', 'copy', `a selected row copies as ${JSON.stringify(copied)}`)
    note('PASS', 'large', `50,000 lines: first line in ${firstPaint} ms, ${shape.nodes} elements, ${fps.toFixed(0)} fps scrolling; go to line, find and copy across unseen blocks`)

    // The cut file says how much of it is on screen.
    await filesAtRoot(page)
    await openFile(page, 'large/three-megabytes.log')
    const cut = await page.locator('[data-testid="preview-truncated"]').textContent().catch(() => null)
    if (!cut || !/2(\.0)?M/.test(cut)) note('FAIL', 'large', `a file past 2 MB says ${JSON.stringify(cut)} about being cut`)
    await ctx.close()
  }
} catch (e) {
  note('FAIL', 'harness', String(e?.stack ?? e))
  for (const ctx of browser?.contexts() ?? []) {
    for (const [i, p] of ctx.pages().entries()) await p.screenshot({ path: join(SHOTS, `harness-${i}.png`) }).catch(() => {})
  }
} finally {
  await cleanup()
}

const fails = findings.filter((f) => f.sev === 'FAIL').length
const warns = findings.filter((f) => f.sev === 'WARN').length
console.log(`=== preview check: ${fails} FAIL, ${warns} WARN ===`)
for (const f of findings) console.log(`[${f.sev}] ${f.area}: ${f.msg}`)
process.exit(fails ? 1 : 0)
