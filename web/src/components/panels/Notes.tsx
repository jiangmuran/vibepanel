import { useCallback, useEffect, useRef, useState } from 'react'

import { api, ConflictError } from '../../protocol/api'
import type { PanelSocket } from '../../protocol/socket'

/** How long typing has to pause before a save goes out. */
const SAVE_DEBOUNCE_MS = 800

/**
 * A project's scratchpad.
 *
 * Saves on a pause rather than behind a button: a note you have to remember to
 * save is a note you lose. The status line says plainly whether what is on
 * screen has reached the server, because "did that save?" is otherwise
 * unanswerable.
 */
export function Notes({ projectId, socket }: { projectId: string; socket: PanelSocket }) {
  const [content, setContent] = useState('')
  const [status, setStatus] = useState<
    'loading' | 'saved' | 'saving' | 'dirty' | 'error' | 'conflict'
  >('loading')
  const [error, setError] = useState<string | null>(null)
  const timer = useRef(0)
  // What the server last confirmed, so a save that changes nothing is skipped.
  const saved = useRef('')
  // The revision the current text is based on. Sent with every save so the
  // server can refuse a write that would land on top of someone else's.
  const base = useRef(0)
  const statusRef = useRef(status)
  useEffect(() => {
    statusRef.current = status
  })

  // No reset to 'loading' here: the caller keys this component by project, so
  // a different project is a fresh instance whose initial state already is
  // 'loading'. Resetting from an effect would also render one frame showing
  // the previous project's note.
  useEffect(() => {
    let ignore = false
    const pending = timer
    api
      .note(projectId)
      .then((note) => {
        if (ignore) return
        saved.current = note.content
        base.current = note.rev
        setContent(note.content)
        setStatus('saved')
      })
      .catch((e: unknown) => {
        if (ignore) return
        setError(e instanceof Error ? e.message : String(e))
        setStatus('error')
      })
    return () => {
      ignore = true
      window.clearTimeout(pending.current)
    }
  }, [projectId])

  // Another viewer wrote. Adopt it when there is nothing local to lose;
  // otherwise leave the text alone — overwriting a half-typed paragraph with
  // somebody else's is exactly the failure this is meant to prevent.
  useEffect(() => {
    return socket.onPanelChange((pid, kind) => {
      if (pid !== projectId || kind !== 'note') return
      if (statusRef.current !== 'saved') return
      void api
        .note(projectId)
        .then((note) => {
          if (statusRef.current !== 'saved') return
          saved.current = note.content
          base.current = note.rev
          setContent(note.content)
        })
        .catch(() => {
          /* the next edit or reload will pick it up */
        })
    })
  }, [projectId, socket])

  const scheduleSave = useCallback(
    (next: string) => {
      window.clearTimeout(timer.current)
      timer.current = window.setTimeout(() => {
        if (next === saved.current) {
          setStatus('saved')
          return
        }
        setStatus('saving')
        void api
          .saveNote(projectId, next, base.current)
          .then((note) => {
            saved.current = next
            base.current = note.rev
            // Only clear the indicator if nothing was typed while the request
            // was in flight; otherwise it would claim saved while dirty.
            setStatus((s) => (s === 'saving' ? 'saved' : s))
          })
          .catch((e: unknown) => {
            if (e instanceof ConflictError) {
              // Somebody else wrote while this text was being typed. Keeping
              // the local text on screen is the only safe move — it is the
              // thing that has not been stored anywhere — so the note is left
              // alone and the status line says what happened.
              base.current = e.current.rev
              saved.current = e.current.content
              setError('This note changed in another window. Yours is still here, unsaved.')
              setStatus('conflict')
              return
            }
            setError(e instanceof Error ? e.message : String(e))
            setStatus('error')
          })
      }, SAVE_DEBOUNCE_MS)
    },
    [projectId],
  )

  const label: Record<typeof status, string> = {
    loading: 'loading…',
    saving: 'saving…',
    saved: 'saved',
    dirty: 'unsaved',
    error: error ?? 'error',
    conflict: error ?? 'changed elsewhere',
  }

  return (
    <div className="flex h-full min-h-0 flex-col" data-testid="notes">
      <textarea
        value={content}
        disabled={status === 'loading'}
        onChange={(e) => {
          setContent(e.target.value)
          setStatus('dirty')
          scheduleSave(e.target.value)
        }}
        placeholder="Notes for this project…"
        spellCheck={false}
        className="min-h-0 flex-1 resize-none bg-transparent px-3 py-2 text-[12.5px] leading-relaxed text-ink outline-none placeholder:text-ink-2"
      />
      <div
        data-testid="notes-status"
        data-status={status}
        className="shrink-0 px-3 py-1 text-right text-[10.5px]"
        style={{ color: status === 'error' ? 'var(--vp-state-waiting)' : 'var(--vp-ink-2)' }}
      >
        {label[status]}
      </div>
    </div>
  )
}
