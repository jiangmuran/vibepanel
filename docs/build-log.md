# Build log

Chronological record of what was built and what fought back. Newest last.

## 2026-08-23 — M1: skeleton, tmux wrapper, store

First milestone: the binary can create, list and kill sessions, and `tmux -L
vibepanel ls` shows them. No HTTP server yet.

Packages landed: `internal/version`, `internal/id`, `internal/config`,
`internal/session` (state enum only), `internal/tmux`, `internal/store`, and
the `cmd/vibepanel` CLI.

### tmux 3.6 segfaults on `window-size manual`

The design called for `window-size manual` plus explicit `resize-window`: one
authoritative grid size per session, set by the panel, immune to whatever size
a second viewer happens to have. That option kills the tmux server.

```
$ tmux -L probe -f <(echo 'setw -g window-size manual') new-session -d /bin/sleep 5
server exited unexpectedly
```

The server log ends inside `spawn_window()`:

```
spawn_window: [new-session/0x...], flags=0
spawn_window: s=$0 wl=none wp0=none idx=-1
spawn_window: name=none
<server dies>
```

Reproduced on tmux 3.6 with and without `-x/-y`, and with an explicit
`default-size`. Every other value of the option (`largest`, `smallest`,
`latest`) works.

**Resolution**: use `window-size latest`. It reaches the same place by a
different route — the panel attaches exactly one client per session, so
"whichever client was last active" is always the panel, and no second viewer
can shrink the grid because no second viewer is a tmux client at all.
`resize-window` was verified to still work under `latest` (80x24 → 140x45).

### tmux target matching is prefix-based

`-t vp_ab` also resolves `vp_abcd`. Left alone, a kill aimed at one session
would eventually land on another — silently, and only once two generated names
happened to share a prefix.

**Resolution**: all targets go through helpers that emit the exact-match form.
Session-scoped commands use `=name`, pane-scoped commands use `=name:`. The
distinction matters: `has-session -t '=name:'` on a missing session reports
"no current target" instead of "can't find session", because the `:` asks for a
window of a session that does not exist. `internal/id` also generates
fixed-length names so that even a forgotten `=` cannot alias.

### `pane_current_command` is eventually consistent

For roughly 200ms after `new-session` returns, the pane still reports `tmux` —
the fork has not exec'd the real command yet.

```
1st immediate: tmux    | after 200ms: sleep
2nd immediate: tmux    | after 200ms: sleep
3rd immediate: tmux    | after 200ms: sleep
```

**Resolution**: documented on `tmux.Info.Command`; the session manager polls
rather than caching the first reading. The naming logic in M3 must not take a
session's identity from a create-time snapshot.

### Session options that only apply at pane creation

`history-limit` is a session option consulted when a pane is created, so
setting it after `new-session` leaves the first pane on tmux's 2000-line
default — and the panel's cold-start replay silently loses most of the
scrollback.

**Resolution**: everything moved into `internal/tmux/vibepanel.conf`, loaded
via `-f` at server start. This also fixed a scope bug found the same way:
`set-clipboard`, `escape-time` and `focus-events` are *server* options, while
`window-size`, `allow-set-title`, `allow-passthrough`, `monitor-bell` and
`remain-on-exit` are *window* options. Six of the sixteen options were being
set at the wrong scope in the first draft.

### Verified

- Five tmux tests against a real tmux on a throwaway socket, including
  `remain-on-exit` preserving a crashed pane's output and `capture-pane -e`
  keeping SGR escapes (cold-start replay is worthless without colour).
- Store tests, including one asserting the SQL state ordering agrees with
  `session.State.SortWeight`.
- Isolation: the panel's socket shows zero foreign sessions; the pre-existing
  ttyd/zellij setup on this host was untouched throughout.

## 2026-08-23 — M2: terminals end to end

The browser can open a session, type into it, close the tab and come back to
find its scrollback intact, and watch the same session from two places at once.
A single static binary serves the whole thing.

Landed: `internal/session` (ring buffer, OSC scanner, attachment manager),
`internal/ws` (multiplexed protocol), `internal/httpapi` (REST, reconcile,
tmux poller), `internal/webui` (embedded frontend), and `web/` (React 19 +
Vite + Tailwind v4 + xterm.js 6).

### Size arbitration, and why the browser scales instead of reflowing

A desktop at 200×50 and a phone at 45×20 cannot both size the same tmux
session. Reflowing to the smaller one turns an agent's full-screen TUI into
confetti under the person actually using it.

The rule that shipped: the backend attaches exactly one tmux client per
session, so tmux itself never has to arbitrate. One viewer *controls* the grid
— whoever last typed — and everyone else renders at that grid and scales it
with a CSS transform. Typing claims control implicitly, which is what makes it
feel right on a phone: you start typing, and the session becomes phone-sized.
A passive viewer's resize is ignored rather than rejected, because a browser
window being resized is not an error.

### Bugs found by tests rather than by users

**`Done()` fired before the manager forgot the session.** The pump's cleanup
closed the channel and *then* removed the entry from the live map, leaving a
window where a caller woken by `Done()` still saw the session listed. Long
enough for a reconnect to attach to a corpse. Deregistration now happens first,
so the signal means what it says.

**The OSC terminator was being counted as an application bell.** Every OSC
sequence ends in BEL, and the shell sets its title on every prompt — so a naive
bell detector marks every session "waiting for you" several times a second.
The scanner consumes the terminator as part of the sequence, and there is a
test pinning it.

**Ring buffer: filling exactly to capacity was flagged as overflow.** That made
`Snapshot` trim a prefix of real output on a buffer that had lost nothing.

**Ring buffer: `trimPartialEscape` ate the first character of ordinary text.**
An empty parameter prefix counted as "all parameter bytes", so any line
starting with a letter looked like the tail of a CSI sequence. The trim now
requires at least one parameter byte, and the remaining ambiguity (digits then
a letter, which is genuinely indistinguishable from a truncated sequence) is
pinned as a documented false positive rather than pretended away.

### Frontend notes

Every version in the first `package.json` was wrong — guessed from memory
rather than looked up. xterm is on 6.0, not 5.6; lucide-react on 1.33, not
0.550; vite on 8.2. Worth remembering that this ecosystem moves faster than
recall.

The React 19 lint rules caught two real correctness problems: a ref written
during render (replaced with the `setSelected` updater form) and a state poll
that used `setInterval` (replaced with a self-scheduling loop, which also stops
requests piling up when the server is slow).

Polling `/api/state` every two seconds is a stopgap. Terminal output already
arrives over the WebSocket; the project and session lists should too, and that
loop should disappear in M3.

### Verified

- Full terminal round trip over a real WebSocket against a real tmux: subscribe,
  type `echo`, read the output back.
- Session survives the server being torn down and rebuilt.
- A slow viewer is dropped rather than stalling the pump that feeds everyone.
- `remain-on-exit`, replay after reconnect, and resize reaching tmux, all with
  `-race`.
- 13 MB static binary (`CGO_ENABLED=0`) serving the embedded frontend, with
  fingerprinted assets marked immutable and SPA fallback working.

## 2026-08-23 — what a real browser found

Added `web/scripts/render-check.mjs`: it boots the real binary against a
throwaway tmux socket, drives it with headless Chromium, and asserts on console
errors, failed requests, contrast ratios, theme behaviour, two-viewer sync and
replay. Every bug below came out of its first three runs. None of them were
visible to the Go tests, which had been passing throughout.

### Replay was typing into the shell

The ring buffer contains everything the application sent, terminal capability
queries included. A freshly created xterm answers those as it parses them — and
after a reload the answer goes to the shell, which types it at the prompt:

```
$ echo MARKER_ALPHA
MARKER_ALPHA
$ ^[[?1;2c^[[>0;276;0c
```

Every page reload was injecting junk into whatever the session was doing. If an
agent had been sitting on a confirmation prompt, a refresh would have answered
it with garbage.

Replay now travels as its own frame type (`FrameReplay`), and the client
suppresses everything the terminal wants to send while it parses it. Enumerating
the query sequences to strip would have been the fragile version of this fix;
suppressing responses catches the ones nobody thought of.

### Writing to a session was claiming the grid

`Live.Write` set the controller, on the theory that typing is a clear statement
of intent. It is not: xterm sends device-attribute and focus replies down the
same channel as keystrokes, so a viewer claimed the grid merely by loading the
page. The narrow second viewer in the harness took ownership on connect and then
reflowed the session on every window resize — 147 columns down to 13.

Fixing the immediate bug also settled a design question the plan had gotten
wrong. Even for genuine keystrokes, claiming the grid is not what the user
wants: glancing at a session on a phone and answering "y" to a prompt is the
mobile use case this panel exists for, and resizing the grid to 45 columns
underneath the desktop that is mid-edit is not part of that. Input and ownership
are now fully separate — passive viewers type freely, and taking the grid is an
explicit tap.

### Three more ownership bugs behind the first

- **Nobody owned a fresh session.** The first viewer was told it was passive,
  so it rendered at the stored grid and scaled it into a corner of a window it
  could have filled — while showing a "take control" button for a session
  nobody else was using.
- **A departing controller handed the grid to whoever was left.** Added as a
  fix, wrong on contact: refreshing your own page gave the grid to the phone
  across the room, which immediately reflowed everything and left the returning
  desktop stuck watching 13 columns. Now the grid freezes where it was and the
  returning viewer reclaims it on subscribe.
- **An unowned grid could be claimed by a resize.** Caught by the test written
  for the fix above. A phone rotating in a pocket would claim a session the
  desktop was about to come back to. Only the owner may resize; claiming is
  either arriving or tapping.

### The terminal palette lagged one theme change behind

React runs a child's effects before its parent's. `TerminalView` re-read the CSS
custom properties to rebuild the xterm palette before `App` had written
`data-theme`, so the largest surface on the page stayed white in dark mode. The
attribute is now written synchronously in the setter, before the state update.

### Contrast

Apple's own `secondaryLabel` (0.60 alpha) and `tertiaryLabel` (0.30) measure
3.44:1 and 1.73:1 against the frosted surfaces they sit on here. The second of
those is barely visible. Retuned to 5.2:1 and 3.0:1, and the labels that carry
information ("idle", the grid readout) moved off the tertiary token.

### Two smaller things

Shell sessions displayed their raw tmux id, because `deriveTitle` returned ""
for them — the fallback is now the directory the shell is in. And the selected
session was reset by every reload to whichever session had printed most
recently, so a session that prints constantly stole your place on each refresh.

### Harness bugs worth recording

Three of the first run's findings were the harness being wrong, which is worth
knowing about the next one: it compared a name against CSS-uppercased text, it
walked `<style>` contents and xterm's offscreen measurement nodes as if they
were visible text, and it forced the theme by setting the attribute directly —
which changes the CSS but not React's state, so it measured a combination no
user can produce. It also read a background tab, which Chromium throttles until
it stops painting.

## 2026-08-23 — M3 part one: pushed state, collapsible sidebar

Polling is gone. A hub tracks every connection and pushes the full project and
session list whenever anything changes, coalesced over a 60ms window so that
creating a session — which touches several rows — is one message rather than
four. The browser keeps a 30-second resync as a safety net for a socket that
dropped a message while a tab was asleep, not as the primary path.

A full snapshot rather than a delta: the list is small, and a delta protocol is
a second source of truth that drifts from the first in ways nobody notices
until the sidebar is showing a session that was killed ten minutes ago.

The sidebar now collapses to a 48px rail of project badges, each carrying the
most urgent state among its sessions. Below 768px it becomes an overlay drawer
instead of taking a column. Renaming is a double click, on both projects and
sessions. Pinning is a hover button.

### last_output_at meant the wrong thing

The poller stamped it with the current time on every tick for every live
session, so the column recorded "when we last looked" rather than "when this
last produced output". That broke activity ordering and would have made it
impossible to tell that a session had gone quiet — which M4's state detection
depends on entirely. Output timestamps now come from the PTY pump, which is the
only thing that knows, debounced to at most one write per session per second.

It also meant the state fingerprint changed on every tick, so pushing would
have degraded straight back into polling.

### Bugs found this round

**The phone opened on the project drawer, with the button that closes it
underneath.** One `sidebarOpen` flag was serving two different ideas: a
remembered desktop preference, and a per-visit mobile overlay. A returning user
with the sidebar open on their laptop got a drawer covering their whole phone
screen, and the menu button was behind it. Now separate: `docked` is
remembered, `drawerOpen` is per-visit and starts closed.

**The drawer was see-through.** It reused the frosted chrome token, so terminal
output showed through the project list. Anything floating over content is now
opaque, and `.vp-blur` falls back to a solid surface under
`@supports not (backdrop-filter)` and `prefers-reduced-transparency` — a design
must not put legibility behind a filter the browser might decline to run.

**Two React correctness errors, both caught by lint rather than by looking.**
`InlineName` synced a prop into state from an effect, which also meant a rename
arriving from another viewer would overwrite what the user was halfway through
typing. `useMediaQuery` read matchMedia through useState plus an effect, so the
first paint was against the wrong breakpoint; it uses `useSyncExternalStore`
now, which is what that primitive is for.

### An hour lost to an orphaned process

Several probe runs reported a bug that had already been fixed. The cause was a
server left listening from an earlier run that `timeout` had killed: a child
does not die because its parent was, and the probes used a hard-coded port, so
each new run bound nothing and quietly talked to the stale one — which served a
build from twenty minutes earlier.

The harness now asks the kernel for a free port and installs SIGINT/SIGTERM/
SIGHUP handlers that tear the server down. Worth remembering the shape of this:
every observation for that hour was real, and every conclusion drawn from them
was wrong.

The harness also grew `data-testid` hooks after its selectors broke on a
refactor that only moved elements around, and a `data-layout` attribute on the
root so a wrong layout mode is assertable rather than something to squint at.

## 2026-08-23 — M3 part two: project ordering

Re-read the original request before building this. It asks for two different
things that the plan had blurred into one:

> 每个窗口根据这三种状态自由排序 也可以手动把某一个置顶到项目的最前面
> 项目之间的排序……默认最活跃的放在最前面 也允许用户手动排序

Sessions sort themselves by state, with a manual pin to the top — already
built. Only *projects* get arbitrary manual ordering. The plan had specified
drag-reordering for sessions too, which nobody asked for and which would fight
the state ordering that is the point of the sidebar. Dropped.

Projects reorder by dragging a grip handle, built on Pointer Events rather than
HTML5 drag-and-drop: the HTML5 API does not fire on touch at all, so a drag
built on it simply does not exist on a phone. Reordering sends the whole list
in one request — a drag moves every project below it, and sending those
individually leaves the sidebar showing an order that never existed if one
fails. An id the server does not recognise fails the whole transaction rather
than writing the positions it does recognise, so a client working from a stale
list cannot quietly produce an order nobody chose.

An explicit order survives activity: that is what setting one means. A control
appears in the header to go back to automatic, and only when there is something
to go back from.

### A black bar under the terminal

Visible in a light-mode screenshot, invisible to every assertion.

`xterm.css` hard-codes `background-color: #000` on `.xterm-viewport`, with a
comment about macOS scrollbar opacity. The rows are transparent and sit on top,
so every pixel the rows do not cover shows through black — the sub-row
remainder at the bottom of the pane, and the scrollbar track.

Two things had to change. The override itself: our rule and xterm's use the
same selector at the same specificity, so it came down to source order, and
with `import '@xterm/xterm/css/xterm.css'` sitting inside `Terminal.tsx` xterm
always won. It is imported from `styles.css` now, above our rules, and no
`!important` is needed.

And the check that should have caught it: the theme assertion walked up from
`.xterm-screen` looking for the nearest painted ancestor. `.xterm-viewport` is
a *sibling* of `.xterm-screen`, so the walk skipped it entirely and found the
container's correct white. It reads the viewport directly now.

Worth noting that the harness reported a clean run while this was on screen.
Looking at the screenshots is not a formality.

## 2026-08-23 — M4: session state

Three states, decided by whichever of {the user said, the agent said, the
terminal did} happened last — recency rather than a fixed ranking of sources,
so a stale declaration cannot outrank what a session is visibly doing and a
fresh one is not undone by the output it predicted.

Hooks are the precise source and are optional throughout. `vibepanel hook`
installs a reporter script and prints the configuration to paste; the script
no-ops outside a panel session, so installing it globally leaves agents started
from an ordinary terminal alone. Without it the heuristic reads output activity
and the terminal bell, which can tell working from quiet but cannot tell
"finished" from "waiting for you".

### State only worked for the session you were looking at

Found by the render check: a session that rang the bell never showed as
waiting. The detector reads the PTY stream, and the panel attached only on
subscribe — so a session could ring, sit there wanting a human, and read as
"done" until you happened to click it. The one failure that makes the feature
pointless.

Polling tmux's flags instead of attaching turned out not to work:
`window_bell_flag` does latch with no client attached, but nothing clears it —
`select-window` only clears flags for a client that is actually viewing, and
`window_activity_flag` latches on the first byte and stays set forever. So the
panel now attaches every session and keeps a replay buffer for each. The cost
is one small tmux client and one buffer per session; the benefit beyond
correctness is that switching sessions is instant, because the buffer is
already warm.

Sessions attach at creation rather than at the next poll — an agent can ring
within a second of starting, and anything before the pump is running is simply
not seen. A bell that rang while the panel was down is recovered from
`window_bell_flag`, which is latched precisely because there was no client.

### bell-action "none" was silently eating every bell

The config said `set -g bell-action none`, with a comment claiming the bell had
to reach the PTY intact. It does the opposite: despite reading like "take no
action", it stops tmux forwarding the bell to its client. Captured the client
PTY under each value — `none` and `other` yield zero bells, `any` and `current`
yield one. Now `any`, because a session nobody is looking at is exactly the one
whose bell matters.

### tmux was re-initialising every session five seconds after attach

Every session's state reset to "working" at the same instant, once, a few
seconds in — wiping a bell that had rung a second earlier. Dumping the PTY
chunks showed why: on attach tmux asks the terminal a batch of questions —

```
\x1b[c  \x1b[>c  \x1b[>q  \x1b]10;?  \x1b]11;?  \x1b[?996n  \x1b[18t  \x1b[14t
```

— and waits. Nothing answered, so five seconds later tmux gave up, applied
defaults, and re-sent its whole initialisation to every client at once.

The panel answers all of them now, mirroring what xterm.js reports so that
tmux's model matches the thing actually rendering. Only the panel answers: two
replies would be worse than none, because the second is delivered to the pane
as though the user had typed it.

Two smaller guards came out of the same investigation. A chunk containing
nothing printable — mode sets, cursor moves — is the terminal being configured,
not the session producing output, and no longer counts as activity. And output
in the first 250ms after attaching is the repaint of content that was already
there, so it does not count either; without that, every session read as
"working" the moment the panel started and any manual state was cleared.

### An hour of guessing, ended by four lines of logging

The five-second mystery survived several rounds of plausible theories —
client churn, resize storms, status-line intervals — and fell immediately once
`VIBEPANEL_DEBUG_CHUNKS` printed what was actually arriving on each PTY. The
env-gated dump is still there. Worth reaching for it sooner next time.

## 2026-08-23 — M5 part one: bottom terminals

A strip of scratch terminals under the main session, following it: switch
sessions and the strip swaps with them. A new one starts in whatever directory
the session above is *currently* in, not the project root — opening a shell
next to an agent that has moved into a worktree and landing somewhere else is
the kind of small wrongness that makes a panel feel untrustworthy.

### They are sessions, not a second kind of thing

The schema had a `bottom_terminals` table. Building on it would have meant a
parallel implementation of everything sessions already have: attaching, replay,
state detection, naming, cleanup. A nullable `parent_session_id` on `sessions`
gets all of it for free, and "a terminal is a session" is one fewer concept to
hold.

That needed real migrations. The existing code applied `schema.sql` wholesale
whenever `user_version` was behind, which only ever worked on a fresh database
— an upgrade would have silently done nothing. Migrations are now a numbered
list, each in its own transaction with the version bump inside it, so a step
that fails leaves the database on the version it actually is.

Two tests pin the part that must never break: an existing v1 database upgrades
in place with its rows intact, and reopening does not re-run anything.

`schema.sql` also carried `PRAGMA journal_mode = WAL` and
`PRAGMA foreign_keys = ON`. Those are per-connection settings, already applied
through the DSN for every pooled connection — and journal_mode cannot be
changed from inside a transaction at all, which is exactly how the migration
test found them.

### A row of tabs all called "bash"

Main shell sessions are named after the directory they are in, which usefully
distinguishes one in a worktree from one at the repo root. Applied to scratch
terminals — which all live in the same directory as the session above them —
the same rule produced a strip of identical tabs. Caught by looking at the
screenshot, then pinned with an assertion that the tab labels are distinct.

The server now leaves a scratch terminal's title empty when it has no useful
automatic name, and the UI numbers those. The UI does not fall back to the
command name: it would have to know which commands are shells, and that
judgement already exists on the server.
