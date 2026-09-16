import { useState } from 'react'
import { Check, Plus, Search, Trash2 } from 'lucide-react'

import { api } from '../../protocol/api'
import type { ChatRoutePreview, ChatRoutes, ChatRule, ChatSettings } from '../../protocol/wire'
import { t } from '../../i18n'
import { askConfirm } from '../ask'
import { showToast } from '../toasts'
import { safeText } from '../text'
import { Card, Section } from './Chat'
import { chatError } from './errors'
import { INPUT, Primary, SELECT, Secondary, errText, useServerCopy } from './form'

const STATES = ['waiting', 'working', 'done'] as const
const KINDS = ['prompt', 'question', 'assistant', 'notice', 'user'] as const
const TOOLS = ['claude', 'codex', 'opencode', 'kimi', 'zcode', 'shell'] as const

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

/**
 * Whether a rule, as it stands, silences permission requests: it is on, it
 * sends to nobody, and nothing in it excludes prompts. The one message that
 * must not go missing quietly, so saving such a rule asks first.
 */
function silencesRequests(rule: ChatRule, paired: Set<string>): boolean {
  if (!rule.enabled) return false
  const to = (rule.to ?? []).filter((k) => k === '*' || paired.has(k))
  const kinds = rule.match.kinds ?? []
  const states = rule.match.states ?? []
  return to.length === 0 && (kinds.length === 0 || kinds.includes('prompt')) && (states.length === 0 || states.includes('waiting'))
}

/** A removed person's destination, as readable as what is left of it:
 *  the app's name and the part of the id before any @. */
function goneLabel(key: string, data: ChatSettings): string {
  const [channel, ...rest] = key.split(':')
  const id = rest.join(':')
  const label = data.factories.find((f) => f.kind === channel)?.label ?? channel
  return `${label} · ${id.split('@')[0]}`
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
  const [dirty, setDirty] = useState(false)
  const [routes, setRoutes] = useServerCopy<ChatRoutes>(data.routes, dirty)
  const [busy, setBusy] = useState(false)
  const [previewId, setPreviewId] = useState('')
  const [preview, setPreview] = useState<ChatRoutePreview | null>(null)

  const edit = (fn: (r: ChatRoutes) => ChatRoutes) => {
    setRoutes((r) => fn(structuredClone(r)))
    setDirty(true)
  }

  const save = async () => {
    const paired = new Set(peers.map((p) => p.key))
    if (routes.rules.some((r) => silencesRequests(r, paired))) {
      const ok = await askConfirm({
        title: t('chat.saveSilentTitle'),
        body: t('chat.saveSilentBody'),
        confirm: t('chat.saveRoutes'),
        cancel: t('chat.cancel'),
        destructive: true,
      })
      if (!ok) return
    }
    setBusy(true)
    try {
      const saved = await api.saveChatRoutes(routes)
      setRoutes(saved)
      setDirty(false)
      showToast({ kind: 'success', key: 'chat.saved' })
      onChange()
    } catch (e) {
      showToast({ kind: 'error', key: 'chat.saveFailed', detail: chatError(e) })
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

  const labels = new Map(data.factories.map((f) => [f.kind, f.label]))
  const peers = data.peers
    .filter((p) => p.status === 'paired')
    .map((p) => ({ key: `${p.channel}:${p.peerId}`, label: `${labels.get(p.channel) ?? p.channel} · ${p.display || p.peerId}` }))
  // The preview answers in "channel:peer" keys; a person reads names.
  const nameOf = (key: string) => peers.find((p) => p.key === key)?.label ?? key
  const ruleName = (rule: string) => (rule === 'default' ? t('chat.defaultRule') : safeText(rule))
  const said = (d: ChatRoutePreview['decision'], who: string[] | null) =>
    d.send
      ? d.hold
        ? t('chat.previewHeld', { rule: ruleName(d.rule) })
        : (who ?? []).length === 0
          ? t('chat.previewNobody', { rule: ruleName(d.rule) })
          : t('chat.previewSent', { rule: ruleName(d.rule), who: (who ?? []).map((k) => safeText(nameOf(k))).join('、') })
      : t('chat.previewSilent', { rule: ruleName(d.rule) })

  return (
    <Section id="routes" title={t('chat.routes')} lead={t('chat.routesLead')}>
      <div className="grid grid-cols-1 gap-3">
        {routes.rules.map((rule, i) => (
          <Card key={rule.id} testid={`chat-rule-${i}`}>
            <RuleEditor
              rule={rule}
              data={data}
              peers={peers}
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
            peers={peers}
            isDefault
            onChange={(next) => edit((r) => ({ ...r, default: next }))}
          />
        </Card>
      </div>

      <div className="mt-3 flex flex-wrap items-center gap-2">
        <Secondary onClick={() => edit((r) => ({ ...r, rules: [...r.rules, newRule()] }))}>
          <Plus size={14} />
          {t('chat.addRule')}
        </Secondary>
        <Primary disabled={busy || !dirty} onClick={() => void save()} data-testid="chat-save-routes">
          {t('chat.saveRoutes')}
        </Primary>
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
        <div className="mt-2 text-vp-sm text-ink-2" data-testid="chat-preview-result">
          <p>
            {t('chat.previewNow')}
            {said(preview.decision, preview.peers)}
          </p>
          {/* A rule for permission requests is the one people check, and
              the session is rarely asking at the moment it is picked. */}
          {preview.request && (
            <p data-testid="chat-preview-request">
              {t('chat.previewRequest')}
              {said(preview.request, preview.requestPeers)}
            </p>
          )}
        </div>
      )}
    </Section>
  )
}

/**
 * One choice in a set. On is a filled chip with a check mark, off an
 * outlined one: the mark is the difference a person who cannot see the
 * colours reads (red line 4), and the fill is what everybody else does.
 */
function Toggle({ list, value, label, onChange }: { list: string[]; value: string; label: string; onChange: (next: string[]) => void }) {
  const on = list.includes(value)
  return (
    <label
      className={`vp-press flex cursor-pointer items-center gap-1 rounded-vp border px-2 py-1 text-vp-sm has-focus-visible:ring-2 has-focus-visible:ring-accent ${on ? 'border-accent' : 'border-hairline text-ink-2'}`}
      style={on ? { background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' } : undefined}
    >
      <input
        type="checkbox"
        className="sr-only"
        checked={on}
        onChange={(e) => onChange(e.target.checked ? [...list, value] : list.filter((x) => x !== value))}
      />
      {on && <Check size={12} aria-hidden="true" />}
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
  const silent = silencesRequests(rule, new Set(peers.map((p) => p.key)))
  // A destination whose person was removed or blocked still sits in the
  // rule and still matches nothing. Shown, so it can be taken out, rather
  // than kept invisibly.
  const gone = to.filter((k) => k !== '*' && !peers.some((p) => p.key === k))

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
            <Secondary className="text-ink-2" title={t('chat.deleteRule')} aria-label={t('chat.deleteRule')} onClick={onDelete}>
              <Trash2 size={14} />
            </Secondary>
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
            {/* The people stay on screen when everyone is chosen: hiding them
                made "everyone" read as nobody in particular. */}
            {everyone ? (
              <span className="self-center text-vp-xs text-ink-3">
                {peers.length > 0
                  ? t('chat.everyoneIncludes', { who: peers.map((p) => safeText(p.label)).join('、') })
                  : t('chat.nobodyPaired')}
              </span>
            ) : (
              peers.map((p) => (
                <Toggle key={p.key} list={to} value={p.key} label={safeText(p.label)} onChange={(next) => set({ to: next })} />
              ))
            )}
            {gone.map((k) => (
              <button
                key={k}
                type="button"
                className="vp-press flex items-center gap-1 rounded-vp border border-dashed px-2 py-1 text-vp-sm text-ink-2"
                style={{ borderColor: 'var(--vp-state-waiting)' }}
                title={t('chat.goneRemove')}
                onClick={() => set({ to: to.filter((x) => x !== k) })}
              >
                <Trash2 size={12} aria-hidden="true" />
                <span className="line-through">{safeText(goneLabel(k, data))}</span>
                <span>{t('chat.goneMark')}</span>
              </button>
            ))}
          </div>
          {silent && (
            <p className="mt-1 flex items-center gap-1 text-vp-sm font-medium" style={{ color: 'var(--vp-state-crashed)' }} data-testid="chat-rule-silent">
              ▲ {t('chat.ruleSilent')}
            </p>
          )}
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
              placeholder="3"
              value={rule.coalesceSeconds || ''}
              onChange={(e) => set({ coalesceSeconds: Math.max(0, Number(e.target.value) || 0) })}
            />
          </label>
          <label className="min-w-0">
            <span className="mb-1 block text-vp-xs text-ink-3">{t('chat.quietHours')}</span>
            <input
              className={`${INPUT} font-mono`}
              placeholder={t('chat.quietExample')}
              value={rule.quietHours}
              onChange={(e) => set({ quietHours: e.target.value })}
            />
          </label>
          {rule.quietHours && <p className="col-span-2 text-vp-xs text-ink-3">{t('chat.quietNote')}</p>}
          <label className="col-span-2 flex items-center gap-1.5 text-vp-sm text-ink-2">
            <input type="checkbox" checked={rule.body} onChange={(e) => set({ body: e.target.checked })} />
            {t('chat.body')}
          </label>
        </div>
      </div>
    </div>
  )
}
