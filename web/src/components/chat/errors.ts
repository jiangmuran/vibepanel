import { t } from '../../i18n'
import { errText } from './form'

/**
 * A server refusal in the page's language.
 *
 * The chat routes answer in English, for logs and for scripts, and every
 * toast on this page used to show that English under a Chinese headline:
 * "chat: nobody is paired on telegram", a key table error naming a column the
 * page calls 允许. The ones a person meets while setting things up are
 * translated here; anything else is shown as it came, which beats nothing.
 */
export function chatError(e: unknown): string {
  const raw = errText(e)
  let m: RegExpMatchArray | null
  if ((m = raw.match(/nobody is paired on (\w+)/))) return t('chat.errNobodyPaired', { channel: m[1] })
  if (/has that code|no such code|not found/i.test(raw) && /code/i.test(raw)) return t('chat.errNoCode')
  if (/quiet hours/.test(raw)) return t('chat.errQuiet')
  if ((m = raw.match(/tmux has no key called "([^"]*)"/))) return t('chat.errNoKey', { key: m[1] })
  if (/allow and deny need a key/.test(raw)) return t('chat.errKeysNeeded')
  if (/submit needs a key/.test(raw)) return t('chat.errSubmitNeeded')
  if (/code they were sent|with their code first/.test(raw)) return t('chat.errEnterCode')
  if ((m = raw.match(/^\w+: (?:a |an )?(.+) is required$/))) return t('chat.errRequired', { field: m[1] })
  return raw
}
