/**
 * The questions the panel used to ask with `window.confirm` and
 * `window.prompt`.
 *
 * Those two are the browser's, not the panel's: they arrive in the operating
 * system's chrome in the operating system's language, they cannot say which
 * project is about to lose its sessions in anything but plain text, they cannot
 * mark the destructive answer as the destructive one, and on a phone installed
 * to the home screen they are a modal sheet that says the hostname above the
 * question. The panel has a modal language of its own -- the directory picker,
 * `.vp-backdrop`, `.vp-panel-in` -- and these questions belong in it.
 *
 * A module-level store for the same reason as toasts.ts: the callers are spread
 * from App down to a row inside the settings dialog, and the alternative is a
 * callback threaded through every component in between.
 *
 * The answer is a string or null, and null is the only thing that means "no".
 * An empty string is a real answer to a question with a field in it -- somebody
 * cleared the name -- and a caller writing `if (!answer) return` on that would
 * treat it as a cancellation. Callers get `askConfirm`, which is a boolean, or
 * `askText`, which is a string or null; neither of them can make that mistake.
 */
export interface AskRequest {
  /** Already translated. Everything here goes through t() at the call site. */
  title: string
  body?: string
  /** The label on the button that does the thing. */
  confirm: string
  cancel: string
  /**
   * The answer destroys something. Changes what the button looks like and,
   * more to the point, how big it is.
   */
  destructive?: boolean
  /** Turns the question into one with an answer: the resolved value is the field. */
  field?: { label: string; value: string }
}

export interface PendingAsk {
  id: number
  request: AskRequest
}

type Waiting = PendingAsk & { resolve: (answer: string | null) => void }

let queue: readonly Waiting[] = []
let nextId = 1
const listeners = new Set<() => void>()

function emit() {
  for (const fn of listeners) fn()
}

function ask(request: AskRequest): Promise<string | null> {
  return new Promise((resolve) => {
    // Queued rather than replaced. Two questions at once is not a shape this
    // panel produces today, but a dropped one is a promise that never settles,
    // and the caller awaiting it is a click that silently did nothing.
    queue = [...queue, { id: nextId++, request, resolve }]
    emit()
  })
}

/** Yes or no. */
export async function askConfirm(request: AskRequest): Promise<boolean> {
  return (await ask({ ...request, field: undefined })) !== null
}

/** A name, or null if the question was dismissed. */
export async function askText(
  request: AskRequest & { field: { label: string; value: string } },
): Promise<string | null> {
  return await ask(request)
}

/** Answer the question on screen. `null` is a cancellation. */
export function answerAsk(answer: string | null) {
  const [head, ...rest] = queue
  if (!head) return
  queue = rest
  emit()
  head.resolve(answer)
}

export function subscribeAsk(fn: () => void): () => void {
  listeners.add(fn)
  return () => {
    listeners.delete(fn)
  }
}

/** Stable between changes -- see toastsSnapshot for why that matters. */
export function currentAsk(): PendingAsk | null {
  return queue[0] ?? null
}
