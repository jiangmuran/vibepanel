import { useEffect, useState } from 'react'
import { Check, Copy } from 'lucide-react'

import { api } from '../../protocol/api'
import type { HookAgent, HookStatus } from '../../protocol/wire'
import {
  HOOK_AGENTS,
  hookAgentInstalled,
  hookAgentName,
  visibleHookAgents,
} from '../hookAgents'
import { t } from '../../i18n'
import { copyTextInGesture } from '../../clipboard'
import { LaunchProfiles } from '../LaunchProfiles'
import { ClaudeAccounts } from '../ClaudeAccounts'
import { Row, Section } from './parts'
import { TuneClaude } from './TuneClaude'
import { PasteSettings } from './PasteSettings'

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
      <Section id="accounts" title={t('acct.title')}>
        <ClaudeAccounts />
      </Section>
      <Section id="profiles" title={t('profile.title')}>
        <LaunchProfiles />
      </Section>
      <HooksSection />
      <TuneClaude />
      <PasteSettings />
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

  /**
   * Tick an agent on or off.
   *
   * Optimistic, and every write through a functional update. Both halves are
   * bugs that were here: the tick did nothing visible until the round trip
   * finished, so a second tick was computed from the list the first one had
   * not yet changed and the first agent was silently dropped; and spreading
   * the `status` this render captured put back a snapshot taken before the
   * request -- pressing Install while a tick was in flight ended with the row
   * saying "not installed" over a file the panel had just written.
   */
  const setAgents = (next: HookAgent[]) => {
    setStatus((cur) => (cur ? { ...cur, agentsShown: next } : cur))
    api
      .setHookAgents(next)
      .then((r) => setStatus((cur) => (cur ? { ...cur, agentsShown: r.agentsShown } : cur)))
      .catch((e: unknown) => {
        setError(e instanceof Error ? e.message : String(e))
        // The optimistic list is now a claim nothing backs. Ask rather than
        // guess which way to put it back.
        api.hookStatus().then(setStatus).catch(() => {})
      })
  }

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
          {/* A row each, because the agents are configured by different
              mechanisms in different files and fail separately — the runbook
              has a section for exactly that — so a single "hooks are
              installed" line would describe a machine where one of them is
              wired as though all were.

              Which rows: the ones ticked below, plus any agent whose hooks are
              actually installed. Five rows of install buttons on a machine
              running one agent is a page people stop reading, and hiding a row
              for hooks this panel wrote would hide the only button that takes
              them out again. */}
          {visibleHookAgents(status).map((id) => {
            const row = agentRow(id, status)
            return (
              <AgentHooks
                key={id}
                label={hookAgentName(id)}
                value={row.value}
                file={row.file}
                installed={row.installed}
                note={row.note}
                busy={busy}
                testid={row.testid}
                onInstall={() => void act(() => api.installHooks(id))}
                onRemove={() => void act(() => api.removeHooks(id))}
              />
            )
          })}

          <AgentsShown
            shown={status.agentsShown}
            status={status}
            onChange={setAgents}
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
              {visibleHookAgents(status).map((id) => {
                const text = agentRow(id, status).snippet
                // opencode has none: the panel writes it a plugin file of its
                // own rather than a block inside a file that is theirs, so
                // there is nothing to read before agreeing to it.
                return text ? <Snippet key={id} label={hookAgentName(id)} text={text} /> : null
              })}
            </div>
          )}
        </div>
      )}
    </Section>
  )
}

/**
 * What one agent's row says: the state, the file it is in, and the text of the
 * snippet if that agent has one.
 *
 * Each of these differs in a way that matters to somebody reading the page --
 * Codex has a trust step, opencode has a whole file rather than a block -- and
 * they used to differ by being five hand-written blocks of JSX. This is the
 * same information with one shape, so a row cannot quietly lose its note.
 */
function agentRow(
  id: HookAgent,
  status: HookStatus,
): { value: string; file: string; installed: boolean; testid: string; note?: string; snippet?: string } {
  switch (id) {
    case 'claude':
      return {
        // "installed", not "reporting". The panel has read a file; it has not
        // heard from anything. Saying "reporting 4 events" the instant the file
        // is written is a claim about behaviour that nothing has checked, and
        // it is wrong for every session that was already running — see the
        // notice below.
        value: status.installed
          ? t('set.installedEvents', { n: status.events.length })
          : t('set.notInstalled'),
        file: status.settingsPath,
        installed: status.installed,
        testid: 'hooks',
        snippet: status.snippet,
      }
    case 'codex':
      return {
        value: status.codexInstalled
          ? t('set.installedHooks', { n: status.codexEvents.length })
          : t('set.notInstalled'),
        file: status.codexPath,
        installed: status.codexInstalled,
        testid: 'codex-hooks',
        // Codex needs one more step than the others: it runs a hook from the
        // user's own hooks.json only after `/hooks` has trusted it. The note is
        // that step until Codex has recorded a decision, and then the count
        // that proves it -- reports arriving is the only thing a file read
        // cannot fake.
        note: codexNote(status),
        snippet: status.codexSnippet,
      }
    // Kimi Code and zcode need no extra step: their hooks run from the
    // user-level file with no trust review.
    case 'kimi':
      return {
        value: status.kimiInstalled
          ? t('set.installedHooks', { n: status.kimiEvents.length })
          : t('set.notInstalled'),
        file: status.kimiPath,
        installed: status.kimiInstalled,
        testid: 'kimi-hooks',
        snippet: status.kimiSnippet,
      }
    case 'zcode':
      return {
        value: status.zcodeInstalled
          ? t('set.installedHooks', { n: status.zcodeEvents.length })
          : t('set.notInstalled'),
        file: status.zcodePath,
        installed: status.zcodeInstalled,
        testid: 'zcode-hooks',
        snippet: status.zcodeSnippet,
      }
    case 'opencode':
      // The one that needs no edit to anybody's config: it auto-discovers
      // every file in its plugin directory, so installing writes a file that
      // did not exist and removing deletes it.
      return {
        value: status.opencodeInstalled ? t('set.installedPlugin') : t('set.notInstalled'),
        file: status.opencodePath,
        installed: status.opencodeInstalled,
        testid: 'opencode-hooks',
      }
  }
}

/**
 * Which agents this panel offers.
 *
 * Reported as 「设置里面可以隐藏/配置」: a panel running one agent had five
 * rows of install buttons for other people's tools. Ticks rather than
 * detection, because "is Kimi Code installed on this machine" is a question
 * about a binary that may not be on the panel's PATH at all, and a page that
 * guesses wrong hides the button somebody came here to press.
 *
 * An agent whose hooks are installed keeps its row whatever the tick says, so
 * this cannot be used to lose track of a file the panel has written.
 */
function AgentsShown({
  shown,
  status,
  onChange,
}: {
  shown: HookAgent[]
  status: HookStatus
  onChange: (next: HookAgent[]) => void
}) {
  return (
    <fieldset className="mt-4 border-t border-hairline pt-3" data-testid="hook-agents">
      <legend className="mb-1 text-vp-sm text-ink-2">{t('set.agentsShown')}</legend>
      <div className="flex flex-wrap gap-x-4 gap-y-1">
        {HOOK_AGENTS.map((a) => {
          const on = shown.includes(a.id)
          return (
            <label key={a.id} className="flex items-center gap-1.5 text-vp-base text-ink">
              <input
                type="checkbox"
                checked={on}
                data-testid={`hook-agent-${a.id}`}
                onChange={() => onChange(on ? shown.filter((x) => x !== a.id) : [...shown, a.id])}
              />
              <span>{hookAgentName(a.id)}</span>
              {/* An installed agent is on screen whether or not it is ticked,
                  and saying so is cheaper than leaving somebody to wonder why
                  unticking it changed nothing. */}
              {!on && hookAgentInstalled(status, a.id) && (
                <span className="text-vp-sm text-ink-3">{t('set.agentInstalledAnyway')}</span>
              )}
            </label>
          )
        })}
      </div>
    </fieldset>
  )
}

/** What the Codex row says under its status, in order of what to do next. */
function codexNote(status: HookStatus): string | undefined {
  if (!status.codexInstalled) {
    return status.codexLegacyNotify ? t('set.codexLegacyNotify') : undefined
  }
  const lines: string[] = []
  lines.push(status.codexTrust === 'trusted' ? t('set.codexTrusted') : t('set.codexTrust'))
  if (status.codexSessions > 0) {
    lines.push(t('set.codexReports', { n: status.codexSessions, m: status.codexReporting }))
  }
  return lines.join(' · ')
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
