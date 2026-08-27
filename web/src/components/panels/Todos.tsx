import { useCallback, useEffect, useState } from 'react'
import { Check, Plus, X } from 'lucide-react'

import { api } from '../../protocol/api'
import type { Todo } from '../../protocol/wire'
import type { PanelSocket } from '../../protocol/socket'
import { InlineName } from '../InlineName'
import type { PanelDensity } from '../chrome'
import { formatAgo } from './ago'
import { t as tr, useLang } from '../../i18n'

/**
 * A project's checklist.
 *
 * Completed items stay on the list rather than vanishing. Seeing what you just
 * finished is most of the value of ticking it off, and a list that empties
 * itself gives no sense of having got anywhere.
 */
export function Todos({
  projectId,
  socket,
  density = 'narrow',
}: {
  projectId: string
  socket: PanelSocket
  density?: PanelDensity
}) {
  // Repaint when the language changes. Without this the strings are
  // resolved once and a switch needs a reload to be believed.
  useLang()
  const [todos, setTodos] = useState<Todo[]>([])
  const [draft, setDraft] = useState('')
  const [error, setError] = useState<string | null>(null)
  // One clock for every row, set when the list lands. See GitPanel's Ago for
  // why this is not a Date.now() in the body.
  const [now, setNow] = useState(() => Math.floor(Date.now() / 1000))

  const load = useCallback(async () => {
    try {
      setTodos(await api.todos(projectId))
      setNow(Math.floor(Date.now() / 1000))
      setError(null)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [projectId])

  // A list is not something anyone is mid-edit on the way a note is, so it can
  // simply refetch whenever another viewer changes it.
  useEffect(() => socket.onPanelChange((pid, kind) => {
    if (pid === projectId && kind === 'todos') void load()
  }), [projectId, socket, load])

  useEffect(() => {
    let ignore = false
    api
      .todos(projectId)
      .then((t) => {
        if (ignore) return
        setTodos(t)
        setNow(Math.floor(Date.now() / 1000))
        setError(null)
      })
      .catch((e: unknown) => {
        if (!ignore) setError(e instanceof Error ? e.message : String(e))
      })
    return () => {
      ignore = true
    }
  }, [projectId])

  const guard = async (fn: () => Promise<unknown>) => {
    try {
      await fn()
      await load()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  const add = () => {
    const text = draft.trim()
    if (!text) return
    setDraft('')
    void guard(() => api.addTodo(projectId, text))
  }

  const outstanding = todos.filter((t) => !t.done).length

  return (
    <div className="flex h-full min-h-0 flex-col" data-testid="todos">
      <div className="flex shrink-0 items-center gap-1 px-2 py-1.5">
        <input
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') add()
          }}
          placeholder={tr('todos.add')}
          data-testid="todo-input"
          className="min-w-0 flex-1 rounded-vp border border-hairline bg-surface px-2 py-1 text-vp-base text-ink outline-none placeholder:text-ink-2 focus:border-accent"
        />
        <button
          type="button"
          onClick={add}
          title={tr('todos.addShort')}
          className="vp-control"
        >
          <Plus size={14} />
        </button>
      </div>

      {/* Under the input rather than at the foot of the panel.
          It was a sibling of the scroller, so with three items in a tall column
          it floated nine hundred pixels below the last one, alone against the
          background -- a summary of something that was no longer on the same
          part of the screen. Here it is adjacent to the list and stays visible
          when the list is long enough to scroll. */}
      {todos.length > 0 && (
        <div className="tabular shrink-0 px-3 pb-1 text-right text-vp-xs text-ink-2">
          {/* The two languages put the numbers in different places, so the
              call passes both facts and the dictionary decides the order. */}
          {tr('todos.leftOf', { left: outstanding, done: todos.length - outstanding, total: todos.length })}
        </div>
      )}

      {error && (
        <p className="px-3 pb-2 text-vp-sm" style={{ color: 'var(--vp-state-waiting)' }}>
          {error}
        </p>
      )}

      <div className="min-h-0 flex-1 overflow-y-auto px-1 pb-2">
        {todos.length === 0 && (
          <p className="px-2 py-3 text-vp-base text-ink-2">{tr('todos.empty')}</p>
        )}
        {todos.map((t) => (
          <div
            key={t.id}
            data-testid="todo-item"
            data-done={t.done}
            className="vp-press group flex items-start gap-2 rounded-vp px-2 py-1 hover:bg-surface-2"
          >
            <button
              type="button"
              onClick={() => void guard(() => api.patchTodo(t.id, { done: !t.done }))}
              title={t.done ? tr('todos.markNotDone') : tr('todos.markDone')}
              className="mt-0.5 flex h-3.5 w-3.5 shrink-0 items-center justify-center rounded-md border transition-colors duration-200 ease-vp"
              style={{
                borderColor: t.done ? 'var(--vp-state-done)' : 'var(--vp-hairline-strong)',
                background: t.done ? 'var(--vp-state-done)' : 'transparent',
              }}
            >
              {t.done && <Check size={10} color="var(--vp-accent-ink)" />}
            </button>
            <InlineName
              value={t.text}
              onCommit={(next) => void guard(() => api.patchTodo(t.id, { text: next }))}
              className={`flex-1 text-vp-base leading-snug !whitespace-normal ${
                t.done ? 'text-ink-2 line-through' : 'text-ink'
              }`}
              title={tr('todos.edit')}
            />
            {/* When it was added, or when it was ticked — both are in every
                row the server has ever sent and neither reached the screen. A
                list where the top item is from March and the bottom from ten
                minutes ago is a different list from one written in one sitting,
                and the panel could not say which it was.

                Above 380px only, and in a fixed column: at the narrow end the
                item's own text is what the row is for. The delete control is
                `.vp-reveal`, which is opacity rather than display, so nothing
                here shifts when the pointer arrives. */}
            {density === 'wide' && (
              <span
                data-testid="todo-when"
                className="tabular mt-0.5 w-14 shrink-0 text-right text-vp-xs text-ink-2"
                title={t.done && t.doneAt ? tr('todos.completed') : tr('todos.added')}
              >
                {formatAgo(t.done && t.doneAt ? t.doneAt : t.createdAt, now)}
              </span>
            )}
            <button
              type="button"
              onClick={() => void guard(() => api.deleteTodo(t.id))}
              title={tr('todos.delete')}
              className="vp-control vp-reveal"
            >
              <X size={11} />
            </button>
          </div>
        ))}
      </div>

    </div>
  )
}
