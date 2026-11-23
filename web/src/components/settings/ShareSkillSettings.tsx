import { useEffect, useState } from 'react'

import { api } from '../../protocol/api'
import type { ShareSkillAgent, ShareSkillAgentId, ShareSkillStatus } from '../../protocol/wire'
import { t } from '../../i18n'
import { Section } from './parts'

/**
 * Optional agent plugin/skill installer for one-prompt share page creation.
 *
 * Writes only a dedicated `vibepanel-share` directory inside each chosen
 * harness (Claude Code, Codex, Antigravity CLI, DeepSeek Harness) and never
 * edits existing user configuration files. Every harness is independently
 * opt-in and removable.
 */
export function ShareSkillSettings() {
  const [status, setStatus] = useState<ShareSkillStatus | null>(null)
  const [busy, setBusy] = useState<ShareSkillAgentId | ''>('')
  const [error, setError] = useState<string | null>(null)
  const [showContent, setShowContent] = useState(false)

  useEffect(() => {
    let ignore = false
    api
      .shareSkillStatus()
      .then((s) => {
        if (!ignore) setStatus(s)
      })
      .catch((e: unknown) => {
        if (!ignore) setError(e instanceof Error ? e.message : String(e))
      })
    return () => {
      ignore = true
    }
  }, [])

  const act = async (agent: ShareSkillAgentId, fn: () => Promise<ShareSkillStatus>) => {
    setBusy(agent)
    setError(null)
    try {
      setStatus(await fn())
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy('')
    }
  }

  return (
    <Section id="shareskill" title={t('shareSkill.title')}>
      <p className="mb-3 text-vp-base leading-relaxed text-ink-2">
        {t('shareSkill.why')}
      </p>

      {error && (
        <p className="mb-2 text-vp-base text-warn-ink" data-testid="share-skill-error">
          {error}
        </p>
      )}

      {!status ? (
        <p className="text-vp-sm text-ink-3">{t('tune.loading')}</p>
      ) : (
        <div data-testid="share-skill-status">
          <div className="overflow-hidden rounded-vp border border-hairline bg-bg/40">
            {status.agents.map((a) => (
              <ShareSkillRow
                key={a.id}
                agent={a}
                busy={busy === a.id}
                disabled={busy !== ''}
                onInstall={() => void act(a.id, () => api.installShareSkill(a.id))}
                onRemove={() => void act(a.id, () => api.removeShareSkill(a.id))}
              />
            ))}
          </div>

          <p className="mt-2.5 text-vp-sm text-ink-3">
            {t('shareSkill.note')}
          </p>

          <div className="mt-2">
            <button
              type="button"
              data-testid="share-skill-preview-toggle"
              onClick={() => setShowContent((v) => !v)}
              className="vp-press rounded-vp px-1.5 py-0.5 text-vp-sm text-ink-2 hover:text-ink"
            >
              {showContent ? t('shareSkill.hideContent') : t('shareSkill.showContent')}
            </button>
            {showContent && (
              <pre
                data-testid="share-skill-preview"
                className="mt-2 max-h-64 overflow-auto rounded-vp border border-hairline bg-surface-2 p-3 font-mono text-vp-xs leading-relaxed text-ink-2"
              >
                {status.content}
              </pre>
            )}
          </div>
        </div>
      )}
    </Section>
  )
}

function ShareSkillRow({
  agent,
  busy,
  disabled,
  onInstall,
  onRemove,
}: {
  agent: ShareSkillAgent
  busy: boolean
  disabled: boolean
  onInstall: () => void
  onRemove: () => void
}) {
  const badge = agent.installed
    ? agent.kind === 'plugin'
      ? t('shareSkill.enabledPlugin')
      : t('shareSkill.enabledSkill')
    : agent.stale
      ? t('shareSkill.stale')
      : t('set.notInstalled')

  return (
    <div
      className="flex flex-wrap items-center justify-between gap-3 border-b border-hairline px-3.5 py-2.5 last:border-b-0"
      data-testid={`share-skill-${agent.id}-row`}
    >
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-vp-base font-medium text-ink">{agent.name}</span>
          <span
            className={`rounded-md px-1.5 py-0.5 text-vp-xs ${
              agent.installed ? 'bg-surface-2 text-done-ink' : 'bg-surface-2 text-ink-2'
            }`}
          >
            {badge}
          </span>
          {agent.detected && (
            <span className="rounded-md bg-surface-2 px-1.5 py-0.5 text-vp-xs text-ink-3">
              {t('shareSkill.detected')}
            </span>
          )}
        </div>
        <div className="mt-0.5 truncate font-mono text-vp-xs text-ink-3" title={agent.path}>
          {agent.path}
        </div>
      </div>
      <div className="flex shrink-0 items-center gap-1.5">
        {agent.installed ? (
          <button
            type="button"
            disabled={disabled}
            data-testid={`share-skill-${agent.id}-remove`}
            onClick={onRemove}
            className="vp-outline text-vp-sm"
          >
            {busy ? t('set.working') : t('set.remove')}
          </button>
        ) : (
          <>
            {agent.stale && (
              <button
                type="button"
                disabled={disabled}
                data-testid={`share-skill-${agent.id}-remove`}
                onClick={onRemove}
                className="vp-outline text-vp-sm"
              >
                {t('set.remove')}
              </button>
            )}
            <button
              type="button"
              disabled={disabled}
              data-testid={`share-skill-${agent.id}-install`}
              onClick={onInstall}
              className="vp-primary text-vp-sm"
            >
              {busy ? t('set.working') : agent.stale ? t('shareSkill.update') : t('set.install')}
            </button>
          </>
        )}
      </div>
    </div>
  )
}
