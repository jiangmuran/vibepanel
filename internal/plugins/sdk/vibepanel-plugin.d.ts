// vibepanel-plugin.d.ts — types for the plugin SDK, contract v1.
// docs/plugins.md §5. Pinned against the Go view by TestTheSDKTypesMatchThePluginView.

export type SessionState = 'working' | 'waiting' | 'done'

export type Status = 'connecting' | 'live' | 'reconnecting' | 'disconnected' | 'revoked'

/** A project as a plugin sees it. `path` is filled only with read:paths. */
export interface PluginViewProject {
  id: string
  name: string
  pinned: boolean
  path: string
  sessions: number
  waiting: number
}

/** A session as a plugin sees it. `cwd` and `command` are filled only with read:paths. */
export interface PluginViewSession {
  id: string
  projectId: string
  name: string
  state: SessionState
  stateSource: string
  stateChangedAt: number
  kind: string
  exited: boolean
  exitStatus: number
  pinned: boolean
  live: boolean
  restored: boolean
  cwd: string
  command: string
}

export interface PluginIdentity {
  id: string
  name: { en: string; 'zh-CN'?: string }
  version: string
}

/** What GET v1/view answers, and what every `view` event carries. */
export interface PluginView {
  v: number
  at: number
  plugin: PluginIdentity
  caps: string[]
  projects: PluginViewProject[]
  sessions: PluginViewSession[]
}

export interface Note {
  projectId: string
  content: string
  rev: number
  updatedAt: number
}

export interface Todo {
  id: string
  projectId: string
  text: string
  done: boolean
  createdAt: number
}

export interface Context {
  session: string | null
  project: string | null
  theme: string
  lang: 'zh' | 'en'
  slot: string
  narrow: boolean
}

export interface Client {
  readonly version: number
  readonly caps: string[]
  readonly plugin: PluginIdentity | null
  readonly status: Status
  readonly context: Context
  readonly view: PluginView | null
  on(event: 'view', fn: (v: PluginView) => void): Client
  on(event: 'context', fn: (c: Context) => void): Client
  on(event: 'status', fn: (s: Status) => void): Client
  off(event: string, fn: (...args: unknown[]) => void): Client
  close(): void
  refresh(): Promise<PluginView>
  request(method: string, path: string, body?: unknown): Promise<unknown>
  data: {
    get(): Promise<{ values: Record<string, unknown> }>
    set(key: string, value: unknown): Promise<{ value: unknown }>
    increment(key: string, by?: number): Promise<{ value: number }>
    append(key: string, item: Record<string, unknown>): Promise<{ value: unknown[] }>
    reset(key: string): Promise<void>
  }
  settings(): Promise<{ values: Record<string, unknown> }>
  notes: {
    get(project: string): Promise<Note>
    set(project: string, content: string, baseRev?: number): Promise<Note>
  }
  todos: {
    list(project: string): Promise<Todo[]>
    add(project: string, text: string): Promise<Todo>
    set(todo: string, patch: { text?: string; done?: boolean }): Promise<Todo>
    remove(todo: string): Promise<void>
  }
  sessions: {
    state(session: string, state: SessionState): Promise<PluginViewSession>
    screen(session: string): Promise<{ text: string }>
  }
  projects: {
    git(project: string): Promise<unknown>
  }
  resources(): Promise<unknown>
  usage(): Promise<unknown>
  route(method: string, path: string, body?: unknown): Promise<unknown>
  ui: {
    height(px: number): void
    open(what: { session?: string; project?: string; settings?: string }): Promise<boolean | null>
    notify(text: string, kind?: 'info' | 'success' | 'error'): Promise<boolean | null>
    toast(text: string, kind?: 'info' | 'success' | 'error'): Promise<boolean | null>
    confirm(q: { title: string; body?: string; confirm?: string; cancel?: string; destructive?: boolean }): Promise<boolean>
    menu(items: { id: string; label: string; destructive?: boolean }[]): Promise<string | null>
  }
  text(el: Element | null, value: unknown): Element | null
  honest(value: unknown): string
  since(unix: number): string
  badge(el: Element | null, state: string): Element | null
  fmt: {
    tokens(n: number): string
    bytes(n: number): string
    percent(n: number, digits?: number): string
    duration(seconds: number): string
    number(n: number): string
  }
}

export interface VibePanelPlugin {
  version: number
  plugin(options?: { grant?: string; base?: string; poll?: boolean }): Client
  fmt: Client['fmt']
  honest(value: unknown): string
}

declare global {
  interface Window {
    VibePanel: VibePanelPlugin
  }
}
