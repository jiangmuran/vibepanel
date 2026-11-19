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
// A minified bundle: most of two megabytes on one line, short enough that
// the server's 2 MB cut leaves its end -- where find has to reach -- in.
writeFileSync(join(PROJ, 'large/bundle.min.js'), 'var a=1;'.repeat(240 * 1024) + 'var needle_at_the_end=2;\n')
// Two hundred pages, for what a long PDF costs: the first page's paint, and
// how many pages hold a canvas once all of them have been scrolled past.
writeFileSync(join(PROJ, 'large/two-hundred-pages.pdf'), longPdf(200))
// One entry past the archive listing's bound: five megabytes of headers, so
// made here rather than committed.
writeFileSync(join(PROJ, 'large/ten-thousand-and-one.tar'), emptyTar(10001))

/**
 * A PDF of `n` pages, a line at the top of each and one at the bottom,
 * written by hand as make.py does. The bottom line is for find: a match low
 * on a page far from the view is where bringing the page into view is not
 * enough, and the match itself has to be centred once the page is drawn.
 */
/** A ustar archive of n empty files, `f00001` and on. */
function emptyTar(n) {
  const out = Buffer.alloc((n + 2) * 512)
  for (let i = 0; i < n; i++) {
    const h = out.subarray(i * 512, i * 512 + 512)
    h.write(`f${String(i + 1).padStart(5, '0')}`, 0, 'ascii')
    h.write('0000644\0', 100, 'ascii')
    h.write('0000000\0', 108, 'ascii')
    h.write('0000000\0', 116, 'ascii')
    h.write('00000000000\0', 124, 'ascii')
    h.write('14524770400\0', 136, 'ascii')
    h.write('        ', 148, 'ascii')
    h.write('0', 156, 'ascii')
    h.write('ustar\0' + '00', 257, 'ascii')
    let sum = 0
    for (const b of h) sum += b
    h.write(sum.toString(8).padStart(6, '0') + '\0 ', 148, 'ascii')
  }
  return out
}

function longPdf(n) {
  const objs = []
  const obj = (body) => objs.push(body)
  obj('<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>')
  const pagesId = 2 + 2 * n
  for (let i = 1; i <= n; i++) {
    const ops = `BT /F1 24 Tf 72 720 Td (Long PDF, page ${i} of ${n}) Tj ET BT /F1 12 Tf 72 60 Td (the bottom of page ${i}.) Tj ET`
    obj(`<< /Length ${ops.length} >>\nstream\n${ops}\nendstream`)
    obj(`<< /Type /Page /Parent ${pagesId} 0 R /MediaBox [0 0 612 792] /Contents ${objs.length} 0 R /Resources << /Font << /F1 1 0 R >> >> >>`)
  }
  const kids = Array.from({ length: n }, (_, i) => `${3 + 2 * i} 0 R`).join(' ')
  obj(`<< /Type /Pages /Kids [${kids}] /Count ${n} >>`)
  obj(`<< /Type /Catalog /Pages ${pagesId} 0 R >>`)
  let out = '%PDF-1.4\n'
  const offsets = []
  objs.forEach((body, i) => {
    offsets.push(out.length)
    out += `${i + 1} 0 obj\n${body}\nendobj\n`
  })
  const xref = out.length
  out += `xref\n0 ${objs.length + 1}\n0000000000 65535 f \n`
  for (const o of offsets) out += `${String(o).padStart(10, '0')} 00000 n \n`
  out += `trailer\n<< /Size ${objs.length + 1} /Root ${objs.length} 0 R >>\nstartxref\n${xref}\n%%EOF\n`
  return out
}

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
 * everything not named is text. Since batch 4 a file that is none of text, an
 * image or a PDF is `binary`, drawn as its bytes; video, audio and archives
 * are that too until batch 5 gives them bodies of their own.
 */
const KIND = [
  [/\.(png|jpe?g|gif|webp|bmp|ico)$/, 'image'],
  // Text, drawn as a picture (batch 5).
  [/^img\/icon\.svg$/, 'text'],
  [/^media\/clip\./, 'video'],
  [/^media\/tone\./, 'audio'],
  // The magic says PDF; pdf.js says it cannot open them (7a).
  [/^docs\/(locked|broken)\.pdf$/, 'error'],
  [/\.pdf$/, 'pdf'],
  // Nine megabytes of 0x07: mostly control characters, so its bytes (batch 5).
  [/^large\/nine-megabytes\.bin$/, 'binary'],
  // Listed by the server (7c). The header of a database and nothing after
  // it is a database SQLite says it cannot read.
  [/^archives\//, 'archive'],
  [/^large\/ten-thousand-and-one\.tar$/, 'archive'],
  [/^db\//, 'sqlite'],
  [/^bin\/sqlite\.db$/, 'error'],
  [/^(media|bin)\//, 'binary'],
  // Bytes, drawn as a specimen (7b).
  [/^fonts\//, 'binary'],
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
    /README|src\/app\.ts|config\.json|table\.csv|table\.tsv|huge\.png|manual\.pdf|fifty-thousand|nine-mega|中文|very-long|minified|page\.html|bundle\.zip|analysis\.ipynb|fixture\.otf|utf16|project\.zip|climbs\.tar|app\.sqlite/.test(f))
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
      errors.push(text)
    })
    const list = shape.name === '1280-light' && !QUICK ? files : SAMPLE
    for (const f of list) {
      errors.length = 0
      try {
        await filesAtRoot(page)
        const v = await openFile(page, f)
        const want = kindOf(f)
        // A PDF pdf.js cannot open is a PDF until pdf.js has said so.
        await page.waitForFunction(([sel, w]) => document.querySelector(`${sel} [data-testid="viewer-content"]`)?.dataset.kind === w,
          [VIEWER, want], { timeout: 5000 }).catch(() => {})
        const kind = await v.locator('[data-testid="viewer-content"]').getAttribute('data-kind')
        if (kind !== want) note('FAIL', 'kinds', `${shape.name} ${f}: opened as ${kind}, want ${want}`)
        // The plain body, where there is one, must be the file's lines. The
        // preview this replaced ran a one-line sanitiser over whole files and
        // drew every newline and tab as U+FFFD -- every log one run-on line.
        // Only a file that really holds a control character may show one.
        const body = v.locator('[data-testid="viewer-text"]')
        // Text only: a binary is drawn as its bytes in hex, not as its lines.
        if (kind === 'text' && (await body.count())) {
          // textContent, not innerText: innerText leaves out a block the
          // browser has not laid out, which is every block below the first
          // screen, and a check that reads only the rows on screen passed with
          // the body's sanitiser swapped for the one that eats tabs.
          const shown = await body.evaluate((el) => el.textContent)
          if (shown.includes('\uFFFD') && !/controls/.test(f)) {
            note('FAIL', 'text', `${shape.name} ${f}: drawn with U+FFFD in it: ${JSON.stringify(shown.slice(0, 80))}`)
          }
          // In its own encoding: the server decodes these two (7b).
          const enc = /latin1/.test(f) ? 'latin1' : /utf16/.test(f) ? 'utf16le' : 'utf8'
          const firstLine = readFileSync(join(PROJ, f), enc).replace(/^\uFEFF/, '').split(/\r\n|\n|\r/)[0]
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
        // A file the table expects to open as an error -- a database that is
        // only a header -- is answered 422 by the server, and the browser logs
        // every 4xx it fetches as a console error. That one is the answer the
        // viewer shows, not noise; any other 4xx, or any 4xx on a file that
        // should open, still fails.
        const unexpected = errors.filter((e) => !(want === 'error' && /status of 422/.test(e)))
        if (unexpected.length) note('FAIL', 'errors', `${shape.name} ${f}: ${unexpected[0].slice(0, 200)}`)
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
    for (const f of ['page.html']) {
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
    // An SVG is a picture now, through <img>, which runs nothing; the frame
    // is for a project's pages. Its script must not have run.
    await filesAtRoot(page)
    const sv = await openFile(page, 'img/icon.svg')
    if (!(await sv.locator('[data-testid="viewer-image"] img').count())) note('FAIL', 'page', 'icon.svg did not open as a picture')
    // And it drew: an SVG in <img> needs image/svg+xml, which only the inline
    // route sends -- the download's octet-stream draws nothing.
    const svgDrawn = await sv.locator('[data-testid="viewer-image"] img').evaluate((el) => new Promise((done) => {
      if (el.complete) return done(el.naturalWidth)
      el.addEventListener('load', () => done(el.naturalWidth))
      el.addEventListener('error', () => done(0))
    })).catch(() => 0)
    if (!svgDrawn) note('FAIL', 'page', 'icon.svg did not draw as a picture')
    if (await sv.locator('[data-testid="preview-frame"]').count()) note('FAIL', 'page', 'icon.svg opened in a frame')
    const dialogs = []
    page.on('dialog', (d) => { dialogs.push(d.message()); void d.dismiss() })
    await sleep(500)
    if (dialogs.length) note('FAIL', 'page', `an SVG's script ran: ${dialogs.join(', ')}`)
    await page.keyboard.press('Escape')
    note('PASS', 'page', 'a project\'s HTML opens in the sandbox, scripts off until switched on; an SVG opens as a picture')
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

  // ── documents ────────────────────────────────────────────────────────────
  // Batch 3: Markdown through mdast with GitHub's extensions, drawn as React
  // elements. An image in the project loads through the download route; a
  // remote one is never requested, because a document must not be able to
  // tell somebody's server it was opened. An #anchor scrolls the document and
  // leaves the panel's own URL alone.
  {
    const ctx = await contextFor({ width: 1280, height: 800, scheme: 'light' })
    const page = await ctx.newPage()
    const requests = []
    page.on('request', (r) => requests.push(r.url()))
    await filesAtRoot(page)
    await openFile(page, 'src/app.ts')
    await page.keyboard.press('Escape')
    if (requests.some((u) => /\/assets\/Markdown-/.test(u))) note('FAIL', 'markdown', 'the document renderer was fetched before a document was opened')
    await filesAtRoot(page)
    const v = await openFile(page, 'README.md')
    const doc = v.locator('[data-testid="preview-markdown"]')
    await doc.waitFor({ timeout: 10000 })
    if (!requests.some((u) => /\/assets\/Markdown-/.test(u))) note('FAIL', 'markdown', 'opening a document fetched no renderer chunk')

    const img = doc.locator('[data-testid="md-image"]')
    const loaded = await img.evaluate((el) => new Promise((done) => {
      if (el.complete) return done(el.naturalWidth)
      el.addEventListener('load', () => done(el.naturalWidth))
      el.addEventListener('error', () => done(0))
    })).catch(() => 0)
    if (!loaded) note('FAIL', 'markdown', 'the README\'s image from the project did not load')
    if (!(await doc.locator('[data-testid="md-remote-image"]').count())) note('FAIL', 'markdown', 'the remote image has no placeholder')
    if (requests.some((u) => u.includes('example.com'))) note('FAIL', 'markdown', `a remote address was requested: ${requests.filter((u) => u.includes('example.com')).join(', ')}`)

    const facts = await doc.evaluate((d) => ({
      tasks: d.querySelectorAll('input[type="checkbox"]').length,
      checked: d.querySelectorAll('input[type="checkbox"]:checked').length,
      nestedLists: d.querySelectorAll('ul ul ul').length,
      centre: getComputedStyle(d.querySelectorAll('table tbody tr')[0].children[1]).textAlign,
      right: getComputedStyle(d.querySelectorAll('table tbody tr')[0].children[2]).textAlign,
      rawHtml: d.querySelector('[data-testid="md-raw-html"]')?.textContent ?? '',
      liveRaw: d.querySelectorAll('script, iframe, div[onclick]').length,
      footnotes: d.querySelectorAll('[data-testid="md-footnotes"] li').length,
    }))
    if (facts.tasks !== 2 || facts.checked !== 1) note('FAIL', 'markdown', `tasks: ${facts.tasks} boxes, ${facts.checked} ticked; want 2 and 1`)
    if (!facts.nestedLists) note('FAIL', 'markdown', 'the nested list was flattened')
    if (facts.centre !== 'center' || facts.right !== 'right') note('FAIL', 'markdown', `table alignment ${facts.centre}/${facts.right}`)
    if (!facts.rawHtml.includes('onclick') || facts.liveRaw) note('FAIL', 'markdown', `raw HTML: shown as ${JSON.stringify(facts.rawHtml.slice(0, 40))}, ${facts.liveRaw} live elements`)
    if (facts.footnotes !== 1) note('FAIL', 'markdown', `${facts.footnotes} footnotes`)

    // An anchor: the document scrolls to the heading, the panel's URL stays.
    const before = page.url()
    const body = v.locator('[data-testid="preview-markdown"]').locator('..')
    await body.evaluate((el) => (el.scrollTop = el.scrollHeight))
    await doc.locator('a[href="#tables"]').first().click()
    await sleep(300)
    const landed = await doc.evaluate((d) => {
      const h = d.querySelector('#md-tables')
      const box = d.parentElement.getBoundingClientRect()
      const r = h.getBoundingClientRect()
      return r.top >= box.top - 2 && r.top < box.top + 80
    })
    if (!landed) note('FAIL', 'markdown', 'the #tables link did not bring the Tables heading to the top')
    if (page.url() !== before) note('FAIL', 'markdown', `an anchor changed the panel's URL to ${page.url()}`)
    await page.screenshot({ path: join(SHOTS, 'markdown-anchor.png') })
    await page.keyboard.press('Escape')
    note('PASS', 'markdown', 'a document: its image from the project, no remote request, tasks, nesting, alignment, raw HTML as text, footnotes, an anchor that stays in the document')
    await ctx.close()
  }

  // ── structured text and bytes ────────────────────────────────────────────
  // Batch 4: a JSON document is a tree, delimited text a table, a binary its
  // bytes as hexdump -C lines; find works in all three. A JSON file that does
  // not parse is code again, not an error.
  {
    const ctx = await contextFor({ width: 1280, height: 800, scheme: 'light' })
    const page = await ctx.newPage()
    const bodyLang = (v) => v.locator('[data-testid="viewer-text"]').getAttribute('data-lang').catch(() => null)

    await filesAtRoot(page)
    let v = await openFile(page, 'data/config.json')
    const tree = v.locator('[data-testid="viewer-json"]')
    if (!(await tree.count())) note('FAIL', 'json', 'config.json did not open as a tree')
    else {
      // Over four hundred nodes, so it opens at its root only.
      if (await tree.locator('[data-json-path="/nested/deep"]').count()) note('FAIL', 'json', 'a large document opened fully expanded')
      // The long array is folded past two hundred, and opens.
      await tree.locator('[data-json-path="/long"] [data-testid="json-toggle"]').click()
      const more = tree.locator('[data-testid="json-more"]')
      if (!(await more.count())) note('FAIL', 'json', 'a 500-item array is not folded')
      else {
        await more.click()
        if (!(await tree.locator('[data-json-path="/long/499"]').count())) note('FAIL', 'json', 'opening the fold did not show the rest')
      }
      // Find reveals a match deep inside, by opening what holds it.
      await page.keyboard.press('Control+f')
      await v.locator('[data-testid="viewer-find"]').fill('deepest')
      // The tree counts its matches a render later, as the table does.
      await page.waitForFunction((sel) => /\d/.test(document.querySelector(`${sel} [data-testid="viewer-find-count"]`)?.textContent ?? ''), VIEWER, { timeout: 5000 }).catch(() => {})
      await page.keyboard.press('Enter')
      const found = await tree.locator('[data-json-path="/nested/deep/deeper/deepest"][data-current="true"]')
        .waitFor({ timeout: 5000 }).then(() => true).catch(() => false)
      if (!found) note('FAIL', 'json', 'find did not open /nested to show its match')
      const str = await tree.locator('[data-json-path="/escaped"]').textContent()
      if (!str?.includes('"quoted"') || str.includes('\\"')) note('FAIL', 'json', `a string is not shown as the text it holds: ${str}`)
      await page.keyboard.press('Escape')
    }
    await page.keyboard.press('Escape')

    await filesAtRoot(page)
    v = await openFile(page, 'data/events.jsonl')
    const lines = await v.locator('[data-testid="viewer-json"] [data-json-path^="/"]').evaluateAll((els) => els.filter((e) => /^\/\d+$/.test(e.dataset.jsonPath)).length)
    if (lines !== 40) note('FAIL', 'json', `events.jsonl drew ${lines} records, want 40`)
    await page.keyboard.press('Escape')

    await filesAtRoot(page)
    v = await openFile(page, 'data/broken.json')
    if ((await v.locator('[data-testid="viewer-json"]').count()) || (await bodyLang(v)) !== 'json') {
      note('FAIL', 'json', `broken.json is not code (${await bodyLang(v)})`)
    }
    await page.keyboard.press('Escape')

    await filesAtRoot(page)
    v = await openFile(page, 'data/table.csv')
    const table = v.locator('[data-testid="viewer-table"]')
    if (!(await table.count())) note('FAIL', 'csv', 'table.csv did not open as a table')
    else {
      const cells = await table.evaluate((t) => {
        const rows = [...t.querySelectorAll('tbody tr')].map((r) => [...r.querySelectorAll('td')].slice(1).map((c) => c.textContent))
        const head = [...t.querySelectorAll('thead th')].slice(1).map((c) => c.textContent)
        return { head, r1: rows[0], r2: rows[1], r3: rows[2], count: rows.length }
      })
      if (cells.head.join('|') !== 'id|name|note|amount') note('FAIL', 'csv', `header ${cells.head.join('|')}`)
      if (cells.r1?.[2] !== 'has, a comma' || cells.r2?.[2] !== 'has "quotes"' || cells.r3?.[2] !== 'spans\ntwo lines') {
        note('FAIL', 'csv', `quoted cells read as ${JSON.stringify([cells.r1?.[2], cells.r2?.[2], cells.r3?.[2]])}`)
      }
      // The header stays at the top when the table scrolls.
      const stuck = await table.evaluate((t) => {
        t.scrollTop = 400
        const th = t.querySelector('thead th:nth-child(2)').getBoundingClientRect()
        return Math.abs(th.top - t.getBoundingClientRect().top) < 2
      })
      if (!stuck) note('FAIL', 'csv', 'the header row did not stay at the top')
      await page.keyboard.press('Control+f')
      await v.locator('[data-testid="viewer-find"]').fill('row4')
      // The table counts its own matches and reports them a render later;
      // wait for the count before stepping, as a reader typing would.
      await page.waitForFunction((sel) => /\d/.test(document.querySelector(`${sel} [data-testid="viewer-find-count"]`)?.textContent ?? ''), VIEWER, { timeout: 5000 }).catch(() => {})
      await page.keyboard.press('Enter')
      const count = await v.locator('[data-testid="viewer-find-count"]').textContent()
      if (!/\b10\b/.test(count ?? '')) note('FAIL', 'csv', `find counted ${count} for row4 (row40..row49)`)
      if (!(await table.locator('[data-current="true"]').count())) {
        note('FAIL', 'csv', 'no current cell after Enter')
      }
      await page.keyboard.press('Escape')
    }
    await page.keyboard.press('Escape')

    await filesAtRoot(page)
    v = await openFile(page, 'data/semicolon.csv')
    const cols = await v.locator('[data-testid="viewer-table"] thead th').count()
    if (cols !== 4) note('FAIL', 'csv', `semicolon.csv has ${cols - 1} columns, want 3`)
    await page.keyboard.press('Escape')

    await filesAtRoot(page)
    v = await openFile(page, 'bin/program')
    const first = await v.locator('[data-line="0"]').textContent()
    if (!first?.includes('7f 45 4c 46') || !first.includes('|.ELF')) note('FAIL', 'hex', `the first row is ${JSON.stringify(first)}`)
    if ((await v.locator('[data-testid="viewer-text"]').getAttribute('data-numbers')) !== 'off' && (await v.locator('[data-line="0"] span[aria-hidden]').count())) {
      note('FAIL', 'hex', 'the hex body has line numbers beside its offsets')
    }
    await page.keyboard.press('Control+f')
    await v.locator('[data-testid="viewer-find"]').fill('ELF')
    await page.keyboard.press('Enter')
    if (!(await v.locator('mark[data-current="true"]').count())) note('FAIL', 'hex', 'find did not find ELF in the bytes')
    await page.keyboard.press('Escape')
    await page.keyboard.press('Escape')
    await filesAtRoot(page)
    for (const f of ['data/table.csv', 'bin/program', 'data/events.jsonl']) {
      await filesAtRoot(page)
      await openFile(page, f)
      await page.screenshot({ path: join(SHOTS, `structured--${f.replace(/[/ ]/g, '_')}.png`) })
      await page.keyboard.press('Escape')
    }
    note('PASS', 'structured', 'JSON as a tree (folded, found, unescaped), JSON Lines as records, a broken JSON as code, CSV as a table (quotes, newlines, sticky header, find), semicolons sniffed, a binary as hex with find')
    await ctx.close()
  }

  // ── media ────────────────────────────────────────────────────────────────
  // Batch 5: pictures fit on open and zoom, about the pointer with Ctrl and
  // the wheel, by the keys, and by two fingers on a phone; they pan by a
  // drag; a transparent one sits on a checkerboard. Video and audio play from
  // the inline route, which answers ranges.
  {
    const ctx = await contextFor({ width: 1280, height: 800, scheme: 'light' })
    const page = await ctx.newPage()
    const inline = []
    page.on('response', (r) => { if (r.url().includes('/inline?')) inline.push({ status: r.status(), url: r.url() }) })
    await filesAtRoot(page)
    let v = await openFile(page, 'img/huge.png')
    const box = v.locator('[data-testid="viewer-image"]')
    await page.waitForFunction((sel) => document.querySelector(`${sel} [data-testid="viewer-image-size"]`), VIEWER, { timeout: 10000 }).catch(() => {})
    const size = await v.locator('[data-testid="viewer-image-size"]').textContent().catch(() => null)
    if (!size?.includes('3,000') || !size.includes('2,400')) note('FAIL', 'media', `the footer says ${JSON.stringify(size)}, want 3,000 × 2,400`)
    const fits = await box.evaluate((b) => {
      const i = b.querySelector('img').getBoundingClientRect()
      const r = b.getBoundingClientRect()
      return i.width <= r.width + 1 && i.height <= r.height + 1
    })
    if (!fits || (await box.getAttribute('data-zoom')) !== 'fit') note('FAIL', 'media', 'a picture larger than the window did not open fitted')
    // Streamed from the inline route, not the download.
    const src = await box.locator('img').getAttribute('src')
    if (!src?.includes('/inline?')) note('FAIL', 'media', `a picture is drawn from ${src}, not the inline route`)
    // The browser leaves two fingers to the viewer. Synthetic touches reach
    // the page either way, so the property is read rather than inferred: on a
    // phone without it the browser takes the pinch as its own page zoom.
    const touch = await box.evaluate((b) => getComputedStyle(b).touchAction)
    if (touch !== 'none') note('FAIL', 'media', `the picture declares touch-action: ${touch}; a phone would zoom the page instead`)
    await page.keyboard.press('+')
    const zoomed = await box.getAttribute('data-zoom')
    if (zoomed === 'fit') note('FAIL', 'media', '`+` did not zoom')
    // Zoomed far in, the top-left of the picture is reachable.
    for (let i = 0; i < 8; i++) await page.keyboard.press('+')
    const topLeft = await box.evaluate((b) => {
      b.scrollLeft = 0
      b.scrollTop = 0
      const i = b.querySelector('img').getBoundingClientRect()
      const r = b.getBoundingClientRect()
      return i.left >= r.left - 1 && i.top >= r.top - 1
    })
    if (!topLeft) note('FAIL', 'media', 'zoomed in, the top-left of the picture cannot be reached')
    // A drag pans.
    const r = await box.boundingBox()
    const before = await box.evaluate((b) => [b.scrollLeft, b.scrollTop])
    await page.mouse.move(r.x + r.width / 2, r.y + r.height / 2)
    await page.mouse.down()
    await page.mouse.move(r.x + r.width / 2 - 200, r.y + r.height / 2 - 150, { steps: 6 })
    await page.mouse.up()
    const after = await box.evaluate((b) => [b.scrollLeft, b.scrollTop])
    if (!(after[0] > before[0] && after[1] > before[1])) note('FAIL', 'media', `a drag did not pan: ${before} -> ${after}`)
    // 0 fits again; Ctrl with the wheel zooms.
    await page.keyboard.press('0')
    if ((await box.getAttribute('data-zoom')) !== 'fit') note('FAIL', 'media', '`0` did not fit the picture')
    await page.mouse.move(r.x + r.width / 3, r.y + r.height / 3)
    await page.keyboard.down('Control')
    await page.mouse.wheel(0, -200)
    await page.keyboard.up('Control')
    await sleep(200)
    if ((await box.getAttribute('data-zoom')) === 'fit') note('FAIL', 'media', 'Ctrl with the wheel did not zoom')
    await page.screenshot({ path: join(SHOTS, 'media-zoom.png') })
    await page.keyboard.press('Escape')

    await filesAtRoot(page)
    v = await openFile(page, 'img/alpha.png')
    const checker = await v.locator('[data-testid="viewer-image"] img').evaluate((i) => getComputedStyle(i).backgroundImage)
    if (!checker.includes('conic-gradient')) note('FAIL', 'media', `a transparent picture has no checkerboard: ${checker}`)
    await page.keyboard.press('Escape')

    await filesAtRoot(page)
    v = await openFile(page, 'media/clip.mp4')
    const video = v.locator('[data-testid="preview-video"]')
    const dur = await video.evaluate((el) => new Promise((done) => {
      if (el.readyState >= 1) return done(el.duration)
      el.addEventListener('loadedmetadata', () => done(el.duration))
      el.addEventListener('error', () => done(-1))
      setTimeout(() => done(-2), 8000)
    }))
    if (!(dur > 0.5 && dur < 2)) note('FAIL', 'media', `the video's duration read ${dur}`)
    // Seeking asks for a range.
    inline.length = 0
    await video.evaluate((el) => { el.currentTime = 0.8 })
    await sleep(800)
    await page.keyboard.press('Escape')

    await filesAtRoot(page)
    v = await openFile(page, 'media/tone.wav')
    const adur = await v.locator('[data-testid="preview-audio"]').evaluate((el) => new Promise((done) => {
      if (el.readyState >= 1) return done(el.duration)
      el.addEventListener('loadedmetadata', () => done(el.duration))
      el.addEventListener('error', () => done(-1))
      setTimeout(() => done(-2), 8000)
    }))
    if (!(adur > 0.3 && adur < 1)) note('FAIL', 'media', `the audio's duration read ${adur}`)
    await page.keyboard.press('Escape')
    const ranged = inline.some((x) => x.status === 206)
    note('PASS', 'media', `a 3,000 × 2,400 picture fitted, zoomed by key and wheel, panned by a drag, its top-left reachable; a checkerboard; a video of ${Number(dur).toFixed(2)} s and audio of ${Number(adur).toFixed(2)} s${ranged ? ', seeking by range' : ''}`)
    if (!ranged) note('WARN', 'media', 'no 206 seen while seeking the video; the browser may have had it whole')
    await ctx.close()
  }

  // Two fingers on a phone.
  {
    const ctx = await contextFor({ width: 390, height: 844, scheme: 'dark', phone: true })
    const page = await ctx.newPage()
    await filesAtRoot(page)
    const v = await openFile(page, 'img/photo.jpg')
    const box = v.locator('[data-testid="viewer-image"]')
    await page.waitForFunction((sel) => document.querySelector(`${sel} [data-testid="viewer-image-size"]`), VIEWER, { timeout: 10000 }).catch(() => {})
    const r = await box.boundingBox()
    const cdp = await ctx.newCDPSession(page)
    const cx = r.x + r.width / 2
    const cy = r.y + r.height / 2
    const pts = (d) => ({ touchPoints: [
      { x: cx - d, y: cy, id: 1, radiusX: 4, radiusY: 4, force: 1 },
      { x: cx + d, y: cy, id: 2, radiusX: 4, radiusY: 4, force: 1 },
    ] })
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', ...pts(30) })
    for (let d = 40; d <= 120; d += 10) {
      await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', ...pts(d) })
      await sleep(16)
    }
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] })
    await sleep(200)
    const z = await box.getAttribute('data-zoom')
    if (z === 'fit' || !(Number(z) > 0)) note('FAIL', 'media', `a pinch on a phone did not zoom (${z})`)
    else note('PASS', 'media', `a pinch on a phone zoomed to ${z}`)
    await ctx.close()
  }

  // ── zoom on screen, and select-all ───────────────────────────────────────
  // 「图片要能缩放」: the zoom as buttons in the footer, and a double click
  // between fitted and 100% about the point clicked. A wheel without Ctrl is
  // the page's on a fitted picture and scrolls a zoomed one. Ctrl/⌘+A selects
  // the body and nothing around it, and is the find box's own in the box.
  {
    const ctx = await contextFor({ width: 1280, height: 800, scheme: 'light' })
    const page = await ctx.newPage()
    await filesAtRoot(page)
    let v = await openFile(page, 'img/huge.png')
    const box = v.locator('[data-testid="viewer-image"]')
    await page.waitForFunction((sel) => document.querySelector(`${sel} [data-testid="viewer-image-size"]`), VIEWER, { timeout: 10000 }).catch(() => {})
    const zoomOf = () => box.getAttribute('data-zoom')
    const label = () => v.locator('[data-testid="viewer-zoom"]').textContent()
    // A wheel on a fitted picture: nothing taken, nothing moved.
    const r = await box.boundingBox()
    const taken = await box.evaluate((b) => {
      const e = new WheelEvent('wheel', { deltaY: 120, bubbles: true, cancelable: true })
      b.dispatchEvent(e)
      return e.defaultPrevented
    })
    if (taken) note('FAIL', 'zoom', 'a wheel without Ctrl on a fitted picture was taken')
    await v.locator('[data-testid="viewer-zoom-in"]').click()
    const z1 = await zoomOf()
    if (z1 === 'fit') note('FAIL', 'zoom', 'the + button did not zoom')
    const l1 = await label()
    if (!/^\d+%$/.test(l1 ?? '')) note('FAIL', 'zoom', `zoomed, the footer says ${JSON.stringify(l1)}`)
    await v.locator('[data-testid="viewer-zoom-out"]').click()
    if (Number(await zoomOf()) >= Number(z1)) note('FAIL', 'zoom', 'the − button did not zoom out')
    await v.locator('[data-testid="viewer-zoom-toggle"]').click()
    if ((await zoomOf()) !== 'fit') note('FAIL', 'zoom', 'the toggle did not fit a zoomed picture')
    await v.locator('[data-testid="viewer-zoom-toggle"]').click()
    if ((await zoomOf()) !== '1.000') note('FAIL', 'zoom', `the toggle from fitted went to ${await zoomOf()}, want 1.000`)
    // A wheel on a zoomed picture scrolls it.
    await page.mouse.move(r.x + r.width / 2, r.y + r.height / 2)
    const top0 = await box.evaluate((b) => b.scrollTop)
    await page.mouse.wheel(0, 300)
    await sleep(300)
    const top1 = await box.evaluate((b) => b.scrollTop)
    if (!(top1 > top0)) note('FAIL', 'zoom', `a wheel on a zoomed picture did not scroll it: ${top0} -> ${top1}`)
    if ((await zoomOf()) !== '1.000') note('FAIL', 'zoom', 'a wheel without Ctrl zoomed')
    // Back to fitted, then a double click: 100%, with the pixel that was under
    // the pointer still under it.
    await page.keyboard.press('0')
    const at = { x: r.x + r.width * 0.3, y: r.y + r.height * 0.4 }
    const pixel = () => box.evaluate((b, p) => {
      const i = b.querySelector('img').getBoundingClientRect()
      const img = b.querySelector('img')
      return [(p.x - i.left) * img.naturalWidth / i.width, (p.y - i.top) * img.naturalHeight / i.height]
    }, at)
    const before = await pixel()
    await page.mouse.dblclick(at.x, at.y)
    await sleep(200)
    if ((await zoomOf()) !== '1.000') note('FAIL', 'zoom', `a double click on a fitted picture went to ${await zoomOf()}`)
    const after = await pixel()
    const drift = Math.hypot(after[0] - before[0], after[1] - before[1])
    if (drift > 3) note('FAIL', 'zoom', `a double click moved the pixel under the pointer by ${drift.toFixed(1)} px (${before.map(Math.round)} -> ${after.map(Math.round)})`)
    await page.mouse.dblclick(at.x, at.y)
    await sleep(200)
    if ((await zoomOf()) !== 'fit') note('FAIL', 'zoom', 'a double click on a zoomed picture did not fit it')
    // The buttons zoom about the middle of the view.
    const middle = { x: r.x + r.width / 2, y: r.y + r.height / 2 }
    const mid = () => box.evaluate((b, p) => {
      const i = b.querySelector('img').getBoundingClientRect()
      const img = b.querySelector('img')
      return [(p.x - i.left) * img.naturalWidth / i.width, (p.y - i.top) * img.naturalHeight / i.height]
    }, middle)
    const m0 = await mid()
    await v.locator('[data-testid="viewer-zoom-toggle"]').click()
    await sleep(200)
    const m1 = await mid()
    const mdrift = Math.hypot(m1[0] - m0[0], m1[1] - m0[1])
    if (mdrift > 3) note('FAIL', 'zoom', `the toggle moved the middle of the view by ${mdrift.toFixed(1)} px of the picture`)
    await page.screenshot({ path: join(SHOTS, 'zoom-controls.png') })
    // Select-all on a picture selects nothing, and nothing behind the dialog.
    await page.keyboard.press('Control+a')
    const picSel = await page.evaluate(() => getSelection().toString())
    if (picSel !== '') note('FAIL', 'select-all', `select-all on a picture selected ${JSON.stringify(picSel.slice(0, 60))}`)
    await page.keyboard.press('Escape')
    note('PASS', 'zoom', `buttons in, out and between fitted and 100%; a double click about the pointer (${drift.toFixed(1)} px); the toggle about the middle (${mdrift.toFixed(1)} px); the wheel left alone fitted and scrolling zoomed`)

    // The PDF's footer has the same buttons.
    await filesAtRoot(page)
    v = await openFile(page, 'docs/manual.pdf')
    const pdf = v.locator('[data-testid="viewer-pdf"]')
    await v.locator('[data-testid="viewer-zoom-in"]').click()
    if ((await pdf.getAttribute('data-zoom')) === 'fit') note('FAIL', 'zoom', 'the + button did not zoom a PDF')
    await v.locator('[data-testid="viewer-zoom-toggle"]').click()
    if ((await pdf.getAttribute('data-zoom')) !== 'fit') note('FAIL', 'zoom', 'the toggle did not fit a PDF')
    await page.keyboard.press('Escape')

    // Select-all, kind by kind: what is selected is inside the body, and
    // holds the first and the last of what the body shows.
    const selection = () => page.evaluate((sel) => {
      const s = getSelection()
      const body = document.querySelector(`${sel} [data-testid="viewer-body"]`)
      if (!s || s.rangeCount === 0) return { text: '', inside: false }
      const r = s.getRangeAt(0)
      return { text: s.toString(), inside: !!body && body.contains(r.startContainer) && body.contains(r.endContainer) }
    }, VIEWER)
    for (const [f, first, last] of [
      ['large/fifty-thousand.log', 'line 1 of', 'line 50000 of'],
      ['src/app.ts', null, null],
      ['data/config.json', null, null],
      ['data/table.csv', null, null],
      ['bin/random.bin', null, null],
    ]) {
      await filesAtRoot(page)
      v = await openFile(page, f).catch(() => null)
      if (!v) { note('FAIL', 'select-all', `${f} did not open`); continue }
      await v.locator('[data-testid="viewer-content"]').click({ position: { x: 40, y: 40 } }).catch(() => {})
      await page.keyboard.press('Control+a')
      const got = await selection()
      if (!got.inside || got.text.length === 0) note('FAIL', 'select-all', `${f}: the selection is ${got.inside ? 'empty' : 'outside the body'}`)
      if (first && !got.text.includes(first)) note('FAIL', 'select-all', `${f}: the selection does not hold ${JSON.stringify(first)}`)
      if (last && !got.text.includes(last)) note('FAIL', 'select-all', `${f}: the selection does not hold ${JSON.stringify(last)} -- a block not yet drawn was left out`)
      if (/^\s*\d+\s/m.test(got.text) && f === 'src/app.ts') note('FAIL', 'select-all', `${f}: the selection holds line numbers`)
      // The footer's own words are not in it.
      const footer = (await v.locator('[data-testid="viewer-footer"]').textContent().catch(() => '')).trim()
      if (footer && got.text.includes(footer)) note('FAIL', 'select-all', `${f}: the footer is selected`)
      await page.keyboard.press('Escape')
    }
    // In the find box, the box's own.
    await filesAtRoot(page)
    v = await openFile(page, 'src/app.ts')
    await page.keyboard.press('Control+f')
    await page.keyboard.type('import')
    await page.keyboard.press('Control+a')
    const field = await page.evaluate(() => {
      const el = document.activeElement
      return el instanceof HTMLInputElement ? [el.selectionStart, el.selectionEnd, el.value.length] : null
    })
    if (!field || field[0] !== 0 || field[1] !== field[2]) note('FAIL', 'select-all', `in the find box, select-all did not select the box: ${JSON.stringify(field)}`)
    const leaked = await selection()
    if (leaked.inside && leaked.text) note('FAIL', 'select-all', 'in the find box, select-all selected the body')
    note('PASS', 'select-all', 'the body and nothing around it, for text, code, a tree, a table and hex, the last line of 50,000 included; a picture selects nothing; the find box keeps its own')
    await ctx.close()
  }

  // ── PDF ──────────────────────────────────────────────────────────────────
  // 7a: pdf.js draws the pages, each when it comes near the view; the text is
  // a layer over them that find searches and marks; PgUp, PgDn, g and G turn
  // pages, Ctrl+G goes to one; the zoom is the image's. The bytes come from
  // the inline route, sandboxed like every other medium, and there is no
  // <object> left anywhere.
  {
    const ctx = await contextFor({ width: 1280, height: 800, scheme: 'light' })
    const page = await ctx.newPage()
    const inline = []
    page.on('response', (r) => {
      if (r.url().includes('/inline?')) inline.push({ status: r.status(), csp: r.headers()['content-security-policy'] ?? '' })
    })
    const errors = []
    page.on('pageerror', (e) => errors.push(String(e)))
    await filesAtRoot(page)
    const t0 = Date.now()
    const v = await openFile(page, 'docs/manual.pdf')
    const pdf = v.locator('[data-testid="viewer-pdf"]')
    const drawn = (n) => page.waitForFunction(([sel, n]) =>
      document.querySelector(`${sel} [data-page="${n}"][data-drawn]`), [VIEWER, n], { timeout: 10000 })
    await drawn(1)
    const firstPaint = Date.now() - t0
    if ((await pdf.getAttribute('data-pages')) !== '3') note('FAIL', 'pdf', `manual.pdf has ${await pdf.getAttribute('data-pages')} pages, want 3`)
    if (await page.locator('object').count()) note('FAIL', 'pdf', 'an <object> is still on the page')
    const footer = () => v.locator('[data-testid="viewer-page"]').textContent()
    if ((await v.locator('[data-testid="viewer-page"]').getAttribute('data-page')) !== '1') note('FAIL', 'pdf', `the footer says ${await footer()} on open`)
    // Ink on the canvas: something was drawn, not a white rectangle.
    const ink = (n) => v.locator(`[data-page="${n}"] canvas`).evaluate((c) => {
      if (!c.width) return { dark: 0, colour: 0, width: 0 }
      const d = c.getContext('2d').getImageData(0, 0, c.width, c.height).data
      let dark = 0
      let colour = 0
      for (let i = 0; i < d.length; i += 4) {
        if (d[i] < 100 && d[i + 1] < 100 && d[i + 2] < 100) dark++
        if (Math.abs(d[i] - d[i + 2]) > 40 && d[i + 2] > 120) colour++
      }
      return { dark, colour, width: c.width }
    })
    const p1 = await ink(1)
    if (p1.dark < 100) note('FAIL', 'pdf', `page 1 has ${p1.dark} dark pixels: nothing was drawn`)
    // The text layer holds the page's words, for selection and find.
    const layer = await v.locator('[data-page="1"] .vp-pdf-text').textContent()
    if (!layer.includes('The first of three pages')) note('FAIL', 'pdf', `page 1's text layer reads ${JSON.stringify(layer.slice(0, 60))}`)
    const sandboxed = inline.length > 0 && inline.every((x) => x.csp.startsWith('sandbox'))
    if (!sandboxed) note('FAIL', 'pdf', `the PDF's bytes came without the sandbox: ${JSON.stringify(inline.slice(0, 2))}`)

    // The page keys.
    const pageIs = async (want, what) => {
      await page.waitForFunction(([sel, w]) => document.querySelector(`${sel} [data-testid="viewer-page"]`)?.dataset.page === String(w),
        [VIEWER, want], { timeout: 3000 })
        .catch(async () => note('FAIL', 'pdf', `${what}: the footer says ${JSON.stringify(await footer())}, want page ${want}`))
    }
    await page.keyboard.press('PageDown')
    await pageIs(2, 'PgDn')
    await page.keyboard.press('G')
    await pageIs(3, 'G')
    await drawn(3)
    const p3 = await ink(3)
    if (p3.colour < 1000) note('FAIL', 'pdf', `page 3's picture is not on its canvas (${p3.colour} coloured pixels)`)
    await page.keyboard.press('PageUp')
    await pageIs(2, 'PgUp')
    await page.keyboard.press('g')
    await pageIs(1, 'g')
    // Ctrl+G is go to page here, and the focus comes back to the pages.
    await page.keyboard.press('Control+g')
    const goto = v.locator('[data-testid="viewer-goto"]')
    await goto.waitFor({ timeout: 3000 })
    if (!(await goto.getAttribute('placeholder'))?.includes('3')) note('FAIL', 'pdf', `go to page says ${await goto.getAttribute('placeholder')}`)
    await goto.fill('3')
    await page.keyboard.press('Enter')
    await pageIs(3, 'Ctrl+G 3')
    const focusBack = await page.evaluate((sel) => document.activeElement?.dataset.testid === 'viewer-pdf' ||
      document.querySelector(sel).contains(document.activeElement) && document.activeElement.tagName !== 'INPUT', VIEWER)
    if (!focusBack) note('FAIL', 'pdf', 'after go to page the focus is not in the viewer')
    await page.keyboard.press('g')
    await pageIs(1, 'g after go to page')

    // Scrolled by hand so page 2 starts a quarter of the way down the view,
    // above the third the footer counts from, the footer says page 2. The
    // pages were once measured from the dialog and not from the scroller, and
    // by the header's height the footer read page 1 with page 2 filling most
    // of the view.
    await pdf.evaluate((b) => {
      const p2 = b.querySelector('[data-page="2"]')
      b.scrollTop += p2.getBoundingClientRect().top - b.getBoundingClientRect().top - b.clientHeight / 4
    })
    await pageIs(2, 'page 2 scrolled to the top of the view')
    await page.keyboard.press('g')
    await pageIs(1, 'g after scrolling')

    // Find: a word only on page two, found, counted, marked and brought into
    // view.
    await page.keyboard.press('Control+f')
    await v.locator('[data-testid="viewer-find"]').fill('lighthouse')
    await page.waitForFunction((sel) => /1/.test(document.querySelector(`${sel} [data-testid="viewer-find-count"]`)?.textContent ?? '') &&
      !document.querySelector(`${sel} [data-testid="viewer-find-count"]`).textContent.includes('+'), VIEWER, { timeout: 5000 })
      .catch(async () => note('FAIL', 'pdf', `find "lighthouse" counted ${JSON.stringify(await v.locator('[data-testid="viewer-find-count"]').textContent())}`))
    await page.keyboard.press('Enter')
    const hit = v.locator('[data-page="2"] .vp-pdf-hit[data-current]')
    await hit.waitFor({ timeout: 5000 }).catch(() => note('FAIL', 'pdf', 'Enter did not mark the match on page 2'))
    if (await hit.count()) {
      const seen = await hit.evaluate((h) => {
        const r = h.getBoundingClientRect()
        const b = h.closest('[data-testid="viewer-pdf"]').getBoundingClientRect()
        return { inView: r.top >= b.top && r.bottom <= b.bottom, text: h.textContent }
      })
      if (!seen.inView) note('FAIL', 'pdf', 'the current match is not in view')
      if (seen.text.toLowerCase() !== 'lighthouse') note('FAIL', 'pdf', `the mark covers ${JSON.stringify(seen.text)}, not the word`)
    }
    await v.locator('[data-testid="viewer-find"]').fill('page')
    await page.waitForFunction((sel) => {
      const t = document.querySelector(`${sel} [data-testid="viewer-find-count"]`)?.textContent ?? ''
      return /\b4\b/.test(t) && !t.includes('+')
    }, VIEWER, { timeout: 5000 })
      .catch(async () => note('FAIL', 'pdf', `find "page" counted ${JSON.stringify(await v.locator('[data-testid="viewer-find-count"]').textContent())}, want 4`))
    await page.screenshot({ path: join(SHOTS, 'pdf-find.png') })
    await page.keyboard.press('Escape')

    // Zoom: the keys, and the canvas drawn again at the new size once it
    // settles rather than stretched for good.
    await page.keyboard.press('g')
    await pageIs(1, 'g before zoom')
    const w0 = (await ink(1)).width
    await page.keyboard.press('+')
    await page.keyboard.press('+')
    if ((await pdf.getAttribute('data-zoom')) === 'fit') note('FAIL', 'pdf', '`+` did not zoom')
    await page.waitForFunction(([sel, w0]) => document.querySelector(`${sel} [data-page="1"] canvas`)?.width > w0 * 1.2,
      [VIEWER, w0], { timeout: 5000 }).catch(() => note('FAIL', 'pdf', 'zoomed in, the page was not drawn again at the new size'))
    await page.keyboard.press('0')
    if ((await pdf.getAttribute('data-zoom')) !== 'fit') note('FAIL', 'pdf', '`0` did not fit')
    const r = await pdf.boundingBox()
    await page.mouse.move(r.x + r.width / 2, r.y + r.height / 3)
    await page.keyboard.down('Control')
    await page.mouse.wheel(0, -200)
    await page.keyboard.up('Control')
    await sleep(200)
    if ((await pdf.getAttribute('data-zoom')) === 'fit') note('FAIL', 'pdf', 'Ctrl with the wheel did not zoom')
    await page.screenshot({ path: join(SHOTS, 'pdf-zoom.png') })
    await page.keyboard.press('Escape')
    await page.locator(VIEWER).waitFor({ state: 'detached', timeout: 3000 }).catch(() => {})

    // A PDF pdf.js cannot open says why, and offers the download.
    for (const [f, want] of [['docs/locked.pdf', /protected by a password|有密码/], ['docs/broken.pdf', /pdf\.js/]]) {
      await filesAtRoot(page)
      const w = await openFile(page, f)
      const msg = w.locator('[data-testid="preview-unavailable"]')
      await msg.waitFor({ timeout: 10000 }).catch(() => {})
      const text = (await msg.textContent().catch(() => '')) ?? ''
      if (!want.test(text)) note('FAIL', 'pdf', `${f} says ${JSON.stringify(text.slice(0, 100))}`)
      await page.keyboard.press('Escape')
    }

    // Two hundred pages: the first paint, and the canvases held after
    // scrolling through all of them.
    await filesAtRoot(page)
    const t1 = Date.now()
    const long = await openFile(page, 'large/two-hundred-pages.pdf')
    await drawn(1)
    const longPaint = Date.now() - t1
    const lp = long.locator('[data-testid="viewer-pdf"]')
    for (let i = 0; i < 25; i++) {
      await lp.evaluate((b, i) => { b.scrollTop = (b.scrollHeight * (i + 1)) / 25 }, i)
      await sleep(60)
    }
    await sleep(600)
    const held = await lp.evaluate((b) => [...b.querySelectorAll('canvas')].filter((c) => c.width > 0).length)
    await page.keyboard.press('G')
    await pageIs(200, 'G on 200 pages')
    // A match at the bottom of a page far away: its page is brought into
    // view, drawn, and then the match centred, which only the page can do.
    await page.keyboard.press('Control+f')
    await long.locator('[data-testid="viewer-find"]').fill('bottom of page 150.')
    await page.waitForFunction((sel) => /\b1\b/.test(document.querySelector(`${sel} [data-testid="viewer-find-count"]`)?.textContent ?? '') &&
      !document.querySelector(`${sel} [data-testid="viewer-find-count"]`).textContent.includes('+'), VIEWER, { timeout: 15000 })
      .catch(async () => note('FAIL', 'pdf', `find on 200 pages counted ${JSON.stringify(await long.locator('[data-testid="viewer-find-count"]').textContent())}`))
    await page.keyboard.press('Enter')
    const far = long.locator('[data-page="150"] .vp-pdf-hit[data-current]')
    await far.waitFor({ timeout: 5000 }).catch(() => note('FAIL', 'pdf', 'the match on page 150 was not marked'))
    await sleep(300)
    if (await far.count()) {
      const inView = await far.evaluate((h) => {
        const r = h.getBoundingClientRect()
        const b = h.closest('[data-testid="viewer-pdf"]').getBoundingClientRect()
        return r.top >= b.top && r.bottom <= b.bottom
      })
      if (!inView) note('FAIL', 'pdf', 'the match at the bottom of page 150 was not brought into view')
    }
    await page.keyboard.press('Escape')
    if (held > 8) note('FAIL', 'pdf', `${held} of 200 pages hold a canvas after scrolling through; pages far from the view must give theirs back`)
    if (longPaint > 5000) note('FAIL', 'pdf', `the first page of 200 took ${longPaint} ms`)
    await page.keyboard.press('Escape')
    if (errors.length) note('FAIL', 'pdf', `thrown: ${errors[0].slice(0, 200)}`)
    note('PASS', 'pdf', `3 pages drawn (first paint ${firstPaint} ms), a picture on page 3, the page keys and go to page, "lighthouse" found and marked on page 2, zoom redrawn; 200 pages: first paint ${longPaint} ms, ${held} canvases held after scrolling through`)
    await ctx.close()
  }

  // A phone: the page fits the width, and two fingers zoom while one scrolls.
  {
    const ctx = await contextFor({ width: 320, height: 640, scheme: 'dark', phone: true })
    const page = await ctx.newPage()
    await filesAtRoot(page)
    const v = await openFile(page, 'docs/manual.pdf')
    await page.waitForFunction((sel) => document.querySelector(`${sel} [data-page="1"][data-drawn]`), VIEWER, { timeout: 10000 })
      .catch(() => note('FAIL', 'pdf', 'phone: page 1 was not drawn'))
    const pdf = v.locator('[data-testid="viewer-pdf"]')
    await page.screenshot({ path: join(SHOTS, 'pdf-phone-fit.png') })
    const fits = await pdf.evaluate((b) => b.querySelector('[data-page="1"]').getBoundingClientRect().width <= b.clientWidth)
    if (!fits) note('FAIL', 'pdf', 'phone: the page is wider than the screen when fitted')
    const touch = await pdf.evaluate((b) => getComputedStyle(b).touchAction)
    if (touch !== 'pan-x pan-y') note('FAIL', 'pdf', `phone: touch-action is ${touch}; one finger must scroll natively and two must reach the viewer`)
    const r = await pdf.boundingBox()
    const cdp = await ctx.newCDPSession(page)
    const cx = r.x + r.width / 2
    const cy = r.y + r.height / 2
    const pts = (d) => ({ touchPoints: [
      { x: cx - d, y: cy, id: 1, radiusX: 4, radiusY: 4, force: 1 },
      { x: cx + d, y: cy, id: 2, radiusX: 4, radiusY: 4, force: 1 },
    ] })
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', ...pts(20) })
    for (let d = 30; d <= 100; d += 10) {
      await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', ...pts(d) })
      await sleep(16)
    }
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] })
    await sleep(300)
    const z = await pdf.getAttribute('data-zoom')
    if (z === 'fit' || !(Number(z) > 0)) note('FAIL', 'pdf', `phone: a pinch did not zoom (${z})`)
    await page.screenshot({ path: join(SHOTS, 'pdf-phone.png') })
    note('PASS', 'pdf', `phone: fitted to 320, touch-action pan-x pan-y, a pinch zoomed to ${z}`)
    await ctx.close()
  }

  // ── notebooks, fonts, encodings ──────────────────────────────────────────
  // 7b. A notebook is its cells: markdown drawn, code coloured with its
  // count, every output as what it is, HTML as its source and never run, a
  // traceback without its terminal colours; Source is its JSON. A font is a
  // specimen set in itself, named from the file, and gone from the document
  // when the viewer closes. Text in another encoding is decoded on the
  // server and the footer says which.
  {
    const ctx = await contextFor({ width: 1280, height: 800, scheme: 'light' })
    const page = await ctx.newPage()
    const errors = []
    page.on('pageerror', (e) => errors.push(String(e)))
    await filesAtRoot(page)
    let v = await openFile(page, 'notebooks/analysis.ipynb')
    const nb = v.locator('[data-testid="viewer-notebook"]')
    await nb.waitFor({ timeout: 10000 }).catch(() => note('FAIL', 'notebook', 'analysis.ipynb did not open as a notebook'))
    if (await nb.count()) {
      const got = await nb.evaluate((el) => ({
        counts: [...el.querySelectorAll('[data-testid="nb-count"]')].map((c) => c.textContent),
        heading: el.querySelector('[data-cell="markdown"] h1')?.textContent ?? null,
        // The heading's own words, without the # anchor that every rendered
        // heading carries after them.
        heading: (() => {
          const h = el.querySelector('[data-cell="markdown"] h1')
          return h ? [...h.childNodes].filter((n) => n.nodeName !== 'A').map((n) => n.textContent).join('') : null
        })(),
        coloured: !!el.querySelector('[data-cell="code"] [data-testid="preview-code"] span[class*="--vp-tok-keyword"]'),
        outputs: [...el.querySelectorAll('[data-testid="nb-output"]')].map((o) => o.dataset.output),
        picture: (() => {
          const img = el.querySelector('[data-output="image"]')
          return img ? { w: img.naturalWidth, src: img.getAttribute('src').slice(0, 22) } : null
        })(),
        htmlAsText: el.querySelector('[data-output="html"]')?.textContent.includes('<script>') ?? false,
        rendered: !!document.getElementById('nb-html-rendered'),
        traceback: el.querySelector('[data-output="error"]')?.textContent ?? '',
      }))
      const want = ['[1]', '[2]', '[3]', '[4]', '[5]', '[ ]']
      if (JSON.stringify(got.counts) !== JSON.stringify(want)) note('FAIL', 'notebook', `execution counts ${JSON.stringify(got.counts)}, want ${JSON.stringify(want)}`)
      if (got.heading !== 'Notebook fixture') note('FAIL', 'notebook', `the markdown cell's heading is ${JSON.stringify(got.heading)}`)
      if (!got.coloured) note('FAIL', 'notebook', 'the code cells are not coloured as Python')
      const outs = ['text', 'stderr', 'image', 'text', 'html', 'error']
      if (JSON.stringify(got.outputs) !== JSON.stringify(outs)) note('FAIL', 'notebook', `outputs ${JSON.stringify(got.outputs)}, want ${JSON.stringify(outs)}`)
      if (!got.picture || got.picture.w !== 48 || got.picture.src !== 'data:image/png;base64') note('FAIL', 'notebook', `the picture output is ${JSON.stringify(got.picture)}`)
      if (!got.picture || got.picture.w !== 48 || got.picture.src !== 'data:image/png;base64,') note('FAIL', 'notebook', `the picture output is ${JSON.stringify(got.picture)}`)
      if (!got.htmlAsText || got.rendered) note('FAIL', 'notebook', 'an HTML output was rendered rather than shown as its source')
      if ((await page.title()) === 'ran') note('FAIL', 'notebook', "an HTML output's script ran")
      if (!got.traceback.includes('ZeroDivisionError') || /\x1b|\[0;31m/.test(got.traceback)) note('FAIL', 'notebook', `the traceback reads ${JSON.stringify(got.traceback.slice(0, 80))}`)
      const cells = await v.locator('[data-testid="viewer-cells"]').textContent().catch(() => '')
      if (!/7/.test(cells)) note('FAIL', 'notebook', `the footer says ${JSON.stringify(cells)}, want 7 cells`)
      await page.screenshot({ path: join(SHOTS, 'notebook.png') })
      await page.keyboard.press('s')
      const json = await v.locator('[data-testid="viewer-text"]').textContent().catch(() => '')
      if (!json.includes('"nbformat": 4')) note('FAIL', 'notebook', 'Source is not the JSON')
    }
    await page.keyboard.press('Escape')

    for (const [ext, format, name] of [
      ['ttf', 'TrueType', 'Vibepanel Fixture Regular'],
      ['otf', 'OpenType', 'Vibepanel Fixture Regular'],
      ['woff', 'WOFF', 'Vibepanel Fixture Regular'],
      // WOFF2's tables are one Brotli stream: the name is the file's.
      ['woff2', 'WOFF2', 'fixture.woff2'],
    ]) {
      await filesAtRoot(page)
      v = await openFile(page, `fonts/fixture.${ext}`)
      const spec = v.locator('[data-testid="viewer-font"]')
      await spec.waitFor({ timeout: 10000 }).catch(() => note('FAIL', 'font', `fixture.${ext} drew no specimen`))
      if (!(await spec.count())) {
        await page.keyboard.press('Escape')
        continue
      }
      const family = await spec.getAttribute('data-family')
      const got = await spec.evaluate((el, family) => {
        // Set in the face, a line measures as the face's boxes, not as the
        // fallback's letters.
        const line = el.querySelector('[data-specimen="40"]')
        const probe = document.createElement('span')
        probe.style.cssText = 'position:absolute;visibility:hidden;font-size:40px;font-family:monospace;white-space:nowrap'
        probe.textContent = line.textContent
        document.body.append(probe)
        const fallback = probe.getBoundingClientRect().width
        probe.style.fontFamily = `"${family}", monospace`
        const set = probe.getBoundingClientRect().width
        probe.remove()
        return {
          name: el.querySelector('[data-testid="font-name"]').textContent,
          loaded: [...document.fonts].some((f) => f.family === family && f.status === 'loaded'),
          differs: Math.abs(set - fallback) > 5,
        }
      }, family)
      if (got.name !== name) note('FAIL', 'font', `fixture.${ext} is named ${JSON.stringify(got.name)}, want ${JSON.stringify(name)}`)
      if (!got.loaded || !got.differs) note('FAIL', 'font', `fixture.${ext}: the specimen is not set in the font (loaded ${got.loaded}, measures differently ${got.differs})`)
      const fmt = await v.locator('[data-testid="viewer-format"]').textContent()
      if (fmt !== format) note('FAIL', 'font', `fixture.${ext}: the footer says ${JSON.stringify(fmt)}, want ${format}`)
      if (ext === 'ttf') {
        await page.screenshot({ path: join(SHOTS, 'font.png') })
        await page.keyboard.press('s')
        if (!(await v.locator('[data-testid="viewer-text"]').count())) note('FAIL', 'font', 'Source does not show the bytes')
      }
      await page.keyboard.press('Escape')
      await page.locator(VIEWER).waitFor({ state: 'detached', timeout: 3000 }).catch(() => {})
      const left = await page.evaluate((family) => [...document.fonts].some((f) => f.family === family), family)
      if (left) note('FAIL', 'font', `fixture.${ext}: the face stays in the document after the viewer closed`)
    }

    for (const [f, enc, words] of [['encodings/utf16.txt', 'UTF-16 LE', 'UTF-16 text'], ['encodings/latin1.txt', 'Latin-1', 'café naïve']]) {
      await filesAtRoot(page)
      v = await openFile(page, f)
      const got = await v.locator('[data-testid="viewer-encoding"]').textContent().catch(() => null)
      const body = await v.locator('[data-testid="viewer-text"]').textContent().catch(() => '')
      if (got !== enc) note('FAIL', 'encoding', `${f}: the footer says ${JSON.stringify(got)}, want ${enc}`)
      if (!body.includes(words) || body.includes('\uFFFD')) note('FAIL', 'encoding', `${f}: the body reads ${JSON.stringify(body.slice(0, 60))}`)
      await page.keyboard.press('Escape')
    }
    if (errors.length) note('FAIL', 'more kinds', `thrown: ${errors[0].slice(0, 200)}`)
    note('PASS', 'more kinds', 'a notebook drawn cell by cell with its outputs, its HTML as text; four font formats set in themselves and taken out on close; UTF-16 and Latin-1 decoded and named')
    await ctx.close()
  }

  // ── archives and databases ───────────────────────────────────────────────
  // 7c. An archive is its table of contents from the server: the directory
  // it is wrapped in opened, the rest folded, a name that climbs out shown
  // whole and marked, find opening the directories of a match, the listing's
  // bound said in the footer. A database is its tables in a strip and the
  // first rows of the one chosen, NULL and a BLOB drawn apart from text, the
  // arrows stepping tables, Source its schema -- and the file untouched, with
  // nothing made beside it.
  {
    const ctx = await contextFor({ width: 1280, height: 800, scheme: 'light' })
    const page = await ctx.newPage()
    const errors = []
    page.on('pageerror', (e) => errors.push(String(e)))
    const rowsOf = (v) =>
      v.locator('[data-testid="viewer-archive"]').evaluate((el) =>
        [...el.querySelectorAll('[role="treeitem"]')].map((r) => ({
          level: Number(r.getAttribute('aria-level')),
          text: r.firstElementChild.textContent,
          unsafe: !!r.querySelector('[data-testid="archive-unsafe"]'),
          when: r.lastElementChild.textContent,
        })),
      )

    await filesAtRoot(page)
    let v = await openFile(page, 'archives/project.zip')
    await v.locator('[data-testid="viewer-archive"]').waitFor({ timeout: 10000 }).catch(() => note('FAIL', 'archive', 'project.zip drew no table of contents'))
    if (await v.locator('[data-testid="viewer-archive"]').count()) {
      let rows = await rowsOf(v)
      const want = ['pkg-1.0', 'docs', 'src', 'README.md', 'needle-in-docs.txt']
      if (JSON.stringify(rows.map((r) => r.text)) !== JSON.stringify(want)) {
        note('FAIL', 'archive', `the zip opens as ${JSON.stringify(rows.map((r) => r.text))}, want the wrapping directory open and ${JSON.stringify(want.slice(1))} folded inside it`)
      }
      // A zip's DOS time is the time as it was written, in no zone: shown as
      // written, not moved by this browser's offset. A directory the zip only
      // implies has no time.
      const times = rows.map((r) => r.when)
      if (JSON.stringify(times) !== JSON.stringify(['—', '—', '—', '2024-03-01 12:30', '2024-03-01 12:30'])) note('FAIL', 'archive', `the time column reads ${JSON.stringify(times)}, want the files at 2024-03-01 12:30 as written`)
      const count = await v.locator('[data-testid="archive-count"]').textContent()
      if (!/\b5\b/.test(count) || !/\b4\b/.test(count)) note('FAIL', 'archive', `the footer counts ${JSON.stringify(count)}, want 5 files and 4 directories`)
      if (!/ZIP/.test(await v.locator('[data-testid="viewer-format"]').textContent())) note('FAIL', 'archive', 'the footer does not say ZIP')
      // Opening a folded directory by its row.
      await v.locator('[data-entry="pkg-1.0/docs/"] [data-testid="archive-toggle"]').click()
      rows = await rowsOf(v)
      if (!rows.some((r) => r.text === 'guide.md' && r.level === 3)) note('FAIL', 'archive', 'opening docs/ does not show guide.md inside it')
      // Find opens the directories of the match and outlines it.
      await page.keyboard.press('Control+f')
      await page.keyboard.type('strings')
      await page.keyboard.press('Enter')
      const cur = v.locator('[data-testid="viewer-archive"] [data-current="true"]')
      await cur.waitFor({ timeout: 3000 }).catch(() => {})
      const found = (await cur.count()) ? await cur.evaluate((r) => ({ text: r.firstElementChild.textContent, inView: (() => {
        const a = r.getBoundingClientRect(), b = r.closest('[data-testid="viewer-archive"]').getBoundingClientRect()
        return a.top >= b.top && a.bottom <= b.bottom
      })() })) : null
      if (found?.text !== 'strings.go' || !found.inView) note('FAIL', 'archive', `find for "strings" reached ${JSON.stringify(found)}, want strings.go opened and in view`)
      const n = await v.locator('[data-testid="viewer-find-count"]').textContent()
      if (!/1/.test(n)) note('FAIL', 'archive', `find counts ${JSON.stringify(n)}`)
      await page.keyboard.press('Escape')
      await page.screenshot({ path: join(SHOTS, 'archive.png') })
    }
    await page.keyboard.press('Escape')

    await filesAtRoot(page)
    v = await openFile(page, 'archives/climbs.tar')
    {
      const rows = await rowsOf(v).catch(() => [])
      const bad = rows.filter((r) => r.unsafe).map((r) => r.text.replace(/\s*\S+$/, '').trim())
      const marked = rows.filter((r) => r.unsafe).length
      if (marked !== 2 || !rows.some((r) => r.text.startsWith('../escape.txt')) || !rows.some((r) => r.text.startsWith('/etc/absolute.txt'))) {
        note('FAIL', 'archive', `climbs.tar: ${marked} names marked as escaping (${JSON.stringify(bad)}), want ../escape.txt and /etc/absolute.txt whole`)
      }
      if (rows.find((r) => r.text === 'fine.txt')?.unsafe !== false) note('FAIL', 'archive', 'climbs.tar: fine.txt is missing or marked')
      if (rows.some((r) => r.text === '..' || r.text === 'etc')) note('FAIL', 'archive', 'climbs.tar: an escaping name was filed as a directory')
    }
    await page.keyboard.press('Escape')

    for (const [f, fmt, noteWanted] of [['archives/log.gz', 'gzip', true], ['archives/bundle.tar.gz', 'tar.gz', false]]) {
      await filesAtRoot(page)
      v = await openFile(page, f)
      const got = await v.locator('[data-testid="viewer-format"]').textContent().catch(() => null)
      if (got !== fmt) note('FAIL', 'archive', `${f}: the footer says ${JSON.stringify(got)}, want ${fmt}`)
      if ((await v.locator('[data-testid="archive-note"]').count()) !== (noteWanted ? 1 : 0)) note('FAIL', 'archive', `${f}: the size note is ${noteWanted ? 'missing' : 'there'}`)
      await page.keyboard.press('Escape')
    }

    await filesAtRoot(page)
    const t0 = Date.now()
    v = await openFile(page, 'large/ten-thousand-and-one.tar')
    await v.locator('[data-testid="viewer-archive"]').waitFor({ timeout: 10000 }).catch(() => {})
    const ms = Date.now() - t0
    const cut = await v.locator('[data-testid="preview-truncated"]').textContent().catch(() => null)
    const drawn = await v.locator('[data-testid="viewer-archive"] [role="treeitem"]').count()
    if (!cut || !/10[,.]?000/.test(cut)) note('FAIL', 'archive', `10,001 entries: the footer says ${JSON.stringify(cut)}, want that the first 10,000 are listed`)
    if (drawn !== 10000) note('FAIL', 'archive', `10,001 entries: ${drawn} rows, want 10,000`)
    note(ms > 4000 ? 'WARN' : 'PASS', 'archive', `10,000 entries listed and drawn in ${ms} ms`)
    await page.keyboard.press('Escape')

    // A database. Its bytes, and the directory it is in, before and after.
    const dbPath = join(PROJ, 'db/app.sqlite')
    const before = readFileSync(dbPath)
    await filesAtRoot(page)
    v = await openFile(page, 'db/app.sqlite')
    const strip = v.locator('[data-testid="sqlite-tables"]')
    await strip.waitFor({ timeout: 10000 }).catch(() => note('FAIL', 'sqlite', 'app.sqlite drew no tables'))
    if (await strip.count()) {
      const tabs = await v.locator('[data-testid="sqlite-table"]').evaluateAll((els) => els.map((e) => [e.dataset.name, e.getAttribute('aria-selected')]))
      const names = tabs.map(([n]) => n)
      if (JSON.stringify(names) !== JSON.stringify(['adults', 'empty', 'orders', 'people'])) note('FAIL', 'sqlite', `the strip is ${JSON.stringify(names)}`)
      if (tabs[0]?.[1] !== 'true') note('FAIL', 'sqlite', 'the first table is not the one shown')
      const cells = () =>
        v.locator('[data-testid="viewer-table"]').evaluate((el) =>
          [...el.querySelectorAll('tbody tr')].slice(0, 6).map((tr) =>
            [...tr.querySelectorAll('td[data-cell]')].map((td) => ({ text: td.textContent, stand: td.dataset.stand === 'true' })),
          ),
        )
      const wait = (name) =>
        page.waitForFunction(([sel, name]) => {
          const t = document.querySelector(`${sel} [data-testid="sqlite-table"][aria-selected="true"]`)
          // The strip flips on the click; the rows arrive a fetch later and say
          // which table they are. Both, or the cells read are the last table's.
          const body = document.querySelector(`${sel} [data-testid="sqlite-body"]`)
          return t?.dataset.name === name && body?.dataset.table === name && !document.querySelector(`${sel} [aria-busy="true"]`)
        }, [VIEWER, name], { timeout: 5000 }).catch(() => note('FAIL', 'sqlite', `${name} did not become the table shown`))
      await v.locator('[data-testid="sqlite-table"][data-name="people"]').click()
      await wait('people')
      const c = await cells()
      // id, name, age, photo, note, big, score
      const want = [
        [c[0]?.[3], 'BLOB', true],
        [c[1]?.[1], 'NULL', true],
        [c[2]?.[4], 'NULL', false],
        [c[5]?.[6], '+Inf', false],
        [c[4]?.[5], '1152921504606846976', false],
      ]
      for (const [got, text, stand] of want) {
        if (!got || !got.text.startsWith(text) || got.stand !== stand) note('FAIL', 'sqlite', `a cell reads ${JSON.stringify(got)}, want ${JSON.stringify(text)}${stand ? ' as a stand-in' : ' as text'}`)
      }
      if (!c[3]?.[4]?.text.endsWith('…')) note('FAIL', 'sqlite', 'a text cut at 1,000 characters does not say so')
      const rowsText = await v.locator('[data-testid="sqlite-rows"]').textContent()
      if (!/200/.test(rowsText) || !/250/.test(rowsText)) note('FAIL', 'sqlite', `the footer says ${JSON.stringify(rowsText)}, want the first 200 of 250`)
      // The arrows step the strip; the last stays.
      await v.locator('[data-testid="viewer-table"]').focus()
      await page.keyboard.press('ArrowRight')
      await sleep(200)
      if ((await v.locator('[data-testid="sqlite-table"][aria-selected="true"]').getAttribute('data-name')) !== 'people') note('FAIL', 'sqlite', '→ past the last table moved')
      await page.keyboard.press('ArrowLeft')
      await wait('orders')
      if ((await v.locator('[data-testid="viewer-table"] tbody tr').count()) !== 10) note('FAIL', 'sqlite', 'orders does not show its 10 rows')
      await page.keyboard.press('ArrowLeft')
      await wait('empty')
      if (!(await v.locator('[data-testid="sqlite-no-rows"]').count())) note('FAIL', 'sqlite', 'an empty table does not say so')
      // Find in the rows.
      await v.locator('[data-testid="sqlite-table"][data-name="people"]').click()
      await wait('people')
      await page.keyboard.press('Control+f')
      await page.keyboard.type('person 25')
      await sleep(200)
      const n = await v.locator('[data-testid="viewer-find-count"]').textContent()
      if (!/\b1\b/.test(n)) note('FAIL', 'sqlite', `find "person 25" counts ${JSON.stringify(n)}, want 1 of the rows shown`)
      await page.keyboard.press('Escape')
      // Source is the schema.
      await page.keyboard.press('s')
      const schema = await v.locator('[data-testid="viewer-text"]').textContent().catch(() => '')
      if (!schema.includes('CREATE TABLE people') || !schema.includes('CREATE VIEW adults')) note('FAIL', 'sqlite', `Source reads ${JSON.stringify(schema.slice(0, 80))}`)
      await page.keyboard.press('s')
      await page.screenshot({ path: join(SHOTS, 'sqlite.png') })
    }
    await page.keyboard.press('Escape')
    const after = readFileSync(dbPath)
    const beside = readdirSync(join(PROJ, 'db')).filter((f) => f !== 'app.sqlite')
    if (!before.equals(after)) note('FAIL', 'sqlite', 'reading the database changed it')
    if (beside.length) note('FAIL', 'sqlite', `reading the database left ${JSON.stringify(beside)} beside it`)

    await filesAtRoot(page)
    v = await openFile(page, 'bin/sqlite.db')
    if (!(await v.locator('[data-testid="preview-unavailable"]').count())) note('FAIL', 'sqlite', 'a database SQLite cannot read does not say so')
    await page.keyboard.press('Escape')

    if (errors.length) note('FAIL', 'archives and databases', `thrown: ${errors[0].slice(0, 200)}`)
    note('PASS', 'archives and databases', 'a zip opened inside its wrapping directory, folded, found into; climbing names whole and marked; 10,000 entries and the bound said; a database stepped table by table with NULL and BLOB apart, its schema as Source, the file untouched')
    await ctx.close()
  }

  // ── the Files tab ────────────────────────────────────────────────────────
  // Batch 6: the root is the project's name; one row is in the tab order and
  // current; the arrows move it; Enter on a directory goes in with the focus;
  // Backspace comes back to the directory just left; typing filters by name
  // and Escape clears it; Tab leaves the listing in one step; one icon size.
  {
    const ctx = await contextFor({ width: 1280, height: 800, scheme: 'light' })
    const page = await ctx.newPage()
    await filesAtRoot(page)
    const tree = page.locator('[data-testid="file-tree"]:visible').first()
    const crumbsText = await tree.locator('[data-testid="file-crumbs"]').textContent()
    if (crumbsText?.trim() !== 'fixtures') note('FAIL', 'tree', `the root reads ${JSON.stringify(crumbsText)}, not the project's name`)
    const sizes = await tree.evaluate((t) => [...new Set([...t.querySelectorAll('svg')].map((s) => s.getAttribute('width')))])
    if (sizes.length !== 1) note('FAIL', 'tree', `icons in ${sizes.length} sizes: ${sizes.join(', ')}`)
    const tabbable = await tree.locator('[data-testid="file-open"][tabindex="0"]').count()
    if (tabbable !== 1) note('FAIL', 'tree', `${tabbable} rows in the tab order, want 1`)

    const current = () => tree.locator('[data-testid="file-entry"][aria-current="true"]').getAttribute('title')
    const focused = () => page.evaluate(() => document.activeElement?.closest('[data-testid="file-entry"]')?.getAttribute('title') ?? null)
    await tree.locator('[data-testid="file-open"][tabindex="0"]').focus()
    const first = await current()
    await page.keyboard.press('ArrowDown')
    const second = await current()
    if (second === first || (await focused()) !== second) note('FAIL', 'tree', `ArrowDown: ${first} -> ${second}, focus on ${await focused()}`)
    await page.keyboard.press('End')
    await page.keyboard.press('Home')
    if ((await current()) !== first) note('FAIL', 'tree', 'Home did not return to the first row')

    // Into a directory and back out by the keyboard.
    for (let i = 0; i < 30 && (await current()) !== 'src'; i++) await page.keyboard.press('ArrowDown')
    await page.keyboard.press('Enter')
    await tree.locator('[data-testid="file-entry"][title="src/app.ts"]').waitFor({ timeout: 5000 })
    await page.waitForFunction(() => document.activeElement?.closest('[data-testid="file-entry"]')?.getAttribute('title')?.startsWith('src/'), null, { timeout: 3000 })
      .catch(() => note('FAIL', 'tree', 'Enter on a directory did not take the focus in with it'))
    await page.keyboard.press('Backspace')
    await page.waitForFunction(() => document.activeElement?.closest('[data-testid="file-entry"]')?.getAttribute('title') === 'src', null, { timeout: 3000 })
      .catch(() => note('FAIL', 'tree', 'Backspace did not land on the directory it left'))

    // Typing filters.
    await page.keyboard.type('read')
    await page.screenshot({ path: join(SHOTS, 'tree-filter.png') })
    const shown = await tree.locator('[data-testid="file-entry"]').evaluateAll((rows) => rows.map((r) => r.getAttribute('title')))
    if (shown.join(',') !== 'README.md') note('FAIL', 'tree', `typing "read" left ${shown.join(', ')}`)
    if (!(await tree.locator('[data-testid="file-filter"]').isVisible())) note('FAIL', 'tree', 'the filter is not shown')
    await page.keyboard.press('Escape')
    const all = await tree.locator('[data-testid="file-entry"]').count()
    if (all < 10) note('FAIL', 'tree', `Escape left ${all} rows`)
    if (await tree.locator('[data-testid="file-filter"]').count()) note('FAIL', 'tree', 'Escape did not clear the filter')

    // Tab leaves the listing in one step.
    await tree.locator('[data-testid="file-open"][tabindex="0"]').focus()
    await page.keyboard.press('Tab')
    const left = await page.evaluate(() => !document.activeElement?.closest('[data-testid="file-list"]'))
    if (!left) note('FAIL', 'tree', 'Tab moved to another row instead of leaving the listing')
    await page.screenshot({ path: join(SHOTS, 'tree-keys.png') })
    note('PASS', 'tree', 'the root by name, one icon size, one row in the tab order, arrows, Enter in and Backspace back to the directory left, typing filters and Escape clears')
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

    // One line of two megabytes opens without freezing the page: it is drawn
    // cut, find still reaches its end, and stepping there opens the line.
    await filesAtRoot(page)
    const t1 = Date.now()
    const lv = await openFile(page, 'large/bundle.min.js')
    await lv.locator('[data-line="0"]').waitFor({ timeout: 15000 })
    const longOpen = Date.now() - t1
    if (longOpen > 3000) note('FAIL', 'large', `a two-megabyte line took ${longOpen} ms to open`)
    if (!(await lv.locator('[data-testid="viewer-long-line"]').count())) note('FAIL', 'large', 'a two-megabyte line is not cut')
    await page.keyboard.press('Control+f')
    await lv.locator('[data-testid="viewer-find"]').fill('needle_at_the_end')
    await page.keyboard.press('Enter')
    const opened = await lv.locator('mark[data-current="true"]').waitFor({ timeout: 10000 }).then(() => true).catch(() => false)
    if (!opened) note('FAIL', 'large', 'find did not open the long line to its match')
    note('PASS', 'large', `a two-megabyte line opened in ${longOpen} ms, cut, with find reaching its end`)
    await page.keyboard.press('Escape')
    await page.keyboard.press('Escape')

    // The cut file says how much of it is on screen.
    await filesAtRoot(page)
    await openFile(page, 'large/three-megabytes.log')
    const cut = await page.locator('[data-testid="preview-truncated"]').textContent().catch(() => null)
    if (!cut || !/2(\.0)?M/.test(cut)) note('FAIL', 'large', `a file past 2 MB says ${JSON.stringify(cut)} about being cut`)
    await ctx.close()
  }

  // ── editing ──────────────────────────────────────────────────────────────
  // docs/file-preview.md, "Editing": `e` opens the editor on a text file the
  // server sent whole; Ctrl+S saves against the version it was read at and
  // the file on disk is what was typed, its line endings and byte order mark
  // kept; a file changed on disk since is refused and offered a reload, and
  // the disk keeps the other writer's bytes; unsaved edits are asked about
  // before they are dropped, in a question that is on top. Last, and in a
  // directory of its own, because it writes files.
  {
    mkdirSync(join(PROJ, 'edit'), { recursive: true })
    const disk = (f) => readFileSync(join(PROJ, 'edit', f))
    writeFileSync(join(PROJ, 'edit/note.ts'), 'export const a = 1\n')
    writeFileSync(join(PROJ, 'edit/crlf.txt'), 'one\r\ntwo\r\n')
    writeFileSync(join(PROJ, 'edit/bom.txt'), Buffer.concat([Buffer.from([0xef, 0xbb, 0xbf]), Buffer.from('hello\n')]))
    writeFileSync(join(PROJ, 'edit/mixed.txt'), 'a\r\nb\n')
    for (const scheme of ['light', 'dark']) {
      const ctx = await contextFor({ width: 1280, height: 800, scheme })
      const page = await ctx.newPage()
      const status = (v) => v.locator('[data-testid="edit-status"]').getAttribute('data-status').catch(() => null)
      const waitStatus = (v, want) =>
        page.waitForFunction(([sel, w]) => document.querySelector(`${sel} [data-testid="edit-status"]`)?.dataset.status === w, [VIEWER, want], { timeout: 8000 })
          .then(() => true, () => false)
      await filesAtRoot(page)
      let v = await openFile(page, 'edit/note.ts')
      const button = v.locator('[data-testid="viewer-edit"]')
      if (!(await button.isEnabled().catch(() => false))) note('FAIL', 'edit', `${scheme}: the Edit button is not there or not enabled on a small text file`)
      await page.keyboard.press('e')
      const editor = v.locator('[data-testid="editor-content"]')
      if (!(await editor.waitFor({ timeout: 10000 }).then(() => true, () => false))) {
        note('FAIL', 'edit', `${scheme}: \`e\` did not open the editor`)
        await ctx.close()
        continue
      }
      const focused = await page.evaluate(() => document.activeElement?.getAttribute('data-testid'))
      if (focused !== 'editor-content') note('FAIL', 'edit', `${scheme}: the editor opened without the focus (${focused})`)
      // The viewer's letters are the editor's now.
      await page.keyboard.press('Control+End')
      await page.keyboard.type('export const wne = 2\n')
      if (await v.locator('[data-testid="viewer-findbar"]').count()) note('FAIL', 'edit', `${scheme}: typing opened the find bar`)
      if (!(await v.locator('[data-testid="viewer-dirty"]').count())) note('FAIL', 'edit', `${scheme}: no dot after the name with unsaved changes`)
      // The grammar coloured it, in the theme's token colour.
      await page.waitForFunction((sel) => [...document.querySelectorAll(`${sel} .cm-line span`)].some((s) => getComputedStyle(s).color !== getComputedStyle(s.closest('.cm-content')).color), VIEWER, { timeout: 8000 })
        .catch(() => note('FAIL', 'edit', `${scheme}: the TypeScript in the editor has no colour`))
      await page.screenshot({ path: join(SHOTS, `edit-${scheme}.png`) })
      await page.keyboard.press('Control+s')
      if (!(await waitStatus(v, 'saved'))) note('FAIL', 'edit', `${scheme}: Ctrl+S did not save (${await status(v)})`)
      const saved = disk('note.ts').toString()
      if (saved !== 'export const a = 1\nexport const wne = 2\n') note('FAIL', 'edit', `${scheme}: the disk holds ${JSON.stringify(saved)}`)
      if (await v.locator('[data-testid="viewer-dirty"]').count()) note('FAIL', 'edit', `${scheme}: the dot stayed after a save`)
      // Esc leaves the editor, and the view is the saved text.
      await page.keyboard.press('Escape')
      await v.locator('[data-testid="viewer-body"]').waitFor({ timeout: 5000 }).catch(() => {})
      const shown = await v.locator('[data-testid="viewer-body"]').textContent().catch(() => '')
      if (!shown.includes('wne')) note('FAIL', 'edit', `${scheme}: after the save the view does not show it`)

      // Somebody else writes the file; the save is refused and the disk keeps theirs.
      await page.keyboard.press('e')
      await editor.waitFor({ timeout: 10000 })
      await page.keyboard.press('Control+End')
      await page.keyboard.type('// mine\n')
      writeFileSync(join(PROJ, 'edit/note.ts'), 'export const theirs = 3\n')
      await page.keyboard.press('Control+s')
      if (!(await waitStatus(v, 'changed'))) note('FAIL', 'edit', `${scheme}: a save over a file changed on disk was not refused (${await status(v)})`)
      if (disk('note.ts').toString() !== 'export const theirs = 3\n') note('FAIL', 'edit', `${scheme}: the refused save changed the disk: ${JSON.stringify(disk('note.ts').toString())}`)
      await v.locator('[data-testid="edit-reload"]').click()
      const reloaded = await page.waitForFunction((sel) => document.querySelector(`${sel} [data-testid="editor-content"]`)?.textContent.includes('theirs'), VIEWER, { timeout: 8000 }).then(() => true, () => false)
      if (!reloaded) note('FAIL', 'edit', `${scheme}: Reload did not bring in the file as it is on disk`)
      if (await v.locator('[data-testid="viewer-dirty"]').count()) note('FAIL', 'edit', `${scheme}: dirty after a reload`)
      // And the reloaded version saves.
      await page.keyboard.press('Control+End')
      await page.keyboard.type('// after\n')
      await page.keyboard.press('Control+s')
      if (!(await waitStatus(v, 'saved')) || disk('note.ts').toString() !== 'export const theirs = 3\n// after\n') note('FAIL', 'edit', `${scheme}: a save after the reload did not land: ${JSON.stringify(disk('note.ts').toString())}`)

      // Unsaved edits: Esc asks, and Cancel keeps them; closing asks, and
      // the question is on top of the viewer.
      await page.keyboard.type('// unsaved')
      await page.keyboard.press('Escape')
      const ask = page.locator('[data-testid="confirm-backdrop"]')
      if (!(await ask.waitFor({ timeout: 3000 }).then(() => true, () => false))) {
        note('FAIL', 'edit', `${scheme}: Esc with unsaved edits asked nothing`)
      } else {
        const onTop = await page.evaluate(() => {
          const panel = document.querySelector('[data-testid="confirm-dialog"]')
          const r = panel.getBoundingClientRect()
          return panel.contains(document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2))
        })
        if (!onTop) note('FAIL', 'edit', `${scheme}: the question is under the viewer`)
        await page.keyboard.press('Escape')
        await sleep(200)
        if (!(await editor.count())) note('FAIL', 'edit', `${scheme}: answering no to the question left the editor`)
      }
      await v.locator('[data-testid="preview-close"]').click()
      if (!(await ask.waitFor({ timeout: 3000 }).then(() => true, () => false))) note('FAIL', 'edit', `${scheme}: closing with unsaved edits asked nothing`)
      else {
        await page.locator('[data-testid="confirm-yes"]').click()
        await v.waitFor({ state: 'detached', timeout: 5000 }).catch(() => note('FAIL', 'edit', `${scheme}: dropping the edits did not close the viewer`))
      }
      if (disk('note.ts').toString().includes('unsaved')) note('FAIL', 'edit', `${scheme}: dropped edits reached the disk`)
      writeFileSync(join(PROJ, 'edit/note.ts'), 'export const a = 1\n')
      await ctx.close()
    }

    // Line endings and a byte order mark survive a save.
    const ctx = await contextFor({ width: 1280, height: 800, scheme: 'light' })
    const page = await ctx.newPage()
    for (const [f, want] of [
      ['crlf.txt', Buffer.from('one\r\ntwo\r\nthree\r\n')],
      ['bom.txt', Buffer.concat([Buffer.from([0xef, 0xbb, 0xbf]), Buffer.from('hello\nthree\n')])],
    ]) {
      await filesAtRoot(page)
      const v = await openFile(page, `edit/${f}`)
      await page.keyboard.press('e')
      await v.locator('[data-testid="editor-content"]').waitFor({ timeout: 10000 })
      await page.keyboard.press('Control+End')
      await page.keyboard.type('three\n')
      await page.keyboard.press('Control+s')
      await page.waitForFunction((sel) => document.querySelector(`${sel} [data-testid="edit-status"]`)?.dataset.status === 'saved', VIEWER, { timeout: 8000 }).catch(() => {})
      const got = disk(f)
      if (!got.equals(want)) note('FAIL', 'edit', `${f}: saved as ${JSON.stringify(got.toString('latin1'))}, want ${JSON.stringify(want.toString('latin1'))}`)
      await page.keyboard.press('Escape')
      await page.keyboard.press('Escape')
    }
    // What cannot be edited says why, on a button that does nothing.
    for (const [f, why] of [['large/three-megabytes.log', 'cut'], ['encodings/utf16.txt', 'encoding'], ['edit/mixed.txt', 'mixed']]) {
      await filesAtRoot(page)
      const v = await openFile(page, f)
      const b = v.locator('[data-testid="viewer-edit"]')
      const state = await b.evaluate((el) => ({ disabled: el.disabled, title: el.title })).catch(() => null)
      if (!state || !state.disabled || !state.title) note('FAIL', 'edit', `${f} (${why}): the Edit button is ${JSON.stringify(state)}, want disabled with a reason`)
      await page.keyboard.press('e')
      if (await v.locator('[data-testid="editor-content"]').count()) note('FAIL', 'edit', `${f} (${why}): \`e\` opened the editor`)
      await page.keyboard.press('Escape')
    }
    await ctx.close()
    note('PASS', 'edit', 'in both themes: `e`, typing that is the editor\'s, a coloured grammar, Ctrl+S to disk, Esc back to the saved view; a file changed on disk refused, kept, reloaded and saved over; unsaved edits asked about, on top, and never written; CRLF and a byte order mark kept; a cut, a UTF-16 and a mixed-endings file not editable, with the reason')
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
