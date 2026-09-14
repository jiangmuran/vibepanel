import { readFileSync } from 'node:fs'
import { describe, expect, it, vi } from 'vitest'

/**
 * The share page SDK, run the way a page runs it: one script, one global, a
 * window it did not choose.
 *
 * It lives in internal/pages/sdk because the panel serves it out of the binary,
 * and it is tested here because this is where the JavaScript tests are. The
 * window below is a fake with exactly the surface the SDK touches -- timers
 * that run when the test says, a fetch that answers what the test says -- so
 * what is under test is the SDK's decisions and not a browser's scheduling.
 * What only a browser can say (the sandbox, postMessage, the badge's pixels)
 * is scripts/pages-check.mjs's job.
 */

const SRC = readFileSync(new URL('../../internal/pages/sdk/vibepanel.js', import.meta.url), 'utf8')
const TOKEN = 'tok_0123456789abcdefghijklmnop'

interface Timer {
  fn: () => void
  ms: number
}

function snapshot(over: Record<string, unknown> = {}) {
  return {
    v: 1,
    page: { id: 'p1', version: 3, draft: false },
    sections: ['sessions'],
    params: { title: 'Lobby' },
    at: 1_000_000,
    name: 'wall',
    detail: 'counts',
    stale: false,
    counts: { waiting: 1 },
    sessions: [],
    ...over,
  }
}

function answer(status: number, body: unknown) {
  return Promise.resolve({ status, ok: status >= 200 && status < 300, json: () => Promise.resolve(body) })
}

function load(href = `https://panel.example/share/${TOKEN}/`) {
  const timers: Timer[] = []
  const fetch = vi.fn()
  const reload = vi.fn()
  const win: Record<string, unknown> = {
    location: { href, reload },
    innerWidth: 1920,
    innerHeight: 1080,
    fetch,
    crypto: globalThis.crypto,
    console: { error: () => {} },
    navigator: { language: 'en' },
    document: { hidden: false, addEventListener: () => {}, documentElement: { lang: 'en' } },
    setTimeout: (fn: () => void, ms: number) => timers.push({ fn, ms }),
    clearTimeout: (id: number) => {
      if (timers[id - 1]) timers[id - 1].fn = () => {}
    },
    addEventListener: () => {},
  }
  win.parent = win
  new Function('window', 'globalThis', 'module', SRC)(win, win, undefined)
  const VibePanel = win.VibePanel as {
    connect: (o?: Record<string, unknown>) => Record<string, any> // eslint-disable-line @typescript-eslint/no-explicit-any
    fmt: Record<string, (...a: unknown[]) => string>
  }
  let ran = 0
  /** Runs every timer queued so far, then lets their promises settle. */
  const step = async () => {
    const due = timers.slice(ran)
    ran = timers.length
    for (const t of due) t.fn()
    for (let i = 0; i < 5; i++) await new Promise((r) => setImmediate(r))
    return due
  }
  return { VibePanel, fetch, reload, timers, step, pending: () => timers.length - ran }
}

describe('the SDK', () => {
  it('polls its own snapshot, without credentials, and says it is live', async () => {
    const env = load()
    env.fetch.mockReturnValue(answer(200, snapshot()))
    const vp = env.VibePanel.connect()
    const statuses: string[] = []
    const seen: unknown[] = []
    vp.on('status', (s: string) => statuses.push(s))
    vp.on('snapshot', (s: unknown) => seen.push(s))
    await env.step()

    const [url, init] = env.fetch.mock.calls[0]
    expect(url).toMatch(new RegExp(`^https://panel.example/api/share/${TOKEN}/v1/snapshot\\?v=[0-9a-f]{16}&w=1920&h=1080$`))
    // A share page never sends a cookie, and the endpoint never reads one.
    expect(init).toEqual({ cache: 'no-store', credentials: 'omit' })
    expect(statuses).toEqual(['connecting', 'live'])
    expect(seen).toHaveLength(1)
    // The next poll is about two seconds away, staggered so a room of screens
    // does not arrive in the same second.
    const next = env.timers.at(-1)!
    expect(next.ms).toBeGreaterThanOrEqual(1600)
    expect(next.ms).toBeLessThanOrEqual(2400)
  })

  it('stops for good when the link is revoked', async () => {
    const env = load()
    env.fetch.mockReturnValue(answer(401, { error: 'this link is not valid' }))
    const vp = env.VibePanel.connect()
    await env.step()
    expect(vp.status).toBe('revoked')
    expect(vp.error).toBe('this link is not valid')
    // Asking again forever is an unauthenticated request in a loop against an
    // endpoint that records every rejection.
    expect(env.pending()).toBe(0)
  })

  it('treats a page that is gone like a revoked link', async () => {
    const env = load()
    env.fetch.mockReturnValue(answer(410, { error: 'this page no longer exists' }))
    const vp = env.VibePanel.connect()
    await env.step()
    expect(vp.status).toBe('revoked')
    expect(env.pending()).toBe(0)
  })

  it('backs off while the panel is unreachable, and tells reconnecting from disconnected', async () => {
    const env = load()
    env.fetch.mockReturnValueOnce(answer(503, { error: 'the panel cannot reach its own database' }))
    const vp = env.VibePanel.connect()
    await env.step()
    // Never live, so not "reconnecting".
    expect(vp.status).toBe('disconnected')
    const first = env.timers.at(-1)!.ms

    env.fetch.mockReturnValueOnce(Promise.reject(new Error('offline')))
    await env.step()
    const second = env.timers.at(-1)!.ms
    expect(second).toBeGreaterThan(first)

    env.fetch.mockReturnValueOnce(answer(200, snapshot()))
    await env.step()
    expect(vp.status).toBe('live')

    env.fetch.mockReturnValueOnce(Promise.reject(new Error('blip')))
    await env.step()
    expect(vp.status).toBe('reconnecting')
    for (let i = 0; i < 12; i++) {
      env.fetch.mockReturnValueOnce(Promise.reject(new Error('down')))
      await env.step()
    }
    expect(env.timers.at(-1)!.ms).toBeLessThanOrEqual(30000 * 1.2)
  })

  it('reloads when a new version is published, and not for a draft', async () => {
    const env = load()
    env.fetch.mockReturnValueOnce(answer(200, snapshot()))
    env.VibePanel.connect()
    await env.step()
    env.fetch.mockReturnValueOnce(answer(200, snapshot({ page: { id: 'p1', version: 4, draft: false } })))
    await env.step()
    expect(env.reload).not.toHaveBeenCalled()
    await env.step()
    expect(env.reload).toHaveBeenCalledTimes(1)

    const draft = load()
    draft.fetch.mockReturnValue(answer(200, snapshot({ page: { id: 'p1', version: 0, draft: true } })))
    draft.VibePanel.connect()
    await draft.step()
    draft.fetch.mockReturnValue(answer(200, snapshot({ page: { id: 'p2', version: 0, draft: true } })))
    await draft.step()
    await draft.step()
    expect(draft.reload).not.toHaveBeenCalled()
  })

  it('reloads when a link that drew nothing is pointed at a page', async () => {
    const env = load()
    env.fetch.mockReturnValueOnce(answer(200, snapshot({ page: null })))
    env.VibePanel.connect()
    await env.step()
    env.fetch.mockReturnValueOnce(answer(200, snapshot()))
    await env.step()
    await env.step()
    expect(env.reload).toHaveBeenCalledTimes(1)
  })

  it('announces parameters only when they change', async () => {
    const env = load()
    env.fetch.mockReturnValue(answer(200, snapshot()))
    const vp = env.VibePanel.connect()
    const got: unknown[] = []
    vp.on('params', (p: unknown) => got.push(p))
    await env.step()
    await env.step()
    env.fetch.mockReturnValue(answer(200, snapshot({ params: { title: 'Kitchen' } })))
    await env.step()
    expect(got).toEqual([{ title: 'Lobby' }, { title: 'Kitchen' }])
  })

  it('keeps drawing when one listener throws', async () => {
    const env = load()
    env.fetch.mockReturnValue(answer(200, snapshot()))
    const vp = env.VibePanel.connect()
    const after = vi.fn()
    vp.on('snapshot', () => {
      throw new Error('a broken widget')
    })
    vp.on('snapshot', after)
    await env.step()
    expect(after).toHaveBeenCalledTimes(1)
    expect(vp.status).toBe('live')
  })

  it('says so when it has no token to ask with', async () => {
    const env = load('https://somewhere.example/wall.html')
    const vp = env.VibePanel.connect()
    await env.step()
    expect(vp.status).toBe('revoked')
    expect(env.fetch).not.toHaveBeenCalled()
  })

  it('uses the base and token it is handed, for a page hosted elsewhere', async () => {
    const env = load('https://somewhere.example/wall.html')
    env.fetch.mockReturnValue(answer(200, snapshot()))
    env.VibePanel.connect({ base: 'https://panel.example/', token: TOKEN })
    await env.step()
    expect(env.fetch.mock.calls[0][0]).toMatch(`https://panel.example/api/share/${TOKEN}/v1/snapshot?`)
  })

  it('refuses a fixture name that is not a plain word', async () => {
    const env = load(`https://panel.example/share/${TOKEN}/?fixture=../../api/state`)
    const vp = env.VibePanel.connect()
    await env.step()
    expect(env.fetch).not.toHaveBeenCalled()
    expect(vp.status).not.toBe('live')
  })

  it('loads a fixture instead of polling', async () => {
    const env = load(`https://panel.example/share/${TOKEN}/?fixture=busy`)
    env.fetch.mockReturnValue(answer(200, { status: 'live', snapshot: snapshot({ name: 'fixture' }) }))
    const vp = env.VibePanel.connect()
    await env.step()
    expect(env.fetch.mock.calls[0][0]).toBe('fixtures/busy.json')
    expect(vp.snapshot.name).toBe('fixture')
    expect(env.pending()).toBe(0)
  })
})

describe('the helpers a page draws with', () => {
  const { VibePanel } = load()
  const vp = VibePanel.connect({ snapshot: snapshot() })

  it('writes text, never markup, and a placeholder for nothing', () => {
    const el = { textContent: '' }
    vp.text(el, '<img src=x onerror=alert(1)>')
    expect(el.textContent).toBe('<img src=x onerror=alert(1)>')
    vp.text(el, null)
    expect(el.textContent).toBe('—')
    vp.text(el, '', 'nothing')
    expect(el.textContent).toBe('nothing')
    vp.text(el, 0)
    expect(el.textContent).toBe('0')
  })

  it('replaces the characters that reorder or hide a name', () => {
    // Built from code points so this file holds none of them. A right-to-left
    // override in a title reverses everything drawn after it.
    const rlo = String.fromCharCode(0x202e)
    const zws = String.fromCharCode(0x200b)
    const zwj = String.fromCharCode(0x200d)
    const el = { textContent: '' }
    vp.text(el, `deploy${rlo}evil`)
    expect(el.textContent).toBe(`deploy${String.fromCharCode(0xfffd)}evil`)
    expect(vp.name({ name: `dep${zws}loy` })).toBe(`dep${String.fromCharCode(0xfffd)}loy`)
    // Joiners are how emoji sequences and Persian are written, and are kept.
    expect(vp.name({ name: `a${zwj}b` })).toBe(`a${zwj}b`)
  })

  it('reads an empty name as no name', () => {
    expect(vp.name({ name: '' })).toBeNull()
    expect(vp.name({ name: '' }, 'session')).toBe('session')
    expect(vp.name({ name: 'deploy' }, 'session')).toBe('deploy')
  })

  it('formats for a screen read across a room', () => {
    const f = VibePanel.fmt
    expect(f.tokens(41_283_904)).toBe('41M')
    expect(f.tokens(4_128_390)).toBe('4.1M')
    expect(f.tokens(9400)).toBe('9.4K')
    expect(f.tokens(412_000_000)).toBe('412M')
    expect(f.tokens(999)).toBe('999')
    // Unknown is a dash, never a zero: an unmeasured CPU is not an idle one.
    expect(f.percent(null)).toBe('—')
    expect(f.percent(Number.NaN)).toBe('—')
    expect(f.percent(42.4)).toBe('42%')
    expect(f.duration(45)).toBe('45s')
    expect(f.duration(3 * 3600 + 5 * 60)).toBe('3h 5m')
    expect(f.duration(2 * 86400 + 4 * 3600)).toBe('2d 4h')
    expect(f.bytes(3.4 * 1024 ** 3)).toBe('3.4 GiB')
  })

  it('offers storage that does not throw', () => {
    vp.storage.setItem('k', 1)
    expect(vp.storage.getItem('k')).toBe('1')
    expect(vp.storage.length).toBe(1)
    vp.storage.removeItem('k')
    expect(vp.storage.getItem('k')).toBeNull()
  })
})
