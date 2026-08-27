import type { Key } from '../i18n'

/**
 * Things the panel needs to say, in a stack that says them and goes away.
 *
 * Not a component and not a context: the same shape as i18n.ts, which is a
 * module-level store plus `useSyncExternalStore`. Anything can say something --
 * the upload that finished inside App, the passkey that would not delete three
 * components down inside the settings dialog -- without a callback threaded
 * through every layer between it and the corner of the screen it appears in.
 * A context would work; it would also mean every one of those components taking
 * a provider it does not otherwise need.
 *
 * The message is a dictionary key and its parameters, never a finished string.
 * That is deliberate and it is the enforcement: a toast cannot be raised in one
 * language, because there is nowhere to put an English sentence. What arrives
 * from outside the panel -- a filename, a server's error text -- goes in
 * `detail`, which is rendered through safeText.
 */
export type ToastKind = 'info' | 'success' | 'error'

export interface ToastSpec {
  kind: ToastKind
  key: Key
  params?: Record<string, string | number>
  /** Text the panel did not write: a server error, a filename. */
  detail?: string
}

export interface Toast extends ToastSpec {
  id: number
  /** How many times this same thing has been said in a row. */
  count: number
}

/**
 * How long each kind stays.
 *
 * An error is read, not glanced at, and the one place this panel already had a
 * transient failure notice -- the socket error banner -- chose eight seconds
 * for the same reason. Four is what the upload note used and is long enough for
 * something you already knew was going to happen.
 */
export const TOAST_MS: Record<ToastKind, number> = {
  info: 4000,
  success: 4000,
  error: 8000,
}

/**
 * The tallest the stack is allowed to get.
 *
 * Dropping a folder of thirty files onto the terminal is one gesture and can
 * be thirty failures. A stack that grows to fit them covers the terminal it is
 * reporting about, which is the one thing a notification must never do.
 */
export const MAX_TOASTS = 4

let current: readonly Toast[] = []
let nextId = 1
const timers = new Map<number, ReturnType<typeof setTimeout>>()
const listeners = new Set<() => void>()

function emit() {
  for (const fn of listeners) fn()
}

function clearTimer(id: number) {
  const timer = timers.get(id)
  if (timer !== undefined) clearTimeout(timer)
  timers.delete(id)
}

function arm(toast: Toast) {
  clearTimer(toast.id)
  timers.set(
    toast.id,
    setTimeout(() => dismissToast(toast.id), TOAST_MS[toast.kind]),
  )
}

/** Is this the same thing being said again? */
function sameAs(a: Toast, b: ToastSpec): boolean {
  return (
    a.kind === b.kind &&
    a.key === b.key &&
    a.detail === b.detail &&
    JSON.stringify(a.params ?? {}) === JSON.stringify(b.params ?? {})
  )
}

/**
 * Say something. Returns the id, so a caller that wants to take it back early
 * -- an upload replacing its own "uploading…" with a result -- can.
 *
 * The same message twice in a row counts up rather than stacking. The panel's
 * failures repeat: a write that failed once fails again on the next keystroke,
 * and three identical rows is a worse answer than one row saying three. The
 * timer restarts with the count, so the stack reflects the last time it
 * happened rather than the first.
 */
export function showToast(spec: ToastSpec): number {
  const last = current[current.length - 1]
  if (last && sameAs(last, spec)) {
    const bumped: Toast = { ...last, count: last.count + 1 }
    current = [...current.slice(0, -1), bumped]
    arm(bumped)
    emit()
    return bumped.id
  }

  const toast: Toast = { ...spec, id: nextId++, count: 1 }
  const next = [...current, toast]
  // Oldest first out. The newest is the one that just happened, and it is the
  // one nearest the corner where the eye already is.
  while (next.length > MAX_TOASTS) {
    const dropped = next.shift()
    if (dropped) clearTimer(dropped.id)
  }
  current = next
  arm(toast)
  emit()
  return toast.id
}

export function dismissToast(id: number) {
  if (!current.some((toast) => toast.id === id)) return
  clearTimer(id)
  current = current.filter((toast) => toast.id !== id)
  emit()
}

/** For tests, and for a sign-out that should not leave the last panel's news up. */
export function clearToasts() {
  for (const id of [...timers.keys()]) clearTimer(id)
  if (current.length === 0) return
  current = []
  emit()
}

export function subscribeToasts(fn: () => void): () => void {
  listeners.add(fn)
  return () => {
    listeners.delete(fn)
  }
}

/**
 * The snapshot useSyncExternalStore reads.
 *
 * Referentially stable between changes, which is not a nicety: React calls this
 * on every render and compares by identity, so returning a fresh array would
 * loop forever.
 */
export function toastsSnapshot(): readonly Toast[] {
  return current
}
