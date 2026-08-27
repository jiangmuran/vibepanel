import { describe, expect, it } from 'vitest'

import { PREVIEW_MAX_BYTES, countLines, formatBytes, tooBigToPreview } from './preview'
import { blobTypeFor } from '../../protocol/api'

describe('the preview ceiling', () => {
  it('lets a file exactly at the limit through and refuses the next byte', () => {
    // Off by one here is a file the server would serve and the panel refuses
    // to ask for, which is invisible: the panel says "too big" about a file
    // that is not, and nothing disagrees with it.
    expect(tooBigToPreview(PREVIEW_MAX_BYTES)).toBe(false)
    expect(tooBigToPreview(PREVIEW_MAX_BYTES + 1)).toBe(true)
    expect(tooBigToPreview(0)).toBe(false)
  })

  it('is answered from the listing, so a core dump costs no request', () => {
    expect(tooBigToPreview(2 * 1024 * 1024 * 1024)).toBe(true)
  })
})

describe('the type a Blob is built with', () => {
  it('takes the media type from a whitelist, never from the file', () => {
    expect(blobTypeFor('image', 'image/png')).toBe('image/png')
    expect(blobTypeFor('pdf', 'application/pdf')).toBe('application/pdf')
  })

  it('refuses anything that is not an image where an image was promised', () => {
    // A Blob's type is the instruction the browser follows when the bytes
    // reach an <img> or an <object>. text/html there would be a page running
    // on the panel's origin, built out of a file an agent wrote.
    expect(blobTypeFor('image', 'text/html')).toBe('application/octet-stream')
    expect(blobTypeFor('image', null)).toBe('application/octet-stream')
    // And a PDF is a PDF whatever the header says, because that is the only
    // type the viewer is being opened for.
    expect(blobTypeFor('pdf', 'text/html')).toBe('application/pdf')
  })
})

describe('the line count under a text preview', () => {
  it('agrees with wc -l about a trailing newline', () => {
    expect(countLines('a\nb\nc\n')).toBe(3)
    expect(countLines('a\nb\nc')).toBe(3)
    expect(countLines('one line')).toBe(1)
    expect(countLines('')).toBe(0)
    // A file that is one empty line is one line, not none.
    expect(countLines('\n')).toBe(1)
  })
})

describe('a size in the shortest form that is still true', () => {
  it('climbs a unit at a time', () => {
    expect(formatBytes(0)).toBe('0')
    expect(formatBytes(1023)).toBe('1023')
    expect(formatBytes(1024)).toBe('1.0K')
    expect(formatBytes(PREVIEW_MAX_BYTES)).toBe('8.0M')
  })
})
