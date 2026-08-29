import { useEffect, useState } from 'react'
import { Check, Copy } from 'lucide-react'

import { api } from '../../protocol/api'
import type { HookStatus } from '../../protocol/wire'
import { t } from '../../i18n'
import { copyTextInGesture } from '../../clipboard'
import { LaunchProfiles } from '../LaunchProfiles'
import { Row, Section } from './parts'
import { TuneClaude } from './TuneClaude'

/**
 * What a session is started with, and how the panel learns what it is doing.
 *
 * The two belong together: a launch profile decides which agent runs, and the
 * hooks below decide whether that agent tells the panel anything or whether
 * every state on the sidebar is a guess from the terminal bell.
 */
export function SessionsGroup() {
  return (
    <>
      <Section id="profiles" title={t('profile.title')}>
        <LaunchProfiles />
      </Section>
      <HooksSection />
      <TuneClaude />
    </>
  )
}

function HooksSection() {
  const [status, setStatus] = useState<HookStatus | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [showSnippet, setShowSnippet] = useState(false)

  useEffect(() => {
    let ignore = false
    api
      .hookStatus()
      .then((h) => {
        if (!ignore) setStatus(h)
      })
      .catch((e: unknown) => {
        if (!ignore) setError(e instanceof Error ? e.message : String(e))
      })
    return () => {
      ignore = true
    }
  }, [])

  // Sessions that were already running when this changed are the reason for
  // the notice below. See it for why.
  const [justChanged, setJustChanged] = useState(false)

  const act = async (fn: () => Promise<HookStatus>) => {
    setBusy(true)
    setError(null)
    try {
      setStatus(await fn())
      setJustChanged(true)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Section id="reporting" title={t('set.reporting')}>
      <p className="mb-3 text-vp-base leading-relaxed text-ink-2">
        {t('set.reportingWhy')}
      </p>

      {error && (
        <p className="mb-2 text-vp-base" style={{ color: 'var(--vp-state-waiting)' }}>
          {error}
        </p>
      )}

      {status && (
        <div data-testid="hooks-status">
          {/* Two agents, two rows, two buttons. They are configured by
              different mechanisms in different files and fail separately — the
              runbook has a section for exactly that — so a single "hooks are
              installed" line would describe a machine where one of them is
              wired as though both were. */}
          <AgentHooks
            label={t('set.claudeCode')}
            // "installed", not "reporting". The panel has read a file; it has
            // not heard from anything. Saying "reporting 4 events" the instant
            // the file is written is a claim about behaviour that nothing has
            // checked, and it is wrong for every session that was already
            // running — see the notice below.
            value={
              status.installed
                ? t('set.installedEvents', { n: status.events.length })
                : t('set.notInstalled')
            }
            file={status.settingsPath}
            installed={status.installed}
            busy={busy}
            testid="hooks"
            onInstall={() => void act(() => api.installHooks('claude'))}
            onRemove={() => void act(() => api.removeHooks('claude'))}
          />
          <AgentHooks
            label={t('set.codex')}
            value={status.codexInstalled ? t('set.installedNotify') : t('set.notInstalled')}
            file={status.codexPath}
            installed={status.codexInstalled}
            busy={busy}
            testid="codex-hooks"
            note={t('set.codexOneEvent')}
            onInstall={() => void act(() => api.installHooks('codex'))}
            onRemove={() => void act(() => api.removeHooks('codex'))}
          />
          {/* opencode is the one that needs no edit to anybody's config: it
              auto-discovers every file in its plugin directory, so installing
              writes a file that did not exist and removing deletes it. */}
          <AgentHooks
            label={t('set.opencode')}
            value={status.opencodeInstalled ? t('set.installedPlugin') : t('set.notInstalled')}
            file={status.opencodePath}
            installed={status.opencodeInstalled}
            busy={busy}
            testid="opencode-hooks"
            onInstall={() => void act(() => api.installHooks('opencode'))}
            onRemove={() => void act(() => api.removeHooks('opencode'))}
          />

          <div className="mt-3 flex flex-wrap items-center gap-2">
            <button
              type="button"
              onClick={() => setShowSnippet((v) => !v)}
              data-testid="hooks-preview"
              className="vp-press rounded-vp border border-hairline px-3 py-1.5 text-vp-base text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
            >
              {showSnippet ? t('set.hide') : t('set.showWrites')}
            </button>
          </div>

          {/* An agent reads its hooks when it starts, so changing them does
              nothing to the sessions already open — which, in a panel built
              for a dozen long-lived agents, is all of them. Without this the
              status says "installed", every state stays guessed, and there is
              nothing on screen connecting the two.

              Claude Code's own instruction to itself, in the binary: "Tell the
              user to open `/hooks` once (reloads config) or restart — you
              can't do this yourself; `/hooks` is a user UI menu and opening it
              ends this turn." So the agent will not even be able to explain
              it. */}
          {justChanged && (
            <p data-testid="hooks-restart-note" className="mt-3 text-vp-base leading-relaxed text-ink-2">
              Sessions that are already running will not pick this up. In each one, open{' '}
              <code className="font-mono">/hooks</code> once to reload, or restart the agent.
            </p>
          )}

          {/* Shown before agreeing, not after. It edits a file that is theirs
              and usually has other things in it — the existing contents are
              merged, every entry is tagged so removing them cannot take
              anyone else's with it, and a backup is written first. */}
          {showSnippet && (
            <div className="mt-3">
              <Snippet label={t('set.claudeCode')} text={status.snippet} />
              <Snippet label={t('set.codex')} text={status.codexSnippet} />
            </div>
          )}
        </div>
      )}
    </Section>
  )
}

/** One agent's row: what is installed, which file, and the button for it. */
function AgentHooks({
  label,
  value,
  file,
  installed,
  busy,
  testid,
  note,
  onInstall,
  onRemove,
}: {
  label: string
  value: string
  file: string
  installed: boolean
  busy: boolean
  testid: string
  note?: string
  onInstall: () => void
  onRemove: () => void
}) {
  return (
    <div className="mb-3" data-testid={`${testid}-block`}>
      <Row label={label} value={value} />
      <Row label={t('set.settingsFile')} value={file} />
      {note && <p className="mt-1 text-vp-sm leading-relaxed text-ink-2">{note}</p>}
      <div className="mt-2">
        {installed ? (
          <button
            type="button"
            disabled={busy}
            data-testid={`${testid}-remove`}
            onClick={onRemove}
            className="rounded-vp border border-hairline px-3 py-1.5 text-vp-base text-ink transition-colors duration-200 ease-vp hover:bg-surface-2 disabled:opacity-50"
          >
            {t('set.remove')}
          </button>
        ) : (
          <button
            type="button"
            disabled={busy}
            data-testid={`${testid}-install`}
            onClick={onInstall}
            className="rounded-vp px-3 py-1.5 text-vp-base font-medium disabled:opacity-50"
            style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
          >
            {busy ? t('set.working') : t('set.install')}
          </button>
        )}
      </div>
    </div>
  )
}

function Snippet({ label, text }: { label: string; text: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <div className="mb-3">
      <div className="mb-1 flex items-center gap-2">
        <span className="text-vp-sm text-ink-2">{label}</span>
        <button
          type="button"
          onClick={() => {
            copyTextInGesture(text, setCopied)
          }}
          className="vp-control"
        >
          {copied ? <Check size={11} /> : <Copy size={11} />}
          <span className="text-vp-xs">{copied ? 'Copied' : 'Copy'}</span>
        </button>
      </div>
      <pre className="max-h-56 overflow-auto rounded-vp border border-hairline bg-bg p-2 font-mono text-vp-sm leading-relaxed text-ink">
        {text}
      </pre>
    </div>
  )
}
