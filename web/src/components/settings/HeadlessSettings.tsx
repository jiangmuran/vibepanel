import { useEffect, useState } from 'react'
import { Plus, Trash2 } from 'lucide-react'

import { api } from '../../protocol/api'
import type { HeadlessSettings as Settings, LaunchProfile } from '../../protocol/wire'
import { t } from '../../i18n'
import { profileLabel } from '../profiles'
import { safeText } from '../text'
import { Section } from './parts'

/** The server's list, in the server's order; see headless.PermissionModes. */
const MODES = ['bypassPermissions', 'auto', 'acceptEdits', 'dontAsk', 'plan'] as const

const INPUT =
  'min-w-0 rounded-vp border border-hairline bg-surface-2 px-2 py-1.5 text-vp-base text-ink outline-none focus:border-accent'

/**
 * The G2 glasses' personal assistant: `claude -p` runs started over HTTP with
 * an API token. One form and one Save, because the fields only make sense
 * together -- the default model has to be in the model list, and switching it
 * on is what creates the workspace the directory field names.
 *
 * The key is write-only. The page is told whether one is stored and never what
 * it is, the same as a launch profile's secrets.
 */
export function HeadlessSettings() {
  const [loaded, setLoaded] = useState<Settings | null>(null)
  const [profiles, setProfiles] = useState<LaunchProfile[]>([])
  const [loadErr, setLoadErr] = useState('')

  useEffect(() => {
    let ignore = false
    api.headlessSettings().then(
      (s) => {
        if (!ignore) setLoaded(s)
      },
      (e: unknown) => {
        if (!ignore) setLoadErr(e instanceof Error ? e.message : String(e))
      },
    )
    api.launchProfiles().then(
      (p) => {
        if (!ignore) setProfiles(p)
      },
      () => {},
    )
    return () => {
      ignore = true
    }
  }, [])

  return (
    <Section id="headless" title={t('hl.title')}>
      {loaded ? (
        <HeadlessForm initial={loaded} profiles={profiles} />
      ) : (
        loadErr && <p className="text-vp-sm text-state-crashed">{safeText(loadErr)}</p>
      )}
    </Section>
  )
}

/** The form itself, once the settings have arrived. */
function HeadlessForm({ initial, profiles }: { initial: Settings; profiles: LaunchProfile[] }) {
  const [draft, setDraft] = useState<Settings>(initial)
  const [apiKey, setApiKey] = useState('')
  const [clearKey, setClearKey] = useState(false)
  const [busy, setBusy] = useState(false)
  const [saved, setSaved] = useState(false)
  const [err, setErr] = useState('')

  const set = (patch: Partial<Settings>) => {
    setSaved(false)
    setDraft({ ...draft, ...patch })
  }
  const setAsr = (patch: Partial<Settings['asr']>) => set({ asr: { ...draft.asr, ...patch } })

  const save = async () => {
    setBusy(true)
    setErr('')
    setSaved(false)
    try {
      const next = await api.saveHeadlessSettings({
        enabled: draft.enabled,
        allowedOrigins: draft.allowedOrigins,
        defaultModel: draft.defaultModel,
        models: draft.models,
        defaultPermissionMode: draft.defaultPermissionMode,
        launchProfileId: draft.launchProfileId,
        assistantDir: draft.assistantDir,
        persona: draft.persona,
        memoryInjection: draft.memoryInjection,
        maxConcurrent: draft.maxConcurrent,
        timeoutMinutes: draft.timeoutMinutes,
        asr: {
          baseUrl: draft.asr.baseUrl,
          model: draft.asr.model,
          language: draft.asr.language,
          prompt: draft.asr.prompt,
          ...(apiKey.trim() ? { apiKey: apiKey.trim() } : {}),
          ...(clearKey ? { clearKey: true } : {}),
        },
      })
      setDraft(next)
      setApiKey('')
      setClearKey(false)
      setSaved(true)
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <div data-testid="headless-settings" className="@container flex flex-col gap-3">
        <p className="text-vp-base leading-relaxed text-ink-2">{t('hl.why')}</p>

        <label className="flex items-center gap-2 text-vp-base text-ink">
          <input
            type="checkbox"
            checked={draft.enabled}
            data-testid="headless-enabled"
            onChange={(e) => set({ enabled: e.target.checked })}
          />
          <span>{t('hl.enabled')}</span>
        </label>
        <p className="text-vp-sm leading-relaxed text-state-waiting">{t('hl.danger')}</p>
        <p className="text-vp-sm leading-relaxed text-ink-2">
          {draft.assistantProjectId
            ? t('hl.projectHint', { path: draft.assistantDir })
            : t('hl.projectPending')}{' '}
          {t('hl.tokenHint')}
        </p>

        <Field label={t('hl.origins')} hint={t('hl.originsHint')}>
          <ListEditor
            items={draft.allowedOrigins}
            placeholder="http://192.168.1.20:5173"
            testid="headless-origin"
            onChange={(allowedOrigins) => set({ allowedOrigins })}
          />
        </Field>

        <Field label={t('hl.models')} hint={t('hl.modelsHint')}>
          <div className="flex flex-col gap-1">
            {draft.models.map((m, i) => (
              <div key={i} className="flex items-center gap-2">
                <input
                  value={m.id}
                  placeholder={t('hl.modelId')}
                  data-testid="headless-model-id"
                  onChange={(e) => {
                    const models = draft.models.slice()
                    models[i] = { ...m, id: e.target.value }
                    set({ models })
                  }}
                  className={`${INPUT} flex-1 font-mono`}
                />
                <input
                  value={m.label}
                  placeholder={t('hl.modelLabel')}
                  onChange={(e) => {
                    const models = draft.models.slice()
                    models[i] = { ...m, label: e.target.value }
                    set({ models })
                  }}
                  className={`${INPUT} flex-1`}
                />
                <button
                  type="button"
                  title={t('hl.remove')}
                  onClick={() => set({ models: draft.models.filter((_, j) => j !== i) })}
                  className="vp-control"
                >
                  <Trash2 size={13} />
                </button>
              </div>
            ))}
            <div>
              <button
                type="button"
                onClick={() => set({ models: [...draft.models, { id: '', label: '' }] })}
                className="vp-control"
              >
                <Plus size={13} />
                <span className="text-vp-sm">{t('hl.add')}</span>
              </button>
            </div>
          </div>
        </Field>

        <div className="flex flex-wrap gap-3">
          <Field label={t('hl.defaultModel')}>
            <select
              value={draft.defaultModel}
              data-testid="headless-default-model"
              onChange={(e) => set({ defaultModel: e.target.value })}
              className={INPUT}
            >
              {draft.models
                .filter((m) => m.id.trim() !== '')
                .map((m) => (
                  <option key={m.id} value={m.id}>
                    {m.label || m.id}
                  </option>
                ))}
            </select>
          </Field>
          <Field label={t('hl.mode')}>
            <select
              value={draft.defaultPermissionMode}
              data-testid="headless-mode"
              onChange={(e) => set({ defaultPermissionMode: e.target.value })}
              className={INPUT}
            >
              {MODES.map((m) => (
                <option key={m} value={m}>
                  {m === 'bypassPermissions' ? t('hl.modeBypass') : m}
                </option>
              ))}
            </select>
          </Field>
          <Field label={t('hl.profile')}>
            <select
              value={draft.launchProfileId}
              onChange={(e) => set({ launchProfileId: e.target.value })}
              className={INPUT}
            >
              <option value="">{t('hl.profileNone')}</option>
              {profiles.map((p) => (
                <option key={p.id} value={p.id}>
                  {profileLabel(p)}
                </option>
              ))}
            </select>
          </Field>
        </div>

        <Field label={t('hl.dir')} hint={t('hl.dirHint')}>
          <input
            value={draft.assistantDir}
            data-testid="headless-dir"
            onChange={(e) => set({ assistantDir: e.target.value })}
            className={`${INPUT} w-full font-mono`}
          />
        </Field>

        <Field label={t('hl.persona')} hint={t('hl.personaHint')}>
          <textarea
            value={draft.persona}
            rows={8}
            placeholder={draft.defaultPersona}
            data-testid="headless-persona"
            onChange={(e) => set({ persona: e.target.value })}
            className={`${INPUT} w-full resize-y font-mono text-vp-sm`}
          />
          <div className="mt-1">
            <button
              type="button"
              disabled={draft.persona === ''}
              data-testid="headless-persona-reset"
              onClick={() => set({ persona: '' })}
              className="vp-control"
            >
              <span className="text-vp-sm">{t('hl.personaReset')}</span>
            </button>
          </div>
        </Field>

        <label className="flex items-center gap-2 text-vp-base text-ink">
          <input
            type="checkbox"
            checked={draft.memoryInjection}
            onChange={(e) => set({ memoryInjection: e.target.checked })}
          />
          <span>{t('hl.memory')}</span>
        </label>

        <div className="flex flex-wrap gap-3">
          <Field label={t('hl.maxConcurrent')}>
            <input
              type="number"
              min={1}
              max={10}
              value={draft.maxConcurrent}
              onChange={(e) => set({ maxConcurrent: Number(e.target.value) })}
              className={`${INPUT} w-24`}
            />
          </Field>
          <Field label={t('hl.timeout')}>
            <input
              type="number"
              min={1}
              max={240}
              value={draft.timeoutMinutes}
              onChange={(e) => set({ timeoutMinutes: Number(e.target.value) })}
              className={`${INPUT} w-24`}
            />
          </Field>
        </div>

        <fieldset className="border-t border-hairline pt-3">
          <legend className="mb-1 text-vp-sm text-ink-2">{t('hl.asr')}</legend>
          <p className="mb-2 text-vp-sm leading-relaxed text-ink-2">{t('hl.asrHint')}</p>
          <div className="grid gap-2 @3xl:grid-cols-2">
            <Field label={t('hl.asrBase')}>
              <input
                value={draft.asr.baseUrl}
                onChange={(e) => setAsr({ baseUrl: e.target.value })}
                className={`${INPUT} w-full font-mono`}
              />
            </Field>
            <Field label={t('hl.asrModel')}>
              <input
                value={draft.asr.model}
                onChange={(e) => setAsr({ model: e.target.value })}
                className={`${INPUT} w-full font-mono`}
              />
            </Field>
            <Field label={t('hl.asrLang')}>
              <input
                value={draft.asr.language}
                onChange={(e) => setAsr({ language: e.target.value })}
                className={`${INPUT} w-full`}
              />
            </Field>
            <Field label={t('hl.asrPrompt')}>
              <input
                value={draft.asr.prompt}
                onChange={(e) => setAsr({ prompt: e.target.value })}
                className={`${INPUT} w-full`}
              />
            </Field>
          </div>
          <div className="mt-2">
            <Field label={t('hl.asrKey')}>
              <div className="flex flex-wrap items-center gap-2">
                <input
                  type="password"
                  autoComplete="off"
                  value={apiKey}
                  data-testid="headless-asr-key"
                  placeholder={draft.asr.hasKey && !clearKey ? t('hl.keySet') : t('hl.keyNone')}
                  onChange={(e) => {
                    setSaved(false)
                    setApiKey(e.target.value)
                  }}
                  className={`${INPUT} min-w-48 flex-1`}
                />
                {draft.asr.hasKey && !clearKey && (
                  <>
                    <span className="text-vp-sm text-state-done">{t('hl.keySet')}</span>
                    <button
                      type="button"
                      data-testid="headless-asr-clear"
                      onClick={() => {
                        setSaved(false)
                        setClearKey(true)
                        setApiKey('')
                      }}
                      className="vp-control"
                    >
                      <span className="text-vp-sm">{t('hl.keyClear')}</span>
                    </button>
                  </>
                )}
                {clearKey && <span className="text-vp-sm text-state-waiting">{t('hl.keyWillClear')}</span>}
              </div>
            </Field>
          </div>
        </fieldset>

        <div className="flex items-center gap-3">
          <button
            type="button"
            disabled={busy}
            data-testid="headless-save"
            onClick={() => void save()}
            className="rounded-vp px-3 py-1.5 text-vp-base font-medium disabled:opacity-50"
            style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
          >
            {busy ? t('set.working') : t('hl.save')}
          </button>
          {saved && <span className="text-vp-sm text-state-done">{t('paste.saved')}</span>}
        </div>
        {err && <p className="text-vp-sm text-state-crashed">{safeText(err)}</p>}
      </div>
    </>
  )
}

function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <span className="text-vp-sm text-ink-2">{label}</span>
      {children}
      {hint && <span className="text-vp-xs leading-relaxed text-ink-3">{hint}</span>}
    </div>
  )
}

/** A list of one-line strings: a row each, a remove button, and an add. */
function ListEditor({
  items,
  placeholder,
  testid,
  onChange,
}: {
  items: string[]
  placeholder: string
  testid: string
  onChange: (next: string[]) => void
}) {
  return (
    <div className="flex flex-col gap-1">
      {items.map((v, i) => (
        <div key={i} className="flex items-center gap-2">
          <input
            value={v}
            placeholder={placeholder}
            data-testid={testid}
            onChange={(e) => {
              const next = items.slice()
              next[i] = e.target.value
              onChange(next)
            }}
            className={`${INPUT} flex-1 font-mono`}
          />
          <button
            type="button"
            title={t('hl.remove')}
            onClick={() => onChange(items.filter((_, j) => j !== i))}
            className="vp-control"
          >
            <Trash2 size={13} />
          </button>
        </div>
      ))}
      <div>
        <button type="button" data-testid={`${testid}-add`} onClick={() => onChange([...items, ''])} className="vp-control">
          <Plus size={13} />
          <span className="text-vp-sm">{t('hl.add')}</span>
        </button>
      </div>
    </div>
  )
}
