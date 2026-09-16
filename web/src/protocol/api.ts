import type {
  ApiToken,
  AuditEntry,
  DirListing,
  AuthState,
  FileListing,
  GitHubResult,
  GitInfo,
  HookAgent,
  HookStatus,
  LaunchProfile,
  SettingsInfo,
  Note,
  PanelState,
  Passkey,
  Project,
  Session,
  SessionState,
  ShareDetail,
  ShareLink,
  SharePage,
  SharePageCatalogue,
  SharePageDetail,
  SharePageDraft,
  SharePageRow,
  SharePagesRoot,
  SharePageData,
  SharePageDataNamespace,
  SharePageSecret,
  SharePageServerLogLine,
  SharePageSource,
  SharingSettings,
  ShareParamValue,
  SystemSample,
  TokenUsage,
  UsageSample,
  UpdateStatus,
  UpdateStarted,
  UpdateRefusal,
  Webhook,
  WebhookTest,
  TuneStatus,
  RestartResult,
  EnvSettings,
  ChatAlerts,
  ChatSettings,
  ChatLogin,
  ChatTestResult,
  ChatPeer,
  ChatPeerMode,
  ChatPeerStatus,
  ChatRoutes,
  ChatRoutePreview,
  ChatToolProfile,
  ChatAssistantConfig,
} from './wire'

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: { 'Content-Type': 'application/json', ...(init?.headers ?? {}) },
  })
  if (!res.ok) {
    // The server always sends {"error": "..."} on failure, but a proxy or a
    // crash can produce something else; fall back to the status rather than
    // throwing a parse error that hides what actually happened.
    let message = `${res.status} ${res.statusText}`
    let setupRequired = false
    let needsTrust = ''
    let answersTo: string[] = []
    try {
      const body = (await res.json()) as {
        error?: string
        setupRequired?: boolean
        originNeedsTrust?: string
        panelAnswersTo?: string[]
      }
      if (body.error) message = body.error
      setupRequired = body.setupRequired === true
      needsTrust = body.originNeedsTrust ?? ''
      answersTo = body.panelAnswersTo ?? []
    } catch {
      /* non-JSON error body */
    }
    // Distinguished so the shell can return to the sign-in screen rather than
    // showing a permission error inside a panel the user cannot use.
    if (res.status === 401) throw new UnauthorizedError(message, setupRequired)
    // Distinguished for the same reason: it is a question, not a failure, and
    // the only thing that can answer it is the person at the wizard.
    if (needsTrust) throw new OriginNotTrustedError(message, needsTrust, answersTo)
    throw new Error(message)
  }
  if (res.status === 204) {
    // Drain it even though there is nothing there. A Response whose body is
    // never read is reported by Chromium as an aborted request, which turns
    // every successful delete into a network error in the devtools log and in
    // anything watching for them.
    await res.arrayBuffer()
    return undefined as T
  }
  return (await res.json()) as T
}

/**
 * Turns a failed response into the error to throw.
 *
 * The calls below that cannot go through `request` -- an upload is multipart, a
 * note save needs `keepalive`, a preview needs the response headers -- each
 * grew their own copy of this, and the copies had already started to differ:
 * `request` reads `setupRequired` and the copies did not, so the same expired
 * session sent you to the sign-in screen or left a permission error inside a
 * panel you could no longer use, depending on which button you had pressed.
 */
async function failure(res: Response): Promise<Error> {
  // The server always sends {"error": "..."} on failure, but a proxy or a
  // crash can produce something else; fall back to the status rather than
  // throwing a parse error that hides what actually happened.
  let message = `${res.status} ${res.statusText}`
  let setupRequired = false
  try {
    const body = (await res.json()) as { error?: string; setupRequired?: boolean }
    if (body.error) message = body.error
    setupRequired = body.setupRequired === true
  } catch {
    /* non-JSON error body */
  }
  if (res.status === 401) return new UnauthorizedError(message, setupRequired)
  return new Error(message)
}

/**
 * A preview of one file, or the reason there is not one.
 *
 * `tooBig` and `none` are answers rather than failures -- there is a file, the
 * panel can still hand it to you, and it is saying it will not pretend to show
 * it -- so they come back as values while a 403 or a 500 throws.
 */
export type FilePreview =
  | { kind: 'text'; text: string; truncated: boolean; markup: Markup | null }
  | { kind: 'image'; blob: Blob }
  | { kind: 'pdf'; blob: Blob }
  | { kind: 'tooBig' }
  | { kind: 'none' }

/**
 * Whether a second endpoint would draw this file as a document.
 *
 * The text response is unchanged by it — still an attachment, still
 * octet-stream, still nothing a browser renders. This only says the choice
 * exists, so the panel can offer it.
 */
export type Markup = 'html' | 'svg'

/** A value this build understands, or nothing. An older tab against a newer
 *  server must not offer to render a kind it has no isolation story for. */
function markupOf(header: string | null): Markup | null {
  return header === 'html' || header === 'svg' ? header : null
}

/**
 * The type a Blob is built with, from the kind the server named.
 *
 * A Blob's type is not a label, it is the instruction the browser follows when
 * the bytes reach an <img> or an <object>. So it is derived from the kind
 * rather than echoed from the response: this is the one place where something
 * out of a project directory could be handed to the browser as something to
 * run, and the answer is that it never gets to name its own type.
 */
export function blobTypeFor(kind: 'image' | 'pdf', header: string | null): string {
  if (kind === 'pdf') return 'application/pdf'
  return header !== null && header.startsWith('image/') ? header : 'application/octet-stream'
}

/**
 * Thrown when a write would have landed on top of somebody else's.
 *
 * Carries what the server currently holds so the caller can show both without
 * a second round trip.
 */
export class ConflictError extends Error {
  readonly current: Note
  constructor(message: string, current: Note) {
    super(message)
    this.name = 'ConflictError'
    this.current = current
  }
}

/** Thrown when the server says the caller is not signed in. */
/**
 * Finishing setup would leave this browser unable to write anything.
 *
 * The panel sees a different name than the one in the URL bar -- nginx's
 * default `proxy_pass` sets Host to the upstream -- so the origin check would
 * refuse the first write after signing in. The server refuses the setup
 * instead, while the person is still holding the one-time token, and names
 * both sides so they can decide.
 */
export class OriginNotTrustedError extends Error {
  readonly origin: string
  readonly answersTo: string[]
  constructor(message: string, origin: string, answersTo: string[]) {
    super(message)
    this.name = 'OriginNotTrustedError'
    this.origin = origin
    this.answersTo = answersTo
  }
}

/**
 * sudo did not run the upgrade, and said why.
 *
 * Its own class because the page does something different for each reason --
 * asks for a password, asks for somebody else's, or stops asking -- and a
 * plain Error would leave it one sentence to show for all of them.
 */
export class UpdateRefusedError extends Error {
  readonly reason: UpdateRefusal
  readonly askedFor: string
  constructor(message: string, reason: UpdateRefusal, askedFor: string) {
    super(message)
    this.name = 'UpdateRefusedError'
    this.reason = reason
    this.askedFor = askedFor
  }
}

/**
 * The upload did not reach the server, or did not come back.
 *
 * A kind rather than a message, because this layer has no dictionary -- and
 * the message of whatever is thrown here is shown to the person who dropped
 * the file. `detail` on a toast is documented as "text the panel did not
 * write: a server error, a filename", and `network error` in the middle of a
 * Chinese page is the panel writing English into that slot. The caller turns
 * the kind into a sentence; see uploadErrorText.
 */
export class UploadTransportError extends Error {
  readonly kind: 'network' | 'aborted' | 'timeout'
  constructor(kind: 'network' | 'aborted' | 'timeout') {
    super(kind)
    this.name = 'UploadTransportError'
    this.kind = kind
  }
}

export class UnauthorizedError extends Error {
  readonly setupRequired: boolean
  constructor(message: string, setupRequired: boolean) {
    super(message)
    this.name = 'UnauthorizedError'
    this.setupRequired = setupRequired
  }
}

/**
 * The project id that means "the note that belongs to no project".
 *
 * A sentinel rather than `null`, because the editor keys its state by this
 * value and `null` is indistinguishable from "no project selected" -- which is
 * the state the global note is most often opened from. Matches
 * store.GlobalNoteID on the server.
 */
export const GLOBAL_NOTE = '@global'

/** Where a note lives. The global one is not under /projects/. */
function notePath(projectId: string): string {
  return projectId === GLOBAL_NOTE ? '/api/notes' : `/api/projects/${projectId}/notes`
}

export const api = {
  authState: () => request<AuthState>('/api/auth/state'),

  // trustOrigin ratifies the origin this request is sent from, and carries no
  // value: the server reads it from the Origin header. See handleSetup.
  setup: (token: string, username: string, password: string, trustOrigin = false) =>
    request<AuthState>('/api/auth/setup', {
      method: 'POST',
      body: JSON.stringify({ token, username, password, trustOrigin }),
    }),

  login: (username: string, password: string) =>
    request<AuthState>('/api/auth/login', {
      method: 'POST',
      body: JSON.stringify({ username, password }),
    }),

  logout: () => request<void>('/api/auth/logout', { method: 'POST' }),

  changePassword: (current: string, next: string) =>
    request<void>('/api/auth/password', {
      method: 'POST',
      body: JSON.stringify({ current, next }),
    }),

  passkeyLoginBegin: () => request<unknown>('/api/auth/passkey/login/begin', { method: 'POST' }),

  passkeyLoginFinish: (assertion: unknown) =>
    request<AuthState>('/api/auth/passkey/login/finish', {
      method: 'POST',
      body: JSON.stringify(assertion),
    }),

  passkeyRegisterBegin: () =>
    request<unknown>('/api/auth/passkey/register/begin', { method: 'POST' }),

  passkeyRegisterFinish: (name: string, attestation: unknown) =>
    request<{ name: string }>(
      `/api/auth/passkey/register/finish?name=${encodeURIComponent(name)}`,
      { method: 'POST', body: JSON.stringify(attestation) },
    ),

  passkeys: () => request<Passkey[]>('/api/auth/passkeys'),

  deletePasskey: (id: string) => request<void>(`/api/auth/passkeys/${id}`, { method: 'DELETE' }),

  settings: () => request<SettingsInfo>('/api/settings'),

  audit: () => request<AuditEntry[]>('/api/settings/audit'),

  hookStatus: () => request<HookStatus>('/api/settings/hooks'),

  /** Puts the first-run tour away, on the server: it is read once per person,
   *  not once per browser. */
  envSettings: () => request<EnvSettings>('/api/settings/env'),
  setTimeZone: (zone: string) =>
    request<{ zone: string; rebuilt: number; offset: number; nowLabel: string }>(
      '/api/settings/timezone',
      { method: 'PUT', body: JSON.stringify({ zone }) },
    ),
  saveEnvSettings: (values: Record<string, string>) =>
    request<EnvSettings>('/api/settings/env', {
      method: 'PUT',
      body: JSON.stringify({ values }),
    }),

  /** Puts text in the tmux paste buffer without pasting it anywhere. */
  setClipboard: (text: string) =>
    request<{ ok: boolean }>('/api/clipboard', { method: 'POST', body: JSON.stringify({ text }) }),

  savePaste: (dir: string, then: string) =>
    request<{ dir: string; then: string }>('/api/settings/paste', {
      method: 'PUT',
      body: JSON.stringify({ dir, then }),
    }),

  tourDone: () => request<{ ok: boolean }>('/api/settings/tour', { method: 'POST' }),
  /** Puts it back, for the button in the settings page. */
  tourAgain: () =>
    request<{ ok: boolean }>('/api/settings/tour?again=1', { method: 'POST' }),

  /** Which agent's configuration to edit. The server refuses anything else
   *  rather than guessing, because the answer decides which file in the user's
   *  home directory gets written. */
  /** What the panel would change in ~/.claude/settings.json beyond hooks. */
  tuneStatus: () => request<TuneStatus>('/api/settings/tune'),
  /** Writes them. Answers with the comparison as it was *before* the write. */
  tuneApply: () => request<TuneStatus>('/api/settings/tune', { method: 'POST' }),

  /**
   * Stops the panel so its supervisor starts a new one.
   *
   * 409 with `reason: "unsupervised"` when nothing would: the caller has to
   * handle that, because on that machine the button is "stop" and the tab it
   * was clicked in is about to go dark.
   */
  restartPanel: () => request<RestartResult>('/api/settings/restart', { method: 'POST' }),

  installHooks: (agent: HookAgent = 'claude') =>
    request<HookStatus>(`/api/settings/hooks?agent=${agent}`, { method: 'POST' }),

  removeHooks: (agent: HookAgent = 'claude') =>
    request<HookStatus>(`/api/settings/hooks?agent=${agent}`, { method: 'DELETE' }),

  /** Which agents the reporting section offers. The server answers with the
   *  list it stored, which is the one the page then draws from: it fixes the
   *  order and drops duplicates, and a page that kept its own copy would show
   *  something the next reload disagrees with. A name the server does not know
   *  is a 400, not a silent omission. */
  setHookAgents: (agents: HookAgent[]) =>
    request<{ agentsShown: HookAgent[] }>('/api/settings/hooks/agents', {
      method: 'PUT',
      body: JSON.stringify({ agents }),
    }),

  state: () => request<PanelState>('/api/state'),

  health: () =>
    request<{
      ok: boolean
      version: string
      /** The build this binary was made from. Together with version it is what
       *  tells an open tab that the panel underneath it has been replaced. */
      commit: string
      tmuxVersion: string
      live: number
      passkeys: boolean
    }>('/api/health'),

  listTokens: () => request<ApiToken[]>('/api/settings/tokens'),
  createToken: (name: string) =>
    request<ApiToken & { token: string }>('/api/settings/tokens', {
      method: 'POST',
      body: JSON.stringify({ name }),
    }),
  deleteToken: (id: string) =>
    request<void>(`/api/settings/tokens/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  listShares: () => request<ShareLink[]>('/api/settings/shares'),

  /**
   * Mints a read-only link. The response is the only time its token is
   * readable, exactly like an API token.
   *
   * `expiresIn` is seconds from now and 0 means never. A duration rather than
   * an instant, because the browser has no standing to be believed about what
   * time it is on the server.
   */
  // Spelled out rather than `ShareLink & { token }`, which is what the token
  // endpoint next to it does. The creation response is not a ShareLink: it has
  // no `lastUsedAt`, because a link made half a second ago has never been used.
  // Declaring a field the server does not send is the drift red line 3 is
  // about — it type-checks and is `undefined` at runtime.
  createShare: (req: {
    name: string
    detail: ShareDetail
    expiresIn: number
    scope: string
    scopeId: string
    /** The owner's label for the screen. Shown to viewers under both modes. */
    remark: string
    locked: boolean
    /** Visitors may run the page's visitor actions through this link. */
    interactive?: boolean
    /** The published share page the link draws, with its settings on it. */
    pageId: string
    params: Record<string, ShareParamValue>
  }) =>
    request<{
      token: string
      id: string
      name: string
      prefix: string
      detail: string
      pageId: string
      params: Record<string, ShareParamValue>
      scope: string
      remark: string
      locked: boolean
      expiresAt: number
      createdAt: number
    }>('/api/settings/shares', {
      method: 'POST',
      body: JSON.stringify(req),
    }),

  /**
   * Renames a link, relabels it, and fixes or unfixes what it draws.
   *
   * Deliberately no `detail` and no `scope`. By the time anybody edits a link
   * its URL is already in an email or typed into a television, and widening
   * what that address discloses is a change the people holding it would never
   * see. The server refuses them too; this signature is the same refusal said
   * where the caller reads it.
   */
  updateShare: (
    id: string,
    fields: { name: string; remark: string; locked: boolean; interactive?: boolean },
  ) =>
    request<void>(`/api/settings/shares/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body: JSON.stringify(fields),
    }),

  /**
   * Unlocks a link, and does nothing else.
   *
   * Its own call rather than `updateShare({locked: false, ...})`, because the
   * server accepts exactly one thing on a locked link and this is it: a single
   * request that could unlock *and* change the link would make the lock a
   * message instead of a guard.
   */
  unlockShare: (id: string) =>
    request<void>(`/api/settings/shares/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body: JSON.stringify({ locked: false }),
    }),

  /**
   * A fifteen-minute copy of a link -- same page, pin, parameters, detail and
   * scope -- so the owner can see what it shows. The panel keeps only a hash
   * of the real link's token and cannot open that one for them.
   */
  viewShare: (id: string) =>
    request<{ token: string; expiresAt: number }>(
      `/api/settings/shares/${encodeURIComponent(id)}/view`,
      { method: 'POST' },
    ),

  deleteShare: (id: string) =>
    request<void>(`/api/settings/shares/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  /**
   * Points a link at a share page, at a version or following the published
   * one, with the page's parameter values. Not what the link discloses: that is
   * `detail` and `scope`, fixed when it was made.
   */
  setSharePage: (
    id: string,
    fields: { pageId: string; pinVersion: number; params: Record<string, ShareParamValue> },
  ) =>
    request<void>(`/api/settings/shares/${encodeURIComponent(id)}/page`, {
      method: 'PUT',
      body: JSON.stringify(fields),
    }),

  // ── share pages ──────────────────────────────────────────────────────────

  listPages: () => request<SharePageRow[]>('/api/settings/pages'),

  pageCatalogue: () => request<SharePageCatalogue>('/api/settings/pages/catalogue'),

  /** A new page from a template, or an existing directory adopted when
   *  `template` is ''. An empty `sourceDir` puts a new one at
   *  <pagesRoot>/page-<slug>. */
  createPage: (req: { name: string; template: string; sourceDir: string }) =>
    request<SharePage>('/api/settings/pages', { method: 'POST', body: JSON.stringify(req) }),

  page: (id: string) => request<SharePageDetail>(`/api/settings/pages/${encodeURIComponent(id)}`),

  updatePage: (id: string, fields: { name: string; sourceDir: string }) =>
    request<void>(`/api/settings/pages/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body: JSON.stringify(fields),
    }),

  /**
   * Makes a page something an agent can start in: writes its published
   * version back out if the directory has gone, and finds or makes the
   * `page-…` project that is that directory.
   */
  openPage: (id: string) =>
    request<{ page: SharePage; projectId: string; restored: number }>(
      `/api/settings/pages/${encodeURIComponent(id)}/open`,
      { method: 'POST' },
    ),

  /** Where new pages go; '' goes back to the default. */
  setPagesRoot: (dir: string) =>
    request<SharePagesRoot>('/api/settings/pages/root', { method: 'PUT', body: JSON.stringify({ dir }) }),

  /** The link's address, again. 409 for a link made before addresses were kept. */
  shareURL: (id: string) =>
    request<{ url: string; token: string }>(`/api/settings/shares/${encodeURIComponent(id)}/url`),

  /** A new address for a link; the old one stops working. */
  rotateShare: (id: string) =>
    request<{ url: string; token: string }>(`/api/settings/shares/${encodeURIComponent(id)}/rotate`, {
      method: 'POST',
    }),

  sharingSettings: () => request<SharingSettings>('/api/settings/sharing'),

  setSharingSettings: (s: SharingSettings) =>
    request<SharingSettings>('/api/settings/sharing', { method: 'PUT', body: JSON.stringify(s) }),

  pageData: (id: string, ns: SharePageDataNamespace = 'live') =>
    request<SharePageData>(`/api/settings/pages/${encodeURIComponent(id)}/data?ns=${ns}`),

  setPageData: (id: string, key: string, value: unknown, ns: SharePageDataNamespace = 'live') =>
    request<{ value: unknown }>(
      `/api/settings/pages/${encodeURIComponent(id)}/data/${encodeURIComponent(key)}?ns=${ns}`,
      { method: 'PUT', body: JSON.stringify({ value }) },
    ),

  incrementPageData: (id: string, key: string, by: number, ns: SharePageDataNamespace = 'live') =>
    request<{ value: number }>(
      `/api/settings/pages/${encodeURIComponent(id)}/data/${encodeURIComponent(key)}/increment?ns=${ns}`,
      { method: 'POST', body: JSON.stringify({ by }) },
    ),

  appendPageData: (id: string, key: string, item: unknown, ns: SharePageDataNamespace = 'live') =>
    request<{ value: unknown[] }>(
      `/api/settings/pages/${encodeURIComponent(id)}/data/${encodeURIComponent(key)}/append?ns=${ns}`,
      { method: 'POST', body: JSON.stringify({ item }) },
    ),

  resetPageData: (id: string, key: string, ns: SharePageDataNamespace = 'live') =>
    request<void>(`/api/settings/pages/${encodeURIComponent(id)}/data/${encodeURIComponent(key)}?ns=${ns}`, {
      method: 'DELETE',
    }),

  clearPageData: (id: string, ns: SharePageDataNamespace = 'live') =>
    request<void>(`/api/settings/pages/${encodeURIComponent(id)}/data?ns=${ns}`, { method: 'DELETE' }),

  pageSources: (id: string) => request<SharePageSource[]>(`/api/settings/pages/${encodeURIComponent(id)}/sources`),

  pageHosts: (id: string) => request<{ hosts: string[] }>(`/api/settings/pages/${encodeURIComponent(id)}/hosts`),

  setPageHosts: (id: string, hosts: string[]) =>
    request<{ hosts: string[] }>(`/api/settings/pages/${encodeURIComponent(id)}/hosts`, {
      method: 'PUT',
      body: JSON.stringify({ hosts }),
    }),

  pageSecrets: (id: string) => request<SharePageSecret[]>(`/api/settings/pages/${encodeURIComponent(id)}/secrets`),

  setPageSecret: (id: string, name: string, value: string) =>
    request<void>(`/api/settings/pages/${encodeURIComponent(id)}/secrets/${encodeURIComponent(name)}`, {
      method: 'PUT',
      body: JSON.stringify({ value }),
    }),

  deletePageSecret: (id: string, name: string) =>
    request<void>(`/api/settings/pages/${encodeURIComponent(id)}/secrets/${encodeURIComponent(name)}`, {
      method: 'DELETE',
    }),

  pageServerLog: (id: string) =>
    request<{ lines: SharePageServerLogLine[] }>(`/api/settings/pages/${encodeURIComponent(id)}/server/log`),

  /** Where a page's admin page opens: behind the panel login, which mints a
   *  grant and redirects. `draft` serves the draft admin page on draft data. */
  pageAdminURL: (id: string, draft = false) => `/pages/${encodeURIComponent(id)}/admin/${draft ? '?draft=1' : ''}`,

  /** A link to the page as a zip: the published version, or the directory for a
   *  page never published. A plain GET, so an <a download> with the cookie works. */
  exportPageURL: (id: string): `/${string}` => `/api/settings/pages/${encodeURIComponent(id)}/export`,

  /** A zip becomes a new, unpublished page under the pages directory. */
  importPage: (file: Blob, name = '') =>
    request<{ page: SharePage; ignored: { path: string; reason: string }[] }>(
      `/api/settings/pages/import${name ? `?name=${encodeURIComponent(name)}` : ''}`,
      { method: 'POST', body: file, headers: { 'Content-Type': 'application/zip' } },
    ),

  deletePage: (id: string) =>
    request<void>(`/api/settings/pages/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  pageDraft: (id: string) =>
    request<SharePageDraft>(`/api/settings/pages/${encodeURIComponent(id)}/draft`),

  /** Cheap: sizes and times, hashed. Asked twice a second while a Preview is open. */
  pageFingerprint: (id: string) =>
    request<{ fingerprint: string }>(`/api/settings/pages/${encodeURIComponent(id)}/draft/fingerprint`),

  publishPage: (id: string, note: string) =>
    request<{ version: number }>(`/api/settings/pages/${encodeURIComponent(id)}/publish`, {
      method: 'POST',
      body: JSON.stringify({ note }),
    }),

  rollbackPage: (id: string, version: number) =>
    request<void>(`/api/settings/pages/${encodeURIComponent(id)}/rollback`, {
      method: 'POST',
      body: JSON.stringify({ version }),
    }),

  /**
   * A fifteen-minute share link that draws the page's draft. A real link, so
   * the Preview cannot show what a wall would not; the token is held in memory
   * by the pane and never stored.
   */
  previewPage: (id: string, detail: ShareDetail) =>
    request<{ id: string; token: string; expiresAt: number }>(
      `/api/settings/pages/${encodeURIComponent(id)}/preview`,
      { method: 'POST', body: JSON.stringify({ detail }) },
    ),

  renewPreview: (pageId: string, linkId: string) =>
    request<{ expiresAt: number }>(
      `/api/settings/pages/${encodeURIComponent(pageId)}/preview/${encodeURIComponent(linkId)}/renew`,
      { method: 'POST', body: '{}' },
    ),

  /** What a preview frame reported, written into the draft for the agent. */
  reportPageErrors: (
    id: string,
    errors: { kind: string; message: string; source: string; line: number }[],
  ) =>
    request<void>(`/api/settings/pages/${encodeURIComponent(id)}/errors`, {
      method: 'PUT',
      body: JSON.stringify({ errors }),
    }),

  startTrial: (id: string, linkId: string, minutes: number) =>
    request<{ version: number; pinUntil: number }>(
      `/api/settings/pages/${encodeURIComponent(id)}/trial`,
      { method: 'POST', body: JSON.stringify({ linkId, minutes }) },
    ),

  keepTrial: (id: string, linkId: string) =>
    request<{ version: number }>(
      `/api/settings/pages/${encodeURIComponent(id)}/trial/${encodeURIComponent(linkId)}/keep`,
      { method: 'POST', body: '{}' },
    ),

  endTrial: (id: string, linkId: string) =>
    request<void>(
      `/api/settings/pages/${encodeURIComponent(id)}/trial/${encodeURIComponent(linkId)}`,
      { method: 'DELETE' },
    ),

  forkPage: (id: string, name: string) =>
    request<SharePage>(`/api/settings/pages/${encodeURIComponent(id)}/fork`, {
      method: 'POST',
      body: JSON.stringify({ name, sourceDir: '' }),
    }),

  /**
   * List directories. No argument means home; `''` means the filesystem root.
   *
   * The two are different requests and the difference is the parameter being
   * there at all, so the default cannot be `''`: that is the path the first
   * crumb carries, and defaulting to it would send everyone who clicked `/`
   * back to their home directory.
   */
  browse: (path?: string) =>
    request<DirListing>(
      path === undefined ? '/api/browse' : `/api/browse?path=${encodeURIComponent(path)}`,
    ),

  mkdir: (path: string, name: string) =>
    request<{ path: string; abs: string }>('/api/browse/mkdir', {
      method: 'POST',
      body: JSON.stringify({ path, name }),
    }),

  createProject: (path: string, name?: string) =>
    request<Project>('/api/projects', {
      method: 'POST',
      body: JSON.stringify({ path, name: name ?? '' }),
    }),

  patchProject: (id: string, patch: Partial<{ name: string; pinned: boolean }>) =>
    request<Project>(`/api/projects/${id}`, { method: 'PATCH', body: JSON.stringify(patch) }),

  deleteProject: (id: string) => request<void>(`/api/projects/${id}`, { method: 'DELETE' }),

  /** Writes an explicit project order, top first. */
  reorderLaunchProfiles: (ids: string[]) =>
    request<void>('/api/launch-profiles/reorder', {
      method: 'POST',
      body: JSON.stringify({ ids }),
    }),

  /** Un-hides every built-in and drops every edit of one. Leaves your own alone. */
  restoreLaunchProfiles: () =>
    request<void>('/api/launch-profiles/restore', { method: 'POST', body: '{}' }),

  reorderProjects: (ids: string[]) =>
    request<void>('/api/projects/reorder', { method: 'POST', body: JSON.stringify({ ids }) }),

  /**
   * Switches to most-active-first ordering, keeping the arrangement.
   *
   * It used to discard it — one click on a clock icon, no confirmation, and
   * the arrangement was gone, with the button removing itself on the way out
   * because it only renders in manual mode.
   */
  autoOrderProjects: () =>
    request<void>('/api/projects/reorder', { method: 'POST', body: JSON.stringify({ auto: true }) }),

  /** Goes back to the arrangement that is already stored. */
  restoreProjectOrder: () =>
    request<void>('/api/projects/reorder', { method: 'POST', body: JSON.stringify({}) }),

  createSession: (
    projectId: string,
    command: string[],
    opts: { title?: string; scratch?: boolean; nearSessionId?: string; launchProfileId?: string } = {},
  ) =>
    request<Session>('/api/sessions', {
      method: 'POST',
      body: JSON.stringify({
        projectId,
        // Left empty by the picker: the server resolves the profile's argv, so
        // that a session created with curl and a profile id gets exactly what
        // the picker would have given it.
        command,
        title: opts.title ?? '',
        scratch: opts.scratch ?? false,
        nearSessionId: opts.nearSessionId ?? '',
        launchProfileId: opts.launchProfileId ?? '',
      }),
    }),

  patchSession: (
    id: string,
    patch: Partial<{
      title: string
      pinned: boolean
      state: SessionState
      restoreOnBoot: boolean
    }>,
  ) => request<Session>(`/api/sessions/${id}`, { method: 'PATCH', body: JSON.stringify(patch) }),

  deleteSession: (id: string) => request<void>(`/api/sessions/${id}`, { method: 'DELETE' }),

  /**
   * A directory served as a page.
   *
   * The response is the only time the token is readable, so the caller has to
   * do something with it there and then -- the database keeps a hash.
   */
  /**
   * `address` is the whole of the visibility model, and it is worth being
   * blunt about why there is not more to it: a preview cannot require a
   * session. It is served into an opaque origin, an opaque origin does not
   * send the cookie, and the version that asked for one rendered the page with
   * every asset it wanted blocked. So the only thing between a stranger and a
   * preview is how hard the address is to guess -- empty for 32 bytes of
   * random, a word for "anyone who knows it".
   */
  createPreview: (req: {
    projectId: string
    path: string
    name: string
    /** Seconds from now; 0 never expires. */
    expiresIn: number
    /** Empty for a random one. */
    address?: string
    /**
     * Whether the served page may load scripts, styles, fonts and images from
     * other origins. Off is the policy that makes a link safe to hand out; on
     * is what a page that pulls three.js off a CDN needs. See dirPreviewCSP.
     */
    allowExternal?: boolean
  }) =>
    request<{
      id: string
      token: string
      name: string
      root: string
      expiresAt: number
      allowExternal: boolean
    }>('/api/settings/previews', { method: 'POST', body: JSON.stringify(req) }),

  restartSession: (id: string) =>
    request<void>(`/api/sessions/${id}/restart`, { method: 'POST' }),

  /**
   * Rebuild sessions whose tmux session went with the machine.
   *
   * A batch, and the ids are always explicit. Answers 200 with one result per
   * id even when some of them failed: after a reboot the ordinary failure is a
   * single project directory that was pruned while the machine was off, and
   * refusing the whole batch over it would leave twenty-three sessions dead to
   * report one.
   */
  restoreSessions: (ids: string[]) =>
    request<{ results: { id: string; ok: boolean; error?: string }[] }>(
      '/api/sessions/restore',
      { method: 'POST', body: JSON.stringify({ ids }) },
    ),

  system: () => request<SystemSample>('/api/system'),

  usage: () => request<UsageSample>('/api/usage'),

  // Not under /api/settings, because the picker fetches this on every page
  // load. See registerLaunchProfileRoutes.
  launchProfiles: () => request<LaunchProfile[]>('/api/launch-profiles'),
  createLaunchProfile: (p: Pick<LaunchProfile, 'name' | 'command' | 'env'>) =>
    request<LaunchProfile>('/api/launch-profiles', { method: 'POST', body: JSON.stringify(p) }),
  updateLaunchProfile: (id: string, p: Pick<LaunchProfile, 'name' | 'command' | 'env'>) =>
    request<void>(`/api/launch-profiles/${id}`, { method: 'PATCH', body: JSON.stringify(p) }),
  deleteLaunchProfile: (id: string) =>
    request<void>(`/api/launch-profiles/${id}`, { method: 'DELETE' }),

  webhooks: () => request<Webhook[]>('/api/settings/webhooks'),
  saveWebhooks: (list: Webhook[]) =>
    request<Webhook[]>('/api/settings/webhooks', { method: 'PUT', body: JSON.stringify(list) }),
  testWebhook: (w: Webhook) =>
    request<WebhookTest>('/api/settings/webhooks/test', { method: 'POST', body: JSON.stringify(w) }),

  // ─── chat ────────────────────────────────────────────────────────────────
  chat: () => request<ChatSettings>('/api/chat'),
  saveChatChannel: (kind: string, enabled: boolean, values: Record<string, string>) =>
    request<void>(`/api/chat/channels/${encodeURIComponent(kind)}`, {
      method: 'PUT',
      body: JSON.stringify({ enabled, values }),
    }),
  removeChatChannel: (kind: string) =>
    request<void>(`/api/chat/channels/${encodeURIComponent(kind)}`, { method: 'DELETE' }),
  testChatChannel: (kind: string, peerId?: string) =>
    request<ChatTestResult>(`/api/chat/channels/${encodeURIComponent(kind)}/test`, {
      method: 'POST',
      body: JSON.stringify({ peerId: peerId ?? '' }),
    }),
  startChatLogin: (kind: string) =>
    request<ChatLogin>(`/api/chat/channels/${encodeURIComponent(kind)}/login`, { method: 'POST' }),
  chatLoginStatus: (kind: string, id: string) =>
    request<ChatLogin>(`/api/chat/channels/${encodeURIComponent(kind)}/login/${encodeURIComponent(id)}`),
  chatLoginCode: (kind: string, id: string, code: string) =>
    request<void>(
      `/api/chat/channels/${encodeURIComponent(kind)}/login/${encodeURIComponent(id)}/code`,
      { method: 'POST', body: JSON.stringify({ code }) },
    ),
  saveChatAlerts: (alerts: ChatAlerts) =>
    request<ChatAlerts>('/api/chat/alerts', { method: 'PUT', body: JSON.stringify(alerts) }),
  chatConsent: () => request<{ consentAt: number }>('/api/chat/consent', { method: 'POST' }),
  pairChat: (code: string) =>
    request<ChatPeer>('/api/chat/pair', { method: 'POST', body: JSON.stringify({ code }) }),
  patchChatPeer: (channel: string, peerId: string, patch: { mode?: ChatPeerMode; status?: ChatPeerStatus; display?: string }) =>
    request<ChatPeer>(`/api/chat/peers/${encodeURIComponent(channel)}/${encodeURIComponent(peerId)}`, {
      method: 'PATCH',
      body: JSON.stringify(patch),
    }),
  removeChatPeer: (channel: string, peerId: string) =>
    request<void>(`/api/chat/peers/${encodeURIComponent(channel)}/${encodeURIComponent(peerId)}`, {
      method: 'DELETE',
    }),
  saveChatRoutes: (routes: ChatRoutes) =>
    request<ChatRoutes>('/api/chat/routes', { method: 'PUT', body: JSON.stringify(routes) }),
  previewChatRoute: (sessionId: string) =>
    request<ChatRoutePreview>('/api/chat/routes/preview', {
      method: 'POST',
      body: JSON.stringify({ sessionId }),
    }),
  saveChatKeys: (tools: Record<string, ChatToolProfile>) =>
    request<Record<string, ChatToolProfile>>('/api/chat/keys', { method: 'PUT', body: JSON.stringify(tools) }),
  saveChatAssistant: (cfg: ChatAssistantConfig) =>
    request<ChatAssistantConfig>('/api/chat/assistant', { method: 'PUT', body: JSON.stringify(cfg) }),
  saveChatLang: (lang: 'zh' | 'en') =>
    request<void>('/api/chat/lang', { method: 'PUT', body: JSON.stringify({ lang }) }),
  chatLog: (n = 100) => request<AuditEntry[]>(`/api/chat/log?n=${n}`),

  /** Asks GitHub now. The button. */
  checkUpdate: () => request<UpdateStatus>('/api/update'),
  /**
   * What the panel already knows, without asking anybody -- unless the
   * automatic check is on and the answer is old, in which case the server
   * asks in the background and this answers with what it has. Cheap enough
   * to poll: while a job runs, the page reads its progress from here.
   */
  updateStatus: () => request<UpdateStatus>('/api/update/status'),
  setUpdateAutoCheck: (autoCheck: boolean) =>
    request<{ autoCheck: boolean }>('/api/update/settings', {
      method: 'PUT',
      body: JSON.stringify({ autoCheck }),
    }),
  /**
   * `expected` is the version the page showed when the button was pressed;
   * the server refuses with `changed` if the newest release is now another.
   *
   * `secret` is only sent when the panel said it cannot replace its own binary
   * and that a helper exists. It is spent on one fixed command and kept
   * nowhere: not in a query string, not in a log, and not in this module
   * beyond the call.
   */
  applyUpdate: async (expected: string, secret?: string): Promise<UpdateStarted> => {
    const res = await fetch('/api/update', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(secret ? { password: secret, expected } : { expected }),
    })
    if (!res.ok) {
      // A refusal from sudo, or from the job already running, carries a
      // reason; anything else is the ordinary error shape and goes through
      // the ordinary path, 401 included.
      const body = (await res
        .clone()
        .json()
        .catch(() => null)) as { error?: string; reason?: UpdateRefusal; askedFor?: string } | null
      if (body?.reason) throw new UpdateRefusedError(body.error ?? '', body.reason, body.askedFor ?? '')
      throw await failure(res)
    }
    return (await res.json()) as UpdateStarted
  },

  /**
   * What the agents recorded spending. Not `usage` above, which is CPU and
   * memory right now — the two are a name apart and mean nothing alike.
   *
   * `project` is a project id, never a path: the server resolves it, so a
   * caller cannot ask about an arbitrary directory and learn from the answer
   * whether an agent has ever run in it.
   */
  tokenUsage: (opts: { days?: number; project?: string; tool?: string } = {}) => {
    const q = new URLSearchParams()
    if (opts.days) q.set('days', String(opts.days))
    if (opts.project) q.set('project', opts.project)
    if (opts.tool) q.set('tool', opts.tool)
    const s = q.toString()
    return request<TokenUsage>('/api/token-usage' + (s ? `?${s}` : ''))
  },

  /** Reads the transcripts again. Returns as soon as a pass has been asked
   *  for; the numbers arrive on the next poll. */
  refreshTokenUsage: () =>
    request<{ started: boolean }>('/api/token-usage/refresh', { method: 'POST' }),

  /**
   * A URL rather than a request: downloading is the browser's job, and it does
   * it better than any fetch-into-a-blob would — progress, resume, and the
   * save dialog, without holding the file in memory.
   */
  downloadURL: (projectId: string, path: string) =>
    `/api/projects/${projectId}/download?path=${encodeURIComponent(path)}`,

  /**
   * Where an <iframe> points to draw a page out of a project.
   *
   * A URL rather than a fetch, and that is the isolation working rather than a
   * convenience. The bytes must arrive carrying the server's
   * Content-Security-Policy — which is what forbids the page a network of any
   * kind, and what makes its sandbox hold even if this URL is opened in a tab.
   * Fetching them into a Blob or a srcdoc throws every one of those headers
   * away and leaves the document's origin inherited from the panel.
   *
   * `scripts` is passed to the *server*, which is the point: the effective
   * sandbox is the intersection of the iframe attribute and the response
   * header, so editing the attribute in devtools cannot enable execution.
   */
  renderURL: (projectId: string, path: string, scripts: boolean) =>
    `/api/projects/${projectId}/preview/render?path=${encodeURIComponent(path)}` +
    (scripts ? '&scripts=1' : ''),

  /** What the working tree says. Reads the disk; never the network. */
  git: (projectId: string) => request<GitInfo>(`/api/projects/${projectId}/git`),

  /**
   * Asks GitHub, once.
   *
   * POST because a GET is something a browser re-issues on its own — a reload,
   * a back button, a prefetch — and this is the one request in the panel that
   * leaves the machine on a person's say-so.
   */
  github: (projectId: string) =>
    request<GitHubResult>(`/api/projects/${projectId}/git/github`, { method: 'POST' }),

  /** Returns the absolute paths the files landed at, ready to type. */
  /**
   * `dest: 'panel'` writes into a directory the panel owns instead of into the
   * project. That is where a pasted screenshot goes by default: the session's
   * working directory is a git repository, and a picture pasted at an agent
   * should not dirty it.
   */
  upload: async (
    projectId: string,
    path: string,
    files: File[],
    dest?: 'panel',
    onProgress?: (fraction: number) => void,
  ) => {
    const form = new FormData()
    for (const f of files) form.append('file', f, f.name)
    // XMLHttpRequest rather than fetch: fetch cannot report request-body
    // progress, and a progress bar is the difference between "uploading…" and
    // knowing a 300MB drop is halfway rather than hung.
    return await new Promise<{ paths: string[] }>((resolve, reject) => {
      const xhr = new XMLHttpRequest()
      xhr.open(
        'POST',
        `/api/projects/${projectId}/upload?path=${encodeURIComponent(path)}` +
          (dest ? `&dest=${dest}` : ''),
      )
      // Capped just short of the end. The last byte leaving the browser is not
      // the upload finishing -- the server still has to write the files, which
      // on the 300MB drop this bar exists for is the part you wait for. A bar
      // that reads 100% while nothing has come back is the same "is it hung?"
      // question moved to the end, and a full bar puts the toast back on its
      // four-second timer.
      xhr.upload.onprogress = (e) => {
        if (e.lengthComputable && onProgress) onProgress(Math.min(e.loaded / e.total, 0.99))
      }
      xhr.onload = () => {
        type Body = { paths?: string[]; error?: string; setupRequired?: boolean }
        let body: Body | null = null
        try {
          body = JSON.parse(xhr.responseText) as Body
        } catch {
          /* non-JSON body: a proxy or a crash, same as failure() covers */
        }
        if (xhr.status >= 200 && xhr.status < 300) {
          // A 200 whose body is not the answer is a proxy in the way, not an
          // upload of nothing: `paths ?? []` reported success and the panel
          // said "0 files uploaded" with no way to tell that apart from a
          // request that really did store none.
          if (!body?.paths) {
            reject(new Error(`${xhr.status} ${xhr.statusText}`))
            return
          }
          // Now it is done, and the bar says so.
          onProgress?.(1)
          resolve({ paths: body.paths })
          return
        }
        const message = body?.error ?? `${xhr.status} ${xhr.statusText}`
        if (xhr.status === 401) {
          reject(new UnauthorizedError(message, body?.setupRequired === true))
          return
        }
        reject(new Error(message))
      }
      // Every other way this ends. A Promise that is never settled leaves the
      // "uploading…" toast on screen with its bar where it stopped, and the
      // await above never returns -- so nothing takes it back and nothing says
      // what happened.
      xhr.onerror = () => reject(new UploadTransportError('network'))
      xhr.onabort = () => reject(new UploadTransportError('aborted'))
      xhr.ontimeout = () => reject(new UploadTransportError('timeout'))
      xhr.send(form)
    })
  },

  /**
   * One request, not two.
   *
   * The server decides what a file is from its leading bytes, so it already
   * knows by the time it has anything to send -- it says so in a header and
   * sends what it read. Asking "what is this" and then "give me it" would read
   * the head of the file twice and let the two answers disagree about a file an
   * agent is writing into.
   *
   * The bytes never become a URL the browser navigates to. They arrive through
   * fetch and become a Blob whose type this side chose, so nothing out of a
   * project directory is ever handed to the browser as something to render on
   * the panel's own origin.
   */
  preview: async (projectId: string, path: string): Promise<FilePreview> => {
    const res = await fetch(`/api/projects/${projectId}/preview?path=${encodeURIComponent(path)}`)
    // Drained rather than ignored, for the reason the 204 branch above is:
    // a body nobody reads is reported by Chromium as an aborted request, which
    // turns every honest refusal into a network error in the devtools log.
    if (res.status === 413) {
      await res.arrayBuffer()
      return { kind: 'tooBig' }
    }
    if (res.status === 415) {
      await res.arrayBuffer()
      return { kind: 'none' }
    }
    if (!res.ok) throw await failure(res)
    const kind = res.headers.get('X-Preview-Kind')
    if (kind === 'text') {
      return {
        kind: 'text',
        text: await res.text(),
        truncated: res.headers.get('X-Preview-Truncated') === 'true',
        markup: markupOf(res.headers.get('X-Preview-Markup')),
      }
    }
    // A kind this build does not know is the shape of an older tab against a
    // newer server. "No preview, here is the download" is true in that case
    // too, and is better than a blank frame.
    if (kind !== 'image' && kind !== 'pdf') {
      await res.arrayBuffer()
      return { kind: 'none' }
    }
    const type = blobTypeFor(kind, res.headers.get('X-Preview-Type'))
    return { kind, blob: new Blob([await res.arrayBuffer()], { type }) }
  },

  /** One directory inside a project. `path` is where, `name` is what.
   *
   *  Not `mkdir`: that name is taken by the directory picker's, against the
   *  home directory. Two keys with one name in this object is the later one
   *  winning silently, which is what happened. */
  projectMkdir: (projectId: string, path: string, name: string) =>
    request<{ path: string }>(`/api/projects/${projectId}/mkdir`, {
      method: 'POST',
      body: JSON.stringify({ path, name }),
    }),

  files: (projectId: string, path = '') =>
    request<FileListing>(`/api/projects/${projectId}/files?path=${encodeURIComponent(path)}`),

  note: (projectId: string) => request<Note>(notePath(projectId)),

  /**
   * baseRev is the revision the caller's text was built on. The server
   * refuses the write if the note has moved since, which is what stops two
   * windows from silently overwriting each other.
   */
  // `keepalive` is for the save issued while the page is going away. A normal
  // fetch from an unloading document is cancelled by the browser, so the last
  // thing typed before closing the tab never reached the server.
  saveNote: async (projectId: string, content: string, baseRev: number, keepalive = false) => {
    const res = await fetch(notePath(projectId), {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ content, baseRev }),
      keepalive,
    })
    if (res.status === 409) {
      const body = (await res.json()) as { error?: string; current: Note }
      throw new ConflictError(body.error ?? 'the note changed elsewhere', body.current)
    }
    if (!res.ok) throw await failure(res)
    return (await res.json()) as Note
  },

  // The four todo methods were here and are gone with the panel that called
  // them. The *routes* are not gone — see the note above registerPanelRoutes
  // in internal/httpapi/panels.go: share pages count todos, and an agent
  // with an API token can still write one. What has no caller is this client,
  // and dead client code is how somebody concludes the feature is dead.
}
