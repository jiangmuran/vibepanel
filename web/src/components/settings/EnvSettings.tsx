import { useEffect, useState } from 'react'
import { AlertTriangle } from 'lucide-react'

import { api } from '../../protocol/api'
import type { EnvSettings as EnvPayload } from '../../protocol/wire'
import { t } from '../../i18n'
import type { Key } from '../../i18n'
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
  VIBEPANEL_PUBLIC_PORT: '443',
  VIBEPANEL_PUBLIC_ORIGINS: 'https://panel.example.com',
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

/**
 * What each field is called, in words, beside the variable it writes.
 *
 * Both, not one. The variable name is what the file says and what somebody
 * greps for; the label is what the setting *is*, and a column of
 * `VIBEPANEL_ACME_DNS_PROVIDER` with nothing else is a dotfile that happens to
 * be rendered in a browser rather than a settings page.
 */
const LABEL: Record<string, Key> = {
  VIBEPANEL_ADDR: 'env.lblAddr',
  VIBEPANEL_DOMAIN: 'env.lblDomain',
  VIBEPANEL_PUBLIC_PORT: 'env.lblPublicPort',
  VIBEPANEL_PUBLIC_ORIGINS: 'env.lblPublicOrigins',
  VIBEPANEL_TLS_MODE: 'env.lblTlsMode',
  VIBEPANEL_CERT_FILE: 'env.lblCert',
  VIBEPANEL_KEY_FILE: 'env.lblKey',
  VIBEPANEL_ACME_DNS_PROVIDER: 'env.lblProvider',
  VIBEPANEL_ACME_EMAIL: 'env.lblEmail',
  VIBEPANEL_ACME_DIRECTORY: 'env.lblDirectory',
  VIBEPANEL_ALLOW_FROM: 'env.lblAllow',
  VIBEPANEL_TRUSTED_PROXIES: 'env.lblProxies',
}

/** The one line under a field that changes what somebody types into it. */
const NOTE: Record<string, Key> = {
  VIBEPANEL_DOMAIN: 'env.domainAlso',
  VIBEPANEL_PUBLIC_PORT: 'env.publicPortWhy',
  VIBEPANEL_PUBLIC_ORIGINS: 'env.publicOriginsWhy',
  VIBEPANEL_ALLOW_FROM: 'env.allowAll',
}

const TLS_MODES: [string, Key][] = [
  ['off', 'env.tlsOff'],
  ['files', 'env.tlsFiles'],
  ['acme', 'env.tlsAcme'],
]

/**
 * Three questions, in the order somebody setting this up asks them.
 *
 * The list used to be every editable variable in one column in the order the
 * server happened to send them, which put the ACME directory between the key
 * file and the allowlist. Grouping is not decoration here: the certificate
 * fields are mutually exclusive, and showing all five at once is what made
 * somebody fill in a cert path while the mode said acme.
 */
const GROUPS: { title: Key; fields: string[] }[] = [
  {
    title: 'env.grpReach',
    fields: [
      'VIBEPANEL_ADDR',
      'VIBEPANEL_DOMAIN',
      'VIBEPANEL_PUBLIC_PORT',
      'VIBEPANEL_PUBLIC_ORIGINS',
    ],
  },
  {
    title: 'env.grpTls',
    fields: [
      'VIBEPANEL_TLS_MODE',
      'VIBEPANEL_CERT_FILE',
      'VIBEPANEL_KEY_FILE',
      'VIBEPANEL_ACME_DNS_PROVIDER',
      'VIBEPANEL_ACME_EMAIL',
      'VIBEPANEL_ACME_DIRECTORY',
    ],
  },
  { title: 'env.grpAccess', fields: ['VIBEPANEL_ALLOW_FROM', 'VIBEPANEL_TRUSTED_PROXIES'] },
]

/** Which certificate fields the chosen mode actually reads. */
function visible(field: string, draft: Record<string, string>) {
  const mode = draft.VIBEPANEL_TLS_MODE || 'off'
  if (field === 'VIBEPANEL_CERT_FILE' || field === 'VIBEPANEL_KEY_FILE') return mode === 'files'
  if (field.startsWith('VIBEPANEL_ACME_')) return mode === 'acme'
  return true
}

/** One field: the label, the variable it writes, and whatever edits it. */
function Row({
  name,
  label,
  note,
  children,
}: {
  name: string
  label?: Key
  note?: Key
  children: React.ReactNode
}) {
  return (
    <label className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
      <span className="w-56 shrink-0">
        {label && <span className="block text-vp-base text-ink">{t(label)}</span>}
        <span className="block font-mono text-vp-xs text-ink-3">{name}</span>
      </span>
      <span className="min-w-0 flex-1">
        {children}
        {note && <span className="mt-1 block text-vp-xs text-ink-3">{t(note)}</span>}
      </span>
    </label>
  )
}

export function EnvSettings() {
  const [env, setEnv] = useState<EnvPayload | null>(null)
  const [draft, setDraft] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  // Held apart from `draft` on purpose: a write-only value is never loaded, so
  // it has no "unchanged" state to compare against and must not count towards
  // `dirty` or be resent on every save.
  const [secret, setSecret] = useState('')
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
    const values = { ...draft }
    if (secret !== '') values.CLOUDFLARE_API_TOKEN = secret
    api
      .saveEnvSettings(values)
      .then((e) => {
        setEnv(e)
        setDraft({ ...e.values })
        setSecret('')
        setSaved(true)
      })
      .catch((e: unknown) => setErr(String(e)))
      .finally(() => setBusy(false))
  }

  // An empty string is what removes an assignment, so this is a real clear and
  // not a no-op: the field being blank means "leave it alone", which is why
  // removing a token needs a button of its own.
  const clearSecret = () => {
    setBusy(true)
    api
      .saveEnvSettings({ ...draft, CLOUDFLARE_API_TOKEN: '' })
      .then((e) => {
        setEnv(e)
        setDraft({ ...e.values })
        setSecret('')
        setSaved(true)
      })
      .catch((e: unknown) => setErr(String(e)))
      .finally(() => setBusy(false))
  }

  const dirty =
    secret !== '' ||
    (env !== null && env.keys.some((k) => (draft[k] ?? '') !== (env.values[k] ?? '')))
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

          {GROUPS.map((g) => {
            const fields = g.fields.filter((f) => visible(f, draft))
            if (fields.length === 0) return null
            return (
              <div key={g.title} className="mb-4">
                <h4 className="mb-2 text-vp-sm font-medium text-ink">{t(g.title)}</h4>
                <div className="flex flex-col gap-2">
                  {fields.map((k) =>
                    k === 'VIBEPANEL_TLS_MODE' ? (
                      <Row key={k} name={k} label={LABEL[k]}>
                        {/* A segmented control and not a text box. The three
                            values are the whole domain of this setting, and
                            typing `acme ` with a trailing space into the box
                            was a panel that started with TLS off. */}
                        <div className="vp-segmented" data-testid="env-tls-mode">
                          {TLS_MODES.map(([value, label]) => (
                            <button
                              key={value}
                              type="button"
                              data-testid={`env-tls-${value}`}
                              aria-pressed={(draft[k] || 'off') === value}
                              data-active={(draft[k] || 'off') === value}
                              onClick={() => setDraft((d) => ({ ...d, [k]: value }))}
                              className="vp-tab px-3 text-vp-base whitespace-nowrap"
                            >
                              {t(label)}
                            </button>
                          ))}
                        </div>
                      </Row>
                    ) : (
                      <Row key={k} name={k} label={LABEL[k]} note={NOTE[k]}>
                        <input
                          value={draft[k] ?? ''}
                          onChange={(e) => setDraft((d) => ({ ...d, [k]: e.target.value }))}
                          placeholder={SHAPE[k] ?? ''}
                          data-testid={`env-${k}`}
                          spellCheck={false}
                          autoCapitalize="off"
                          autoCorrect="off"
                          className="w-full rounded-vp border border-hairline bg-surface-2 px-2 py-1 font-mono text-vp-sm text-ink outline-none focus:border-accent"
                        />
                      </Row>
                    ),
                  )}
                  {/* The ACME credential, write-only. It lives here rather than
                      nowhere because the one TLS mode this panel recommends
                      could not otherwise be configured from the panel: somebody
                      who rotated the token had to go and find the file. The
                      response says whether one is set and never what it is. */}
                  {g.title === 'env.grpTls' && (draft.VIBEPANEL_TLS_MODE || 'off') === 'acme' && (
                    <Row name="CLOUDFLARE_API_TOKEN" label="env.lblToken">
                      <div className="flex flex-wrap items-center gap-2">
                        <input
                          type="password"
                          value={secret}
                          onChange={(e) => setSecret(e.target.value)}
                          placeholder={
                            env.secretSet?.CLOUDFLARE_API_TOKEN
                              ? t('env.tokenSet')
                              : t('env.tokenUnset')
                          }
                          data-testid="env-secret"
                          spellCheck={false}
                          autoComplete="off"
                          className="min-w-0 flex-1 rounded-vp border border-hairline bg-surface-2 px-2 py-1 font-mono text-vp-sm text-ink outline-none focus:border-accent"
                        />
                        {env.secretSet?.CLOUDFLARE_API_TOKEN && (
                          <button
                            type="button"
                            data-testid="env-secret-clear"
                            onClick={clearSecret}
                            className="vp-press rounded-vp border border-hairline px-2 py-1 text-vp-sm text-ink-2 hover:bg-surface-2 hover:text-ink"
                          >
                            {t('env.tokenClear')}
                          </button>
                        )}
                      </div>
                    </Row>
                  )}
                </div>
              </div>
            )
          })}

          <div className="mb-3 flex flex-col gap-2">
            <Row name="VIBEPANEL_TMUX_SOCKET" label="env.lblSocket">
              {/* Shown and not editable. The reason is on screen because a
                  greyed-out field with no explanation reads as an oversight. */}
              <span className="font-mono text-vp-sm text-ink-3">
                {env.socket} — {t('env.socketFixed')}
              </span>
            </Row>
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
