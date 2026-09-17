import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ResourceAlert } from './protocol/wire'

// The memory question buzzes the phone of somebody who is away. It must buzz
// again when the same question becomes a countdown: deduped by id alone, the
// panel ended a process with nothing on the phone.
describe('notifyOnResourceAlert', () => {
  let shown: string[]

  beforeEach(() => {
    vi.resetModules()
    shown = []
    const reg = { showNotification: (title: string) => { shown.push(title); return Promise.resolve() } }
    vi.stubGlobal('localStorage', { getItem: () => 'on', setItem: () => {} })
    vi.stubGlobal('Notification', { permission: 'granted' })
    vi.stubGlobal('navigator', { serviceWorker: { ready: Promise.resolve(reg) } })
    vi.stubGlobal('window', { Notification: {}, isSecureContext: true })
  })
  afterEach(() => vi.unstubAllGlobals())

  const base: ResourceAlert = {
    id: 'q1', level: 'warn', reason: 'stall', poolCurrent: 1, poolMax: 2, available: 1, total: 4,
  }

  async function run(alerts: ResourceAlert[]) {
    const { notifyOnResourceAlert } = await import('./notify')
    for (const a of alerts) notifyOnResourceAlert(a, [], false)
    await new Promise((r) => setTimeout(r, 0))
    return shown.length
  }

  it('notifies once for a question that does not change', async () => {
    expect(await run([base, { ...base, poolCurrent: 2 }, base])).toBe(1)
  })

  it('notifies again when the same question escalates to a countdown', async () => {
    expect(await run([base, { ...base, level: 'critical', autoAt: 100 }])).toBe(2)
  })

  it('does not notify again for a level flapping back and forth', async () => {
    const critical = { ...base, level: 'critical' as const }
    expect(await run([base, critical, base, critical, base])).toBe(2)
  })
})
