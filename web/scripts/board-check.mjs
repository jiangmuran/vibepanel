// Every board preset, on every screen one gets put on.
//
// This existed once as a scratch file and found more than forty layout defects
// across thirty presets and seven viewports. It was never committed, the
// temporary directory holding it was cleared, and the next report from a tablet
// arrived with nothing to run. So it lives here now.
//
// What it measures, and why each one rather than "does it look right":
//
//   scale spread   Within a filled board every tile computes its own type size
//                  from `6.5cqmin`, the tile's *shorter* side. A wide short
//                  strip beside a tall block therefore lands at the 13px floor
//                  next to a neighbour at 36px, in the same board, at the same
//                  viewing distance. Measured on a tablet: 13px and 35.7px side
//                  by side. That is 「有的地方满有的地方空」 in one number, and
//                  a screenshot review will not produce it.
//
//   clipping       Content taller or wider than the box that hides it. The
//                  threshold scales with the type: a glyph's ink box exceeds
//                  its line box by a few percent on a correctly rendered
//                  figure, so a flat pixel count reports every large number in
//                  the product as broken. Measured at 3px against a 75px font.
//
//   overflow       The document taller than the viewport on a board declared
//                  `fill`. Those are drawn to occupy a screen nobody will
//                  scroll; if one scrolls, the bottom of it does not exist.
//
//   emptiness      The fraction of a tile its content actually uses. A wall is
//                  meant to be airy and this is not a failure on its own, which
//                  is why it is reported rather than failed on.
//
// Run: node scripts/board-check.mjs            (against a panel it starts)
//      VP_BOARD_SIZES=ipad-landscape node ...  (one viewport)
//      VP_BOARD_PRESETS=glance,wall node ...   (some presets)

import { chromium } from 'playwright'
import { spawn, execSync } from 'node:child_process'
import { mkdtempSync, mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const BIN = join(process.cwd(), '..', 'vibepanel')
const PORT = 18996
const BASE = `http://127.0.0.1:${PORT}`

/** The screens a board actually gets put on, and why each is in the list. */
const SIZES = {
  // The two the presets were drawn for.
  'tv-1080': { width: 1920, height: 1080 },
  'tv-4k-scaled': { width: 2560, height: 1440 },
  // A laptop, which is what most people first open a share link on.
  laptop: { width: 1440, height: 900 },
  // Both tablet orientations. Not narrow -- NARROW_QUERY is 767px -- so a
  // tablet gets the full board and every one of its assumptions.
  'ipad-portrait': { width: 820, height: 1180 },
  'ipad-landscape': { width: 1180, height: 820 },
  // A phone, where `fill` is deliberately turned off.
  phone: { width: 390, height: 844 },
  // A portrait panel screwed to a wall, which is what "kiosk" means here.
  'kiosk-portrait': { width: 1080, height: 1920 },
}

/** Type sizes within one board that differ by more than this read as two boards. */
const SPREAD_LIMIT = 2.0

const findings = []
const note = (sev, where, msg) => {
  findings.push({ sev, where, msg })
  console.log(`[${sev}] ${where}: ${msg}`)
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

function wanted(name, envVar) {
  const raw = process.env[envVar]
  if (!raw) return true
  return raw.split(',').map((s) => s.trim()).includes(name)
}

const work = mkdtempSync(join(tmpdir(), 'vpboard-'))
const shots = join(work, 'shots')
mkdirSync(shots, { recursive: true })

let server
try {
  server = spawn(BIN, ['serve', '-addr', `127.0.0.1:${PORT}`, '-data-dir', work,
    '-tmux-socket', 'vp-board-check'], { stdio: ['ignore', 'pipe', 'pipe'] })
  let log = ''
  server.stdout.on('data', (d) => { log += d })
  server.stderr.on('data', (d) => { log += d })

  // The one-time setup token, out of the log the server just printed.
  let token = ''
  for (let i = 0; i < 40 && !token; i++) {
    await sleep(300)
    token = (/^ {6}([A-Za-z0-9_-]{30,})$/m.exec(log) ?? [])[1] ?? ''
  }
  if (!token) throw new Error(`no setup token in:\n${log}`)

  const jar = []
  const api = async (path, init = {}) => {
    const res = await fetch(BASE + path, {
      ...init,
      headers: { 'Content-Type': 'application/json', cookie: jar.join('; '), ...(init.headers ?? {}) },
    })
    const set = res.headers.getSetCookie?.() ?? []
    for (const c of set) jar.push(c.split(';')[0])
    return res
  }

  await api('/api/auth/setup', {
    method: 'POST',
    body: JSON.stringify({ token, username: 'board', password: 'board-check-12345' }),
  })
  // A project, so the widgets that count things have something to count.
  await api('/api/projects', {
    method: 'POST',
    body: JSON.stringify({ path: process.cwd(), name: 'board-check' }),
  })

  // Every preset the server will admit to, rather than a list kept here: a
  // preset added to internal/store/presets.go and not to this file is exactly
  // the one nobody looked at.
  const cat = await (await api('/api/settings/shares/catalogue')).json()
  const presets = (cat.presets ?? []).map((p) => p.id ?? p).filter((id) => wanted(id, 'VP_BOARD_PRESETS'))
  if (presets.length === 0) throw new Error('the catalogue named no presets')

  const links = {}
  for (const id of presets) {
    const res = await api('/api/settings/shares', {
      method: 'POST',
      body: JSON.stringify({ name: id, detail: 'counts', expiresIn: 3600, preset: id }),
    })
    if (!res.ok) { note('FAIL', `preset/${id}`, `share refused: ${res.status} ${await res.text()}`); continue }
    links[id] = (await res.json()).token
  }

  const browser = await chromium.launch({ headless: true })
  const sizes = Object.entries(SIZES).filter(([n]) => wanted(n, 'VP_BOARD_SIZES'))
  console.log(`presets: ${Object.keys(links).length}  sizes: ${sizes.map(([n]) => n).join(',')}\n`)

  for (const [id, tok] of Object.entries(links)) {
    for (const [sn, viewport] of sizes) {
      const ctx = await browser.newContext({ viewport, colorScheme: 'dark' })
      const page = await ctx.newPage()
      await page.goto(`${BASE}/share/${tok}`, { waitUntil: 'networkidle' })
      await sleep(1800)

      const measure = () => page.evaluate(() => {
        // Visible geometry, not getBoundingClientRect: a rect ignores every
        // clipping ancestor, so once tiles started clipping their hidden
        // content read as colliding. This intersects up the chain.
        const visible = (el) => {
          let r = el.getBoundingClientRect()
          let box = { top: r.top, left: r.left, right: r.right, bottom: r.bottom }
          for (let p = el.parentElement; p; p = p.parentElement) {
            const cs = getComputedStyle(p)
            if (cs.overflow === 'visible' && cs.overflowX === 'visible' && cs.overflowY === 'visible') continue
            const pr = p.getBoundingClientRect()
            box = {
              top: Math.max(box.top, pr.top), left: Math.max(box.left, pr.left),
              right: Math.min(box.right, pr.right), bottom: Math.min(box.bottom, pr.bottom),
            }
          }
          return box
        }

        const board = document.querySelector('.vp-board')
        const fill = board?.getAttribute('data-fill') === 'true'
        const sections = [...document.querySelectorAll('.vp-board > section')]
        const tiles = sections.map((el) => {
          const r = el.getBoundingClientRect()
          const unit = parseFloat(getComputedStyle(el).getPropertyValue('--vp-wall')) || 0
          // How much of the tile the content actually occupies, vertically.
          let top = Infinity, bottom = -Infinity
          for (const c of el.querySelectorAll('*')) {
            const cr = c.getBoundingClientRect()
            if (cr.height <= 0 || cr.width <= 0) continue
            top = Math.min(top, cr.top); bottom = Math.max(bottom, cr.bottom)
          }
          const used = bottom > top ? (bottom - top) / Math.max(r.height, 1) : 0
          return { w: Math.round(r.width), h: Math.round(r.height), unit, used }
        })

        const clipped = []
        for (const el of document.querySelectorAll('.vp-board *')) {
          const cs = getComputedStyle(el)
          const text = (el.textContent ?? '').trim()
          if (!text || el.children.length > 0) continue
          const lh = parseFloat(cs.lineHeight) || parseFloat(cs.fontSize) || 16
          const overY = el.scrollHeight - el.clientHeight
          const overX = el.scrollWidth - el.clientWidth
          if (cs.overflowY === 'hidden' && overY > Math.max(6, lh * 0.2)) {
            clipped.push(`cut off vertically by ${overY}px: ${JSON.stringify(text.slice(0, 30))}`)
          }
          if (cs.overflowX === 'hidden' && cs.textOverflow === 'ellipsis' && overX > 2) {
            clipped.push(`truncated: ${JSON.stringify(text.slice(0, 30))}`)
          }
          // Only for something that was laid out in the first place. An
          // element with no box of its own is `display:none`, a rotating
          // board's off-turn panel, or a branch that chose not to render --
          // none of which is content being hidden by a container too small for
          // it, which is what this is looking for. Without the guard the first
          // run reported fifty-seven of them and every one was a false one.
          const own = el.getBoundingClientRect()
          if (own.width > 0 && own.height > 0) {
            const box = visible(el)
            if (box.bottom - box.top < 1 || box.right - box.left < 1) {
              clipped.push(`clipped away entirely: ${JSON.stringify(text.slice(0, 30))}`)
            }
          }
        }
        return {
          fill, tiles, clipped,
          docH: document.documentElement.scrollHeight,
          vpH: window.innerHeight,
        }
      })

      // Twice, and only what both agree on.
      //
      // Boards animate in -- staggered rows, bars growing from zero -- and a
      // single reading catches whatever was mid-flight. The first run of this
      // reported thirteen clipped elements in one board that a screenshot
      // showed as perfectly laid out, and running the same board on its own
      // reported one. A transient is exactly the finding that wastes the most
      // time, because chasing it means looking for a bug that is not there.
      const m = await measure()
      await sleep(1200)
      const again = await measure()
      const stable = new Set(again.clipped)
      m.clipped = m.clipped.filter((c) => stable.has(c))

      const where = `${id}/${sn}`
      const units = m.tiles.map((t) => t.unit).filter((u) => u > 0)
      if (units.length > 1) {
        const lo = Math.min(...units), hi = Math.max(...units)
        if (hi / lo > SPREAD_LIMIT) {
          note('FAIL', where,
            `type sizes differ by ${(hi / lo).toFixed(1)}x within one board ` +
            `(${lo.toFixed(1)}px to ${hi.toFixed(1)}px); the tiles do not read as one screen`)
        }
      }
      if (m.fill && m.docH > m.vpH + 4) {
        note('FAIL', where, `a fill board scrolls: ${m.docH}px of content in ${m.vpH}px`)
      }
      for (const c of [...new Set(m.clipped)]) {
        // Truncation and vertical cut-off are pinned to the box the browser
        // itself reports as overflowing, and they agree with what a screenshot
        // shows. "Clipped away entirely" is a geometry heuristic and does not
        // yet: it reported thirteen elements in a board that looks correct, so
        // until it is understood it is a lead rather than a verdict. A check
        // nobody trusts is a check everybody learns to skip past.
        note(c.startsWith('clipped away entirely') ? 'WARN' : 'FAIL', where, c)
      }

      const airy = m.tiles.filter((t) => t.h > 120 && t.used > 0 && t.used < 0.35)
      if (airy.length > 0) {
        note('WARN', where,
          `${airy.length} tile(s) use under a third of their height ` +
          `(${airy.map((t) => `${Math.round(t.used * 100)}%`).join(', ')})`)
      }

      await page.screenshot({ path: join(shots, `${id}-${sn}.png`) })
      await ctx.close()
    }
  }
  await browser.close()
} finally {
  if (server) server.kill()
  try { execSync('tmux -L vp-board-check kill-server', { stdio: 'ignore' }) } catch { /* none started */ }
}

const fails = findings.filter((f) => f.sev === 'FAIL').length
const warns = findings.filter((f) => f.sev === 'WARN').length
console.log(`\nshots: ${shots}`)
console.log(`\n=== board check: ${fails} FAIL, ${warns} WARN ===`)
process.exit(fails > 0 ? 1 : 0)
