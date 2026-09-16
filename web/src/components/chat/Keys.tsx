

import { api } from '../../protocol/api'
import type { ChatSettings, ChatToolProfile } from '../../protocol/wire'
import { t } from '../../i18n'
import { askConfirm } from '../ask'
import { showToast } from '../toasts'
import { Card, Section } from './Chat'
import { chatError } from './errors'
import { INPUT, Primary, Secondary, useServerCopy } from './form'
import { useState } from 'react'

const FIELDS = ['approve', 'deny', 'interrupt', 'submit'] as const

function join(keys: string[] | null): string {
  return (keys ?? []).join(' ')
}

function split(s: string): string[] {
  return s.split(/\s+/).filter(Boolean)
}

/**
 * Which keys "allow" is, per agent, in tmux's key names.
 *
 * Shown because the defaults are guesses for three of the six: opencode,
 * Kimi Code and zcode draw their prompts like Claude Code's, and Enter and
 * Escape are what that shape answers to, but nobody measured them. A person
 * who finds their agent needs "y" fixes it here rather than in a release.
 */
export function Keys({ data, onChange }: { data: ChatSettings; onChange: () => void }) {
  const [dirty, setDirty] = useState(false)
  const [tools, setTools] = useServerCopy<Record<string, ChatToolProfile>>(data.tools, dirty)
  const [busy, setBusy] = useState(false)

  const set = (tool: string, field: (typeof FIELDS)[number], value: string) => {
    setTools((all) => ({ ...all, [tool]: { ...all[tool], [field]: split(value) } }))
    setDirty(true)
  }

  // An empty table is what the server fills from its defaults, so reset is a
  // save of nothing rather than a second copy of the defaults in the page.
  const save = async (table = tools) => {
    setBusy(true)
    try {
      setTools(await api.saveChatKeys(table))
      setDirty(false)
      showToast({ kind: 'success', key: 'chat.saved' })
      onChange()
    } catch (e) {
      showToast({ kind: 'error', key: 'chat.saveFailed', detail: chatError(e) })
    } finally {
      setBusy(false)
    }
  }

  const names = Object.keys(tools).sort()
  const labels: Record<(typeof FIELDS)[number], string> = {
    approve: t('chat.keyApprove'),
    deny: t('chat.keyDeny'),
    interrupt: t('chat.keyInterrupt'),
    submit: t('chat.keySubmit'),
  }
  return (
    <Section id="keys" title={t('chat.keys')} lead={t('chat.keysLead')}>
      <Card testid="chat-keys">
        {/* Below the card's own 2xl width the five columns do not fit and a
            clipped table hides its last column without saying so; one block
            per tool, with the four fields labelled, is what a phone gets. */}
        <div className="grid grid-cols-1 gap-3 @2xl:hidden">
          {names.map((name) => (
            <div key={name} className="rounded-vp border border-hairline p-2">
              <div className="mb-1 font-mono text-vp-sm text-ink">{name}</div>
              <div className="grid grid-cols-2 gap-2">
                {FIELDS.map((f) => (
                  <label key={f} className="min-w-0">
                    <span className="mb-0.5 block text-vp-xs text-ink-3">{labels[f]}</span>
                    <input className={`${INPUT} font-mono`} value={join(tools[name][f])} onChange={(e) => set(name, f, e.target.value)} />
                  </label>
                ))}
              </div>
            </div>
          ))}
        </div>
        <div className="hidden @2xl:block">
          <table className="w-full text-vp-sm">
            <thead>
              <tr className="text-left text-vp-xs text-ink-3">
                <th className="py-1 pr-2 font-normal">{t('chat.tool')}</th>
                <th className="py-1 pr-2 font-normal">{t('chat.keyApprove')}</th>
                <th className="py-1 pr-2 font-normal">{t('chat.keyDeny')}</th>
                <th className="py-1 pr-2 font-normal">{t('chat.keyInterrupt')}</th>
                <th className="py-1 font-normal">{t('chat.keySubmit')}</th>
              </tr>
            </thead>
            <tbody>
              {names.map((name) => (
                <tr key={name} className="border-t border-hairline">
                  <td className="py-1 pr-2 font-mono text-ink">{name}</td>
                  {FIELDS.map((f) => (
                    <td key={f} className="py-1 pr-2">
                      <input
                        className={`${INPUT} min-w-20 font-mono`}
                        value={join(tools[name][f])}
                        onChange={(e) => set(name, f, e.target.value)}
                        data-testid={`chat-key-${name}-${f}`}
                      />
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <p className="mt-2 text-vp-xs text-ink-3">{t('chat.keysHint')}</p>
        <div className="mt-2 flex flex-wrap items-center gap-2">
          <Primary disabled={busy || !dirty} onClick={() => void save()}>
            {t('chat.save')}
          </Primary>
          <Secondary
            disabled={busy}
            data-testid="chat-keys-reset"
            onClick={() =>
              void askConfirm({ title: t('chat.keysResetTitle'), confirm: t('chat.keysReset'), cancel: t('chat.cancel') }).then(
                (ok) => ok && void save({}),
              )
            }
          >
            {t('chat.keysReset')}
          </Secondary>
        </div>
      </Card>
    </Section>
  )
}
