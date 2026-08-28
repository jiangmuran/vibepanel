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
  ShareBoard,
  ShareCatalogue,
  ShareDashboard,
  ShareDetail,
  ShareLink,
  SystemSample,
  TokenUsage,
  UsageSample,
  UpdateCheck,
  Webhook,
  WebhookTest,
  UpdateResult,
  VncTarget,
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
    try {
      const body = (await res.json()) as { error?: string; setupRequired?: boolean }
      if (body.error) message = body.error
      setupRequired = body.setupRequired === true
    } catch {
      /* non-JSON error body */
    }
    // Distinguished so the shell can return to the sign-in screen rather than
    // showing a permission error inside a panel the user cannot use.
    if (res.status === 401) throw new UnauthorizedError(message, setupRequired)
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
export class UnauthorizedError extends Error {
  readonly setupRequired: boolean
  constructor(message: string, setupRequired: boolean) {
    super(message)
    this.name = 'UnauthorizedError'
    this.setupRequired = setupRequired
  }
}

export const api = {
  authState: () => request<AuthState>('/api/auth/state'),

  setup: (token: string, username: string, password: string) =>
    request<AuthState>('/api/auth/setup', {
      method: 'POST',
      body: JSON.stringify({ token, username, password }),
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

  /** Which agent's configuration to edit. The server refuses anything else
   *  rather than guessing, because the answer decides which file in the user's
   *  home directory gets written. */
  installHooks: (agent: HookAgent = 'claude') =>
    request<HookStatus>(`/api/settings/hooks?agent=${agent}`, { method: 'POST' }),

  removeHooks: (agent: HookAgent = 'claude') =>
    request<HookStatus>(`/api/settings/hooks?agent=${agent}`, { method: 'DELETE' }),

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
    board: ShareBoard
    scope: string
    scopeId: string
    /** The owner's label for the screen. Shown to viewers under both modes. */
    remark: string
    locked: boolean
  }) =>
    request<{
      token: string
      id: string
      name: string
      prefix: string
      detail: string
      board: ShareBoard
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
   * Renames a link, relabels it, rearranges its board and fixes or unfixes it.
   *
   * This is how a television on a wall is changed: from a laptop, signed in,
   * with the wall picking it up on its next poll. There is nothing to do at the
   * screen itself, which is the whole point — and the reason the share surface
   * is still exactly one GET.
   *
   * Deliberately no `detail` and no `scope`. By the time anybody edits a link
   * its URL is already in an email or typed into a television, and widening
   * what that address discloses is a change the people holding it would never
   * see. The server refuses them too; this signature is the same refusal said
   * where the caller reads it.
   */
  updateShare: (
    id: string,
    fields: { name: string; remark: string; board: ShareBoard; locked: boolean },
  ) =>
    request<void>(`/api/settings/shares/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body: JSON.stringify(fields),
    }),

  /**
   * Unlocks a link, and does nothing else.
   *
   * Its own call rather than `updateShare({locked: false, ...})`, because the
   * server accepts exactly one thing on a locked link and this is it. A locked
   * board is a guard against rearranging the wall a customer is sitting in
   * front of from an editor left open on the wrong row; a single request that
   * could unlock *and* apply a board would make it a message instead.
   */
  unlockShare: (id: string) =>
    request<void>(`/api/settings/shares/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body: JSON.stringify({ locked: false }),
    }),

  /**
   * What one link's screen is showing right now.
   *
   * The same body the dashboard itself receives, built by the same function on
   * the server. Not a second reduction written here: that would diverge on the
   * first field either side gained, in the direction "the preview shows
   * something the real screen does not".
   */
  sharePreview: (id: string) =>
    request<ShareDashboard>(`/api/settings/shares/${encodeURIComponent(id)}/preview`),

  /** The vocabulary a board is built from: presets, widget kinds, bounds. */
  shareCatalogue: () => request<ShareCatalogue>('/api/settings/shares/catalogue'),

  deleteShare: (id: string) =>
    request<void>(`/api/settings/shares/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  /**
   * The whole surface a share token can reach.
   *
   * The token is in the path because that is what makes the URL the
   * capability: one address you can put on a second screen, with nothing to
   * sign in to. Everything else about the panel answers 401 to it, which is
   * enforced by the server's routing rather than by this file.
   */
  shareDashboard: (token: string, viewer: string, width: number, height: number) =>
    request<ShareDashboard>(
      `/api/share/${encodeURIComponent(token)}/dashboard` +
        `?v=${encodeURIComponent(viewer)}&w=${width}&h=${height}`,
    ),

  browse: (path = '') =>
    request<DirListing>(`/api/browse?path=${encodeURIComponent(path)}`),

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
    opts: { title?: string; parentSessionId?: string; launchProfileId?: string } = {},
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
        parentSessionId: opts.parentSessionId ?? '',
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

  vncTargets: () => request<VncTarget[]>('/api/vnc/targets'),

  /**
   * `password` is write-only in both directions of this pair.
   *
   * Omitting it on a save leaves whatever is stored alone; sending `''`
   * clears it. That is why it is optional here rather than a string with a
   * default — a default of `''` would mean every rename wiped the password,
   * silently, because the field never comes back for anything to notice.
   */
  saveVncTarget: (
    id: string | null,
    body: { name: string; host: string; port: number; viewOnly: boolean; password?: string },
  ) =>
    request<VncTarget>(id === null ? '/api/vnc/targets' : `/api/vnc/targets/${id}`, {
      method: id === null ? 'POST' : 'PATCH',
      body: JSON.stringify(body),
    }),

  deleteVncTarget: (id: string) => request<void>(`/api/vnc/targets/${id}`, { method: 'DELETE' }),

  checkUpdate: () => request<UpdateCheck>('/api/update'),
  applyUpdate: () => request<UpdateResult>('/api/update', { method: 'POST' }),

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
  upload: async (projectId: string, path: string, files: File[]) => {
    const form = new FormData()
    for (const f of files) form.append('file', f, f.name)
    const res = await fetch(
      `/api/projects/${projectId}/upload?path=${encodeURIComponent(path)}`,
      { method: 'POST', body: form },
    )
    if (!res.ok) throw await failure(res)
    return (await res.json()) as { paths: string[] }
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

  files: (projectId: string, path = '') =>
    request<FileListing>(`/api/projects/${projectId}/files?path=${encodeURIComponent(path)}`),

  note: (projectId: string) => request<Note>(`/api/projects/${projectId}/notes`),

  /**
   * baseRev is the revision the caller's text was built on. The server
   * refuses the write if the note has moved since, which is what stops two
   * windows from silently overwriting each other.
   */
  // `keepalive` is for the save issued while the page is going away. A normal
  // fetch from an unloading document is cancelled by the browser, so the last
  // thing typed before closing the tab never reached the server.
  saveNote: async (projectId: string, content: string, baseRev: number, keepalive = false) => {
    const res = await fetch(`/api/projects/${projectId}/notes`, {
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
  // in internal/httpapi/panels.go: the wall boards count todos, and an agent
  // with an API token can still write one. What has no caller is this client,
  // and dead client code is how somebody concludes the feature is dead.
}
