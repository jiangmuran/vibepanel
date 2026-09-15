/*
 * vibepanel.d.ts — types for the share page SDK, contract v1.
 *
 * Every field the panel sends in a snapshot is declared here, and the panel's
 * own test suite fails if the two ever disagree. Use it from an editor with
 *
 *   /// <reference path="./vibepanel.d.ts" />
 *
 * or from a `// @ts-check` script. Names are '' — not missing — when the link's
 * detail is "counts"; `vp.name(row)` turns that into null.
 */

export type SessionState = 'working' | 'waiting' | 'done'

export type Status = 'connecting' | 'live' | 'reconnecting' | 'disconnected' | 'revoked'

export type Section = 'sessions' | 'todos' | 'spend' | 'trend' | 'flow' | 'feed' | 'repo'

export interface Snapshot {
  /** The contract version, 1. */
  v: number
  /** Which page and version this is. Never null from this panel today (a link that draws no page answers 410); typed nullable because the contract always allowed it. */
  page: SnapshotPage | null
  /** The sections this page asked for. Anything not listed is null or empty below. */
  sections: Section[]
  /** Every parameter the page declares, with this link's value or its default. */
  params: Record<string, string | number | boolean>

  /** When the panel took this reading, unix seconds, on the panel's clock. */
  at: number
  /** What the owner called the link. */
  name: string
  /** The owner's note for whoever is looking at this screen. */
  remark: string
  /** "counts" sends no names at all; "names" adds session and project names. */
  detail: 'counts' | 'names'
  /** Unix seconds the link stops working, or 0 for never. */
  expiresAt: number
  /** False where the panel cannot read per-session CPU and memory at all. */
  usageReadable: boolean
  /** True when the panel has stopped keeping its records up to date. Say so on screen. */
  stale: boolean

  machine: Machine
  counts: Counts
  /** Projects with at least one session, in the panel's order. */
  projects: Project[]
  /** Empty unless "sessions" is in sections. */
  sessions: Session[]
  spend: Spend | null
  todos: Todos | null
  trend: Trend | null
  flow: Flow | null
  feed: Feed | null
  repo: Repo | null

  /** '' for the whole panel, or "project" / "session" for a narrowed link. */
  scope: '' | 'project' | 'session'
  /** The scoped project's or session's name; '' under "counts". */
  scopeName: string
  /** The scoped project's GitHub owner and repository; '' unless names and a project scope. */
  scopeRepoOwner: string
  scopeRepoName: string
}

export interface SnapshotPage {
  /** This link's id for the page. Stable for the link, different on every other link. */
  id: string
  version: number
  /** True in the Preview pane, drawing the directory being edited. version is 0 then. */
  draft: boolean
}

export interface Machine {
  cpuReadable: boolean
  /** Null when there is no previous sample to measure against — not 0. */
  cpuPercent: number | null
  cores: number
  load1: number
  load5: number
  load15: number
  /** Bytes. */
  memTotal: number
  memAvailable: number
  swapTotal: number
  swapFree: number
  diskTotal: number
  diskFree: number
  /** Seconds. */
  uptime: number
}

export interface Counts {
  projects: number
  sessions: number
  waiting: number
  working: number
  done: number
  exited: number
  crashed: number
  /** Sessions that reached done since the start of the panel's local day. */
  doneToday: number
  /** When the longest-waiting session started waiting, unix seconds, or 0. */
  longestWaitAt: number
}

export interface Project {
  id: string
  /** '' under "counts". */
  name: string
  waiting: number
  working: number
  done: number
  total: number
}

export interface Session {
  id: string
  projectId: string
  /** '' under "counts". */
  name: string
  state: SessionState
  /** "agent", "shell" or "other" — never the command line. */
  kind: 'agent' | 'shell' | 'other'
  /** When it entered its current state, unix seconds. */
  stateChangedAt: number
  exited: boolean
  exitStatus: number
  /** False when no usage reading was found; the three below are then not zero, they are unknown. */
  measured: boolean
  cpuPercent: number
  rss: number
  procs: number
}

export interface SpendTotals {
  input: number
  output: number
  cacheRead: number
  cacheWrite: number
  requests: number
  total: number
}

export interface SpendBucket {
  /** "2026-08-23" for a day, "2026-08" for a month, on the panel's clock. */
  label: string
  total: number
  requests: number
  input: number
  output: number
  cacheRead: number
  cacheWrite: number
}

export interface SpendGroup {
  /** '' for the catch-all "outside every project" row. */
  id: string
  name: string
  total: number
  requests: number
}

export interface Spend {
  /** False until the first pass over the transcripts finishes. Not the same as nothing spent. */
  readable: boolean
  scannedAt: number
  /** The panel's local day, "2026-08-23". */
  date: string
  hoursToday: number
  windowDays: number
  today: SpendTotals
  yesterday: SpendTotals
  month: SpendTotals
  lastMonth: SpendTotals
  window: SpendTotals
  allTime: SpendTotals
  /** Empty unless the manifest's spend.days is set. */
  days: SpendBucket[]
  /** Empty unless spend.months. */
  months: SpendBucket[]
  /** Empty unless spend.heatmap. */
  heatmap: SpendBucket[]
  /** Each empty unless named in spend.split. */
  tools: SpendGroup[]
  models: SpendGroup[]
  projects: SpendGroup[]
}

export interface Todos {
  open: number
  done: number
  closedToday: number
  projects: TodosProject[]
}

export interface TodosProject {
  id: string
  name: string
  open: number
  done: number
  closedToday: number
}

export interface Trend {
  /** Seconds between points. */
  every: number
  points: TrendPoint[]
}

export interface TrendPoint {
  at: number
  cpu: number | null
  memory: number
  load: number
  /** Running total of today's tokens; differences are the rate. */
  tokens: number
}

export interface FlowTotals {
  started: number
  waited: number
  finished: number
  waitSeconds: number
  waitEnded: number
}

export interface FlowBucket {
  at: number
  started: number
  waited: number
  finished: number
  waitSeconds: number
  waitEnded: number
}

export interface Flow {
  /** Bucket width in seconds. */
  every: number
  since: number
  windowDays: number
  today: FlowTotals
  window: FlowTotals
  buckets: FlowBucket[]
}

export interface FeedEntry {
  at: number
  sessionId: string
  projectId: string
  name: string
  from: SessionState
  to: SessionState
  forSeconds: number
}

export interface Feed {
  entries: FeedEntry[]
}

export interface RepoTotals {
  commits: number
  added: number
  removed: number
  files: number
}

export interface RepoDay {
  label: string
  commits: number
  added: number
  removed: number
  files: number
}

export interface RepoProject {
  id: string
  name: string
  /** False for a project directory that is not a git working tree. */
  repo: boolean
  today: RepoTotals
  window: RepoTotals
  ahead: number
  behind: number
  dirty: number
}

export interface RepoPRs {
  /** False until the first fetch finishes, or with no GitHub token. Not the same as none open. */
  readable: boolean
  ageSeconds: number
  open: number
  draft: number
  green: number
  red: number
  pending: number
  approved: number
  changesRequested: number
  mergedToday: number
  /** The merge count is a floor. */
  mergedPartial: boolean
}

export interface Repo {
  /** False until the first background read finishes. "Not counted yet" is not zero. */
  readable: boolean
  /** How old the reading is in seconds, or -1. */
  ageSeconds: number
  repos: number
  projects: number
  windowDays: number
  today: RepoTotals
  window: RepoTotals
  /** Empty unless the manifest's repo.days is set. */
  days: RepoDay[]
  byProject: RepoProject[]
  /** Null unless repo.prs. */
  prs: RepoPRs | null
}

export interface Formatters {
  /** 480, 9.4K, 41M. One decimal below ten, none above. */
  tokens(n: number): string
  /** 512 B, 3.4 GiB. */
  bytes(n: number): string
  /** 42%. Null and NaN are "—", never 0%. */
  percent(n: number | null, digits?: number): string
  /** 45s, 14m, 3h 5m, 2d 4h. */
  duration(seconds: number): string
  number(n: number): string
}

export interface ConnectOptions {
  /** The panel's address, for a page hosted somewhere else. Defaults to this page's. */
  base?: string
  /** The share token, for a page hosted somewhere else. Defaults to the one in the address. */
  token?: string
  /** Load fixtures/<name>.json instead of polling. Also read from ?fixture= in the address. */
  fixture?: string
  /** Use this snapshot instead of polling, for tests. */
  snapshot?: Snapshot
  status?: Status
}

export interface Client {
  readonly version: number
  /** The latest snapshot, or null before the first. */
  readonly snapshot: Snapshot | null
  readonly status: Status
  readonly params: Snapshot['params']
  /** The last error the panel gave, if any. */
  readonly error: string | null
  /** Storage for as long as the page is open. localStorage throws in a page. */
  readonly storage: Storage
  readonly fmt: Formatters

  /** Returns a function that removes the listener. A late listener hears the current value at once. */
  on(event: 'snapshot', fn: (snapshot: Snapshot, vp: Client) => void): () => void
  on(event: 'status', fn: (status: Status, vp: Client) => void): () => void
  on(event: 'params', fn: (params: Snapshot['params'], vp: Client) => void): () => void
  on(event: 'error', fn: (message: string, vp: Client) => void): () => void

  /** The panel's clock, unix seconds, corrected for this screen's. */
  now(): number
  /** How long ago a unix time was: "14m". */
  since(unix: number): string
  /** Set textContent; null, undefined and '' become the placeholder ("—"). */
  text<E extends Element>(el: E | null, value: unknown, placeholder?: string): E | null
  /** A row's name, or null (or the fallback) when the link sends no names. */
  name(row: { name: string } | null | undefined): string | null
  name(row: { name: string } | null | undefined, fallback: string): string
  /** Draw the connection state — shape and word — into an element. Returns a function that stops it. */
  badge(el: Element | null): () => void
  /** Stop polling. */
  stop(): void
}

export interface VibePanelStatic {
  readonly version: number
  connect(options?: ConnectOptions): Client
  readonly fmt: Formatters
}

declare global {
  const VibePanel: VibePanelStatic
}
