import type { Key } from '../../i18n'

/**
 * What the settings dialog is divided into, and what lives in each part.
 *
 * The dialog was twelve sections stacked in one scroll — 「太长太恶心了」 — and
 * the fix is not a shorter page but a rail: five names on the left, one group
 * on screen. That only helps if the names predict their contents, so the test
 * a grouping has to pass is that somebody who wants to change one thing can
 * guess which name it is under *without reading the other four*.
 *
 * The five, and what each answers:
 *
 *   - **Sessions** — what a session is started with, and how the panel learns
 *     what it is doing. Launch profiles and state reporting are two halves of
 *     one sentence, and this is where the "states are being guessed" notice
 *     lands.
 *   - **Notifications** — how you are told an agent wants you. Both mechanisms
 *     together: the permission this browser has, and the webhook that reaches
 *     a phone which is not looking at the panel. They were four sections
 *     apart, and "turn on notifications" found only one of them.
 *   - **Sharing** — the read-only links, and the board a wall shows. One
 *     section, and it keeps its own group: it is the one surface that shows
 *     anything to somebody who is not signed in (red line 8).
 *   - **Account** — the ways in, and who has come in: password, passkeys, API
 *     tokens, activity log. A token is a credential you make and revoke one at
 *     a time, which is the same object as a passkey and belongs beside it
 *     rather than beside the webhooks it used to sit next to.
 *   - **This panel** — facts about the installation. Version, what an update
 *     would do, uptime, tmux socket, listening address, certificate. Named for
 *     the machine rather than 「面板」, which is what the *side* panel is called
 *     four inches to the left of this dialog.
 *
 * The language switch is in none of them; it is in the dialog's header, always
 * on screen. Somebody hunting for it cannot read the rail — that is why they
 * are hunting for it.
 */
export const SETTINGS_GROUPS = ['sessions', 'notify', 'sharing', 'account', 'panel'] as const

export type SettingsGroup = (typeof SETTINGS_GROUPS)[number]

export const GROUP_TITLE: Record<SettingsGroup, Key> = {
  sessions: 'grp.sessions',
  notify: 'grp.notify',
  sharing: 'grp.sharing',
  account: 'grp.account',
  panel: 'grp.panel',
}

/**
 * Every block the dialog can show, by the name a caller asks for it by.
 *
 * Nothing outside this file names a *group*. The sidebar's "states are being
 * guessed" notice asks for `reporting`, and which rail item that turns out to
 * be is this file's problem. The alternative — each call site naming a group —
 * is a set of links that quietly point at the wrong place the first time a
 * section moves, and moving sections is the whole content of this change.
 */
export const SETTINGS_SECTIONS = [
  'profiles',
  'reporting',
  'tune',
  'paste',
  'browser',
  'webhooks',
  'shares',
  'password',
  'passkeys',
  'tokens',
  'activity',
  'update',
  'status',
  'env',
  'restart',
  'tour',
] as const

export type SettingsSection = (typeof SETTINGS_SECTIONS)[number]

/**
 * Which rail item each section is under.
 *
 * `settings.wiring.test.ts` checks this against where the sections are
 * actually rendered: a map that says one thing while the JSX says another is a
 * deep link that opens the wrong group, and nothing on screen says so.
 */
export const SECTION_GROUP: Record<SettingsSection, SettingsGroup> = {
  profiles: 'sessions',
  reporting: 'sessions',
  // Beside reporting, because both edit the same file in somebody else's home
  // directory and the question "what is the panel allowed to write into my
  // agent's configuration" is one question, not two.
  tune: 'sessions',
  // Where a screenshot pasted into a terminal goes: about what a session
  // does, and next to the other things that write outside the panel.
  paste: 'sessions',
  browser: 'notify',
  webhooks: 'notify',
  shares: 'sharing',
  password: 'account',
  passkeys: 'account',
  tokens: 'account',
  activity: 'account',
  update: 'panel',
  status: 'panel',
  // Above the restart button on purpose: these take effect on the next start,
  // and the two controls are one sentence.
  env: 'panel',
  restart: 'panel',
  tour: 'panel',
}

export function groupOf(section: SettingsSection): SettingsGroup {
  return SECTION_GROUP[section]
}

/** The sections a group shows, in the order they are declared above. */
export function sectionsIn(group: SettingsGroup): SettingsSection[] {
  return SETTINGS_SECTIONS.filter((s) => SECTION_GROUP[s] === group)
}

/**
 * Where the gear lands when nobody asked for anything in particular.
 *
 * A section rather than a group, so the one caller with no opinion goes
 * through the same door as the ones that have one.
 */
export const SETTINGS_HOME: SettingsSection = 'profiles'

/**
 * Arrow-key navigation on the rail.
 *
 * Both axes, deliberately: the same rail is a column beside the body on a
 * laptop and a row above it on a phone, so which arrow means "further down the
 * list" depends on a viewport this function is not told about. Answering to
 * both costs two comparisons and removes the only reason it would have needed
 * to know the width.
 *
 * Wrapping, and Home/End, for the reason the panel's tab strip has them: a
 * list with a stop at each end punishes holding a key down.
 */
export function groupFromKey(key: string, current: SettingsGroup): SettingsGroup | null {
  const at = SETTINGS_GROUPS.indexOf(current)
  if (at < 0) return null
  const n = SETTINGS_GROUPS.length
  if (key === 'ArrowDown' || key === 'ArrowRight') return SETTINGS_GROUPS[(at + 1) % n]
  if (key === 'ArrowUp' || key === 'ArrowLeft') return SETTINGS_GROUPS[(at - 1 + n) % n]
  if (key === 'Home') return SETTINGS_GROUPS[0]
  if (key === 'End') return SETTINGS_GROUPS[n - 1]
  return null
}
