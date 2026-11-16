/**
 * What the panel knows about previewing a file before it asks the server.
 *
 * Separate from the component because none of it needs a DOM, and vitest here
 * runs in node on purpose — anything that needs a browser is checked against a
 * real one instead: the ceiling an older server keeps, and the two numbers
 * the panel puts on screen.
 */

/**
 * The largest image or PDF the preview route sends as bytes, to a tab older
 * than the viewer -- which asks with meta=1 and streams media from the inline
 * route instead, with no cap. Kept because an older server still answers 413
 * past it, and the viewer says so with this number.
 *
 * The same number as `previewMaxBytes` in internal/httpapi/panels.go, where the
 * reasoning behind it is written down, and pinned to it by
 * TestThePreviewBoundIsTheSameOnBothSides — which reads this line, so it is a
 * plain literal rather than an expression that would be clearer to read.
 *
 * It used to answer a two-gigabyte core dump from the listing's size, before
 * any request. That stopped being right when every kind got a bounded answer:
 * a binary is its first 64 KB and a medium streams, so a large file is shown
 * rather than refused.
 */
export const PREVIEW_MAX_BYTES = 8388608

/**
 * Lines in what arrived, which is what the panel puts under a text preview.
 *
 * A trailing newline ends the last line rather than starting another one:
 * `wc -l` and every editor agree, and "3 lines" for a three-line file is the
 * only answer nobody has to think about.
 */
export function countLines(text: string): number {
  if (text === '') return 0
  const n = text.split('\n').length
  return text.endsWith('\n') ? n - 1 : n
}

/**
 * A size, in the shortest form that is still true.
 *
 * Lives here rather than in FileTree because the preview says it too — on the
 * one screen where the number is the whole message, which is the file that was
 * too big to show.
 */
export function formatBytes(n: number): string {
  if (n < 1024) return `${n}`
  const units = ['K', 'M', 'G', 'T']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(v < 10 ? 1 : 0)}${units[i]}`
}
