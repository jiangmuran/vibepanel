import type { ElevateRefusal } from '../protocol/wire'
import type { Key } from '../i18n'

/** How the elevated part of the update section is drawn. */
export interface ElevateState {
  /** Ask for a password. False means a button that sends none. */
  field: boolean
  /** Whose password the field asks for. */
  who: string
  /** Nothing typed can help; offer neither, and leave the shell command. */
  blocked: boolean
}

/** Before anything has been tried: whatever the check said. */
export function initialElevate(noPassword: boolean | undefined, elevateAs: string | undefined): ElevateState {
  return { field: !noPassword, who: elevateAs ?? '', blocked: false }
}

/**
 * What a refusal from sudo changes, and what the page says about it.
 *
 * A wrong password keeps the field and names whose password sudo wanted --
 * root, under rootpw or targetpw, which the field could not have known
 * beforehand. A password that turned out to be needed brings the field up in
 * place of the button. Not being allowed, or sudo insisting on a terminal, is
 * not fixed by typing anything, and nor is a panel running with no_new_privs,
 * so neither is offered and the shell command below is what is left.
 */
export function afterRefusal(
  state: ElevateState,
  reason: ElevateRefusal,
  askedFor: string,
  elevateAs: string | undefined,
): { state: ElevateState; key: Key; params: Record<string, string> } {
  const user = elevateAs ?? ''
  switch (reason) {
    case 'wrongPassword': {
      const who = askedFor || state.who || user
      return { state: { ...state, field: true, who }, key: 'upd.wrongPassword', params: { who } }
    }
    case 'needPassword':
      return { state: { ...state, field: true }, key: 'upd.needPassword', params: {} }
    case 'notAllowed':
      return { state: { ...state, blocked: true }, key: 'upd.notAllowed', params: { user } }
    case 'needsTty':
      return { state: { ...state, blocked: true }, key: 'upd.needsTty', params: {} }
    case 'cannotElevate':
      return { state: { ...state, blocked: true }, key: 'upd.cannotElevate', params: {} }
    default:
      return { state, key: 'upd.failed', params: { why: '' } }
  }
}
