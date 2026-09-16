import { afterEach, describe, expect, it, vi } from 'vitest'

import { api, UnauthorizedError, UploadTransportError } from './api'

/**
 * The upload is the one call that does not go through `request`.
 *
 * It was rewritten from fetch to XMLHttpRequest for the progress bar, and
 * nothing covered it: the status handling, the 401 that returns the shell to
 * the sign-in screen, and the three ways an upload ends without a response
 * could all be deleted with every test still passing. `upload.test.ts` mocks
 * the whole api module, so it exercises none of this.
 */
class FakeXHR {
  static last: FakeXHR | null = null
  status = 0
  statusText = ''
  responseText = ''
  method = ''
  url = ''
  sent: unknown = null
  upload: { onprogress?: (e: { lengthComputable: boolean; loaded: number; total: number }) => void } =
    {}
  onload?: () => void
  onerror?: () => void
  onabort?: () => void
  ontimeout?: () => void

  constructor() {
    FakeXHR.last = this
  }
  open(method: string, url: string) {
    this.method = method
    this.url = url
  }
  send(body: unknown) {
    this.sent = body
  }

  /** The server answered. */
  answer(status: number, body: string, statusText = '') {
    this.status = status
    this.statusText = statusText
    this.responseText = body
    this.onload?.()
  }
  /** Bytes left the browser. */
  progress(loaded: number, total: number, lengthComputable = true) {
    this.upload.onprogress?.({ lengthComputable, loaded, total })
  }
}

function withFakeXHR() {
  vi.stubGlobal('XMLHttpRequest', FakeXHR as unknown as typeof XMLHttpRequest)
  return () => {
    const xhr = FakeXHR.last
    if (!xhr) throw new Error('nothing opened a request')
    return xhr
  }
}

const file = () => new File(['x'], 'shot.png', { type: 'image/png' })

afterEach(() => {
  vi.unstubAllGlobals()
  FakeXHR.last = null
})

describe('uploading a file', () => {
  it('posts to the project and hands back the paths', async () => {
    const xhr = withFakeXHR()
    const done = api.upload('p1', 'sub dir', [file()])
    expect(xhr().method).toBe('POST')
    expect(xhr().url).toBe('/api/projects/p1/upload?path=sub%20dir')
    xhr().answer(200, JSON.stringify({ paths: ['/p/shot.png'] }))
    await expect(done).resolves.toEqual({ paths: ['/p/shot.png'] })
  })

  it('asks for the panel directory when told to', async () => {
    const xhr = withFakeXHR()
    const done = api.upload('p1', '', [file()], 'panel')
    expect(xhr().url).toContain('&dest=panel')
    xhr().answer(200, JSON.stringify({ paths: [] }))
    await done
  })

  // A 200 whose body is not the answer is a proxy in the way, not an upload of
  // nothing. `paths ?? []` reported success, and "0 files uploaded" is
  // indistinguishable from a request that really did store none.
  it('refuses a success whose body is not the answer', async () => {
    const xhr = withFakeXHR()
    const done = api.upload('p1', '', [file()])
    xhr().answer(200, '<html>gateway</html>', 'OK')
    await expect(done).rejects.toThrow('200 OK')
  })

  // Distinguished so the shell can return to the sign-in screen rather than
  // showing a permission error inside a panel the person cannot use.
  it('tells an expired session apart from a refusal', async () => {
    const xhr = withFakeXHR()
    const done = api.upload('p1', '', [file()])
    xhr().answer(401, JSON.stringify({ error: 'sign in', setupRequired: true }))
    await expect(done).rejects.toBeInstanceOf(UnauthorizedError)
  })

  it("carries the server's own message on a refusal", async () => {
    const xhr = withFakeXHR()
    const done = api.upload('p1', '', [file()])
    xhr().answer(409, JSON.stringify({ error: 'shot.png already exists' }))
    await expect(done).rejects.toThrow('shot.png already exists')
  })

  // Every way this ends without a response. An unsettled promise leaves the
  // "uploading…" toast on screen with its bar where it stopped, for good.
  for (const [name, kind] of [
    ['onerror', 'network'],
    ['onabort', 'aborted'],
    ['ontimeout', 'timeout'],
  ] as const) {
    it(`settles on ${name}`, async () => {
      const xhr = withFakeXHR()
      const done = api.upload('p1', '', [file()])
      xhr()[name]?.()
      await expect(done).rejects.toBeInstanceOf(UploadTransportError)
      await expect(done).rejects.toMatchObject({ kind })
    })
  }
})

describe('what the progress bar is told', () => {
  it('stops just short of the end until the server has answered', async () => {
    const xhr = withFakeXHR()
    const seen: number[] = []
    const done = api.upload('p1', '', [file()], undefined, (f) => seen.push(f))
    xhr().progress(50, 100)
    xhr().progress(100, 100)
    // The last byte leaving the browser is not the upload finishing: the
    // server still has to write the files, and a full bar puts the toast back
    // on its four-second timer.
    expect(seen).toEqual([0.5, 0.99])
    xhr().answer(200, JSON.stringify({ paths: ['/p/shot.png'] }))
    await done
    expect(seen[seen.length - 1]).toBe(1)
  })

  it('says nothing when the length is unknown', async () => {
    const xhr = withFakeXHR()
    const seen: number[] = []
    const done = api.upload('p1', '', [file()], undefined, (f) => seen.push(f))
    xhr().progress(50, 0, false)
    expect(seen).toEqual([])
    xhr().answer(200, JSON.stringify({ paths: [] }))
    await done
  })

  // A failed upload must not leave a full bar behind saying it worked.
  it('does not fill the bar on a failure', async () => {
    const xhr = withFakeXHR()
    const seen: number[] = []
    const done = api.upload('p1', '', [file()], undefined, (f) => seen.push(f))
    xhr().progress(100, 100)
    xhr().answer(500, JSON.stringify({ error: 'disk full' }))
    await expect(done).rejects.toThrow('disk full')
    expect(seen).not.toContain(1)
  })
})
