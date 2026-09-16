import { useState } from 'react'

import { api } from '../../protocol/api'
import type { ChatSettings, ChatToolProfile } from '../../protocol/wire'
import { t } from '../../i18n'
import { showToast } from '../toasts'
import { Card, INPUT, Section } from './Chat'

const FIELDS = ['approve', 'deny', 'interrupt', 'submit'] as const

function errText(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

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
  const [tools, setTools] = useState<Record<string, ChatToolProfile>>(data.tools)
  const [dirty, setDirty] = useState(false)
  const [busy, setBusy] = useState(false)

  // See Assistant.tsx: the server's copy replaces the form only while
  // nothing is being edited, during render rather than in an effect.
  const [seen, setSeen] = useState(data.tools)
  if (seen !== data.tools) {
    setSeen(data.tools)
    if (!dirty) setTools(data.tools)
  }

  const set = (tool: string, field: (typeof FIELDS)[number], value: string) => {
    setTools((all) => ({ ...all, [tool]: { ...all[tool], [field]: split(value) } }))
    setDirty(true)
  }

  const save = async () => {
    setBusy(true)
    try {
      setTools(await api.saveChatKeys(tools))
      setDirty(false)
      showToast({ kind: 'success', key: 'chat.saved' })
      onChange()
    } catch (e) {
      showToast({ kind: 'error', key: 'chat.saveFailed', detail: errText(e) })
    } finally {
      setBusy(false)
    }
  }

  const names = Object.keys(tools).sort()
  return (
    <Section id="keys" title={t('chat.keys')} lead={t('chat.keysLead')}>
      <Card testid="chat-keys">
        <div className="overflow-x-auto">
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
        <button type="button" className="vp-control vp-press mt-2" disabled={busy || !dirty} onClick={() => void save()}>
          {t('chat.save')}
        </button>
      </Card>
    </Section>
  )
}
