// Wire protocol, mirroring internal/ws/protocol.go.
//
// Kept by hand rather than generated because it is small and stable; the Go
// side has the same constant names so a mismatch is easy to spot in review.
// If this file and the Go one ever disagree, the Go one is right.

/** Session states. Mirrors internal/session/state.go, the source of truth. */
export type SessionState = 'working' | 'waiting' | 'done'

/** Ordered by urgency, matching State.SortWeight on the server. */
export const STATE_ORDER: SessionState[] = ['waiting', 'working', 'done']

export const FRAME_DATA = 0x00

/**
 * Scrollback sent when a viewer subscribes, tagged so the client can tell it
 * apart from live output.
 *
 * The buffer contains whatever the application sent, capability queries
 * included. A freshly created xterm answers those as it parses them, and the
 * answer goes to the shell, which types it at the prompt — so replaying
 * without suppressing responses injects junk like "[?1;2c" into the session on
 * every page reload.
 */
export const FRAME_REPLAY = 0x01

const BINARY_HEADER_LEN = 5

/** Builds a binary data frame: [type][uint32 ref][payload]. */
export function encodeData(ref: number, payload: Uint8Array): Uint8Array {
  const out = new Uint8Array(BINARY_HEADER_LEN + payload.length)
  out[0] = FRAME_DATA
  new DataView(out.buffer).setUint32(1, ref, false)
  out.set(payload, BINARY_HEADER_LEN)
  return out
}

/** Splits a binary frame. Returns null for anything malformed. */
export function decodeData(
  buf: ArrayBuffer,
): { ref: number; payload: Uint8Array; replay: boolean } | null {
  if (buf.byteLength < BINARY_HEADER_LEN) return null
  const view = new DataView(buf)
  const kind = view.getUint8(0)
  if (kind !== FRAME_DATA && kind !== FRAME_REPLAY) return null
  return {
    ref: view.getUint32(1, false),
    payload: new Uint8Array(buf, BINARY_HEADER_LEN),
    replay: kind === FRAME_REPLAY,
  }
}

export interface ClientMessage {
  t: 'subscribe' | 'unsubscribe' | 'resize' | 'takeControl' | 'ping' | 'paste'
  sessionId?: string
  cols?: number
  rows?: number
  /** MsgPaste only. Input otherwise travels as binary frames. */
  text?: string
  /** MsgPaste only: send a carriage return once the paste has landed. */
  submit?: boolean
}

export interface ServerMessage {
  t:
    | 'subscribed'
    | 'size'
    | 'clipboard'
    | 'title'
    | 'exit'
    | 'dropped'
    | 'error'
    | 'pong'
    | 'state'
    // A project's note or todo list changed in another viewer.
    | 'panel'
  sessionId?: string
  ref?: number
  cols?: number
  rows?: number
  text?: string
  message?: string
  controlling?: boolean
}

// ── REST shapes, mirroring internal/store ──────────────────────────────────

export interface Project {
  id: string
  name: string
  path: string
  sortIndex: number | null
  pinned: boolean
  lastActiveAt: number
  createdAt: number
}

/**
 * The one exit status that is not a wait status.
 *
 * Mirrors store.ExitStatusVanished. It marks a session whose tmux session
 * simply disappeared — killed from a shell, lost with the server, gone in a
 * reboot — where nothing was around to observe how it ended. Real wait
 * statuses are never negative, so the two cannot be confused.
 *
 * It exists here rather than in a component because three separate places put
 * it in front of a person, and the first version leaked the raw number into
 * all three: a badge reading "exit -1", a tooltip promising "the process
 * exited with status -1", and a project summary that counted it as a crash.
 * A status a person can read as a number has to be a status a process could
 * actually have returned.
 */
export const EXIT_VANISHED = -1

export interface Session {
  id: string
  projectId: string
  tmuxName: string
  title: string
  titleSource: 'auto' | 'manual'
  state: SessionState
  stateSource: 'heuristic' | 'hook' | 'manual'
  stateChangedAt: number
  pinned: boolean
  sortIndex: number | null
  cwd: string
  command: string
  cols: number
  rows: number
  createdAt: number
  archivedAt: number | null

  /**
   * The argv this session was created with — the only version of it that can be
   * run again.
   *
   * `command` above is whatever is in the pane right now, rewritten by the
   * poller every couple of seconds: "node" for an agent, "bash" for a shell
   * somebody used. It is a label. This is what a restore executes, and what the
   * restore dialog shows before anything is pressed.
   */
  launchCommand: string[]
  /**
   * False for a row written before the panel recorded commands.
   *
   * Not the same as an empty `launchCommand`, which is a session deliberately
   * created with no command — a login shell, exactly reproducible. This one
   * means nobody knows, and the UI has to say so rather than quietly offering
   * a shell under an agent's name.
   */
  launchRecorded: boolean
  /**
   * The launch profile this session was started with, empty for one started
   * without one.
   *
   * The id rather than a copy of the variables, so a restore rebuilds the
   * environment from whatever the profile says now. A profile deleted since
   * leaves this pointing at nothing, which the restore dialog says out loud
   * rather than implying the session still has it.
   */
  launchProfileId: string
  /** Rebuild this session at startup without asking, if its tmux session is gone. */
  restoreOnBoot: boolean
  /**
   * When this session was last rebuilt from its row, 0 if never.
   *
   * The process behind a restored session is new: the agent's context did not
   * survive the reboot and nothing can bring it back. The pane carries a banner
   * saying so, and banners scroll; this is what keeps the header able to say it.
   */
  restoredAt: number
  /**
   * When the archived scrollback was captured, 0 if there is none.
   *
   * What makes the restore dialog able to promise the right thing. A session
   * created since the last archive pass has nothing stored, and saying "your
   * scrollback comes back" about it would be a lie.
   */
  scrollbackAt: number

  /**
   * The pane's process is gone; tmux is showing its last screen.
   *
   * Orthogonal to `state`, which describes the task. A crashed agent and an
   * agent that finished are both "done" as far as the heuristic can tell, and
   * telling them apart is the whole reason this exists.
   */
  exited: boolean
  /** The wait status, meaningful only while `exited`. */
  exitStatus: number

  /**
   * Set for a scratch terminal opened under a main session.
   *
   * Bottom terminals are ordinary sessions with a parent rather than their own
   * kind of thing, so they arrive in the same list and get state, replay and
   * naming without a second implementation of each.
   */
  parentSessionId: string | null
}

export interface PanelState {
  projects: Project[]
  sessions: Session[]
  live: string[]
  /**
   * Sessions with a full-screen program drawing in them.
   *
   * The browser cannot work this out. tmux emulates the alternate screen per
   * pane and composes the result, and the panel keeps tmux's own client out of
   * the alternate screen so that scrollback exists at all — so a TUI's output
   * looks like any other output on the wire.
   */
  fullscreen: string[]
  /** 'auto' orders projects by recent activity; 'manual' by explicit position. */
  projectOrder: 'auto' | 'manual'
  /**
   * True when an arrangement is stored, whichever ordering is showing.
   *
   * Which is what makes the way back offerable at all: switching to automatic
   * keeps the positions now, so there is something to return to.
   */
  hasProjectOrder: boolean
  /**
   * True when an agent is running and nothing is reporting its state.
   *
   * The inference does not work for the agent most people run here: Claude
   * Code does not ring the terminal bell when it stops for a decision, and the
   * bell is the only signal the heuristic has. Saying so beats quietly
   * under-reporting the one state the panel exists for.
   */
  stateGuessed: boolean
  /**
   * Whether the reporter is installed, which decides which way out to offer
   * when the state is guessed rather than whether to say anything.
   */
  hooksInstalled: boolean

  /**
   * Why the panel has stopped keeping its records up to date, or '' when it
   * has not.
   *
   * A full disk is the case this exists for. The terminals keep working —
   * they belong to tmux — so nothing else on screen looks wrong while every
   * state change, every derived title and every note is being dropped.
   */
  stale: string
}

/**
 * A pushed state snapshot.
 *
 * The server sends the whole picture rather than a delta. The list is small,
 * and a delta protocol would be a second source of truth that drifts from the
 * first in ways nobody notices until the sidebar is showing a session that was
 * killed ten minutes ago.
 */
export interface StateMessage extends PanelState {
  t: 'state'
}

// ── side panels ────────────────────────────────────────────────────────────

/** What one session's process tree is costing. */
export interface SessionUsage {
  /**
   * A share of the whole machine, not top's convention where 100% is one core.
   *
   * The machine meter is an inch away on the same panel, and a session reading
   * 310% beside a machine reading 31% invites exactly one wrong conclusion.
   */
  cpuPercent: number
  /** Summed resident set across the tree; double-counts shared pages. */
  rss: number
  /** How many processes were found. 1 is a bare shell. */
  procs: number
}

export interface UsageSample {
  /** False where there is no /proc to walk, which is not "everything is idle". */
  readable: boolean
  cores: number
  /** Keyed by session id. A session whose pane has gone is absent, not zero. */
  sessions: Record<string, SessionUsage>
}

// ── token usage ────────────────────────────────────────────────────────────
//
// Read out of the agents' own transcripts, which is why almost every interface
// here carries a way of saying "unknown". A zero from this API means one of two
// things — nothing was spent, or nothing could be read — and the panel is not
// allowed to render them the same way.

/** Which agent a number came from. Mirrors internal/usage.Tools. */
export type UsageTool = 'claude' | 'codex'

export interface UsageTotals {
  /** Tokens sent fresh. Cache reads are counted separately, in both agents. */
  input: number
  output: number
  cacheRead: number
  cacheWrite: number
  /** API calls, after duplicates were removed. Zero requests is what tells a
   *  quiet day apart from a day whose transcripts said zero. */
  requests: number
}

// The rows below spell their token columns out rather than extending
// UsageTotals. TestTypeScriptRowsMatchWhatIsSent reads each interface's own
// properties and compares them with the Go struct's flattened json tags, so an
// `extends` would hide exactly the fields it exists to pin.

/** One day, or — in `byMonth` — one month, where `day` is `YYYY-MM`. */
export interface UsageDay {
  day: string
  input: number
  output: number
  cacheRead: number
  cacheWrite: number
  requests: number
}

/** One agent's transcript directory as the last pass found it. */
export interface TokenUsageSource {
  tool: string
  root: string
  /**
   * False means this agent contributed nothing *because nothing could be
   * read*. Render the reason, never a zero.
   */
  found: boolean
  problem: string
  files: number
  bytes: number
  /** Records the reader could not use. Non-zero makes every total a lower bound. */
  skipped: number
}

export interface TokenUsageTool {
  tool: string
  input: number
  output: number
  cacheRead: number
  cacheWrite: number
  requests: number
  files: number
  skipped: number
  problems: number
  problem: string
}

export interface TokenUsageModel {
  model: string
  input: number
  output: number
  cacheRead: number
  cacheWrite: number
  requests: number
}

export interface TokenUsageProject {
  /** Empty for the catch-all row: work done outside every known project. */
  id: string
  name: string
  path: string
  input: number
  output: number
  cacheRead: number
  cacheWrite: number
  requests: number
}

export interface TokenUsageSession {
  /** The *agent's* session id, from its own transcript. Not a vibepanel id. */
  session: string
  tool: string
  cwd: string
  models: string
  firstDay: string
  lastDay: string
  days: number
  input: number
  output: number
  cacheRead: number
  cacheWrite: number
  requests: number
  /** Empty when the directory belongs to no project the panel knows about. */
  projectId: string
  projectName: string
}

export interface TokenUsage {
  /** Zero until a pass has finished. Not the same as "nothing was spent". */
  scannedAt: number
  scanning: boolean
  passMs: number
  passError: string
  sources: TokenUsageSource[]

  /** The server's local date. The buckets are local days, so the browser must
   *  not decide for itself which square is today. */
  today: string
  from: string
  to: string
  days: number

  total: UsageTotals
  byDay: UsageDay[]
  /** Always the last 371 days, whatever the range control says. */
  heatmap: UsageDay[]
  /** Always every month there has been. */
  byMonth: UsageDay[]
  byTool: TokenUsageTool[]
  /** A row per model, biggest first. The axis that maps onto money: two tools
   *  is not a breakdown, and five models an order of magnitude apart is. */
  byModel: TokenUsageModel[]
  projects: TokenUsageProject[]
  sessions: TokenUsageSession[]
  sessionCount: number
  sessionLimit: number
}

export interface SystemSample {
  at: number
  /** Null on the very first sample: there is nothing to difference against. */
  cpuPercent: number | null
  /**
   * Whether the counters exist here at all.
   *
   * A null cpuPercent means "no sample yet" or "nothing to sample on this
   * machine", and only one of those is worth saying "sampling…" about.
   */
  cpuReadable: boolean
  cores: number
  load1: number
  load5: number
  load15: number
  memTotal: number
  memAvailable: number
  swapTotal: number
  swapFree: number
  diskTotal: number
  diskFree: number
  diskPath: string
  uptime: number
}

export interface FileEntry {
  name: string
  /**
   * The symlink points outside the project.
   *
   * Shown rather than hidden — pretending a file is not there is its own kind
   * of lie — but nothing is offered for it. The download resolves symlinks and
   * refuses anything that leaves the project, so a link to /etc/passwd in a
   * project used to render a download button that answered 403.
   */
  escapes: boolean
  /** Relative to the project root, forward slashes. */
  path: string
  isDir: boolean
  size: number
  modTime: number
  symlink: boolean
  readable: boolean
}

export interface FileListing {
  path: string
  parent: string | null
  entries: FileEntry[]
  /** How many items the directory holds, which is not entries.length once the
   * server's cap bites. */
  total: number
  truncated: boolean
}

/** A directory listing for the "where should this project live" picker. */
export interface DirListing {
  /** The absolute path the picker is rooted at, shown so "~" means something. */
  root: string
  path: string
  parent: string | null
  entries: FileEntry[]
  total: number
  truncated: boolean
}

export interface Note {
  projectId: string
  content: string
  updatedAt: number
  /**
   * Advances on every write. Sent back with a save so the server can refuse
   * one that would land on top of another window's — which `updatedAt` cannot
   * do, being in whole seconds.
   */
  rev: number
}

export interface Todo {
  id: string
  projectId: string
  text: string
  done: boolean
  sortIndex: number
  createdAt: number
  doneAt: number | null
}

export interface AuthState {
  configured: boolean
  authenticated: boolean
  username?: string
  passkeysUsable: boolean
  /** Why the passkey button is disabled; the browser's own error is opaque. */
  passkeyReason?: string
}

export interface Passkey {
  id: string
  name: string
  createdAt: number
  lastUsedAt: number | null
}

export interface SettingsInfo {
  version: string
  commit: string
  built: string
  go: string
  uptime: number
  tmuxVersion: string
  tmuxSocket: string
  /**
   * The running tmux server was started with a different config from the one
   * this binary carries — the half of an upgrade nothing else can see.
   */
  tmuxConfigStale: boolean
  /** The running server predates the stamp, so the question has no answer. */
  tmuxConfigUnknown: boolean
  sessions: number
  attached: number
  viewers: number
  dataDir: string
  dbBytes: number
  addr: string
  url: string
  tlsMode: string
  /** Unix seconds when the served certificate expires; absent if unknown. */
  certExpiry?: number
  domain: string
  allowAll: boolean
  passkeysUsable: boolean
  passkeyReason?: string
  username: string
  /** Whether the first-run tour has been put away. */
  tourDone: boolean
  /** Where a screenshot pasted into a terminal lands: "panel" | "session". */
  pasteDir: string
  /** What happens to its path: "type" | "buffer" | "both". */
  pasteThen: string
  /** What the panel calls a day. Empty means the machine's own zone. */
  timezone?: string
  timezoneOffset?: number
  panelDay?: string
}

/** Which agent an install request is about. The server accepts these two and
 *  refuses anything else; it is not a free-text field because it chooses a file
 *  in the user's home directory to edit. */
export type HookAgent = 'claude' | 'codex' | 'opencode'

export interface HookStatus {
  settingsPath: string
  scriptPath: string
  installed: boolean
  // Never null. The server sends [] for "nothing installed", which is every
  // fresh panel; this said `string[] | null` and the one reader guarded with
  // `?? []`, which is the symptom patched at the reader rather than the
  // contract fixed at the source.
  events: string[]
  snippet: string
  codexSnippet: string
  /** Where the Codex notify line goes: ~/.codex/config.toml. */
  codexPath: string
  /** Whether that file's `notify` is this panel's. Separate from `installed`,
   *  which is Claude's: the two agents are configured by different mechanisms
   *  and fail separately, so one flag would describe a machine where half of
   *  them are wired as if it were all of them. */
  codexInstalled: boolean
  /** The plugin file opencode auto-discovers. */
  opencodePath: string
  /** Whether the plugin in place is this build's, not merely that one exists. */
  opencodeInstalled: boolean
}

/** A credential a program uses instead of the session cookie. */
export interface ApiToken {
  id: string
  /** The first characters of the token, kept so a row can be named. The rest is
   *  only ever readable in the response that created it. */
  prefix: string
  name: string
  createdAt: number
  lastUsedAt: number
}

export interface AuditEntry {
  at: number
  event: string
  username: string
  ip: string
  detail: string
}

/** What `GET /api/update` answers. */
export interface UpdateCheck {
  current: string
  /** The newest release's tag, empty when the repository has none. */
  version?: string
  /** Whether that tag is ahead of what is running. */
  newer?: boolean
  url?: string
  notes?: string
  /** Empty when the release has no archive for this platform. */
  asset?: string
  /**
   * Why GitHub could not be reached. An air-gapped panel is a normal state,
   * not a broken one, so this arrives with 200 rather than as an error.
   */
  unreachable?: string
  /**
   * Set when this panel cannot replace its own binary — a system install owns
   * it as root — and carries the command that can. Present means the update
   * button must not be offered: it would download and then fail.
   */
  byHand?: string
}

/** What `POST /api/update` answers, before it restarts. */
export interface UpdateResult {
  installed: string
  previous: string
  restarting: boolean
  restartWhy: string
}

// ── read-only share links ──────────────────────────────────────────────────

/**
 * How much a share link is allowed to say. Mirrors store.ShareDetail.
 *
 * 'counts' is shapes and numbers and no text at all; 'names' adds session
 * titles and project names. Neither ever carries a path, a command line or a
 * real id — that is not a mode, it is the shape of the payload.
 */
export type ShareDetail = 'counts' | 'names'

/** A link as the settings page lists it. The token is never in here. */
export interface ShareLink {
  id: string
  /** The first characters, kept so a row can be named on the way to revoking
   *  it. The rest is only ever readable in the response that created it. */
  prefix: string
  name: string
  detail: string
  /** Unix seconds, or 0 for a link that does not expire. */
  expiresAt: number
  createdAt: number
  lastUsedAt: number
  /** '', 'project' or 'session'. The real id it points at is never sent. */
  scope: string
  /** What the scoped project or session is called, resolved on every listing
   *  rather than stored: it can be renamed, and it can be deleted. */
  scopeName: string
  /** What this link opens. Decoded and re-validated by the server on every
   *  read, so what arrives here is always a board this build's vocabulary
   *  covers. */
  board: ShareBoard
  /** The owner's own label for the screen: which room, which audience. Shown
   *  to viewers under both detail modes — it is the owner's sentence to them,
   *  not one of the panel's own words. */
  remark: string
  /** The board is fixed. The server refuses an edit to a locked link, so this
   *  is a guard and not a hint: it is what stops the wall a customer is
   *  looking at being rearranged from an editor left open on the wrong row. */
  locked: boolean
  /** How many screens had this open a moment ago, counted from their polls.
   *  Not stored: it is true for about two seconds and must be 0 again after a
   *  restart. */
  viewers: number
  /** The largest live viewer's screen, or 0 when nothing is looking. What the
   *  owner is composing for when they cannot see it. */
  viewportWidth: number
  viewportHeight: number
}

/**
 * The machine, with the path taken out.
 *
 * Not SystemSample: that carries `diskPath`, which is the panel's data
 * directory and so names a user account and a filesystem layout. The server
 * restates the fields deliberately so each one is a decision somebody made.
 */
export interface ShareMachine {
  cpuReadable: boolean
  cpuPercent: number | null
  cores: number
  load1: number
  load5: number
  load15: number
  memTotal: number
  memAvailable: number
  swapTotal: number
  swapFree: number
  diskTotal: number
  diskFree: number
  uptime: number
}

export interface ShareCounts {
  projects: number
  sessions: number
  waiting: number
  working: number
  done: number
  exited: number
  crashed: number
  /** How many sessions reached "done" since the server's local midnight. What
   *  came out today, as opposed to what is finished. */
  doneToday: number
  /** When the session that has waited longest entered that state, or 0.
   *  Sent as a count rather than derived from the rows, because a board that
   *  is one number carries no rows to derive it from. */
  longestWaitAt: number
}

export interface ShareProject {
  /** Pseudonymous and stable for the life of one link; not the panel's id. */
  id: string
  /** Empty under 'counts'. The dashboard numbers the groups instead. */
  name: string
  waiting: number
  working: number
  done: number
  total: number
}

export interface ShareSession {
  id: string
  projectId: string
  /** Empty under 'counts'. */
  name: string
  state: SessionState
  /** 'agent' | 'shell' | 'other' — a summary of the pane's foreground process,
   *  never the command line. Widened to string because the server owns the set
   *  and an unknown value has to render as something rather than crash. */
  kind: string
  stateChangedAt: number
  exited: boolean
  exitStatus: number
  /** A usage reading was found. A session whose pane has gone is absent from
   *  the sampler rather than zero, and zero is a real reading. */
  measured: boolean
  cpuPercent: number
  rss: number
  procs: number
}

/** Everything a share link discloses, in one object. Read it as the list. */
export interface ShareDashboard {
  /** When the server took this reading. The dashboard counts up from it, which
   *  is what stops a frozen page from looking like a quiet system. */
  at: number
  name: string
  /** The owner's label for this screen. Sent under both detail modes: `detail`
   *  governs whether the panel's own words may leave the machine, and this is
   *  the owner's sentence to whoever is standing in front of the screen. */
  remark: string
  /** The owner has fixed this board. Said on screen because the lock is about
   *  which wall is safe to rearrange, and the wall is where you find out. */
  locked: boolean
  detail: string
  /** Unix seconds, 0 when the link does not expire. */
  expiresAt: number
  usageReadable: boolean
  /** The panel has stopped keeping its records up to date. The reason is not
   *  sent: it is a message about this machine's storage, and a wall display can
   *  do nothing with it. */
  stale: boolean
  /** What this link opens. The page draws this and nothing else — there is no
   *  second copy of the layout here to drift from the stored one. */
  board: ShareBoard
  machine: ShareMachine
  counts: ShareCounts
  projects: ShareProject[]
  /** Empty unless a widget on the board shows rows. */
  sessions: ShareSession[]
  /** Null unless a widget on the board shows spend. Null and a zeroed object
   *  are different facts, and `readable` tells the second from "nothing has
   *  been counted yet". */
  spend: ShareSpend | null
  /** Null unless a widget on the board shows checklist progress. */
  todos: ShareTodos | null
  /** Null unless a widget on the board draws a moving line. Short after a
   *  restart or a screen that has just been switched on: the ring is filled by
   *  the polls that draw it. */
  trend: ShareTrend | null
  /** Null unless a widget on the board draws how the day went. Out of the
   *  session-event log, which is what made a trend possible at all: before it
   *  the panel stored one timestamp per session and nothing about what came
   *  before, so every time axis degraded to a single current number. */
  flow: ShareFlow | null
  /** Null unless a widget on the board lists what just happened. */
  feed: ShareFeed | null
  /** Null unless a widget on the board shows what was built. The only section
   *  read off a disk, and refreshed in the background rather than by the poll —
   *  a wall asking every two seconds must never be the thing that runs
   *  `git log`. `readable` tells "not counted yet" from "nothing today". */
  repo: ShareRepo | null
  /** '', 'project' or 'session': what this link is about. A scoped board
   *  showing nothing means "nothing in the thing you were sent", which is a
   *  different sentence from "nothing is running". */
  scope: string
  /** The scoped project's or session's name under 'names'; empty under
   *  'counts', and empty when the scoped row no longer exists. */
  scopeName: string
  /**
   * The scoped project's repository, as two parsed halves.
   *
   * Both empty unless the link is project-scoped, in 'names' mode, and points
   * at a github.com remote — see the disclosure note on the Go struct. The
   * page builds the URL from these with githubURL(); the raw remote and the
   * project's path are never sent, in any mode.
   */
  scopeRepoOwner: string
  scopeRepoName: string
}

// ── boards ─────────────────────────────────────────────────────────────────

/**
 * One thing on a board. Mirrors store.Widget.
 *
 * `kind` is widened to string rather than a union of the kinds this build
 * knows, and that is the whole client-side half of the safety story: a stored
 * board may name a widget from a newer server, and the renderer's switch has to
 * fall through to nothing rather than fail to compile or throw. Nothing here is
 * a URL, a path or a template — every option is an enum or a bounded number,
 * validated by the server on the way in and again on the way out.
 */
export interface ShareWidget {
  kind: string
  span: number
  metric?: string
  filter?: string
  order?: string
  /** What a session list is broken into: project, state, or nothing. */
  group?: string
  /** The dimension a chart is cut along — day/month for a series, agent,
   *  project or model for a breakdown. A setting rather than four widget
   *  kinds, so "split it by X" is one control. */
  by?: string
  days?: number
  /** Which page of a rotating board this widget is on, 0-based. */
  page?: number
  /** Seconds one page of a long list stays on screen, or absent for none. */
  rotate?: number
  /** A caption the owner typed. The only free text on a board, so the only
   *  thing here that goes through safeText. */
  text?: string
  /** How many grid rows tall, 1..catalogue.maxRows. Absent means one.
   *  The dimension that makes a hero a hero: a flat list of equal tiles is a
   *  dashboard, and a wall needs one thing four times the size of the rest. */
  height?: number
}

/** An arrangement. Mirrors store.Board. */
export interface ShareBoard {
  /** How many columns the spans are counted in. Twelve, for anything this
   *  build wrote; the server converts a board stored in the old quarters on
   *  the way through, so a board that arrives here is always in twelfths. */
  grid: number
  /** Which preset it started from, kept as provenance for the editor. Nothing
   *  renders from it. */
  preset: string
  /** Seconds each page stays on screen, or 0 for a board that does not move. */
  rotate: number
  /** Stretch the rows to the height of the screen instead of flowing down it.
   *  The difference between a board and a wall — nobody is going to scroll a
   *  television. */
  fill: boolean
  /**
   * How much each widget says: 1 spare, 3 dense. Not how large it is drawn.
   *
   * Scale and density are two axes and the whole point of this field is that
   * they are independent. How large everything is drawn follows the viewport
   * and is settled in CSS with no stored value (`.vp-wall` in styles.css); how
   * much is on screen is this. Somebody sitting in front of the same
   * television wants it denser, not smaller.
   */
  density: number
  widgets: ShareWidget[]
}

/** What one widget kind accepts. Mirrors store.WidgetSpec. */
export interface ShareWidgetSpec {
  kind: string
  span: number
  metrics: string[] | null
  filters: string[] | null
  orders: string[] | null
  groups: string[] | null
  bys: string[] | null
  days: boolean
  text: boolean
  /** This kind draws a list, so it can page through one that does not fit. */
  rotate: boolean
  /** How many grid rows tall this kind may be made. */
  rows: number
}

/** A starting arrangement offered by the settings page. Mirrors store.Preset. */
export interface SharePreset {
  id: string
  /** Who the board is for: the axis the catalogue is organised on. A label,
   *  nothing renders from it except the grouping in the editor. */
  audience: string
  /** What it was composed for: phone, laptop, wall, bigwall. The question
   *  somebody can always answer, unlike "which of twenty-four do I want". */
  screen: string
  rotate: number
  /** This arrangement was drawn to occupy a whole screen. */
  fill: boolean
  /** The disclosure mode this preset is only correct at, or '' when that is
   *  the owner's call. Applied by the editor; validated by the server on its
   *  own, from the request, exactly as before. */
  detail: string
  /** This arrangement is meaningless pointed at the whole panel. */
  needsScope: boolean
  /** How much each widget on it says. See ShareBoard.density — a wall preset
   *  and a sit-in-front-of-it preset can be for the same screen. */
  density: number
  widgets: ShareWidget[]
}

/**
 * The vocabulary a board is built from, served rather than mirrored.
 *
 * The editor offers exactly what the validator accepts because both read this.
 * A second copy of the table in this file is how a settings page comes to offer
 * a widget the server refuses.
 */
export interface ShareCatalogue {
  presets: SharePreset[]
  widgets: ShareWidgetSpec[]
  /** The screen sizes a preset can be composed for, in the order to offer. */
  screens: string[]
  /** The widths worth offering, in twelfths. The server accepts any span from
   *  1 to `maxSpan`; these are the ones a select should hold. Served rather
   *  than listed here, so a preset's widths and the editor's cannot drift. */
  steps: number[]
  maxWidgets: number
  maxSpan: number
  maxRows: number
  maxCaption: number
  maxRemark: number
  maxDays: number
  /** How many density steps there are. Served, so the editor's control and the
   *  validator's bound cannot drift. */
  maxDensity: number
}

/** How many transitions of each kind happened in a span. */
export interface ShareFlowTotals {
  started: number
  waited: number
  finished: number
  /** The dwell of every wait that *ended* in the span, and how many ended. Two
   *  numbers rather than an average, so an empty span is empty rather than a
   *  zero-second wait. */
  waitSeconds: number
  waitEnded: number
}

/** One interval of the flow series. Carries no id of any kind.
 *
 *  Written out flat rather than `extends ShareFlowTotals`, because the Go side
 *  embeds the totals struct and the wire test compares the *flattened* keys
 *  against one interface block. An `extends` here declares the same shape and
 *  is invisible to the thing that checks the two have not drifted. */
export interface ShareFlowBucket {
  at: number
  started: number
  waited: number
  finished: number
  waitSeconds: number
  waitEnded: number
}

/** The session-event log, bucketed. Transitions, never a census — see the Go
 *  side for why a flow log cannot honestly draw a queue depth. */
export interface ShareFlow {
  /** Bucket width in seconds, so the axis can be labelled rather than guessed
   *  from the gaps. */
  every: number
  since: number
  windowDays: number
  today: ShareFlowTotals
  window: ShareFlowTotals
  buckets: ShareFlowBucket[]
}

/** One thing that happened. Exactly the fields a session row already carries,
 *  in the order they happened — no new fact reaches the wire because a board
 *  asked for a feed. */
export interface ShareFeedEntry {
  at: number
  sessionId: string
  projectId: string
  /** Empty under 'counts', and also empty for a session that has since been
   *  deleted: the log outlives the row on purpose. */
  name: string
  from: SessionState
  to: SessionState
  forSeconds: number
}

export interface ShareFeed {
  entries: ShareFeedEntry[]
}

/** Production, counted. Added and removed are two numbers and never a net one:
 *  +1200/−800 is a different day from +400/−0. */
export interface ShareRepoTotals {
  commits: number
  added: number
  removed: number
  files: number
}

/** One local day of it, labelled '2026-08-27' on the server's clock.
 *
 *  Flat rather than `extends`, for the reason ShareFlowBucket gives. */
export interface ShareRepoDay {
  label: string
  commits: number
  added: number
  removed: number
  files: number
}

/** One project's production, renamed for this link. */
export interface ShareRepoProject {
  id: string
  name: string
  /** False for a project directory that is not a working tree. Sent rather
   *  than omitted so the widget can say so — it has to look deliberate. */
  repo: boolean
  today: ShareRepoTotals
  window: ShareRepoTotals
  /** Counts only. A branch *name* is a feature name and often a ticket or a
   *  customer, so it is refused at both detail settings. */
  ahead: number
  behind: number
  dirty: number
}

/** A repository's pull requests, as counts. No title, number, author, branch
 *  or URL: a count says how much is in flight, a title says what somebody is
 *  building for whom. */
export interface ShareRepoPRs {
  /** False until the first fetch has finished, or with no token in the
   *  panel's environment. Different from "nothing is open". */
  readable: boolean
  /** How old the answer is. On screen, because there is no poller behind it. */
  ageSeconds: number
  open: number
  draft: number
  green: number
  red: number
  pending: number
  approved: number
  changesRequested: number
  mergedToday: number
  /** The merge count is a floor: every recent merge examined was today. */
  mergedPartial: boolean
}

/** What the working trees say. No path, filename, branch name, commit subject,
 *  sha or author — `git log --shortstat` is what produces it, and that is the
 *  disclosure decision rather than a parsing convenience. */
export interface ShareRepo {
  readable: boolean
  ageSeconds: number
  repos: number
  projects: number
  windowDays: number
  today: ShareRepoTotals
  window: ShareRepoTotals
  days: ShareRepoDay[]
  byProject: ShareRepoProject[]
  prs: ShareRepoPRs | null
}

/** One reading of the machine and the running token total. */
export interface ShareTrendPoint {
  at: number
  /** Whole-machine percent, or null where /proc could not be read. Null rather
   *  than 0, because 0 is a real and very different reading. */
  cpu: number | null
  /** Fractions in use, 0..1. */
  memory: number
  load: number
  /** The running total for the server's local day, within this link's scope.
   *  A rate is drawn from the differences; the total is what survives a
   *  dropped sample. */
  tokens: number
}

/**
 * The last few minutes, for the widgets that draw a line rather than a number.
 *
 * A wall of still numbers cannot be told from a wall that has frozen, and a
 * line that moves is the cheapest honest proof the screen is alive.
 */
export interface ShareTrend {
  /** The sampling interval in seconds, so the axis means something without
   *  being guessed from the gaps. */
  every: number
  /** Oldest first. Short after a restart: the ring is in memory on purpose. */
  points: ShareTrendPoint[]
}

// ── token spend on a board ─────────────────────────────────────────────────

/** Tokens, never money: prices differ per model, per tier and over time. */
export interface ShareSpendTotals {
  input: number
  output: number
  cacheRead: number
  cacheWrite: number
  requests: number
  total: number
}

/** One labelled column: `label` is "2026-08-23" or "2026-08", never formatted
 *  text — the browser knows the reader's language and the server does not. */
export interface ShareSpendBucket {
  label: string
  total: number
  requests: number
  /** The four columns behind `total`, so a bar can be stacked. The same tokens
   *  the totals already disclose, cut the same way: a day that is nine tenths
   *  cache reads and one that is nine tenths output are the same number and
   *  different afternoons. */
  input: number
  output: number
  cacheRead: number
  cacheWrite: number
}

/** One bar of a by-tool or by-project breakdown. `id` is empty for the row that
 *  collects work done outside every project. */
export interface ShareSpendGroup {
  id: string
  name: string
  total: number
  requests: number
}

export interface ShareSpend {
  /** False until a pass over the transcripts has finished. Different from
   *  "nothing was spent", and a zero rendered for the first is the failure this
   *  flag exists to prevent. */
  readable: boolean
  scannedAt: number
  /** How far into the server's local day it is, so a rate can be "so far
   *  today". The browser does not know the server's timezone. */
  hoursToday: number
  /** The server's local day. The buckets are local days, so a phone in another
   *  timezone must not decide for itself which square is today. */
  date: string
  windowDays: number
  today: ShareSpendTotals
  /** What makes `today` and `month` mean anything: a total says what, a
   *  comparison says whether that is a lot. */
  yesterday: ShareSpendTotals
  month: ShareSpendTotals
  lastMonth: ShareSpendTotals
  window: ShareSpendTotals
  /** Every token this panel has recorded within this scope. The only figure
   *  here that only ever goes up, which is what an odometer needs. */
  allTime: ShareSpendTotals
  /** Empty unless a widget on the board asks for them. */
  days: ShareSpendBucket[]
  months: ShareSpendBucket[]
  heatmap: ShareSpendBucket[]
  tools: ShareSpendGroup[]
  models: ShareSpendGroup[]
  projects: ShareSpendGroup[]
}

/**
 * How much of each project's checklist is finished.
 *
 * Counts, and only counts. A todo line says what somebody is about to do about
 * a customer, a bug or a deadline; neither detail mode offers it.
 */
export interface ShareTodos {
  open: number
  done: number
  closedToday: number
  projects: ShareTodosProject[]
}

export interface ShareTodosProject {
  id: string
  name: string
  open: number
  done: number
  closedToday: number
}

/**
 * One variable a launch profile sets.
 *
 * `value` is always empty for a secret, whatever is stored — the server never
 * sends one back, so the settings page, a screenshot of it and an unlocked
 * phone disclose the name and nothing else. `hasValue` is the only thing this
 * side can learn about a secret, and sending a secret back with an empty
 * `value` means "keep the one that is already there".
 */
export interface LaunchEnvVar {
  name: string
  value: string
  secret: boolean
  hasValue: boolean
}

/**
 * A named way to start a session: an argv, and the environment to start it in.
 *
 * `builtin` profiles are Go constants rather than rows, so their names live in
 * the dictionary in both languages and a release can correct one. They cannot
 * be edited or removed; the settings page duplicates them into a row instead.
 * Their variables carry names and no values, which is what makes duplicating
 * one a form with the right variable names already spelled correctly — an
 * empty value is not passed to the process at all.
 */
export interface LaunchProfile {
  id: string
  name: string
  builtin: boolean
  command: string[]
  env: LaunchEnvVar[]
  createdAt: number
  updatedAt: number
}

/** One outbound notification destination. */
export interface Webhook {
  id: string
  name: string
  method: string
  url: string
  headers?: Record<string, string>
  body?: string
  /** Which transitions fire it. Empty means waiting only. */
  states?: string[]
  enabled: boolean
}

/** What a test send answers with. */
export interface WebhookTest {
  ok: boolean
  /** What the destination replied, bounded. */
  said: string
  error?: string
}

/** One path the working tree has something to say about. */
export interface GitChange {
  path: string
  /** staged, unstaged, untracked or conflict. */
  kind: string
  /** Where the file came from, empty when it did not move. */
  renamed: string
}

/**
 * One working tree, right now.
 *
 * `repo` false is an ordinary answer: plenty of project directories are not
 * repositories, and the tab says so in a line rather than showing an error.
 */
export interface GitStatus {
  repo: boolean
  branch: string
  detached: boolean
  head: string
  upstream: string
  ahead: number
  behind: number
  staged: number
  unstaged: number
  untracked: number
  conflicted: number
  changes: GitChange[]
  /** The list above is a prefix; the counts are still exact. */
  changesTruncated: boolean
}

export interface GitCommit {
  sha: string
  subject: string
  author: string
  /** Unix seconds. */
  when: number
}

/** Where origin points, once it has been recognised. */
export interface GitRemote {
  url: string
  host: string
  owner: string
  name: string
}

/**
 * A session sitting on a different commit than the project root.
 *
 * Only those are listed. Six agents in one directory are six identical rows;
 * six agents in six worktrees are the thing the tab exists for.
 */
export interface GitSession {
  sessionId: string
  branch: string
  detached: boolean
  head: string
  ahead: number
  behind: number
  staged: number
  unstaged: number
  untracked: number
  conflicted: number
}

/** Everything the local half of the git tab knows. Nothing here is a request
 *  to another machine. */
export interface GitInfo {
  status: GitStatus
  commits: GitCommit[]
  remote: GitRemote | null
  /** The remote is one the network half could query. */
  github: boolean
  /** The panel was started with a token, so the button can do something. */
  tokenSet: boolean
  sessions: GitSession[]
  sessionsTruncated: boolean
}

/** One open pull request, reduced to what a person watching agents needs. */
export interface GitPR {
  number: number
  title: string
  /** The head branch, which is what joins this to a session's branch. */
  branch: string
  base: string
  draft: boolean
  author: string
  url: string
  updatedAt: number
  /** approved, changes_requested, review_required, or empty. */
  review: string
  /** success, failure, pending, error, expected, or empty. */
  checks: string
}

/** One press of the button that asks GitHub. */
export interface GitHubResult {
  total: number
  prs: GitPR[]
  /** When this answer was fetched. There is no poller behind it, so the age
   *  is part of the answer. */
  checkedAt: number
}

/**
 * One setting the panel offers to change in Claude Code's own settings.json.
 *
 * `what` and `whatZh` arrive from the server rather than being looked up in
 * i18n.ts. They live beside the keys they describe in internal/hooks, for the
 * reason recorded there: a description that drifts from the key it names is a
 * summary reporting something other than what was written, and this list is
 * the whole of what somebody agrees to.
 */
export interface TuneRow {
  key: string
  what: string
  whatZh: string
  have: unknown
  want: unknown
  same: boolean
}

export interface TuneStatus {
  path: string
  exists: boolean
  changes: number
  rows: TuneRow[]
}

export interface RestartResult {
  ok: boolean
  supervisor?: string
  reason?: string
}

/** The service's environment file, as the settings page edits it. */
export interface EnvSettings {
  path: string
  /** The editable keys, in the order to draw them. From the server so the page
   *  and the PUT cannot disagree about what is accepted. */
  keys: string[]
  values: Record<string, string>
  /** What this process is running with, which is not the same as the file. */
  live: Record<string, string>
  /** Reported, never editable: red line 1. */
  socket: string
  /** Which write-only settings have a value. Never the value itself. */
  secrets?: string[]
  secretSet?: Record<string, boolean>
}
