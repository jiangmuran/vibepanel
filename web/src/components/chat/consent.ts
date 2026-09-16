import { api } from '../../protocol/api'
import { t } from '../../i18n'
import { askConfirm } from '../ask'

/**
 * The one question asked before chat talks to anything outside this machine.
 *
 * Until a channel is switched on the chat code connects to nothing, and the
 * owner is told that is what they are giving up, once, at the moment they
 * give it up: switching a channel on, signing in to 微信, turning the
 * advanced mode on. Not as a banner on the page, which reads as a warning
 * about a page that has not done anything yet.
 *
 * The server holds the answer and refuses those same writes without it, so
 * this is the page asking, not the page deciding. `accepted` only spares a
 * second question in the seconds before the next poll brings the recorded
 * time back.
 */
let accepted = false

export async function ensureChatConsent(consentAt: number): Promise<boolean> {
  if (consentAt > 0 || accepted) return true
  const ok = await askConfirm({
    title: t('chat.consentTitle'),
    body: t('chat.consentBody'),
    confirm: t('chat.consentAccept'),
    cancel: t('chat.cancel'),
  })
  if (!ok) return false
  await api.chatConsent()
  accepted = true
  return true
}
