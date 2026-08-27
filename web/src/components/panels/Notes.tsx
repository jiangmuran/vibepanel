import { useCallback, useEffect, useRef, useState } from 'react'

import { api, ConflictError } from '../../protocol/api'
import type { PanelSocket } from '../../protocol/socket'
import { t, useLang } from '../../i18n'

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
  // Repaint when the language changes. Without this the strings are
  // resolved once and a switch needs a reload to be believed.
  useLang()
  const [content, setContent] = useState('')
  const [status, setStatus] = useState<
    'loading' | 'saved' | 'saving' | 'dirty' | 'error' | 'conflict'
  >('loading')
  const [error, setError] = useState<string | null>(null)
  const timer = useRef(0)
  // The text a scheduled save is going to write, kept outside React state so
  // the flush below can reach it from an unmount or an unload, when there is
  // no render left to read it from.
  const queued = useRef<string | null>(null)
  // What the server last confirmed, so a save that changes nothing is skipped.
  const saved = useRef('')
  // The revision the current text is based on. Sent with every save so the
  // server can refuse a write that would land on top of someone else's.
  const base = useRef(0)
  const statusRef = useRef(status)
  useEffect(() => {
    statusRef.current = status
  })

  // Send a scheduled save now instead of losing it.
  //
  // The debounce used to be cancelled on unmount, and every one of the ways
  // this component goes away discarded whatever was still waiting: switching
  // to the Files tab (the panel renders one tab at a time), switching project
  // (the component is keyed by it), and closing the page. All three are
  // ordinary one-click actions, all three silently dropped up to 800ms of
  // typing, and the status line read "unsaved" right until it vanished — for
  // a panel whose entire premise is that a note you have to remember to save
  // is a note you lose.
  //
  // Deliberately touches no React state: two of the three callers have no
  // component left to update. A conflict here is unreportable and is dropped,
  // which is still strictly better than never attempting the write.
  // `unloading` picks the transport. keepalive is what survives a document
  // going away, but it caps the whole body at 64KB; a tab switch leaves the
  // page very much alive, so an ordinary fetch is both sufficient there and
  // free of that limit.
  const flush = useCallback(
    (unloading: boolean) => {
      const text = queued.current
      queued.current = null
      window.clearTimeout(timer.current)
      if (text === null || text === saved.current) return
      void api.saveNote(projectId, text, base.current, unloading).catch((e: unknown) => {
        // There is no component left to show this in, which is not the same as
        // there being nowhere to put it. Swallowing it entirely made a failed
        // flush indistinguishable from one that never had anything to send —
        // and that difference is the whole question when a note goes missing.
        console.warn('vibepanel: a note could not be saved on the way out',
          e instanceof Error ? e.message : e)
      })
    },
    [projectId],
  )

  // pagehide rather than beforeunload: beforeunload is unreliable on mobile
  // and does not fire when a phone discards a backgrounded tab. visibilitychange
  // is the one that does, and it is also the last event a swipe-away delivers.
  useEffect(() => {
    const onHidden = () => {
      if (document.visibilityState === 'hidden') flush(true)
    }
    const onPageHide = () => flush(true)
    window.addEventListener('pagehide', onPageHide)
    document.addEventListener('visibilitychange', onHidden)
    return () => {
      window.removeEventListener('pagehide', onPageHide)
      document.removeEventListener('visibilitychange', onHidden)
    }
  }, [flush])

  // No reset to 'loading' here: the caller keys this component by project, so
  // a different project is a fresh instance whose initial state already is
  // 'loading'. Resetting from an effect would also render one frame showing
  // the previous project's note.
  useEffect(() => {
    let ignore = false
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
      flush(false)
    }
  }, [projectId, flush])

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
      queued.current = next
      timer.current = window.setTimeout(() => {
        queued.current = null
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

  // Through the dictionary like everything else. These were six English
  // literals in an object, which is a shape neither of the untranslated-string
  // rules can see: not an attribute, and not a line of prose between tags. The
  // panel said "saved" in English under a Chinese heading for as long as the
  // translation had existed.
  //
  // A server error keeps its own text -- it is the only thing that says which
  // failure it was -- with the translated label in front of it.
  const label: Record<typeof status, string> = {
    loading: t('notes.loading'),
    saving: t('notes.saving'),
    saved: t('notes.saved'),
    dirty: t('notes.unsaved'),
    error: error ?? t('notes.error'),
    conflict: error ?? t('notes.conflict'),
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
        placeholder={t('notes.placeholder')}
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
