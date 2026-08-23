import type { PanelState, Project, Session, SessionState } from './wire'

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
    try {
      const body = (await res.json()) as { error?: string }
      if (body.error) message = body.error
    } catch {
      /* non-JSON error body */
    }
    throw new Error(message)
  }
  if (res.status === 204) return undefined as T
  return (await res.json()) as T
}

export const api = {
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

  createSession: (projectId: string, command: string[], title = '') =>
    request<Session>('/api/sessions', {
      method: 'POST',
      body: JSON.stringify({ projectId, command, title }),
    }),

  patchSession: (
    id: string,
    patch: Partial<{ title: string; pinned: boolean; state: SessionState }>,
  ) => request<Session>(`/api/sessions/${id}`, { method: 'PATCH', body: JSON.stringify(patch) }),

  deleteSession: (id: string) => request<void>(`/api/sessions/${id}`, { method: 'DELETE' }),
}
