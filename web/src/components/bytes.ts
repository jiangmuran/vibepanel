/**
 * A byte count as people read one.
 *
 * Binary units, because the two places this is shown -- the database on disk
 * and a download in progress -- are both things somebody will compare with
 * `du` and `ls -l`, which count the same way. One decimal from KiB up: "7.1
 * MiB of 7.1 MiB" is a bar that has finished, and "7 MiB of 7 MiB" was one
 * that looked finished at 6.6.
 */
export function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return ''
  if (n < 1024) return `${n} B`
  const units = ['KiB', 'MiB', 'GiB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(1)} ${units[i]}`
}
