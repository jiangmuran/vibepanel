import { useEffect, useState } from 'react'
import { AlertTriangle } from 'lucide-react'

import { api } from '../../protocol/api'
import type { EnvSettings as EnvPayload } from '../../protocol/wire'
import { t } from '../../i18n'
import { Section } from './parts'

/**
 * The service's environment file, as fields.
 *
 * These were only ever editable by finding ~/.config/vibepanel.env over SSH,
 * which is a strange thing to require of somebody who is already signed in to
 * the panel as the account that owns it.
 *
 * Two things this deliberately does not do. It does not restart -- applying
 * these costs every connection, and somebody changing three of them wants to
 * decide once rather than three times, so the button is its own block below.
 * And it does not offer the tmux socket or the ACME credential: red line 1 for
 * the first, and a page that shows the second puts it in every screenshot.
 */
/**
 * The shape of each value, for a field that is empty.
 *
 * Not a translated label: the row is already named by the variable, and a
 * second name for it is a second thing that can disagree with the file. What
 * is missing from a bare `VIBEPANEL_TLS_MODE` is not what it means, it is what
 * you are allowed to put in it -- so these are formats, and they are the same
 * examples deploy/vibepanel.env carries in its comments.
 */
const SHAPE: Record<string, string> = {
  VIBEPANEL_ADDR: ':18443',
  VIBEPANEL_DOMAIN: 'panel.example.com',
  // No spaces, and not only to keep i18n.untranslated.test.ts quiet: the
  // spaces were never part of the value, and a placeholder that looks like a
  // sentence invites somebody to type one.
  VIBEPANEL_TLS_MODE: 'off|files|acme',
  VIBEPANEL_CERT_FILE: '/etc/ssl/panel.pem',
  VIBEPANEL_KEY_FILE: '/etc/ssl/panel.key',
  VIBEPANEL_ACME_DNS_PROVIDER: 'cloudflare',
  VIBEPANEL_ACME_EMAIL: 'you@example.com',
  VIBEPANEL_ACME_DIRECTORY: 'https://acme-staging-v02.api.letsencrypt.org/directory',
  VIBEPANEL_ALLOW_FROM: '192.168.8.0/24,10.0.0.0/8',
  VIBEPANEL_TRUSTED_PROXIES: '127.0.0.1/32',
}

export function EnvSettings() {
  const [env, setEnv] = useState<EnvPayload | null>(null)
  const [draft, setDraft] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [saved, setSaved] = useState(false)

  const load = () => {
    api
      .envSettings()
      .then((e) => {
        setEnv(e)
        setDraft({ ...e.values })
        setErr('')
      })
      .catch((e: unknown) => setErr(String(e)))
  }
  useEffect(load, [])

  const save = () => {
    setBusy(true)
    setSaved(false)
    api
      .saveEnvSettings(draft)
      .then((e) => {
        setEnv(e)
        setDraft({ ...e.values })
        setSaved(true)
      })
      .catch((e: unknown) => setErr(String(e)))
      .finally(() => setBusy(false))
  }

  const dirty = env !== null && env.keys.some((k) => (draft[k] ?? '') !== (env.values[k] ?? ''))
  // What is on disk versus what this process is running with. A file edited an
  // hour ago and never applied looks exactly like one that is in force, and
  // that is the mistake this block is most likely to cause.
  //
  // A key the file does not set is not a pending change. It used to count as
  // one, and the file ships with most of its variables commented out -- so
  // `VIBEPANEL_TLS_MODE` was "" on disk and "off" in the process, and every
  // fresh panel opened this block already telling its owner to restart. A
  // false alarm on first sight is worse than no alarm: it is the reason the
  // real one gets read past.
  const pending =
    env !== null &&
    env.keys.some((k) => {
      const onDisk = env.values[k] ?? ''
      return onDisk !== '' && env.live[k] !== undefined && onDisk !== env.live[k]
    })

  return (
    <Section id="env" title={t('env.title')}>
      {!env ? (
        <p className="text-vp-sm text-ink-3">{err || t('tune.loading')}</p>
      ) : (
        <>
          <p className="mb-1 text-vp-sm text-ink-2">{t('env.what')}</p>
          <p className="mb-3 font-mono text-vp-xs break-all text-ink-3">{env.path}</p>

          <div className="mb-3 flex flex-col gap-2">
            {env.keys.map((k) => (
              <label key={k} className="flex flex-wrap items-baseline gap-2">
                <span className="w-56 shrink-0 font-mono text-vp-xs text-ink-2">{k}</span>
                <input
                  value={draft[k] ?? ''}
                  onChange={(e) => setDraft((d) => ({ ...d, [k]: e.target.value }))}
                  placeholder={SHAPE[k] ?? ''}
                  data-testid={`env-${k}`}
                  spellCheck={false}
                  autoCapitalize="off"
                  autoCorrect="off"
                  className="min-w-0 flex-1 rounded-vp border border-hairline bg-surface-2 px-2 py-1 font-mono text-vp-sm text-ink outline-none focus:border-accent"
                />
              </label>
            ))}
            <label className="flex flex-wrap items-baseline gap-2">
              <span className="w-56 shrink-0 font-mono text-vp-xs text-ink-2">
                VIBEPANEL_TMUX_SOCKET
              </span>
              {/* Shown and not editable. The reason is on screen because a
                  greyed-out field with no explanation reads as an oversight. */}
              <span className="min-w-0 flex-1 font-mono text-vp-sm text-ink-3">
                {env.socket} — {t('env.socketFixed')}
              </span>
            </label>
          </div>

          <div className="flex flex-wrap items-center gap-3">
            <button
              type="button"
              disabled={busy || !dirty}
              onClick={save}
              data-testid="env-save"
              className="vp-press rounded-vp border border-hairline px-3 py-1.5 text-vp-base text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink disabled:opacity-50"
            >
              {t('env.save')}
            </button>
            <span className="text-vp-sm text-ink-3">{t('env.backup')}</span>
          </div>

          {(pending || saved) && (
            <p
              className="mt-3 flex items-start gap-2 text-vp-sm text-state-waiting"
              data-testid="env-pending"
            >
              {/* A shape as well as a colour: this is the one line somebody has
                  to act on, and it is easy to read past. */}
              <AlertTriangle size={14} className="mt-0.5 shrink-0" />
              {t('env.pending')}
            </p>
          )}
          {err && <p className="mt-2 text-vp-sm text-state-crashed">{err}</p>}
        </>
      )}
    </Section>
  )
}
