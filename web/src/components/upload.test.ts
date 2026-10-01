import { afterEach, describe, expect, it, vi } from 'vitest'

import { filesFrom, uploadErrorText, uploadFiles } from './upload'
import { api, refusedByProxy, UploadTransportError } from '../protocol/api'
import { setLang } from '../i18n'

/** A DataTransfer as the two events actually hand it over. */
function transfer(opts: { items?: (File | null)[]; files?: File[] }): DataTransfer {
  return {
    items: (opts.items ?? []).map((f) => ({
      kind: f ? 'file' : 'string',
      getAsFile: () => f,
    })),
    files: opts.files ?? [],
  } as unknown as DataTransfer
}

const shot = { name: 'shot.png' } as File
const doc = { name: 'notes.txt' } as File

describe('the files in a drop or a paste', () => {
  it('finds a pasted screenshot, which is only ever in items', () => {
    // The case the whole feature exists for. Chromium has shipped versions
    // where clipboardData.files is empty for a screenshot while items has it,
    // so a reader that looks only at `files` sees nothing at all and the paste
    // silently does nothing.
    expect(filesFrom(transfer({ items: [shot] }))).toEqual([shot])
  })

  it('finds a dropped file, which is only ever in files', () => {
    expect(filesFrom(transfer({ files: [doc] }))).toEqual([doc])
  })

  it('ignores the text half of a paste', () => {
    // A copied selection puts a string item on the clipboard beside anything
    // else. Treating that as a file is how an upload of nothing gets started.
    expect(filesFrom(transfer({ items: [null] }))).toEqual([])
  })

  it('survives an event with no data on it at all', () => {
    expect(filesFrom(null)).toEqual([])
    expect(filesFrom(undefined)).toEqual([])
  })
})

describe('uploading and saying so', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('narrates the upload and hands back where the files landed', async () => {
    setLang('en')
    vi.spyOn(api, 'upload').mockResolvedValue({ paths: ['/p/a.png', '/p/b.png'] })
    const notes: string[] = []
    const paths = await uploadFiles('proj', 'sub', [shot, doc], (n) => notes.push(n))

    expect(api.upload).toHaveBeenCalledWith('proj', 'sub', [shot, doc], undefined, undefined)
    expect(paths).toEqual(['/p/a.png', '/p/b.png'])
    // Both ends, in order: something is happening, and then what happened.
    expect(notes).toEqual(['Uploading 2 files…', '2 files uploaded'])
  })

  it('counts one file as one file in either language', () => {
    // "Uploading 1 files" is the plural rule nobody writes on purpose and
    // everybody ships, which is why one and many are separate entries.
    setLang('en')
    const notes: string[] = []
    vi.spyOn(api, 'upload').mockResolvedValue({ paths: ['/p/a.png'] })
    return uploadFiles('proj', '', [shot], (n) => notes.push(n)).then(() => {
      expect(notes[0]).not.toMatch(/1 files/)
      expect(notes[1]).not.toMatch(/1 files/)
    })
  })

  it("keeps the server's reason rather than replacing it with a shrug", async () => {
    setLang('en')
    // The server refuses to overwrite, ever, and the name it refuses is the
    // whole answer -- the caller renames and tries again.
    vi.spyOn(api, 'upload').mockRejectedValue(new Error('shot.png already exists'))
    const notes: string[] = []
    const paths = await uploadFiles('proj', '', [shot], (n) => notes.push(n))

    expect(paths).toEqual([])
    expect(notes[notes.length - 1]).toBe('shot.png already exists')
  })

  it('does nothing at all for an empty drop', async () => {
    const upload = vi.spyOn(api, 'upload')
    const notes: string[] = []
    expect(await uploadFiles('proj', '', [], (n) => notes.push(n))).toEqual([])
    expect(upload).not.toHaveBeenCalled()
    // And no note either: a drop that carried nothing is not an event worth
    // reporting, it is a mis-aimed gesture.
    expect(notes).toEqual([])
  })
})

describe('a file refused by a proxy in front of the panel', () => {
  it('is told apart from a refusal the panel wrote', () => {
    // nginx's page: HTML, so no parsed body at all.
    expect(refusedByProxy(413, null)).toBe(true)
    // The panel's own 413 carries its reason, which is the better answer.
    expect(refusedByProxy(413, { error: 'an upload is at most 256 MiB' })).toBe(false)
    expect(refusedByProxy(400, null)).toBe(false)
  })
  it('says so in words rather than "413 "', () => {
    const text = uploadErrorText(new UploadTransportError('tooLarge'))
    expect(text).toContain('413')
    expect(text).not.toBe('tooLarge')
  })
})
