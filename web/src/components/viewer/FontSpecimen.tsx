import { useEffect, useState } from 'react'
import { Loader2 } from 'lucide-react'

import { t } from '../../i18n'
import { safeText } from '../text'
import type { FontFormat } from './fontkind'
import { nameFromTable, sfntNameTable, SPECIMEN, SPECIMEN_SIZES, woffNameTable } from './fontname'

let next = 0

/**
 * A font, set in itself: its name, the alphabet in both cases, digits, a
 * pangram and the file's own name, at three sizes.
 *
 * The bytes are fetched whole and handed to FontFace under a family name of
 * this viewer's own, added to the document while the specimen is on screen
 * and taken out when it goes -- a font the panel's own text could pick up by
 * name would be a project's file restyling the panel. The browser sanitises
 * the font before it uses it, as it does every web font.
 */
export default function FontSpecimen({ url, fileName, format }: { url: string; fileName: string; format: FontFormat }) {
  const [state, setState] = useState<{ family: string; name: string | null } | 'loading' | 'failed'>('loading')
  useEffect(() => {
    let face: FontFace | null = null
    let dead = false
    const family = `vp-specimen-${++next}`
    void (async () => {
      try {
        const res = await fetch(url)
        if (!res.ok) throw new Error(String(res.status))
        const bytes = new Uint8Array(await res.arrayBuffer())
        face = new FontFace(family, bytes)
        await face.load()
        if (dead) return
        document.fonts.add(face)
        setState({ family, name: await nameOf(bytes, format) })
      } catch {
        if (!dead) setState('failed')
      }
    })()
    return () => {
      dead = true
      if (face) document.fonts.delete(face)
    }
  }, [url, format])

  if (state === 'loading') {
    return (
      <p className="flex flex-1 items-center justify-center gap-2 px-6 py-12 text-vp-base text-ink-2">
        <Loader2 size={13} className="animate-spin" />
        {t('preview.loading')}
      </p>
    )
  }
  if (state === 'failed') {
    return (
      <p data-testid="font-failed" className="flex flex-1 items-center justify-center px-6 py-12 text-center text-vp-base text-ink-2">
        {t('font.failed')}
      </p>
    )
  }
  const lines = [SPECIMEN.upper, SPECIMEN.lower, SPECIMEN.digits, SPECIMEN.pangram, fileName]
  return (
    <div data-testid="viewer-font" data-family={state.family} className="min-h-0 flex-1 overflow-auto px-4 py-4 @md:px-6">
      <p className="mb-4 text-vp-sm text-ink-2" data-testid="font-name">
        {safeText(state.name ?? fileName)}
      </p>
      {SPECIMEN_SIZES.map((px) => (
        <section key={px} className="mb-5 min-w-0">
          <p className="tabular mb-1 text-vp-xs text-ink-3">{px}px</p>
          {lines.map((l, i) => (
            <p
              key={i}
              data-specimen={px}
              style={{ fontFamily: `"${state.family}", monospace`, fontSize: px, lineHeight: 1.3 }}
              className="break-words text-ink"
            >
              {safeText(l)}
            </p>
          ))}
        </section>
      ))}
    </div>
  )
}

/** The name the font gives itself, or null where it cannot be read here. */
async function nameOf(b: Uint8Array, format: FontFormat): Promise<string | null> {
  if (format === 'TrueType' || format === 'OpenType') {
    const t = sfntNameTable(b)
    return t ? nameFromTable(t) : null
  }
  if (format === 'WOFF') {
    const w = woffNameTable(b)
    if (!w) return null
    if (!w.compressed) return nameFromTable(w.data)
    try {
      const out = new Blob([w.data.slice()]).stream().pipeThrough(new DecompressionStream('deflate'))
      return nameFromTable(new Uint8Array(await new Response(out).arrayBuffer()))
    } catch {
      return null
    }
  }
  return null
}
