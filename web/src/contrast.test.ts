import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

/**
 * WCAG contrast of the text tokens, computed from the values in styles.css.
 *
 * The tertiary ink was 2.85-3.0:1 on the light surfaces while carrying about
 * 180 pieces of information text, and the comment beside it said 3.0 as though
 * that were a pass. A number in a comment is not a guard; this reads the real
 * declarations, in every block that declares them, so a token cannot slide
 * without a red test.
 */
const css = readFileSync(new URL('./styles.css', import.meta.url), 'utf8').replace(/\/\*[\s\S]*?\*\//g, '')

type RGB = [number, number, number]

function parse(value: string): { rgb: RGB; a: number } {
  const hex = /^#([0-9a-f]{6})$/i.exec(value.trim())
  if (hex) {
    const n = parseInt(hex[1], 16)
    return { rgb: [(n >> 16) & 255, (n >> 8) & 255, n & 255], a: 1 }
  }
  const fn = /^rgb\(\s*(\d+)\s+(\d+)\s+(\d+)(?:\s*\/\s*([\d.]+))?\s*\)$/.exec(value.trim())
  if (!fn) throw new Error(`cannot read colour ${value}`)
  return { rgb: [+fn[1], +fn[2], +fn[3]], a: fn[4] === undefined ? 1 : +fn[4] }
}

function lum([r, g, b]: RGB): number {
  const ch = (c: number) => {
    const v = c / 255
    return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4
  }
  return 0.2126 * ch(r) + 0.7152 * ch(g) + 0.0722 * ch(b)
}

function over(fg: { rgb: RGB; a: number }, bg: RGB): RGB {
  return [0, 1, 2].map((i) => fg.rgb[i] * fg.a + bg[i] * (1 - fg.a)) as RGB
}

function ratio(a: RGB, b: RGB): number {
  const [hi, lo] = [lum(a), lum(b)].sort((x, y) => y - x)
  return (hi + 0.05) / (lo + 0.05)
}

/** Body of the block opened by the first `{` at or after `from`. */
function body(from: number): string {
  const open = css.indexOf('{', from)
  let depth = 0
  for (let i = open; i < css.length; i++) {
    if (css[i] === '{') depth++
    else if (css[i] === '}' && --depth === 0) return css.slice(open + 1, i)
  }
  return ''
}

const base = body(css.search(/(^|\n):root\s*\{/))
const mediaAt = css.indexOf('@media (prefers-color-scheme: dark)')
const media = body(mediaAt)
const forced = body(css.indexOf(":root[data-theme='dark']"))

// The dark theme exists twice: the media query (system) and the explicit
// attribute. The light theme is the bare :root.
const themes: Record<string, string> = {
  light: base,
  'dark (prefers-color-scheme)': media,
  "dark ([data-theme='dark'])": forced,
  // The two built-in extras. Each declares every colour token itself (the
  // comment beside them in styles.css says why), so each is measured alone.
  porcelain: body(css.indexOf(":root[data-theme='porcelain']")),
  wujin: body(css.indexOf(":root[data-theme='wujin']")),
}

function token(block: string, name: string): { rgb: RGB; a: number } {
  const m = new RegExp(`${name}:\\s*([^;]+);`).exec(block)
  if (!m) throw new Error(`${name} is not declared`)
  return parse(m[1])
}

describe('text contrast', () => {
  for (const [label, block] of Object.entries(themes)) {
    describe(label, () => {
      const surfaces = ['--vp-bg', '--vp-surface', '--vp-surface-2'].map((n) => [n, token(block, n).rgb] as const)

      it('declares the tokens', () => {
        expect(block.length, `no block found for ${label}`).toBeGreaterThan(100)
      })

      for (const ink of ['--vp-ink', '--vp-ink-2', '--vp-ink-3']) {
        for (const [name, bg] of surfaces) {
          it(`${ink} on ${name} is at least 4.5:1`, () => {
            const fg = over(token(block, ink), bg)
            expect(ratio(fg, bg)).toBeGreaterThanOrEqual(4.5)
          })
        }
      }

      it('keeps the ink steps in order', () => {
        const bg = surfaces[1][1]
        const r = (n: string) => ratio(over(token(block, n), bg), bg)
        expect(r('--vp-ink-2')).toBeGreaterThan(r('--vp-ink-3'))
        expect(r('--vp-ink')).toBeGreaterThan(r('--vp-ink-2'))
      })

      for (const [name, bg] of surfaces) {
        it(`--vp-accent as text on ${name} is at least 4.5:1`, () => {
          expect(ratio(token(block, '--vp-accent').rgb, bg)).toBeGreaterThanOrEqual(4.5)
        })
      }

      // Code in the file viewer, one colour per kind of token. Read where the
      // viewer's text sits (--vp-surface) and on the other two, because a
      // fenced block in a document sits on --vp-surface-2.
      const toks = [...block.matchAll(/(--vp-tok-[a-z]+):/g)].map((m) => m[1])
      it('declares the code tokens', () => {
        expect(toks.length).toBeGreaterThanOrEqual(13)
      })
      for (const tok of toks) {
        for (const [name, bg] of surfaces) {
          it(`${tok} on ${name} is at least 4.5:1`, () => {
            expect(ratio(over(token(block, tok), bg), bg)).toBeGreaterThanOrEqual(4.5)
          })
        }
      }

      // The current find match is the ink on a tint over the surface.
      it('--vp-ink on the current find match is at least 4.5:1', () => {
        const bg = over(token(block, '--vp-find-current'), token(block, '--vp-surface').rgb)
        expect(ratio(over(token(block, '--vp-ink'), bg), bg)).toBeGreaterThanOrEqual(4.5)
      })

      it('--vp-accent-ink on --vp-accent is at least 4.5:1', () => {
        expect(ratio(token(block, '--vp-accent-ink').rgb, token(block, '--vp-accent').rgb)).toBeGreaterThanOrEqual(4.5)
      })
    })
  }
})
