import type {
  AuditEntry,
  AuthState,
  FileListing,
  HookStatus,
  SettingsInfo,
  Note,
  PanelState,
  Passkey,
  Project,
  Session,
  SessionState,
  SystemSample,
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

  installHooks: () => request<HookStatus>('/api/settings/hooks', { method: 'POST' }),

  removeHooks: () => request<HookStatus>('/api/settings/hooks', { method: 'DELETE' }),

  state: () => request<PanelState>('/api/state'),

  health: () =>
    request<{ ok: boolean; version: string; tmuxVersion: string; live: number; passkeys: boolean }>(
      '/api/health',
    ),

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

  /** Discards manual positions and returns to most-active-first ordering. */
  autoOrderProjects: () =>
    request<void>('/api/projects/reorder', { method: 'POST', body: JSON.stringify({ auto: true }) }),

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

  files: (projectId: string, path = '') =>
    request<FileListing>(`/api/projects/${projectId}/files?path=${encodeURIComponent(path)}`),

  note: (projectId: string) => request<Note>(`/api/projects/${projectId}/notes`),

  saveNote: (projectId: string, content: string) =>
    request<Note>(`/api/projects/${projectId}/notes`, {
      method: 'PUT',
      body: JSON.stringify({ content }),
    }),

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
