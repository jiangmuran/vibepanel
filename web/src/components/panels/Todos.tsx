import { useCallback, useEffect, useState } from 'react'
import { Check, Plus, X } from 'lucide-react'

import { api } from '../../protocol/api'
import type { Todo } from '../../protocol/wire'
import { InlineName } from '../InlineName'

/**
 * A project's checklist.
 *
 * Completed items stay on the list rather than vanishing. Seeing what you just
 * finished is most of the value of ticking it off, and a list that empties
 * itself gives no sense of having got anywhere.
 */
export function Todos({ projectId }: { projectId: string }) {
  const [todos, setTodos] = useState<Todo[]>([])
  const [draft, setDraft] = useState('')
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async () => {
    try {
      setTodos(await api.todos(projectId))
      setError(null)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [projectId])

  useEffect(() => {
    let ignore = false
    api
      .todos(projectId)
      .then((t) => {
        if (ignore) return
        setTodos(t)
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
      <div className="flex shrink-0 items-center gap-1 px-2 py-2">
        <input
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') add()
          }}
          placeholder="Add an item"
          data-testid="todo-input"
          className="min-w-0 flex-1 rounded-vp border border-hairline bg-surface px-2 py-1 text-[12px] text-ink outline-none placeholder:text-ink-2 focus:border-accent"
        />
        <button
          type="button"
          onClick={add}
          title="Add"
          className="rounded-md p-1.5 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
        >
          <Plus size={14} />
        </button>
      </div>

      {error && (
        <p className="px-3 pb-2 text-[11px]" style={{ color: 'var(--vp-state-waiting)' }}>
          {error}
        </p>
      )}

      <div className="min-h-0 flex-1 overflow-y-auto px-1 pb-2">
        {todos.length === 0 && (
          <p className="px-2 py-3 text-[12px] text-ink-2">Nothing on the list.</p>
        )}
        {todos.map((t) => (
          <div
            key={t.id}
            data-testid="todo-item"
            data-done={t.done}
            className="group flex items-start gap-2 rounded-vp px-2 py-1.5 hover:bg-surface-2"
          >
            <button
              type="button"
              onClick={() => void guard(() => api.patchTodo(t.id, { done: !t.done }))}
              title={t.done ? 'Mark as not done' : 'Mark as done'}
              className="mt-0.5 flex h-3.5 w-3.5 shrink-0 items-center justify-center rounded border transition-colors duration-200 ease-vp"
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
              className={`flex-1 text-[12.5px] leading-snug !whitespace-normal ${
                t.done ? 'text-ink-2 line-through' : 'text-ink'
              }`}
              title="Double click to edit"
            />
            <button
              type="button"
              onClick={() => void guard(() => api.deleteTodo(t.id))}
              title="Delete"
              className="mt-0.5 shrink-0 rounded p-0.5 text-ink-2 opacity-0 transition-opacity duration-200 ease-vp group-hover:opacity-100 hover:text-ink focus-visible:opacity-100"
            >
              <X size={11} />
            </button>
          </div>
        ))}
      </div>

      {todos.length > 0 && (
        <div className="tabular shrink-0 px-3 py-1 text-right text-[10.5px] text-ink-2">
          {outstanding} of {todos.length} left
        </div>
      )}
    </div>
  )
}
