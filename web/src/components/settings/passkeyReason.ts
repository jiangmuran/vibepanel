import type { Key } from '../../i18n'

/**
 * Which sentence the server's `passkeyReason` code means.
 *
 * Its own module because both the login page and the settings page show it,
 * and because a table of keys exported from a component file is what the
 * react-refresh rule complains about.
 *
 * A table rather than a switch: `return 'pk.no-domain'` reads to the
 * untranslated-string scanner exactly like a literal about to be rendered, and
 * it is right to be suspicious of one.
 *
 * There is no entry for TLS. Whether the connection is secure is the browser's
 * to answer -- behind a proxy that terminates TLS, the panel's own mode says
 * nothing about it -- so that case is `pk.insecure`, shown by whichever page
 * finds `window.PublicKeyCredential` missing.
 */
export const BLOCKER: Record<string, Key> = {
  'no-domain': 'pk.no-domain',
  'ip-domain': 'pk.ip-domain',
}

/** The key for a code, falling back to the commonest cause. */
export function blockerKey(code: string | undefined): Key {
  return BLOCKER[code ?? ''] ?? 'pk.no-domain'
}
