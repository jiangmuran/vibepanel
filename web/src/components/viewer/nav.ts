/**
 * Where a file is and what is beside it: the viewer's breadcrumb and its
 * previous and next.
 */

export interface Crumb {
  /** What the segment says. */
  name: string
  /** The directory it moves the tree to: '' is the project's root. */
  path: string
}

/**
 * The directories above a file, root first, each one a place the tree can
 * move to. The root is named by the caller -- the project's name, never a bare
 * `/`, which says nothing about which project this is.
 */
export function crumbs(filePath: string, rootName: string): Crumb[] {
  const parts = filePath.split('/').filter((p) => p !== '')
  parts.pop()
  const out: Crumb[] = [{ name: rootName, path: '' }]
  let at = ''
  for (const p of parts) {
    at = at ? `${at}/${p}` : p
    out.push({ name: p, path: at })
  }
  return out
}

/**
 * The file before and after this one among `files`, in the order the tree
 * lists them; null at either end rather than wrapping, because the reader
 * reaching the last file should know they have.
 */
export function neighbours<T extends { path: string }>(
  files: readonly T[],
  path: string,
): { prev: T | null; next: T | null } {
  const i = files.findIndex((f) => f.path === path)
  if (i < 0) return { prev: null, next: null }
  return { prev: files[i - 1] ?? null, next: files[i + 1] ?? null }
}
