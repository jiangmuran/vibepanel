import type {
  ApiToken,
  AuditEntry,
  DirListing,
  AuthState,
  FileListing,
  HookAgent,
  HookStatus,
  SettingsInfo,
  Note,
  PanelState,
  Passkey,
  Project,
  Session,
  SessionState,
  SystemSample,
  UsageSample,
  Todo,
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
  | { kind: 'text'; text: string; truncated: boolean }
  | { kind: 'image'; blob: Blob }
  | { kind: 'pdf'; blob: Blob }
  | { kind: 'tooBig' }
  | { kind: 'none' }

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
    opts: { title?: string; parentSessionId?: string } = {},
  ) =>
    request<Session>('/api/sessions', {
      method: 'POST',
      body: JSON.stringify({
        projectId,
        command,
        title: opts.title ?? '',
        parentSessionId: opts.parentSessionId ?? '',
      }),
    }),

  patchSession: (
    id: string,
    patch: Partial<{ title: string; pinned: boolean; state: SessionState }>,
  ) => request<Session>(`/api/sessions/${id}`, { method: 'PATCH', body: JSON.stringify(patch) }),

  deleteSession: (id: string) => request<void>(`/api/sessions/${id}`, { method: 'DELETE' }),

  restartSession: (id: string) =>
    request<void>(`/api/sessions/${id}/restart`, { method: 'POST' }),

  system: () => request<SystemSample>('/api/system'),

  usage: () => request<UsageSample>('/api/usage'),

  /**
   * A URL rather than a request: downloading is the browser's job, and it does
   * it better than any fetch-into-a-blob would — progress, resume, and the
   * save dialog, without holding the file in memory.
   */
  downloadURL: (projectId: string, path: string) =>
    `/api/projects/${projectId}/download?path=${encodeURIComponent(path)}`,

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

  todos: (projectId: string) => request<Todo[]>(`/api/projects/${projectId}/todos`),

  addTodo: (projectId: string, text: string) =>
    request<Todo>(`/api/projects/${projectId}/todos`, {
      method: 'POST',
      body: JSON.stringify({ text }),
    }),

  patchTodo: (id: string, patch: Partial<{ text: string; done: boolean }>) =>
    request<Todo>(`/api/todos/${id}`, { method: 'PATCH', body: JSON.stringify(patch) }),

  deleteTodo: (id: string) => request<void>(`/api/todos/${id}`, { method: 'DELETE' }),
}
