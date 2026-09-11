import { describe, expect, it } from 'vitest'

import { PanelSocket } from './socket'
import type { ClientMessage, ServerMessage } from './wire'

/**
 * The frame the client used to drop on the floor.
 *
 * `handleControl`'s switch had no case for `error` and no default, so all six
 * senders were discarded: you type into a terminal, the write fails
 * server-side, the server says so, and nothing reaches the screen — which is
 * indistinguishable from a network problem. `TestMessageTypesMatchTheClient`
 * compares the server's sends against the *declared union*, which `error` was
 * in, so it passed the whole time. Nothing pinned the union against the switch.
 *
 * The switch is now exhaustive with a `never` default, so that half is the
 * compiler's job — adding a member to `ServerMessage['t']` without a case stops
 * the build, which was measured by adding one. What the compiler cannot say is
 * whether the case actually delivers anything, which is this.
 *
 * Two liberties, both deliberate. The stub is `window.addEventListener` and
 * nothing else: this file's vitest config keeps anything that needs a browser
 * in render-check, "because a jsdom approximation of those would pass while the
 * real thing was broken", and that is the right rule for the terminal and for
 * WebAuthn. Dispatching a decoded frame to a set of listeners is not a browser
 * behaviour. And `handleControl` is reached through a cast rather than through
 * a fake WebSocket, because faking the socket would mean faking `location`,
 * `sessionStorage` and the whole open/close lifecycle to test a switch.
 */
function newSocket(): PanelSocket {
  ;(globalThis as unknown as { window: { addEventListener: () => void } }).window = {
    addEventListener: () => {},
  }
  return new PanelSocket()
}

function deliver(sock: PanelSocket, msg: ServerMessage) {
  ;(sock as unknown as { handleControl(m: ServerMessage): void }).handleControl(msg)
}

describe('error frames', () => {
  it('reaches the listener with the message and the session it belongs to', () => {
    const sock = newSocket()
    const seen: Array<[string, string]> = []
    sock.onError((sessionId, message) => seen.push([sessionId, message]))

    deliver(sock, { t: 'error', sessionId: 's1', message: 'write failed: broken pipe' })

    expect(seen).toEqual([['s1', 'write failed: broken pipe']])
  })

  it('never delivers an empty message, because the banner would render blank', () => {
    const sock = newSocket()
    let message = 'unset'
    sock.onError((_sessionId, m) => {
      message = m
    })

    // Message is optional on the wire; a frame without one must still say
    // something rather than painting an empty red bar.
    deliver(sock, { t: 'error' })

    expect(message).not.toBe('')
    expect(message).not.toBe('unset')
  })

  it('stops delivering once unsubscribed', () => {
    const sock = newSocket()
    let calls = 0
    const off = sock.onError(() => {
      calls++
    })
    deliver(sock, { t: 'error', message: 'one' })
    off()
    deliver(sock, { t: 'error', message: 'two' })

    expect(calls).toBe(1)
  })
})

/**
 * A terminal kept mounted off-screen, as the server hears about it.
 *
 * The server decides who drives a session's grid from who is looking at it, so
 * a hidden terminal has to say so, and has to go on saying so across every
 * subscribe that follows: one that arrives without the flag is taken for a
 * viewer arriving, and claims the grid.
 */
describe('hidden terminals', () => {
  const handlers = { onData: () => {}, onSize: () => {} }

  /** A socket that is open and records what it sends. */
  function openSocket() {
    const sock = newSocket()
    ;(sock as unknown as { dark: () => boolean }).dark = () => false
    const sent: ClientMessage[] = []
    ;(sock as unknown as { ws: unknown }).ws = {
      readyState: WebSocket.OPEN,
      send: (s: string) => sent.push(JSON.parse(s) as ClientMessage),
    }
    return { sock, sent }
  }

  it('says once that a terminal went off-screen and once that it came back', () => {
    const { sock, sent } = openSocket()
    sock.subscribe('s1', 80, 24, handlers)
    sock.setHidden('s1', false)
    sock.setHidden('s1', true)
    sock.setHidden('s1', true)
    sock.setHidden('s1', false)

    expect(sent.filter((m) => m.t === 'visibility')).toEqual([
      { t: 'visibility', sessionId: 's1', hidden: true },
      { t: 'visibility', sessionId: 's1', hidden: false },
    ])
  })

  it('subscribes hidden when the terminal is mounted off-screen', () => {
    const { sock, sent } = openSocket()
    sock.subscribe('s1', 80, 24, handlers, true)

    expect(sent).toEqual([expect.objectContaining({ t: 'subscribe', sessionId: 's1', hidden: true })])

    // And it is remembered, not only sent: the next subscribe for this stream
    // comes from the socket, which knows nothing else about the terminal.
    sent.length = 0
    deliver(sock, { t: 'dropped', sessionId: 's1' })
    expect(sent).toEqual([expect.objectContaining({ t: 'subscribe', sessionId: 's1', hidden: true })])
  })

  it('resubscribes hidden after being cut off for falling behind', () => {
    const { sock, sent } = openSocket()
    sock.subscribe('s1', 80, 24, handlers)
    sock.setHidden('s1', true)
    sent.length = 0

    deliver(sock, { t: 'dropped', sessionId: 's1' })

    expect(sent).toEqual([expect.objectContaining({ t: 'subscribe', sessionId: 's1', hidden: true })])
  })

  it('resubscribes hidden after a reconnect', () => {
    const sock = newSocket()
    ;(sock as unknown as { dark: () => boolean }).dark = () => false
    const g = globalThis as unknown as Record<string, unknown>
    const realWebSocket = g.WebSocket
    const sent: ClientMessage[] = []
    class FakeWebSocket {
      static OPEN = 1
      readyState = 1
      onopen: (() => void) | null = null
      send(s: string) {
        sent.push(JSON.parse(s) as ClientMessage)
      }
      close() {}
    }
    g.WebSocket = FakeWebSocket
    g.location = { protocol: 'http:', host: 'panel.test' }
    Object.assign(g.window as object, { setInterval: () => 0, clearInterval: () => {} })
    try {
      // Before the socket opens: neither of these reaches the wire, and the
      // subscribe the open sends is the only thing that can carry the flag.
      sock.subscribe('s1', 80, 24, handlers)
      sock.setHidden('s1', true)
      sock.connect()
      ;(sock as unknown as { ws: FakeWebSocket }).ws.onopen?.()

      expect(sent.filter((m) => m.t === 'subscribe')).toEqual([
        expect.objectContaining({ sessionId: 's1', hidden: true }),
      ])
    } finally {
      g.WebSocket = realWebSocket
      delete g.location
    }
  })
})
