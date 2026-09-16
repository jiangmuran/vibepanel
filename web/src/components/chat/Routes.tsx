import { useState } from 'react'
import { Plus, Search, Trash2 } from 'lucide-react'

import { api } from '../../protocol/api'
import type { ChatRoutePreview, ChatRoutes, ChatRule, ChatSettings } from '../../protocol/wire'
import { t } from '../../i18n'
import { showToast } from '../toasts'
import { safeText } from '../text'
import { Card, INPUT, SELECT, Section } from './Chat'

const STATES = ['waiting', 'working', 'done'] as const
const KINDS = ['prompt', 'question', 'assistant', 'notice', 'user'] as const
const TOOLS = ['claude', 'codex', 'opencode', 'kimi', 'zcode', 'shell'] as const

function errText(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

function stateLabel(s: string): string {
  switch (s) {
    case 'waiting':
      return t('chat.stateWaiting')
    case 'working':
      return t('chat.stateWorking')
    case 'done':
      return t('chat.stateDone')
  }
  return s
}

function kindLabel(k: string): string {
  switch (k) {
    case 'prompt':
      return t('chat.kindPrompt')
    case 'question':
      return t('chat.kindQuestion')
    case 'assistant':
      return t('chat.kindAssistant')
    case 'notice':
      return t('chat.kindNotice')
    case 'user':
      return t('chat.kindUser')
  }
  return k
}

function newRule(): ChatRule {
  return {
    id: Math.random().toString(36).slice(2, 10),
    name: '',
    enabled: true,
    match: { projects: [], sessions: [], tools: [], states: [], kinds: [] },
    to: ['*'],
    screenshot: 'auto',
    coalesceSeconds: 0,
    quietHours: '',
    body: true,
  }
}

/**
 * Which changes go to which phones.
 *
 * Read top down, first match wins, the default applies otherwise. The
 * preview under the table is the part worth having: pick a session and it
 * says which rule would fire and who would be told, which is the answer to
 * "why did I not get that" without waiting for the next one.
 */
export function Routes({ data, onChange }: { data: ChatSettings; onChange: () => void }) {
  const [routes, setRoutes] = useState<ChatRoutes>(data.routes)
  const [dirty, setDirty] = useState(false)
  const [busy, setBusy] = useState(false)
  const [previewId, setPreviewId] = useState('')
  const [preview, setPreview] = useState<ChatRoutePreview | null>(null)

  // The server's copy replaces the form only while nothing is being edited:
  // a poll landing mid-edit must not put the rule back. During render, as
  // React's "adjusting state when a prop changes" pattern.
  const [seen, setSeen] = useState(data.routes)
  if (seen !== data.routes) {
    setSeen(data.routes)
    if (!dirty) setRoutes(data.routes)
  }

  const edit = (fn: (r: ChatRoutes) => ChatRoutes) => {
    setRoutes((r) => fn(structuredClone(r)))
    setDirty(true)
  }

  const save = async () => {
    setBusy(true)
    try {
      const saved = await api.saveChatRoutes(routes)
      setRoutes(saved)
      setDirty(false)
      showToast({ kind: 'success', key: 'chat.saved' })
      onChange()
    } catch (e) {
      showToast({ kind: 'error', key: 'chat.saveFailed', detail: errText(e) })
    } finally {
      setBusy(false)
    }
  }

  const runPreview = async (id: string) => {
    setPreviewId(id)
    if (!id) {
      setPreview(null)
      return
    }
    try {
      setPreview(await api.previewChatRoute(id))
    } catch (e) {
      showToast({ kind: 'error', key: 'chat.previewFailed', detail: errText(e) })
    }
  }

  const peers = data.peers.filter((p) => p.status === 'paired')
  const labels = new Map(data.factories.map((f) => [f.kind, f.label]))

  return (
    <Section id="routes" title={t('chat.routes')} lead={t('chat.routesLead')}>
      <div className="grid grid-cols-1 gap-3">
        {routes.rules.map((rule, i) => (
          <Card key={rule.id} testid={`chat-rule-${i}`}>
            <RuleEditor
              rule={rule}
              data={data}
              peers={peers.map((p) => ({ key: `${p.channel}:${p.peerId}`, label: `${labels.get(p.channel) ?? p.channel} · ${p.display || p.peerId}` }))}
              isDefault={false}
              onChange={(next) => edit((r) => ({ ...r, rules: r.rules.map((x, j) => (j === i ? next : x)) }))}
              onDelete={() => edit((r) => ({ ...r, rules: r.rules.filter((_, j) => j !== i) }))}
            />
          </Card>
        ))}
        <Card testid="chat-rule-default">
          <RuleEditor
            rule={routes.default}
            data={data}
            peers={peers.map((p) => ({ key: `${p.channel}:${p.peerId}`, label: `${labels.get(p.channel) ?? p.channel} · ${p.display || p.peerId}` }))}
            isDefault
            onChange={(next) => edit((r) => ({ ...r, default: next }))}
          />
        </Card>
      </div>

      <div className="mt-3 flex flex-wrap items-center gap-2">
        <button type="button" className="vp-control gap-1.5" onClick={() => edit((r) => ({ ...r, rules: [...r.rules, newRule()] }))}>
          <Plus size={14} />
          {t('chat.addRule')}
        </button>
        <button
          type="button"
          className="vp-control vp-press gap-1.5"
          disabled={busy || !dirty}
          onClick={() => void save()}
          data-testid="chat-save-routes"
        >
          {t('chat.saveRoutes')}
        </button>
        {dirty && <span className="text-vp-xs text-ink-3">{t('chat.unsaved')}</span>}
      </div>

      <div className="mt-4 flex flex-wrap items-center gap-2">
        <Search size={14} className="text-ink-3" />
        <label htmlFor="chat-preview" className="text-vp-sm text-ink-2">
          {t('chat.preview')}
        </label>
        <select
          id="chat-preview"
          className={SELECT}
          value={previewId}
          onChange={(e) => void runPreview(e.target.value)}
          data-testid="chat-preview-session"
        >
          <option value="">{t('chat.previewPick')}</option>
          {data.sessions.map((s) => (
            <option key={s.id} value={s.id}>
              [{s.handle}] {safeText(s.title)} · {safeText(s.project)} · {stateLabel(s.state)}
            </option>
          ))}
        </select>
      </div>
      {preview && (
        <p className="mt-2 text-vp-sm text-ink-2" data-testid="chat-preview-result">
          {preview.decision.Send
            ? preview.decision.Hold
              ? t('chat.previewHeld', { rule: safeText(preview.decision.Rule) })
              : preview.peers.length === 0
                ? t('chat.previewNobody', { rule: safeText(preview.decision.Rule) })
                : t('chat.previewSent', { rule: safeText(preview.decision.Rule), who: preview.peers.map(safeText).join(', ') })
            : t('chat.previewSilent', { rule: safeText(preview.decision.Rule) })}
        </p>
      )}
    </Section>
  )
}

function Toggle({ list, value, label, onChange }: { list: string[]; value: string; label: string; onChange: (next: string[]) => void }) {
  const on = list.includes(value)
  return (
    <label className={`flex items-center gap-1 rounded-vp border px-1.5 py-0.5 text-vp-xs ${on ? 'border-accent text-ink' : 'border-hairline text-ink-2'}`}>
      <input
        type="checkbox"
        className="sr-only"
        checked={on}
        onChange={(e) => onChange(e.target.checked ? [...list, value] : list.filter((x) => x !== value))}
      />
      {label}
    </label>
  )
}

function RuleEditor({
  rule,
  data,
  peers,
  isDefault,
  onChange,
  onDelete,
}: {
  rule: ChatRule
  data: ChatSettings
  peers: { key: string; label: string }[]
  isDefault: boolean
  onChange: (r: ChatRule) => void
  onDelete?: () => void
}) {
  const set = (patch: Partial<ChatRule>) => onChange({ ...rule, ...patch })
  const match = { projects: rule.match.projects ?? [], sessions: rule.match.sessions ?? [], tools: rule.match.tools ?? [], states: rule.match.states ?? [], kinds: rule.match.kinds ?? [] }
  const setMatch = (patch: Partial<typeof match>) => set({ match: { ...match, ...patch } })
  const to = rule.to ?? []
  const everyone = to.includes('*')

  return (
    <div className="grid grid-cols-1 gap-3 @2xl:grid-cols-[1fr_1fr]">
      <div className="min-w-0">
        <div className="mb-2 flex items-center gap-2">
          {isDefault ? (
            <span className="text-vp-md font-semibold text-ink">{t('chat.defaultRule')}</span>
          ) : (
            <input
              className={`${INPUT} flex-1`}
              placeholder={t('chat.ruleName')}
              value={rule.name}
              onChange={(e) => set({ name: e.target.value })}
            />
          )}
          {!isDefault && (
            <label className="flex shrink-0 items-center gap-1 text-vp-sm text-ink-2">
              <input type="checkbox" checked={rule.enabled} onChange={(e) => set({ enabled: e.target.checked })} />
              {t('chat.ruleEnabled')}
            </label>
          )}
          {onDelete && (
            <button type="button" className="vp-control text-ink-2" title={t('chat.deleteRule')} onClick={onDelete}>
              <Trash2 size={14} />
            </button>
          )}
        </div>
        <div className="grid grid-cols-1 gap-2">
          <div>
            <span className="mb-1 block text-vp-xs text-ink-3">{t('chat.matchStates')}</span>
            <div className="flex flex-wrap gap-1">
              {STATES.map((s) => (
                <Toggle key={s} list={match.states} value={s} label={stateLabel(s)} onChange={(states) => setMatch({ states })} />
              ))}
            </div>
          </div>
          <div>
            <span className="mb-1 block text-vp-xs text-ink-3">{t('chat.matchKinds')}</span>
            <div className="flex flex-wrap gap-1">
              {KINDS.map((k) => (
                <Toggle key={k} list={match.kinds} value={k} label={kindLabel(k)} onChange={(kinds) => setMatch({ kinds })} />
              ))}
            </div>
          </div>
          {!isDefault && (
            <>
              <div>
                <span className="mb-1 block text-vp-xs text-ink-3">{t('chat.matchTools')}</span>
                <div className="flex flex-wrap gap-1">
                  {TOOLS.map((k) => (
                    <Toggle key={k} list={match.tools} value={k} label={k} onChange={(tools) => setMatch({ tools })} />
                  ))}
                </div>
              </div>
              <div>
                <span className="mb-1 block text-vp-xs text-ink-3">{t('chat.matchProjects')}</span>
                <div className="flex flex-wrap gap-1">
                  {data.projects.map((p) => (
                    <Toggle key={p.id} list={match.projects} value={p.id} label={safeText(p.name)} onChange={(projects) => setMatch({ projects })} />
                  ))}
                  {data.projects.length === 0 && <span className="text-vp-xs text-ink-3">{t('chat.any')}</span>}
                </div>
              </div>
            </>
          )}
        </div>
      </div>
      <div className="grid grid-cols-1 gap-2">
        <div>
          <span className="mb-1 block text-vp-xs text-ink-3">{t('chat.to')}</span>
          <div className="flex flex-wrap gap-1">
            <Toggle list={to} value="*" label={t('chat.toEveryone')} onChange={(next) => set({ to: next })} />
            {!everyone &&
              peers.map((p) => (
                <Toggle key={p.key} list={to} value={p.key} label={safeText(p.label)} onChange={(next) => set({ to: next })} />
              ))}
            {to.length === 0 && <span className="text-vp-xs" style={{ color: 'var(--vp-state-waiting)' }}>{t('chat.toNobody')}</span>}
          </div>
        </div>
        <div className="grid grid-cols-2 gap-2">
          <label className="min-w-0">
            <span className="mb-1 block text-vp-xs text-ink-3">{t('chat.screenshot')}</span>
            <select className={`${SELECT} w-full`} value={rule.screenshot || 'auto'} onChange={(e) => set({ screenshot: e.target.value as ChatRule['screenshot'] })}>
              <option value="auto">{t('chat.shotAuto')}</option>
              <option value="always">{t('chat.shotAlways')}</option>
              <option value="never">{t('chat.shotNever')}</option>
            </select>
          </label>
          <label className="min-w-0">
            <span className="mb-1 block text-vp-xs text-ink-3">{t('chat.coalesce')}</span>
            <input
              className={INPUT}
              type="number"
              min={0}
              max={600}
              value={rule.coalesceSeconds}
              onChange={(e) => set({ coalesceSeconds: Math.max(0, Number(e.target.value) || 0) })}
            />
          </label>
          <label className="min-w-0">
            <span className="mb-1 block text-vp-xs text-ink-3">{t('chat.quietHours')}</span>
            <input
              className={`${INPUT} font-mono`}
              placeholder="23:00-08:00"
              value={rule.quietHours}
              onChange={(e) => set({ quietHours: e.target.value })}
            />
          </label>
          <label className="flex items-end gap-1 pb-1.5 text-vp-sm text-ink-2">
            <input type="checkbox" checked={rule.body} onChange={(e) => set({ body: e.target.checked })} />
            {t('chat.body')}
          </label>
        </div>
      </div>
    </div>
  )
}
