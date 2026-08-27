import { api } from '../protocol/api'
import { t } from '../i18n'

/**
 * Every file in a drop or a paste.
 *
 * Two lists, and neither one alone is enough. `files` is what a drop from the
 * desktop fills in; `items` is where a *pasted* screenshot lives, because the
 * clipboard holds a rendition rather than a file the user picked, and on
 * Chromium `clipboardData.files` has been empty for one at various points while
 * `items` had it. Reading items first and falling back covers both without
 * either caller having to know which event it is holding.
 *
 * There were two copies of this before, one in the terminal's paste handler and
 * one in the shell's drop handler, and only the paste one knew about `items` —
 * so the file panel would have inherited the half that does not see a
 * screenshot, which is the case this whole feature exists for.
 */
export function filesFrom(data: DataTransfer | null | undefined): File[] {
  if (!data) return []
  const out: File[] = []
  for (const item of data.items ?? []) {
    if (item.kind !== 'file') continue
    const f = item.getAsFile()
    if (f) out.push(f)
  }
  if (out.length > 0) return out
  for (const f of data.files ?? []) out.push(f)
  return out
}

/**
 * How long a finished upload's line stays on screen.
 *
 * Long enough to read the count, short enough that it is gone before it starts
 * to look like part of the panel.
 */
const NOTE_MS = 4000

/**
 * Upload into a directory and narrate it, which is the same job in both places
 * that do it.
 *
 * The absolute paths come back because the terminal's caller types them at a
 * prompt; the file panel ignores them and rereads the directory instead. What
 * is shared is everything between those two lines — the three states, their
 * wording in two languages, and the timer that clears the last one.
 *
 * Failure returns an empty array rather than throwing. Both callers would do
 * exactly nothing with the exception except show it, which has already
 * happened by then, and the caller that did throw ended up with an unhandled
 * rejection because the drop handler could not await it.
 */
export async function uploadFiles(
  projectId: string,
  path: string,
  files: File[],
  onNote: (note: string) => void,
): Promise<string[]> {
  if (files.length === 0) return []
  // One and many are separate entries rather than a plural rule: English wants
  // "1 file" and Chinese wants neither ending, and a rule that gets both right
  // is a library this project does not have.
  onNote(files.length === 1 ? t('upload.one') : t('upload.many', { n: files.length }))
  try {
    const { paths } = await api.upload(projectId, path, files)
    onNote(paths.length === 1 ? t('upload.doneOne') : t('upload.doneMany', { n: paths.length }))
    clearLater(onNote)
    return paths
  } catch (err) {
    // The server's message, when there is one: "shot.png already exists" is the
    // whole answer, and t('upload.failed') would replace it with a shrug.
    onNote(err instanceof Error ? err.message : t('upload.failed'))
    clearLater(onNote)
    return []
  }
}

function clearLater(onNote: (note: string) => void) {
  setTimeout(() => onNote(''), NOTE_MS)
}
