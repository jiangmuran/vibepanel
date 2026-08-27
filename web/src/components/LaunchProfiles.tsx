import { useEffect, useState } from 'react'
import { Copy, Pencil, Plus, Sliders, Trash2 } from 'lucide-react'

import type { LaunchEnvVar, LaunchProfile } from '../protocol/wire'
import { api } from '../protocol/api'
import { envCount, looksSecret, profileLabel } from './profiles'
import { joinArgv, splitArgv } from '../shell'
import { safeText } from './text'
import { askConfirm } from './ask'
import { t, useLang } from '../i18n'

/** What the editor holds while it is open. A profile, before it is a profile. */
interface Draft {
  /** Empty for one that does not exist yet. */
  id: string
  name: string
  command: string
  env: LaunchEnvVar[]
}

function draftOf(p: LaunchProfile, id: string, name: string): Draft {
  return {
    id,
    name,
    command: joinArgv(p.command),
    // The values of secrets are not here, because they were never sent. An
    // empty one on the way back means "keep the stored value", which is what
    // stops renaming a profile from wiping every key in it.
    env: p.env.map((v) => ({ ...v })),
  }
}

const EMPTY: Draft = { id: '', name: '', command: '', env: [] }

/**
 * Making and editing launch profiles.
 *
 * Built-ins are listed with the rest and cannot be edited — they are Go
 * constants so that their names exist in both languages and a release can
 * correct one. Duplicating a built-in is the intended way in: the copy arrives
 * with the agent's command and the names of the variables it reads already
 * filled in, and empty values, which are not passed to the process at all.
 */
export function LaunchProfiles() {
  useLang()
  const [profiles, setProfiles] = useState<LaunchProfile[]>([])
  const [draft, setDraft] = useState<Draft | null>(null)
  const [error, setError] = useState('')

  const reload = async () => {
    setProfiles(await api.launchProfiles())
  }

  useEffect(() => {
    let cancelled = false
    api.launchProfiles().then(
      (list) => {
        if (!cancelled) setProfiles(list)
      },
      (e: unknown) => {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e))
      },
    )
    return () => {
      cancelled = true
    }
  }, [])

  const save = async () => {
    if (!draft) return
    const body = {
      name: draft.name,
      command: splitArgv(draft.command),
      env: draft.env,
    }
    try {
      if (draft.id) await api.updateLaunchProfile(draft.id, body)
      else await api.createLaunchProfile(body)
      setDraft(null)
      setError('')
      await reload()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  const remove = async (p: LaunchProfile) => {
    const yes = await askConfirm({
      title: t('profile.removeTitle', { name: profileLabel(p) }),
      body: t('profile.removeBody'),
      confirm: t('profile.remove'),
      cancel: t('profile.cancel'),
      destructive: true,
    })
    if (!yes) return
    try {
      await api.deleteLaunchProfile(p.id)
      setError('')
      await reload()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  return (
    <div data-testid="launch-profiles">
      <p className="mb-2 text-vp-base leading-relaxed text-ink-2">{t('profile.why')}</p>

      {error && (
        <p
          className="mb-2 text-vp-base"
          data-testid="profile-error"
          style={{ color: 'var(--vp-state-crashed)' }}
        >
          {safeText(error)}
        </p>
      )}

      {profiles.map((p) => (
        <div
          key={p.id}
          data-testid="profile-row"
          data-profile={p.id}
          className="flex items-center gap-2 border-t border-hairline py-2 text-vp-base first:border-t-0"
        >
          <Sliders size={13} className="shrink-0 text-ink-2" />
          <span className="min-w-0 flex-1">
            <span className="block truncate text-ink">{safeText(profileLabel(p))}</span>
            {p.command.length > 0 && (
              <span className="block truncate font-mono text-vp-sm text-ink-2">
                {safeText(joinArgv(p.command))}
              </span>
            )}
          </span>
          {envCount(p) > 0 && (
            <span className="tabular shrink-0 text-vp-sm text-ink-2">
              {envCount(p) === 1 ? t('profile.envSetOne') : t('profile.envSet', { n: envCount(p) })}
            </span>
          )}
          {p.builtin ? (
            <>
              <span className="shrink-0 text-vp-sm text-ink-3">{t('profile.builtinTag')}</span>
              <button
                type="button"
                onClick={() => setDraft(draftOf(p, '', t('profile.copySuffix', { name: profileLabel(p) })))}
                title={t('profile.duplicate')}
                data-testid="profile-duplicate"
                className="vp-control vp-press"
              >
                <Copy size={13} />
              </button>
            </>
          ) : (
            <>
              <button
                type="button"
                onClick={() => setDraft(draftOf(p, p.id, p.name))}
                title={t('profile.edit')}
                data-testid="profile-edit"
                className="vp-control vp-press"
              >
                <Pencil size={13} />
              </button>
              <button
                type="button"
                onClick={() => void remove(p)}
                title={t('profile.remove')}
                data-testid="profile-remove"
                className="vp-control vp-press"
              >
                <Trash2 size={13} />
              </button>
            </>
          )}
        </div>
      ))}

      {!draft && (
        <button
          type="button"
          onClick={() => setDraft({ ...EMPTY })}
          data-testid="profile-new"
          className="vp-press mt-3 flex items-center gap-1.5 rounded-vp px-3 py-1.5 text-vp-base"
          style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
        >
          <Plus size={13} />
          {t('profile.new')}
        </button>
      )}

      {draft && (
        <Editor
          // A fresh editor per profile. It remembers which rows the person has
          // decided about themselves, and that is about *these* rows; without
          // the key, opening a second profile applies the first one's answers
          // to variables at the same positions.
          key={draft.id || 'new'}
          draft={draft}
          onChange={setDraft}
          onSave={() => void save()}
          onCancel={() => setDraft(null)}
        />
      )}
    </div>
  )
}

function Editor({
  draft,
  onChange,
  onSave,
  onCancel,
}: {
  draft: Draft
  onChange: (d: Draft) => void
  onSave: () => void
  onCancel: () => void
}) {
  useLang()
  // Which rows the person has decided about themselves. The checkbox ticks
  // itself for a name that looks like a credential, and a guess that keeps
  // re-applying itself over an explicit answer is worse than no guess.
  const [decided, setDecided] = useState<Set<number>>(new Set())
  const field =
    'min-w-0 flex-1 rounded-vp border border-hairline bg-surface-2 px-2 py-1.5 text-vp-md text-ink outline-none focus:border-accent'

  const setVar = (i: number, patch: Partial<LaunchEnvVar>) => {
    const env = draft.env.map((v, j) => (i === j ? { ...v, ...patch } : v))
    onChange({ ...draft, env })
  }

  const setName = (i: number, name: string) => {
    const patch: Partial<LaunchEnvVar> = { name }
    if (!decided.has(i)) patch.secret = looksSecret(name)
    setVar(i, patch)
  }

  const setSecret = (i: number, secret: boolean) => {
    setDecided((prev) => new Set(prev).add(i))
    setVar(i, { secret })
  }

  // `decided` is a set of positions, so removing a row has to move the ones
  // after it. Otherwise deleting the first variable makes the second inherit
  // its answer and stop guessing.
  const removeVar = (i: number) => {
    setDecided((prev) => {
      const next = new Set<number>()
      for (const j of prev) {
        if (j < i) next.add(j)
        else if (j > i) next.add(j - 1)
      }
      return next
    })
    onChange({ ...draft, env: draft.env.filter((_, j) => j !== i) })
  }

  return (
    <div
      data-testid="profile-editor"
      className="mt-3 rounded-vp border border-hairline bg-surface-2 p-3"
    >
      <div className="mb-2 flex flex-wrap items-center gap-2">
        <input
          value={draft.name}
          onChange={(e) => onChange({ ...draft, name: e.target.value })}
          placeholder={t('profile.name')}
          data-testid="profile-name"
          className={field}
        />
      </div>
      <div className="mb-1 flex flex-wrap items-center gap-2">
        <input
          value={draft.command}
          onChange={(e) => onChange({ ...draft, command: e.target.value })}
          placeholder={t('profile.command')}
          data-testid="profile-command"
          className={`${field} font-mono`}
        />
      </div>
      <p className="mb-3 text-vp-sm text-ink-3">{t('profile.commandHint')}</p>

      <p className="mb-1 text-vp-sm font-semibold tracking-wide text-ink-2 uppercase">
        {t('profile.env')}
      </p>
      {draft.env.map((v, i) => (
        <div key={i} data-testid="profile-env-row" className="mb-1.5 flex flex-wrap items-center gap-2">
          <input
            value={v.name}
            onChange={(e) => setName(i, e.target.value)}
            placeholder={t('profile.envName')}
            data-testid="profile-env-name"
            className={`${field} font-mono`}
          />
          <input
            value={v.value}
            type={v.secret ? 'password' : 'text'}
            onChange={(e) => setVar(i, { value: e.target.value })}
            placeholder={v.secret && v.hasValue ? t('profile.secretKept') : t('profile.envValue')}
            data-testid="profile-env-value"
            className={`${field} font-mono`}
          />
          <label className="flex shrink-0 items-center gap-1 text-vp-sm text-ink-2">
            <input
              type="checkbox"
              checked={v.secret}
              data-testid="profile-env-secret"
              onChange={(e) => setSecret(i, e.target.checked)}
            />
            {t('profile.secret')}
          </label>
          <button
            type="button"
            onClick={() => removeVar(i)}
            title={t('profile.envRemove')}
            data-testid="profile-env-remove"
            className="vp-control vp-press"
          >
            <Trash2 size={13} />
          </button>
        </div>
      ))}
      <button
        type="button"
        onClick={() =>
          onChange({
            ...draft,
            env: [...draft.env, { name: '', value: '', secret: false, hasValue: false }],
          })
        }
        data-testid="profile-env-add"
        className="vp-press mt-1 flex items-center gap-1.5 rounded-vp border border-hairline px-2 py-1 text-vp-sm text-ink-2 hover:text-ink"
      >
        <Plus size={12} />
        {t('profile.envAdd')}
      </button>

      {draft.env.some((v) => v.secret) && (
        <>
          <p className="mt-2 text-vp-sm text-ink-3">{t('profile.plaintext')}</p>
          {draft.env.some((v) => v.secret && v.hasValue) && (
            <p className="text-vp-sm text-ink-3">{t('profile.secretRenamed')}</p>
          )}
        </>
      )}

      <div className="mt-3 flex flex-wrap items-center gap-2">
        <button
          type="button"
          onClick={onSave}
          data-testid="profile-save"
          className="vp-press shrink-0 rounded-vp px-3 py-1.5 text-vp-base"
          style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
        >
          {t('profile.save')}
        </button>
        <button
          type="button"
          onClick={onCancel}
          data-testid="profile-cancel"
          className="vp-press shrink-0 rounded-vp px-3 py-1.5 text-vp-base text-ink-2 hover:text-ink"
        >
          {t('profile.cancel')}
        </button>
      </div>
    </div>
  )
}
