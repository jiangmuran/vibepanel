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

## 2026-08-23 — M5 part two: the side panel

Four tabs in a collapsible, resizable column: files, system, notes, todo.
Notes and todo can also sit one above the other, split by a draggable line —
they are the pair worth seeing together, what you are thinking and what you
have left. Files and the monitor are lookups, not companions.

Everything is per project and everything persists: width, which tab, whether
the split is on.

New packages: `internal/sysmon` reads /proc directly rather than taking a
dependency for four numbers, and `internal/browse` lists directories under a
root and refuses to leave it.

### Containment, tested as its own thing

`browse` is a separate package because refusing to leave the project is the
whole point of it and deserves testing on its own — every path it sees arrives
from a URL. Both the root and the request are resolved through `EvalSymlinks`
before comparison, so a symlink inside the project pointing at `/etc` is
refused; a textual prefix check follows it happily, and that is the classic way
this goes wrong. There is a test for exactly that case.

### Numbers that would be wrong if taken from the obvious place

The monitor reports `MemAvailable`, not `MemFree`: free memory on a healthy
Linux box is near zero because the rest is cache, and showing that as "almost
out of memory" is alarming and false. Disk uses `Bavail` rather than `Bfree`,
which includes root's reserved blocks. CPU excludes iowait from busy time —
a machine doing nothing but reading files is not pegged. And the first sample
reports no CPU figure at all rather than a zero, because there is nothing yet
to difference against.

### Bugs found this round

**The collapse button fell off the edge of the panel.** Labelling all four tabs
looked better and overflowed a 280px column, pushing the last control out of
sight. Only the selected tab is labelled now — which answers the more useful
question anyway — and the check asserts every header control stays inside the
header on every tab.

**Three components reset state from an effect**, which React's lint rules
caught. In each case the fix was the same and better: the caller keys the
component by project, so switching projects is a fresh instance rather than a
reset, and the fetch uses the documented shape where the state update happens
in a callback guarded by an ignore flag. Without that, a response arriving
after the directory changed overwrites the newer one.

**`schema.sql` shipped PRAGMAs that cannot run where it runs** — found last
round by the migration test, fixed here alongside the notes and todo tables.

## 2026-08-23 — M6 part one: authentication

Everything except the health probe and the agent-hook endpoint now needs a
session, the WebSocket included — it is the terminal itself, so leaving it open
would have made the rest decoration.

First run prints a one-time setup token to the console. That is the handover:
whoever can read the server's output is entitled to claim the panel, and merely
reaching it over the network is not. The endpoint closes permanently once an
account exists.

argon2id at 64 MiB and two passes, with the parameters written into the encoded
hash so raising them later does not lock anyone out. Sessions are rows rather
than signed cookies, so they can be revoked individually. Failed sign-ins back
off exponentially per source address — delay rather than lockout, because
locking an account means anyone who knows the username can deny its owner
access. `X-Forwarded-For` is believed only from proxies the operator listed;
trusting it by default would let an attacker invent a new address on every
attempt and skip the throttle entirely.

An unknown username and a wrong password produce the same response and the same
work: a fast "no such user" and a slow "wrong password" tells an attacker which
usernames exist, which is the first half of guessing one.

### Merely looking at a session cleared the state that said it wanted you

The trace made it obvious once the check recorded state at each step:

```
seeded=waiting -> signed-in=working -> typed=done -> ...
```

Signing in auto-selects the first session, and the first session is the waiting
one, because waiting sorts first. Subscribing resizes its grid, tmux repaints
the pane, and the repaint counted as output — which cleared the bell.

Third instance of the same underlying mistake, after the attach repaint and
tmux's re-initialisation: a redraw is the terminal being configured, not the
session doing something. The settle window now covers resizes as well as
attaches, and the constant is named for what it means rather than for one of
its causes.

### A successful request reported as a network failure

The check flagged `POST /api/projects/reorder — net::ERR_ABORTED`, on a request
that had already received its 204 and whose effect had visibly worked. Logging
the request lifecycle showed a response arriving and `requestfinished` never
following.

A `Response` whose body is never read is reported by Chromium as aborted, even
when there is no body to read. The client drains empty responses now. The check
also distinguishes an abort that follows a response from a real network
failure, because the two mean different things.

### Still to do in M6

Passkey registration and sign-in, and TLS: certificate files with hot reload,
and ACME over DNS-01. The panel reports honestly in the meantime — the sign-in
screen says *why* passkeys are unavailable rather than showing a dead button,
which is the part that would otherwise be baffling.

## 2026-08-23 — M6 part two: passkeys and TLS

Passkeys register and sign in, passwordless. Discoverable credentials, so
nothing is typed and no username is disclosed before the key proves itself. The
challenge stays on the server and the browser carries only an opaque id for it,
single use. An authenticator whose sign count goes backwards is refused — that
counter exists to detect a cloned credential, and honouring it is the only
reason it is there.

TLS two ways. Certificate files, reloaded when they change so an external
renewal needs no restart — and a reload that fails keeps serving the previous
pair, because a renewal that writes the certificate and the key a moment apart
would otherwise take the panel down for the length of that gap. Or ACME over
DNS-01 with Cloudflare, resolved before the listener opens: a panel that binds
first and discovers it has no certificate second greets its first visitor with
a handshake error and nothing to explain it.

### Tested with a real browser and a virtual authenticator

WebAuthn is not something to take on faith from unit tests. The render check
adds a CDP virtual authenticator, registers a passkey through the dialog, signs
out, and signs back in with no password. The server runs with
`--domain localhost` for that, which is a valid Relying Party ID and a secure
context even over plain HTTP.

### Error messages aimed at the failure people will actually hit

The most likely ACME problem by far is a missing `CLOUDFLARE_API_TOKEN`, so
that is its own error naming the variable rather than a provider failure
surfacing from three layers down. Asking for ACME without a DNS provider says
plainly that HTTP-01 cannot work on a non-standard port, instead of failing
after a minute of retries. And the sign-in screen explains *why* passkeys are
unavailable rather than showing a button that does nothing.

## 2026-08-23 — M7: the phone

Not a squeezed desktop. Below 768px the terminal becomes a display and input
arrives from two things beside it.

A compose box, because typing straight into a terminal is unusable on a phone
with an input method: every composition keystroke reaches the shell, so
Chinese, Japanese and Korean input produce garbage and even autocorrect fights
the line editor. You compose, then send. A toggle decides whether Enter goes
with it, for the times you want to type at a prompt rather than run something.

A key bar for what a phone cannot produce: Escape, Tab, Ctrl, Alt, arrows,
Home, End — and `y`, `n`, `1`, `2`, `3`, which are most of what anybody sends
an agent from a phone. Ctrl and Alt are sticky: tap, then tap what they apply
to, because holding two places at once is not a gesture a thumb can make.

xterm no longer takes input at this width, so tapping the terminal does not
raise the software keyboard over the thing being read. Selection is the
browser's own long-press — the gesture people already know, and far better than
anything hand-rolled over a grid of spans — with a copy button that appears
while something is selected, because taking the selection is the part a page
like this does not otherwise offer.

### The keys that matter were off the screen

The first version was one scrolling row of eighteen keys. On a 390px screen
that shows about eight, and after any horizontal scroll the ones out of view
were `y`, `n` and Escape — the exact set the bar exists for.

Two rows now. The first holds the answers and the modifiers and never scrolls;
the second may, because losing sight of `~` costs far less than losing sight of
Escape. The check measures every primary key against the row's bounds.

Found by looking at the screenshot. The assertions were all passing.

### A badge on the menu button

On a phone the session list is behind a menu, so nothing on screen could say
that something wanted a human. The waiting count sits on the button that opens
the list. The check marks a session waiting through the API and waits for the
badge, rather than depending on one an earlier step happened to leave behind.

## 2026-08-23 — M8: settings, and packaging

A settings page: what the panel is running, whether agents report their own
state, passkeys, and a log of sign-ins and configuration changes. Read-only for
anything that lives in a flag — the panel is started by a unit file or a
compose file, and a setting changeable in two places is one that disagrees with
itself after the next restart.

Installing the state-reporting hooks edits `~/.claude/settings.json`. That file
is the user's, it usually has other things in it, and one of those may be their
own hook on the same event. So: the existing contents are merged rather than
replaced, every entry the panel adds is tagged so removing them cannot take
anybody else's with it, the file is copied beside itself first, and the write
goes to a temporary file and is renamed — a crash half way through must not
leave someone with a truncated settings file and an agent that will not start.
What it will write is shown before you agree to it, not after.

Packaging: a systemd **user** service, a release script producing static
archives for linux/amd64, linux/arm64 and darwin/arm64, a Makefile, and a
Dockerfile offered second with a note about why.

### The unit deliberately does not sandbox the filesystem

The obvious hardening — `ProtectSystem=strict`, `ProtectHome`, a narrow
`ReadWritePaths` — is wrong for this. The panel's job is to run coding agents
as the user, and those agents write to their repositories, their caches and
their home directory. Locking that down does not make the panel safer; it makes
it useless, and the first thing anyone would do is delete the lines. What stays
is the hardening that does not fight the job, plus the memory accounting and
`OOMPolicy=continue` so one runaway session cannot take the panel and every
other session down with it.

### The check edited the real ~/.claude/settings.json

The first run of the settings check installed the hooks — into the actual file
on this machine, because the server it spawns inherits the environment and
therefore `HOME`.

Nothing was lost: the merge worked, every key survived, and removing the hooks
left the file correct. But it was reformatted, and the original bytes are gone,
because install and remove both wrote a backup within the same second and the
timestamp only had second resolution, so the second overwrote the first.

Two fixes. Backups are stamped to the millisecond and never overwrite an
existing file. And the check gives its server a throwaway `HOME`, with an
assertion that the settings path it reports is inside it — a test that reaches
outside its own directories is not a test, it is an incident waiting for the
right moment.

### Verified

The release archive was unpacked and run under `env -i` with nothing but
`PATH`, `HOME` and `TERM`: version information baked in, frontend embedded,
setup token printed. That is the whole deployment story, checked rather than
assumed.

## 2026-08-23 — fidelity under load

A second check, `web/scripts/stress-check.mjs`, for the places a terminal
corrupts quietly rather than failing loudly. The interface check asks whether
the panel works; this one asks whether the terminal underneath it is faithful.

Results on this machine: wide characters wrap at exactly half the column count,
so they are being measured as two cells; Chinese, Japanese and Korean all
render with the trailing pipe aligned against a Latin row; the alternate screen
restores what was underneath it when vim exits; twenty thousand lines arrive in
0.3 seconds with the interface still responsive and the tail in order; a reload
after the replay buffer has wrapped brings back coherent output with no
injected terminal responses; and an offline/online cycle reconnects on its own.

### Three wrong measurements before one right one

Worth recording, because each was confidently wrong.

**The wrap test proved nothing.** Printing forty wide characters into a
140-column terminal and checking they fit is satisfied whether they are
measured as one cell or two. Printing more than fits, and asserting *where* it
wraps, is the measurement.

**The marker matched the echoed command.** Searching the rows for a string that
also appears in the command that produced it finds the echo. The marker is
split in the source now, so the typed line does not contain it.

**Canvas metrics measured something xterm does not use.** `measureText` said a
wide character was 1.67 cells and the grid was therefore broken. It is not:
xterm sets `letter-spacing` on wide-character spans to force them onto the
grid, which the DOM shows plainly — ten characters at 15.6px against a 7.79px
cell, and the pipe after them exactly one cell. The check measures the rendered
DOM now.

Also `document.fonts.check` is useless for this: it returns true for font
families that are not installed at all, so it cannot distinguish a real glyph
from a missing one.

### And one wrong diagnosis

I read a correctly rendered 中 as a missing-glyph box and spent a while on a
font problem that did not exist — the character is a rectangle with a vertical
stroke through it. Rendering something with more varied shapes settled it in
one screenshot.

The CJK fallbacks added to `--font-mono` during that detour are kept. They are
hardening rather than a fix: naming the faces makes the stack explicit instead
of depending on the browser's implicit fallback, which differs between
platforms and does sometimes produce tofu.

## The panel admits when it is guessing

I finally tested against a real Claude Code TUI, at zero API cost — start it,
submit nothing, watch. Three things came out of ten minutes of watching a
program do nothing.

The first was good news. The M4 plan flagged a risk that an agent TUI redraws
continuously and would therefore read as `working` forever. It does not: bytes
went 1620 → 3348 → 3348 → 3348 while it sat at a prompt. An idle Claude Code
is genuinely quiet, so the activity heuristic has something real to measure.

The second was that `pane_current_command` reports `claude`, so automatic
naming works without any cooperation from the agent.

The third undermines the feature this whole panel exists for. It stopped on a
real trust prompt — "Is this a project you created or one you trust?" — the
exact moment the panel is supposed to light up orange and sort the session to
the top. It rang **zero** bells: no BEL, no OSC 9, no OSC 777. The bell is the
heuristic's only `waiting` signal. So without hooks installed, the panel does
not merely detect `waiting` late for Claude Code — it never detects it at all,
and shows `done` instead. Worse than no information: confidently wrong
information, on the one question you open the panel to answer.

Hooks fix it, and hooks are one click away in settings. But the panel was
silent about needing them, and a user who never visits settings would just
conclude the state dots are broken.

So the panel now says so. `stateIsGuessed` is true only when all three hold:
an agent-named process is running, nothing has ever reported a state through a
hook, and the hook script is not installed. Any one of those failing removes
the notice, so it clears itself the moment the situation improves rather than
nagging. It is a line in the sidebar that names the specific failure and links
to the fix.

The rule worth keeping: **a heuristic that cannot see the thing it is looking
for must say so.** Every honest signal here was cheap to gather — no tokens
spent, just a TUI left alone and a `wc -c` in a loop.

## Restarting the backend, for real

The one claim the whole architecture rests on — tmux owns the processes, the
panel is a thin client, so restarting the panel costs you nothing — had never
been tested. stress-check drops the WebSocket, which feels like the same test
and is not: the ring buffer is still sitting in memory on the other side, so
replay comes from the hot path. Killing the server deletes every buffer, and
then the only thing that can put your scrollback back is `capture-pane`, and
the only thing that can keep you logged in is the session row in SQLite.

`restart-check.mjs` kills the server outright and checks four things that were
all previously taken on faith: the pane pids are byte-identical across the
restart (the processes were never touched), a page left open heals by itself
and accepts input again without a reload, a page opened fresh afterwards is
filled from the cold path, and the login survives. All four passed, which is
the good kind of anticlimax — but they passed unverified for a week.

The rule: a test that exercises a nearby path is not a test of this path. Ask
what would have to be deleted for the fallback to run, then delete it.

## The affordance that offered nothing

The screenshots from that check showed a "147x45 · take control" pill floating
over a terminal that was already exactly 147x45 in that window. The condition
was `!controlling` — am I the owner? — when the question that matters is
whether this viewer is being made to look at somebody else's grid. Two windows
the same size see an identical picture, so the button changed nothing when
pressed, except to move ownership, at which point the other window grew the
same useless button. A permanent invitation for two monitors to pass a token
back and forth.

It now appears only when this window would render a different grid. Getting
there took a second bug: the first attempt measured the viewer's own fit with
`proposeDimensions()` in the passive branch, where the host element has
already been set to `max-content` and scaled — so the fit addon dutifully
measured the grid that was already on screen and answered the question with
itself. The measurement has to be taken with the host still filling its box,
before the transform. The harness caught it because the new assertion was
written as the converse of the old one, not as a restatement of it.

## A crash and a finished job were the same green check

Three sessions, three different realities, one row each in the sidebar:

    crashed   bash -c 'echo boom >&2; exit 3'   →  done
    finished  bash -c 'echo all done'           →  done
    alive     sleep 300                         →  done

tmux knew the difference perfectly well — `#{pane_dead}=1`, `#{pane_dead_status}=3`
— and the panel read the first of those fields and threw the second away. The
whole point of the sidebar is to answer "which of these needs me", and it was
answering "all fine" over a process that had died four hours ago.

The state enum is a red line and it stays three values, because the three
states describe the *task* and the user asked for exactly those. Whether a
process still exists is a different axis, so `exited` and `exit_status` are
columns on the session rather than a fourth state. Sorting is untouched for the
same reason: reordering the sidebar was a design decision, not something to
change as a side effect of a bug fix.

Two new shapes, since red line 4 rules out doing this with colour: a cross for
a non-zero status and a hollow square for a clean exit, next to the number in
text, because a shape cannot carry "3" and 3 is what tells you whether to care.

Then the corpse needed a way back, so `respawn-pane -k` behind a restart button
that is *not* hover-gated — a dead session is a thing to act on, and hover does
not exist on a phone. Writing the comment for it, I claimed the scrollback
survives a respawn. Measuring it instead: the pane's history survives, the
visible screen does not, which is exactly where the crash message and the tail
of a stack trace are. The comment now says that, because a comment that
overstates what a call preserves is how someone later builds a feature on a
guarantee that was never there.

The check that would have caught the original bug is not "does the state say
done" — it did, correctly, by its own rules. It is: do three different
situations draw three different glyphs? render-check now compares the rendered
SVG of a crashed, a cleanly exited and a running session pairwise and fails if
any two are identical.

## Every phone check so far was run with a mouse attached

Chasing the previous fix produced a worse finding than the fix. Seven controls
in the panel are revealed by hovering the row they sit in — pin, kill, the
project grip, the project menu, close-this-terminal-tab, delete-todo, delete-
passkey — which on a phone means seven controls that are invisible for the
entire life of the session. "Pin this to the top of the project" is a feature
that was asked for by name. It was a button you had to know the pixel position
of.

The reason it survived a render check with a phone section in it: the harness
emulated a phone by calling `setViewportSize`, and a narrow window is not a
phone. Chromium reports `(hover: hover)` and `(pointer: fine)` until touch is
actually emulated on the *context*:

    viewport only          hover: true   pointer: fine
    hasTouch: true         hover: false  pointer: coarse

So every mobile assertion written so far — the key bar, the compose box, the
drawer, swipe-to-copy — has been measured with a mouse. None of them were
wrong, but none of them were testing what they claimed to test either. The
check now opens a second browser context with `hasTouch` and `isMobile`, and
warns if the media query does not actually flip, so this cannot quietly regress
to a resized desktop again.

The fix itself is one class: `.vp-reveal` is opaque by default and only hides
itself inside `@media (hover: hover) and (pointer: fine)`. The failure
direction matters — if some environment misreports, controls are visible when
they could have been tidy, rather than absent when they are needed.

## Swipe to copy did not exist

It was asked for by name and shipped as a component that watches
`document.selectionchange` — the browser's own selection. Over a terminal that
is unlikely to work: xterm sets `user-select: none` on the terminal and handles
pointer input itself, so what a long press does over it depends on the browser.
There was no test of any kind.

Trying to write one produced the more useful lesson. A probe that long-pressed
and dragged over terminal text found nothing selected — and a control run of
the same gesture over an ordinary `<div>` of plain text found nothing selected
either. Headless Chromium performs no native touch text selection at all. That
probe would have reported the feature broken no matter how it was built, which
makes it worthless in both directions. Two false starts in one afternoon from
the same root cause: the harness measuring itself rather than the app. The
first version even dispatched `new TouchEvent()` from JavaScript, which bypasses
Chromium's gesture recogniser entirely.

The plan called for a hand-rolled touch selection layer for exactly this
reason, and the implementation had quietly substituted the native one. So the
gesture is now ours: press and hold for 450ms to anchor, drag to extend, lift
to keep, driving xterm's own selection API through touch events. It behaves the
same on every phone, and — the part that matters here — it can be driven by CDP
touch events, so there is now a check that presses, drags past the end of the
line, reads the character count, presses Copy and asserts on the clipboard
contents.

Two things fell out of building it. Dragging off the end of a line has to clamp
to the last cell rather than index past the buffer, because dragging off the
edge is precisely how anyone selects to the end of a line. And selecting down a
column must count cells in reading order, not draw a rectangle — the unit test
for that is three lines and would have caught the obvious wrong implementation.

The gate is `(pointer: coarse)`, not the layout breakpoint. A tablet in
landscape is not narrow and has nothing but fingers.

## "Even file transfer" was never built

The brief asked the terminal to support "copy and paste, even file transfer",
and M5's acceptance criterion was that dragging a file onto the terminal
uploads it and inserts the path. The file panel could list a directory and
nothing else: no download, no upload, no drop target. Not a bug — a feature
marked done that did not exist. Worth saying plainly, because the way this
happens is that the *panel* got built and the half of the milestone that lived
outside it did not.

Download is a plain link with `download` on it rather than a fetch into a blob:
the browser already has progress, resume and a save dialog, and a blob holds
the whole file in memory first. It goes out as `application/octet-stream` with
`nosniff`, because a project directory contains whatever an agent wrote and
some of that is HTML that must never render on the panel's own origin.

Upload streams part by part rather than through `ParseMultipartForm`, which
buffers the entire request before the handler sees it, and opens with `O_EXCL`
— an upload silently replacing a file an agent has open is a debugging session
nobody will enjoy. The response carries the absolute paths back so the drop
handler can type them at the prompt, shell-quoted only when they need it.

Three things the tests caught, all of them mine:

- `browse.Resolve` resolves symlinks, so it requires the path to *exist* — and
  an upload target by definition does not. The target is now a `filepath.Join`
  onto an already-validated directory with a `filepath.Base`d name, which
  cannot contain a separator. Containment is preserved by construction rather
  than by a call that cannot work here.
- Two of my own assertions were wrong about what safety means. `../..` as a
  target directory is *clamped to the project root*, not rejected — Resolve
  cleans against "/" first — and a filename containing a path keeps only its
  last element rather than being refused. Both tests now assert where the bytes
  end up, which is the property that matters, instead of a status code.
- The file listing is a snapshot taken on mount. An agent writing a file into
  the project could not be seen without leaving the panel and coming back, so
  there is a refresh button now. The harness needed it too, which is how it
  surfaced.

## The notepad was not synced, and quietly ate what you wrote

"Open it in many places and they stay in sync" was the first thing asked for.
It was true of sessions and false of the notepad. Measured with two browser
windows:

    note written in window A     →  A: "WRITTEN_ON_VIEWER_A"   B: ""
    a todo added in window A     →  A: 1 item                  B: 0 items
    then B typed its own note    →  the server kept only B's

The last line is the one that matters. Not a stale view — silent loss of the
user's own writing, in the one place in the panel that holds it. The state
snapshot carries projects and sessions and nothing else, and notes and todos
were fetched once on mount and never again.

Notes and todo lists deliberately stay out of the snapshot: they are per
project, they can be long, and pushing a document to every viewer on every
keystroke is waste. What goes out is which project changed and what kind of
thing it was; the panel that cares refetches. A note only adopts the remote
text when there is nothing local to lose — overwriting a half-typed paragraph
with someone else's is the same bug wearing a different hat.

That still leaves both windows writing. So the save now carries what its text
was based on and the server refuses a write that would land on top of another
one, handing back the current note so the client can show both. The first
version of that check used `updated_at`, and the harness immediately caught it
letting a stale write through: `updated_at` is unix seconds, and *every*
interesting conflict — one person typing while another saves — happens well
inside a second. A precondition that cannot see the case it exists for is worse
than none, because it reads as protection. It is a revision counter now.

This is the second time in this project that second-resolution timestamps have
been the bug; the first was two settings backups written in the same second,
one overwriting the other. Worth remembering as a class rather than as two
incidents.

## The panel opened collapsed for everyone, once

Found while setting the two-window test up: a fresh browser showed no right
panel and no terminal strip. The layout remembers its sizes, where 0 means
collapsed, and read them with `Number(readStored(key))` — and `Number(null)` is
0. Every first-time visitor was therefore treated as someone who had
deliberately closed the files, the system monitor, the notes and the todo list.

The harness never saw it because the harness opens those panels itself. Same
shape as the phone bug from the round before: state accumulated by the test
hid the state every real user starts in. There is now a check that logs in
through a brand-new browser context and asserts what is on screen before
anything has been clicked.

## Copying inside tmux worked, and then silently did not

The panel's answer to the WebSocket shim the old ttyd setup used: `set-clipboard
on` makes tmux forward OSC 52 to its client, the panel parses it and writes to
the system clipboard. Proper channel instead of sniffing the output stream.

It works. A `printf '\033]52;c;...'` inside a pane reached
`navigator.clipboard` two seconds later, which is what the first probe measured
— because the probe granted the page `clipboard-write`. Without that grant, the
same write comes back `NotAllowedError: Write permission`. The handler caught
the rejection and ignored it, with a comment saying there was nothing useful to
do.

There was. The write is not inside a user gesture, and that is not an edge
case: Chromium refuses it without the permission, Firefox and Safari require an
activation, and over plain http `navigator.clipboard` does not exist at all —
which is exactly how a self-hosted panel on a LAN address is reached. In all of
those, copying inside tmux did nothing and said nothing.

The outcome is now reported back to the shell, and a refused write turns into a
line above the terminal saying what was copied and offering the click that
makes it legal. Inside that click the write is allowed, with
`document.execCommand('copy')` as the fallback for the insecure-origin case,
where there is no async clipboard API to fall back *to*.

The lesson is about the comment as much as the code. "Nothing useful to do" was
written about the common case, not an edge case, and it stopped anyone looking
again — including me, twice, until a probe happened to run without a permission
grant. A caught-and-ignored error deserves the same suspicion as an unchecked
one; the difference is only that it looks deliberate.

## The archive nobody had ever unpacked

The distribution story is one sentence: unpack the tar.gz on a machine with
tmux and it runs. `scripts/build-release.sh` had never been executed. Neither
had anything downstream of it — which makes that sentence the claim in this
repository most likely to be quietly false, because it is about files nobody in
the development loop ever touches.

`scripts/release-check.sh` now builds the archives, verifies the checksums,
unpacks one into a throwaway HOME and drives it: is the binary actually static,
is the version stamp real, does `doctor` pass on a machine with nothing set up,
does it serve `/api/health` and its own embedded assets straight out of the
archive, does the one-time token appear and work.

Most of that passed first time. Three failures, two of them mine: `ldd` exits
non-zero for a static binary, so under `pipefail` the check failed on the very
evidence it was looking for, and creating the first account returns 201 rather
than 200. The kind of mistake worth writing down because both made a working
product look broken, which is the failure mode that wastes the most time.

The third was real. The shipped unit names `%h/.local/bin/vibepanel`, and
nothing put a binary there: the archive extracts into a directory of its own,
and the only documented route from "downloaded" to "running" was five manual
steps written in a comment inside the unit file — which you read after you have
already found and opened it. For a project whose brief asked for deployment to
other machines to be quick and easy, the first five minutes were undocumented
and unscripted.

So there is a `deploy/install.sh` in the archive now. It refuses to run without
tmux, puts the binary exactly where `ExecStart` looks for it, never overwrites
an env file the user has edited — that file holds the domain and any ACME
credentials — and it will not shut up about `loginctl enable-linger`, because a
systemd user service stops when your last session ends. A panel whose entire
purpose is outliving your terminal, that dies when you log out, is a panel that
only appears to work.

## A TLS setting that was silently ignored

Everything up to here ran over http on localhost. The deployment this was built
for terminates its own TLS on a public hostname, and three things only happen
there: the WebSocket upgrades to `wss`, the session cookie carries `Secure`, and
a certificate gets replaced under a running server. So `scripts/tls-check.mjs`
generates a self-signed pair, serves with `--tls files`, logs in through a real
browser and drives a terminal.

The first run never got as far as a handshake. `VIBEPANEL_TLS=files` had done
nothing: the variable the code reads is `VIBEPANEL_TLS_MODE`, while the README
promises that every flag has a `VIBEPANEL_<UPPER_SNAKE>` equivalent — and
`--tls` maps to `VIBEPANEL_TLS` under that rule. Three more diverged the same
way: `--tls-cert`, `--tls-key`, `--acme-dns`.

Every one of those is a security-relevant setting. Following the README to
enable TLS produced a panel serving **plaintext on a public port**, with a
banner cheerfully printing an `http://` URL, and nothing anywhere saying the
setting had been dropped.

Both spellings work now, and — the part that matters more — any `VIBEPANEL_*`
variable nothing reads is printed at startup above the setup token and reported
by `doctor`. A misspelling that used to be inert is now loud. The hook script's
own variables are excluded, or every session would produce a warning.

The rest passed first time: `wss` on an https page, the cookie `Secure`,
`HttpOnly` and `SameSite=Strict`, a certificate replaced by rename picked up
within the poll interval without disturbing an open session, and a deliberately
corrupted certificate file *not* taking the listener down — the file source
keeps the last good pair, which is the difference between a warning and an
outage during a botched renewal.

One assertion of mine was wrong again: I called it a failure that a plaintext
request to the TLS port is answered at all. Go replies with a fixed 400 saying
the client spoke HTTP to an HTTPS server, and that is right — it cannot
redirect, because a redirect would have to follow a handshake that is never
going to happen. The check now asserts what actually matters: no application
response, and no hang.
