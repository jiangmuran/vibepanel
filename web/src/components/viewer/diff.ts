/**
 * Whether a text is a diff, by its name or by how it starts.
 *
 * By name: `.diff` and `.patch` (grammars.ts). By content, for the diff saved
 * as `changes.txt` or piped into a file with no extension: `diff --git` on the
 * first line, or a `---` line followed by a `+++` line before any line that
 * is neither -- the unified header -- within the first lines. A file that
 * merely has a `---` in it (a YAML document separator, a Markdown rule) is
 * not one: the `+++` is what makes it a diff.
 */
export function looksLikeDiff(text: string): boolean {
  const head = text.slice(0, 4096).split(/\r?\n/, 40)
  if (head[0]?.startsWith('diff --git ')) return true
  for (let i = 0; i < head.length - 1; i++) {
    if (head[i].startsWith('--- ') && head[i + 1].startsWith('+++ ')) return true
  }
  return false
}

export type DiffRow = 'add' | 'del' | 'hunk' | 'meta' | 'context'

/**
 * What a line of a diff is, which decides its tint.
 *
 * The tint is never the only difference: an added line still starts with its
 * `+` and a removed one with its `-`, drawn as they are in the file (red line
 * 4). `+++` and `---` are the file header, not changes.
 */
export function diffRow(line: string): DiffRow {
  if (line.startsWith('+++ ') || line.startsWith('--- ')) return 'meta'
  if (line.startsWith('@@')) return 'hunk'
  if (line.startsWith('+')) return 'add'
  if (line.startsWith('-')) return 'del'
  if (/^(diff |index |new file|deleted file|similarity|rename |Binary files)/.test(line)) return 'meta'
  return 'context'
}
