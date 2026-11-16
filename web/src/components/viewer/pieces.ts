import type { Match } from './text'
import type { Tok, TokKind } from './tokens'

/** A run of one token kind inside one match, or outside any (-1). */
interface Piece {
  k: TokKind
  v: string
  match: number
}

/**
 * The line cut at token edges and at match edges both, so a match can start
 * halfway through a keyword and end halfway through a string and each part
 * keeps its colour inside the highlight.
 */
export function pieces(
  text: string,
  tokens: readonly Tok[] | undefined,
  hits: readonly number[],
  matches: readonly Match[],
  /** Where `text` starts in its line, when it is a window of a long one. */
  offset = 0,
): Piece[] {
  const toks = tokens && tokens.length ? tokens : text ? [{ k: 'plain' as TokKind, v: text }] : []
  const cuts = new Set<number>()
  for (const i of hits) {
    cuts.add(matches[i].start - offset)
    cuts.add(matches[i].end - offset)
  }
  const matchAt = (pos: number) => {
    for (const i of hits) if (pos >= matches[i].start - offset && pos < matches[i].end - offset) return i
    return -1
  }
  const out: Piece[] = []
  let pos = 0
  for (const t of toks) {
    let from = 0
    for (let j = 1; j <= t.v.length; j++) {
      if (j === t.v.length || cuts.has(pos + j)) {
        const v = t.v.slice(from, j)
        const m = matchAt(pos + from)
        const last = out[out.length - 1]
        if (last && last.k === t.k && last.match === m) last.v += v
        else out.push({ k: t.k, v, match: m })
        from = j
      }
    }
    pos += t.v.length
  }
  return out
}
