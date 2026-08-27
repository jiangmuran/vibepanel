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

> **Later, applying that rule to this entry.** The third check was not testing
> the cold path either. Deleting the `capture-pane` priming outright left it
> green, and left the rendered screen byte-identical. What fills a fresh page
> is tmux repainting on attach; the priming was inert, because the attach
> begins with `ESC[?1049h` and the alternate screen has no scrollback. See
> "The replay that was drawn on a screen nobody could see".

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

## One header turned off both of the controls that keep strangers out

`--allow-from` narrows who may reach the panel; the login throttle stops
guessing. Neither had ever been driven over HTTP. Measured against a panel
whose allowlist was set to a network this machine is not on:

    GET /api/state                                     403  the allowlist works
    GET /api/state  X-Forwarded-For: <allowed address> 401  ...and does not
    GET /api/state  X-Real-IP: <allowed address>       401
    12 wrong passwords, one address        401 429 429 429 …   throttled
    12 wrong passwords, a new header each  401 401 401 401 …   not throttled

In the Go test, where the client carries a session cookie, the spoofed request
came back **200** — full authenticated access from an address the operator had
excluded.

The cause was one line: `r.Use(middleware.RealIP)`. chi's RealIP rewrites
`r.RemoteAddr` from `X-Forwarded-For` or `X-Real-IP` with no trust model at
all, and it ran in front of everything. `auth.ClientIP` right next to it does
the job properly — it believes the header only from a proxy the operator listed
— and its own doc comment says that trusting it by default "would let anyone
bypass the login throttle by inventing a new address on every attempt, which is
the whole reason the throttle exists". That is precisely what was happening,
two lines away from the comment warning about it.

RealIP is gone, the allowlist judges the same address the throttle and the
audit log do, and both properties have tests that fail loudly against the old
code with the messages above. The audit log deserves a mention too: while this
was live it recorded whatever address the caller claimed, which quietly poisons
the fail2ban story the README advertises.

The general lesson, and the third time this project has produced it: a
protective mechanism that has never been *attacked* in a test has not been
tested. Every one of these controls passed the obvious check — the allowlist
refused a plain request, the throttle throttled a repeated one — and both were
defeated by the first thing an attacker would try.

## A viewer the server cut loose could never come back

**Status: everything from here to the end of the log came out of a read-only
review, and is written but unbuilt and untested — the session that made these
changes could not run anything. Nothing below has been through `make check`,
and none of it should be believed until it has. The findings are solid; the
fixes are unproven.**

The ten files touched: `internal/ws/conn.go`, `internal/session/manager.go`,
`internal/httpapi/api.go`, `internal/hooks/install.go`,
`internal/webui/webui.go`, `internal/auth/throttle.go`,
`internal/config/config.go`, `web/src/protocol/socket.ts`,
`web/src/components/Terminal.tsx` and `web/src/hooks/useDragList.ts`.

Also unrun: `web/scripts/scale-check.mjs` and the `check:scale` script it adds
to `web/package.json`.

Two of the sections below are findings with no code behind them at all — the
missing password change, and the proposal about what a quiet agent means. Those
are marked where they appear.

`broadcast` drops a viewer whose queue is full rather than stalling the pump —
correct, because stalling the pump stalls the agent. The viewer is told with a
`dropped` message, and `pumpStream`'s comment says that message exists "so it
can resubscribe and replay, rather than sitting on a dead terminal". The client
does resubscribe. The server then ignores it.

`subscribe` short-circuits when the session is already registered on that
connection, and nothing had removed the registration when the subscriber died.
So the resubscribe was accepted, returned nil, and sent nothing: no `subscribed`
message, no new ref, no snapshot, no further output. The terminal stayed frozen
until the page was reloaded — the exact state the message was added to escape.

Found by reading rather than by running, which is worth noting: the stress check
covers *losing the connection*, where a new `Conn` starts with empty maps and
recovery works. The per-stream drop is a different path that looks like the same
one. A test for it has to fill one viewer's queue while leaving the socket up.

Two smaller things in the same file. `handleBinary` carried a comment saying
writing marks the viewer as the controller, which is the opposite of what
`Live.Write` does and says — the sort of comment that gets the code "fixed" to
match it. And a dropped stream never had its context cancelled, leaving an entry
in the connection context's child list for every drop.

## The same buffer, twice

Following that thread: on reconnect the client resubscribes every stream and the
server replays the whole ring buffer, and the client writes it into a terminal
that still holds everything from before. The scrollback ends up two copies deep.

It is invisible on screen, which is why nobody noticed: tmux sends a full
repaint to a newly attached client, so the *viewport* looks right and the
duplicate sits above it in the scrollback. Anyone scrolling back to read what an
agent printed sees the history twice, interleaved with redraws.

The fix arms a flag when the stream restarts and acts on it when the snapshot
actually arrives, rather than clearing on the restart itself: if no snapshot
follows — an empty ring buffer on a server that has only just started — clearing
eagerly would blank a terminal that still had something worth reading.

## Taking the grid without moving it told nobody

`TakeControl` sets the controller and then calls `Resize`, and `Resize` returns
early when the requested size equals the current grid. `EventSize` is the only
message that carries who the controller is, so in that case nothing was
broadcast and nothing changed for anyone: the new owner's interface still
offered to take a grid it already held, and — the worse half — the previous
owner went on believing its window drove the session while its resizes were
being silently ignored.

Reachable whenever two windows are the same size, or one has been through a
layout change and back. `TakeControl` now broadcasts the size itself when
ownership moved but the grid did not.

Same review pass, smaller: deleting a project killed its sessions but did not
tell the detector to forget them, while deleting a single session did. A tracker
per deleted session is a trivial leak; two paths doing almost the same thing is
not, and that asymmetry is what turns into a real bug later.

## Proposal: a quiet agent is waiting for you, not done

Not implemented — it changes what the sidebar says about every agent session and
the order it sorts them in, which is not a change to make without being able to
run the tests. Recorded here with its evidence so it can be decided on.

`done` is currently carrying three unrelated meanings: the task finished, the
process is quiet, and nothing is known. Measured against a real Claude Code TUI
earlier in this project: an idle one is completely silent (bytes went 1620 →
3348 → 3348 → 3348 while it sat at a trust prompt), and it rings no bell. So
without hooks the panel says `done` — "dealt with" — about a session that is
sitting there waiting for a human. That is the one question the panel exists to
answer, answered backwards.

The narrow version: when the foreground process is a known agent rather than a
shell, and it has been quiet for longer than the activity window, the honest
state is `waiting`, not `done`. An agent that has stopped printing has finished
its turn, and finishing its turn is precisely when it is your move. Spinners
mean a *thinking* agent keeps reading as `working`, which the same measurement
confirmed.

Blast radius is limited to sessions whose command is an agent, which is exactly
where today's answer is wrong. It needs `Observation` to carry the command class
rather than only `ShellOnly`. The existing rules stay as they are:
`TestQuietSessionIsDone` passes `Observation{}`, so an unclassified session goes
on reading as done and only a session known to be running an agent changes.

What it does interact with is the "states are being guessed" notice, which
exists precisely because `waiting` never fires for Claude Code without hooks. If
this lands, that notice needs rewording rather than removing: the guess would
then be a good one, but it would still be a guess.

## Writing to somebody's settings file to change nothing

Reviewing the hook installer, which is the only code here that edits a file the
user owns and which has already caused one incident. Two things, both of the
same kind: doing something invisible to a file that is not ours.

Pressing "install" when the hooks are already installed re-encoded the file,
renamed a new copy over it, and left a backup beside it — recording an edit that
did not happen. The settings page invites that press. Over a few months the
result is a directory of near-identical backups around a file whose formatting
quietly became ours. It now compares what it would write against what is there,
through the same encoder so the comparison is about content rather than
indentation, and does nothing when nothing would change. Uninstalling when
nothing of ours is present does nothing too. `TestInstallIsIdempotent` already
installs twice, so it becomes a regression test for this by accident.

## The flag that could not turn a thing off

`flags.go` had gone unread — the environment path was audited, the primary one
was not. Its precedence is right and neatly done: every flag is registered with
the environment-derived value as its default, so "not passed" and "passed the
same thing" are identical and the ordering falls out with no bookkeeping.

Two flags cannot be registered that way, being joined strings that need
splitting, and they were handled by testing the parsed value for emptiness:

    proxies := fs.String("trusted-proxies", "", ...)
    if *proxies != "" { c.TrustedProxies = splitAndTrim(*proxies) }

So `--allow-from=""` did nothing whenever the environment had set one. The
value stays, the flag is ignored, and nothing says so.

That is the scenario this function's own comment gives as the reason flags are
parsed last: "an operator debugging a systemd-managed instance can override one
value on the command line without editing the unit's Environment= lines". An
allowlist is the value most likely to need it, because getting it wrong is what
locks you out — and turning it off was the one thing the flag could not do. It
looked like the flag had no effect, because it had none.

`fs.Visit` reports which flags were actually typed, which is what the question
was all along. Three cases are pinned: absent keeps the environment, a value
replaces it, and an empty one clears it.

One thing found in passing and already true. The usage text promises "every
flag has a VIBEPANEL_<UPPER_SNAKE> environment equivalent" — the same claim
that was false in the README earlier in this log. Checking all thirteen flags
against the aliases added then: it now holds for every one of them. The README
was corrected at the time and this copy was not looked at, which is the more
useful half of the observation.

## A partial struct used as a namespace, and a correction

`cmdHook` needs the hook token, which lives behind a method on the HTTP server,
so it builds one:

    srv := &httpapi.Server{Cfg: a.cfg, DB: a.db, Tmux: a.tmux, Log: slog.Default()}
    if _, err := srv.HookToken(ctx); err != nil { ... }

No Manager, no Hub, no Auth, no Detector. It works because `HookToken` touches
only `s.DB` — today. It is a nil dereference waiting for a change to an
unrelated method: the moment anyone gives `HookToken` a reason to audit
something or notify anyone, the CLI panics on a path that has nothing to do
with what they changed.

**Correcting the previous entry.** It said the CLI cannot inject
`VIBEPANEL_TOKEN` because the token is behind that method, and used the size of
the repair as the reason not to make it. Half of that was wrong: the precedent
is four lines away in the same file, and `session new` could do exactly what
`cmdHook` already does. Only the URL half genuinely blocks, `loopbackURL` being
unexported.

The two findings share a cause, which is more useful than either. The defect is
not that the CLI forgets two variables; it is that both things a session needs
live on the HTTP server. So the CLI does without them in one place and fakes a
Server in another, and both are symptoms.

Neither belongs there. The hook token is a row in the settings table and
belongs in the store. `loopbackURL` is a pure function of `Addr` and `TLSMode`
and belongs next to `BindHost` in config. Moved, `session new` becomes a few
lines and the partial struct in `cmdHook` disappears on its own — which is the
shape of a fix worth waiting to make properly rather than patching twice.

## The container path, which nothing has ever run

Continuing the sweep for secondary paths. There is a Dockerfile and a compose
file, no test touches either, and the README does not mention them at all — a
deployment route that ships without documentation is already a strange object,
and it had two problems.

There was no `.dockerignore`, and the build stage does `COPY . .`. The
`.gitignore` beside it says, in as many words, "never commit a database with
real session/credential data" and lists `/data/` and `*.db`. Those are exactly
the files that were being copied into an image layer — somewhere considerably
easier to hand to somebody else than a git repository. The rule had been
written down once and not carried across the boundary, which is the shape this
review keeps finding. Also `web/node_modules` and `.git`, which are merely
waste.

The second is not fixable, only sayable. In a container, restarting the panel
kills every session. Everywhere else the tmux server outlives the Go process,
and that is the entire premise: `systemctl restart` is harmless, an upgrade is
harmless, a crash is harmless. Here tmux is a child of the entrypoint and the
container is the boundary, so `docker restart` or `compose up -d` after a
rebuild takes the agents with it.

The Dockerfile's header already discussed the container's drawbacks — tools,
credentials, the smaller world an agent finds itself in — and did not mention
the one that contradicts the reason the project exists. It does now, in
capitals, because somebody choosing between the two deployments needs that
fact more than any of the others.

## A session made from the CLI can never report its state

Went looking for more of what the previous entry described — a defect on a
secondary path, masked because the primary path does not touch it. Four were
already known: the relative `--static-dir`, the ignored `VIBEPANEL_TLS`, the
`go generate` that has never existed, the filename header. All of them the same
shape.

The CLI is the largest secondary path, and it has one.

Creating a session over HTTP injects four variables: the session id, the
project id, the panel's URL and the hook token. Creating one with
`vibepanel session new` injects two — the ids. `report.sh` requires all three
of id, token and URL, and exits silently when any is missing, which is exactly
what it is built to do.

So a session created from the command line never reports state through a hook.
It falls back to the output heuristic, which — per an earlier entry — never
raises `waiting` for Claude Code at all, because Claude Code does not ring the
bell. The session sits permanently in the degraded mode.

Two things hide it. The comment directly above that env block says "the hooks
that report precise state identify themselves by reading this out of their
environment", describing the mechanism while supplying a third of what it
needs. And the "states are being guessed" notice added earlier in this log
checks whether the *script* is installed, not whether a given session is
equipped to use it — so somebody who installed hooks and creates sessions from
the CLI gets neither the reporting nor the warning.

The CLI is not an exotic route. The README's own "try it" uses it, the runbook
is written in it, and anybody running seventeen agents is a candidate for
scripting session creation.

Not fixed. The repair needs `loopbackURL` to stop being a method on the HTTP
server and become what it actually is — a pure function of `Addr` and
`TLSMode`, belonging next to `BindHost` in config — plus a get-or-create for
the token in the store. That is a cross-package move and a read-modify-write
with a race window, neither of which should be done without being able to run
anything. Copying `loopbackURL` into the CLI instead would manufacture exactly
the duplication these rounds have spent their time cataloguing.

## Reading my own changes against each other

Thirty-seven files changed across these rounds with nothing able to run, which
is its own risk: each change was reasoned about alone. So this round audited
them as a set, looking for pairs that touch the same behaviour.

Most are complementary, and the ones that interact do so on purpose.
Deregistering a dropped stream is what lets the client resubscribe; clearing
before replay is what stops that resubscription showing everything twice — one
is useless without the other. `TakeControl` broadcasting on an unchanged size
is what makes the take-control affordance disappear for its new owner, since
that flag only travels on a size event. Making the hook script idempotent and
caching the hook status both remove work from `snapshot()`, from opposite ends.

One overstatement found and corrected, in the Content-Disposition entry: the
file tree's link sets `download={e.name}`, which overrides the header for a
same-origin URL, so the mangled name never appeared in the panel itself. The
fix is still right — a copied link or a `curl` gets the header and nothing else
— but the claim about what a user sees was wrong, and an entry in this log is
worth less than nothing if it is more alarming than accurate.

There is a second lesson in that. The attribute masking the header is precisely
why the bug survived: no amount of using the panel would have surfaced it,
because the one route anybody takes does not consult the thing that was wrong.

## Two constants that are the same number

Not a duplication this time but an interaction:

    activityWindow = 2 * time.Second   // output this recent means working
    pollInterval   = 2 * time.Second   // how often the state is recomputed

The state is recomputed from scratch every tick, with no hysteresis. So a
session whose last output was 1.9 seconds ago is `working` at one poll and
`done` at the next, and any new output puts it back.

That is not a corner case, it is what a tool looks like. An agent streaming
tokens prints continuously and stays `working` correctly. An agent running a
test suite, installing dependencies or reading a large file prints in bursts
with quiet between them — and each quiet stretch longer than two seconds
demotes it, each burst promotes it again.

The consequence is visible twice over: the indicator alternates between the
breathing circle and the check, and because `working` sorts above `done`, the
row moves up and down the list while it does. On a panel built because a list
of tabs was "messy", a row that will not hold still is the wrong failure.

The fix is asymmetric thresholds — promote on any output, demote only after a
longer silence — which is one constant and a comparison. Both existing tests
survive it: `TestQuietSessionIsDone` waits ten seconds and
`TestRecentOutputIsWorking` looks at 500 milliseconds, so a five-second
demotion threshold sits between them untouched.

Not made, for the same reason the "a quiet agent is waiting" proposal earlier
in this log was not: this is the semantics of the state machine, and the
particular number is a judgement about how still somebody wants that list to be
while they are scanning it.

## The frame layout, defined twice, agreeing so far

Same search, next contract: the binary frame. `FrameData`, `FrameReplay`, a
five-byte header and a big-endian reference, written once in Go and once in
`wire.ts`.

They agree today, which is the point at which a check is worth adding rather
than after they do not. What drift would look like is worth writing down,
because none of it points at a constant: a wrong header length shifts every
byte of terminal output, a wrong frame type makes replayed scrollback arrive as
live output — undoing the clear-before-replay fix from earlier in this log —
and a swapped byte order routes frames to stream references that do not exist,
so the terminal simply goes quiet. Each of those presents as "the terminal is
broken".

The values are parsed rather than string-matched, base 0, so the TypeScript is
free to write `0x00` or `0` without a spurious failure. The byte order is
checked by asserting both DataView calls pass `false` for littleEndian — that
argument is the one place where a single character silently reverses the
protocol.

## Looking for the pattern instead of tripping over it

Five instances of one rule kept in two places had turned up by accident. The
sixth was found by going to look: the WebSocket message names, defined as
constants in `internal/ws/protocol.go` and again as string unions in
`wire.ts`, with nothing comparing them.

The failure runs in the worst direction. A message the server sends and the
client has no case for is discarded in silence — no error, no log, nothing on
screen. That is precisely how a viewer cut off for falling behind would stop
recovering: the server says "dropped", the client hears nothing, and the
terminal sits frozen looking like a network problem. The same bug found earlier
in this log by a different route, which is a reasonable argument that this
contract deserves a check rather than care.

A drift was already there, and it was mine. `panel` — added for the notes and
todo broadcast — existed in the client union and was sent from `notifyPanel` as
a bare string, while its nine siblings all had constants. Found by looking for
the pattern, in the code written while cataloguing the pattern.

`MsgPanel` exists now, the sender uses it, and both directions are enumerated
into slices so a test can compare them against the TypeScript. The test reports
each direction separately, because they fail differently: a name the server
sends and the client ignores is a silent discard, while a name the client
expects and nothing sends is dead code in a switch.

One detail worth keeping: the parser only accepts quotes that follow the `t:`
or a `|` continuation. One of those interfaces already contains a comment with
an apostrophe in it, and a looser pattern would have been reading English prose
as protocol.

## The same three lines in two files, and the names they produce

`sessionLabel` existed twice, character for character, in App.tsx and
Sidebar.tsx. The fifth instance of one rule kept in two places in this review,
after the shell lists, the state enums, the theme storage key and the header
height. Nothing would have caught them drifting, and the symptom — the sidebar
row and the title bar disagreeing about what the session in front of you is
called — reads as a rendering glitch rather than as two functions.

It could not simply move into App.tsx: App renders Sidebar, so importing the
other way is a cycle. Nor into wire.ts, which describes what the server sends
rather than how it is shown. So it is its own small module, with the ordering
of its fallbacks written down, since that ordering is the actual content: the
derived title first, the command as the honest second best, and a last resort
that exists only so a row is never unlabelled and therefore unclickable.

Not fixed, and worth a decision: nothing disambiguates two sessions that
produce the same label. A shell falls back to the base name of its directory,
and at the scale this panel is for — seventeen agents across worktrees — two of
them sitting in directories called `src`, or two worktrees both named `main`,
is ordinary rather than unlucky. The sidebar then shows two identical rows and
the only way to tell them apart is to click one.

That is close to the complaint the project started from: tabs that cannot be
named and a list that is a mess. The bottom terminal strip already numbers its
tabs when the server declines to name them, so the shape of an answer exists;
choosing between numbering, showing the parent directory, or something else is
a judgement about what a person scanning that list actually needs.

## A filename left to the browser's guess

Same method, next scenario: download a file with a Chinese name. This is code
written earlier in this same log, and `Content-Disposition` is where HTTP is
least forgiving about anything that is not ASCII.

It put the raw UTF-8 bytes into `filename="…"`. That parameter is ISO-8859-1 by
specification, so the result is left to the browser to guess: Chromium usually
guesses UTF-8 and gets it right, Firefox has historically read it as Latin-1,
and the file lands on disk with a mangled name.

**Correcting the first version of this entry**, which claimed the file arrives
mangled. Through the panel it does not: the file tree's link carries
`download={e.name}`, and for a same-origin URL that attribute overrides the
header entirely. The header matters for the paths that do not use it — a copied
URL opened directly, `curl`, anything scripted.

Which is also why the bug was there to find. The one route anybody exercises
masks it, so no amount of using the panel would have shown it; only reading the
header would. Worth keeping the fix and worth keeping the correction: a claim
about what a user sees is not improved by being more alarming.

Both forms are sent now. `filename*=UTF-8''…` states the encoding, and the
quoted ASCII fallback stays for clients too old to understand it. One trap
avoided on the way: `url.PathEscape` is not an RFC 5987 encoder — it leaves
`;` and `,` unescaped, and `;` is the parameter separator, so a filename
containing one would have broken the header outright. The encoder here escapes
everything that is not an attr-char.

The test asserts the encoded form *and* that no byte above 0x7f survives
anywhere in the header, which is the property the whole exercise is about.

Three rounds, three bugs, one question each time: what happens when the person
this was built for uses it in their own language. A name truncated mid
character, an emoji cut in half, a filename left to a browser's guess. All
three read as correct code.

## Half an emoji on the rail

Swept both sides for the same fault as the passkey name: anywhere user text is
*truncated* rather than rejected. Rejecting is safe — the note and todo limits
and the username check all refuse rather than cut, so nothing invalid is ever
stored.

The Go side had only the one already fixed. The frontend had `initials()`,
which makes the two-letter badge on the collapsed rail:

    if (words.length === 1) return words[0].slice(0, 2).toUpperCase()
    return (words[0][0] + words[1][0]).toUpperCase()

`[0]` and `slice` count UTF-16 code units and an emoji is a surrogate pair, so
the first unit of a leading emoji is half a character. "📊 monitoring" becomes a
replacement glyph followed by an "M".

Worth stating why this is not a hypothetical. The zellij setup this panel was
built to replace names its tabs "📊 监控" and "🐚 shell" — read at the start of
this project. The person it is for demonstrably puts an emoji in front of
things, and the collapsed rail is where they would see it broken.

Counted in code points now. CJK was already safe, being one unit per character;
the change removes the distinction rather than adding a case.

Two rounds, two bugs, both found the same way: not by reading the code for
correctness, but by putting this project's actual user through it. Both read
perfectly well until you do.

## A passkey name cut in half

Read the WebAuthn client and the registration path looking for the classic
base64url mistakes. There are none: the padding arithmetic is right for every
valid remainder, the alphabet swap is symmetric, an empty `userHandle`
ArrayBuffer takes the same branch as a null one and produces the same empty
string, and `allowCredentials: []` is what a discoverable sign-in wants. Both
directions are used — registration lives in the settings page, sign-in in the
gate — so nothing there is dead.

The server side is careful in the way that matters: the account comes from the
session context rather than the request, and a ceremony started by one account
cannot be finished by another, with a comment naming the attack.

One thing, in the middle of all that care:

    if len(name) > 64 {
        name = name[:64]
    }

Both of those are byte-wise. Sixty-four bytes is about twenty-one Chinese
characters, so a descriptive name reaches it easily, and the cut lands inside a
character — storing an invalid UTF-8 sequence that renders as a replacement
character in the passkey list, permanently.

Small, and worth fixing rather than noting because the fix is one line with no
judgement in it. It also sits oddly against the rest of the project, which
thinks about this constantly elsewhere: the font stack carries CJK fallbacks,
the compose box exists because input methods and raw terminals do not mix. This
is a field people name in their own language, and it was the one place counting
bytes.

## Machinery with no route to it

Having found one dead column, the same sweep across the rest turned up three
more paths that exist and cannot be reached. None is wrong behaviour; all four
are surface the interface does not expose, and an unreachable path is an
untested path that will rot.

`stateChangedAt` is stored, carried in the struct, serialised into every state
snapshot and pushed to every viewer on every broadcast. No frontend code reads
it. Worth keeping rather than trimming, though: it is precisely the groundwork
for the most obvious missing feature on a panel whose job is telling you what
needs you — "waiting for five minutes". The number is already on the wire.

Project pinning has a store method, a field on the patch endpoint and the first
clause of the ordering query. The sidebar's project header offers a rename and
a new shell, and nothing else. So the ordering honours a flag no one can set.

`deleteProject` has a handler, a client method and a CLI command. The frontend
never calls it. Projects can be created from the interface with a button and
removed only from a terminal — and deleting one kills its sessions, which is a
fair reason to keep it away from a stray click, but nothing in the interface
says so.

For contrast, `autoOrderProjects` *is* wired up, to the clock in the sidebar
header. So this is not "project operations were never connected"; it is these
particular ones, which is the harder kind to notice.

## A session that no longer exists shows as whatever it was

`archived_at` is in the schema, in the struct, in the column list and in the
scan. Nothing writes it. Nothing on the frontend reads it. It is null forever.

The schema says what it is for: "Set when the tmux session is gone but the user
has not dismissed the row." `Reconcile` says what that is supposed to buy:

    if !alive {
        // The tmux session is gone. The row is kept rather than deleted so
        // the user can see what happened and dismiss it themselves; losing
        // a session silently is worse than showing a dead one.
        continue
    }

The row is kept. The part that makes keeping it worth anything — saying it is
gone — was never built, so the user cannot see what happened. `pollOnce` skips
the same way, so this is not only a startup condition.

What that looks like: the tmux server restarts, or somebody runs `kill-server`,
or the box reboots and the panel comes back before tmux does. Every row
survives, showing its last known state. A green check. A blue working dot that
will never move again. Or an orange triangle meaning an agent needs you, for a
session that does not exist — the panel's most important signal, pointing at
nothing.

Clicking it does reveal the truth, because attaching fails on a session tmux
does not have. The list lies until then.

The `exited` work earlier in this log makes it worse by contrast: a crashed
process now gets a red cross, so a session whose entire tmux session has
vanished is the *only* dead thing still wearing a green check.

Two comments in two files describing one unbuilt feature, with a database
column standing there making it look finished. The same shape as the
`go generate` claim, and harder to spot for exactly that reason.

Not built. It spans the store, the poller and the sidebar, and the choice
between removing such rows automatically and marking them for the user to
dismiss is a product decision — though both comments already lean towards
marking, and the CLI's `session kill --id` is described as clearing the row,
which is the dismissal half already in place. The natural shape is a sibling of
`exited`: written where `!alive` is already detected, rendered with its own
glyph, dismissible.

## The other half, and a smaller decision inside it

The top edge turned out worse than the previous entry allowed for. It is not
only landscape: `index.html` declares the panel installable to the home screen
and asks for a translucent status bar, and that combination means an installed
copy starts its content at y=0 in portrait too, with the clock and battery
drawn over whatever is there. What is there is the header — the menu button
that is the only route to the session list on a phone, the session name and the
connection dot.

Same remedy, and worth recording the decision inside it because the obvious
version was wrong. Tailwind's preflight makes everything `border-box`, so
`h-11` includes padding: adding `padding-top` would have squeezed the header's
contents rather than moving them down. The tidy fix is `box-sizing:
content-box`, which also makes the border stop counting toward the height — so
every device without an inset would shift by a pixel. Devices that do not have
this problem should not pay anything for it, so the height is grown explicitly
instead and the class repeats the 2.75rem.

That repetition is a real cost of exactly the kind this log keeps finding: two
places holding one number with nothing checking. It is written down at both
ends, which is the least that can be done about it, and it buys not changing
the box model under a component that is already blurred, bordered and
positioned inside a flex column.

## Half of a pair of settings

`index.html` asks for `viewport-fit=cover`. Nothing in the stylesheet mentions
`env(safe-area-inset-*)`.

Those two go together. `cover` extends the layout viewport into the unsafe
areas, and the inset is how the content is given back. With only the first
half, the lowest thing on screen sits under the home indicator — and on a phone
the lowest thing here is the second row of the key bar: arrows, home, end,
digits, symbols. Drawn exactly where the system's swipe-up gesture lives, on
every iPhone since the X.

That is the hand-rolled keyboard that was asked for by name, with its bottom
row placed where the operating system will fight it for every tap.

Fixed, and the reason this one is fixed rather than reported is that it cannot
make anything worse: `env(safe-area-inset-bottom, 0px)` is zero on hardware
without an inset, so devices that do not have the problem pay nothing. The
class sits with the other `vp-` utilities and the key bar is the only thing
using it, being the only thing at the bottom.

Two related things not done, both needing a device to judge. The top edge has
the same issue in landscape, where the header would run under the notch — less
severe, because the header's controls are not at the extreme edge. And the same
line disables zoom entirely with `maximum-scale=1.0, user-scalable=no`, which
matters more here than in most apps: a phone looking at a session owned by a
desktop renders that grid scaled down, and the screenshots from these checks
show it small enough to be genuinely hard to read. The panel's answer is "take
control", which reflows the desktop mid-edit — precisely what the arbitration
design exists to avoid. So the person who most needs to zoom is the one who
cannot, and their alternative is disruptive. Whether removing it interferes
with the press-and-hold selection is exactly the kind of thing that has to be
tried rather than reasoned about.

## Six theme cases, all correct, and one string in two places

Enumerated the theme system rather than reading it: three choices against two
system preferences, checked against the three CSS blocks.

All six land correctly, and the mechanism is neat. `:root` carries light as the
base; the dark media query is scoped `:root:not([data-theme='light'])`, which is
exactly what lets an explicit light choice win on a dark system; and
`:root[data-theme='dark']` wins the other direction on specificity and order.
The documented palette-lag fix is intact too — `setTheme` calls `applyTheme`
before `setThemeState`, so the attribute is on the DOM before anything re-reads
the computed values — and `themeKey` includes the system preference, so the
terminal repaints when the OS flips while the choice is "system".

One thing worth pinning. `index.html` applies the stored theme before first
paint, with the storage key spelled out inline, and `theme.ts` holds the same
string. Nothing tied them together.

Drift there does not cause a flash, which is what the inline script exists to
prevent — it causes the theme choice to stop working entirely. The script would
find nothing under the old key, and nothing else applies a stored choice:
`applyTheme` runs only from the toggle's handler. The session would follow the
system preference while the toggle showed the setting it was ignoring.

The key is exported now and a test asserts `index.html` uses it, along with
three properties that are easy to lose in a file nobody opens: the script must
not write `data-theme="system"` (its absence is what means "follow the system"),
it must sit above the module script or it paints once in the wrong palette
first, and it must keep its try/catch — an exception there happens before
anything has rendered, so private mode would produce a blank page rather than
merely the wrong colour.

## The CPU number gets worse the more people are watching

The sampler keeps the previous `/proc/stat` counters on the server, shared by
every caller, and each request advances them. So the window a percentage covers
is not "since this viewer last asked" but "since anybody last asked".

One viewer polling every two seconds gets a two-second average, which is what
the number claims to be. Two viewers, out of phase, each get roughly a
one-second average — noisier, but still meaningful. Two viewers landing a few
milliseconds apart get a percentage computed across those milliseconds, which
is not a measurement at all: it reads 0 or 100 depending on where the sample
fell relative to a scheduler tick. The reading flaps, and it flaps *because*
somebody else opened the panel.

That is the ordinary case here rather than a corner of one. "Open it in several
places and they stay in sync" is the first thing this project was asked for.

Below a 500ms floor the previous answer is repeated and the counters are left
alone, so the next caller still has a window worth measuring. A single viewer
at the current two-second cadence never reaches the branch; two viewers a
second apart do not either. It only catches the collisions, which is all it is
for.

The frontend polling was already right, and worth recording as such: it
self-schedules after each response rather than using `setInterval`, so a slow
answer cannot stack requests behind it, and it stops entirely when the panel is
unmounted.

## Every route, checked against who can reach it

A completeness pass rather than a hunt: list every registration and see which
sit outside `RequireAuth`. An endpoint accidentally left open on a panel that
hands out a shell is the kind of thing worth checking once, deliberately,
rather than assuming.

It is clean. Public: the health probe, the four auth endpoints, the two passkey
sign-in ceremonies, the hook endpoint with its own constant-time token, and the
frontend itself — which has to be reachable or nobody could see the login page.
Everything else is inside the authenticated group, including passkey
registration, listing and deletion, which sit in their own inner group in
`registerPasskeyRoutes`, and the WebSocket, which is the terminal and carries
the same requirement.

One thing for a decision rather than a fix. `/api/health` answers an
unauthenticated caller with the version, the tmux version and the number of
live sessions. There is a test that it leaks no password, token or hash, and
that test is right. But the comment beside it says the probe "says nothing
sensitive", and the count of live sessions says something: not what you are
doing, but when you are working. The versions say which advisories to read.

Left as it is, because exposing version and liveness is what health probes are
usually for and monitoring wants it. The two ways to narrow it, if that trade
looks wrong: answer `{ok: true}` to an unauthenticated caller and keep the
detail for `/api/settings`, which already requires a session; or keep the
behaviour and make the comment a list of what is deliberately public. The
current wording is a shade more comfortable than the facts.

## Two lists of what counts as a shell, already disagreeing

    session.IsShellCommand:  bash sh zsh fish dash ksh tcsh csh tmux ""
    httpapi.isShell:         bash sh zsh fish dash ksh           tmux

Both answer "is this a plain shell". One drives the state machine — a shell
sitting at its prompt is done, not working — and the other drives naming, where
a shell falls back to the directory it is in because every shell is called
bash and the directory is the only thing that distinguishes them.

So a session running csh was a shell to the state machine and not a shell to
the namer: labelled "csh", which tells you nothing, instead of the worktree it
was opened in. Nothing in the repository noticed, because nothing compared the
two lists.

The same shape as the Go and TypeScript state enums earlier in this log, and
this time the fix is better than a test: there is only one list now.
`httpapi.isShell` calls `session.IsShellCommand`, which is where the judgement
belongs — its own comment already claimed ownership, and being a shell has
consequences in the state machine while naming only wants the same answer.

Traced before changing it, because a shared helper is exactly where a quiet
behaviour change hides. csh and tcsh now reach the directory fallback, which is
the documented intent. The empty command — which happens for a moment after
creation while `pane_current_command` still reads "tmux" — returns the empty
string down both the old path and the new one. Nothing else moves.

## The compose box fired on the input method's own confirm key

The box exists because typing into a raw terminal with an input method is
unusable: every composition keystroke reaches the shell and Chinese, Japanese
and Korean input come out as garbage. Its own comment says so.

Its Enter handler had no `isComposing` guard. With an IME, Enter is how a
candidate is chosen, and Chromium reports that keypress as `key: "Enter"` with
`isComposing` set — so picking the first word of a Chinese sentence sent the
sentence. The component built to keep an input method away from the terminal
was firing on the input method's own confirm key.

Fixed rather than reported, which is a departure worth justifying: it is one
well-understood condition, no layout and nothing asynchronous, and for anyone
not using an IME `isComposing` is always false, so the change cannot affect
them. That combination is rare enough in this log to be worth naming — most of
what has been left alone was left alone because the repair needed a judgement
about what something should look like.

Found by the same method as the Ctrl finding directly above: taking the inputs
the interface can actually produce and following them through, rather than
reading the code for whether it looks right. Both of those bugs read perfectly
well.

Unfixed, from the same file and the same principle: the Enter-appends toggle
beside the send button distinguishes its two states by colour alone — the same
icon, the same border, only `text-accent` against `text-ink-2`. That control
decides whether pressing Send runs the command or merely types it, the title
attribute explaining it is hover-only and a phone has no hover, and this
project's own rule is that colour is never the only carrier of meaning. Left
alone because choosing what the off state should look like is a design
decision, and one that wants seeing rather than reasoning.

## You cannot interrupt anything from a phone

`withCtrl` is written correctly — folds the top three bits, upper-cases first so
ctrl+c and ctrl+C agree, handles space as Ctrl-@. It is also unreachable for
every character that matters.

The key bar renders no letter keys. `y`, `n`, the digits and `/ - | ~`, and
that is all. Ctrl is applied only by `sendChar`, which only the digits and
symbols use. So Ctrl+C, Ctrl+D, Ctrl+Z, Ctrl+R and Ctrl+L cannot be produced at
all: the compose box sends literal text, and the terminal is read-only at phone
width, so there is no second route.

It is worse than "no letter keys", which is how this was first written down.
Ctrl folds the top three bits, so it only changes bytes in 0x40–0x5f. Put every
character the bar can send through the modifier path against that range —
`1` `2` `3` are 0x31–0x33, `/` is 0x2f, `-` is 0x2d, `|` is 0x7c, `~` is 0x7e —
and all seven fall outside it, so `withCtrl` returns each of them unchanged.
The modifier latches, un-latches, and cannot alter a single byte. It is
decorative.

For a panel whose stated purpose includes giving agents instructions from a
phone, interrupting a command that has gone wrong is the most urgent
instruction there is, and it is the one that cannot be given. The render check
knew, in a way: its ctrl test reaches for `key-c` with a `.catch()` around it,
because that key has never existed.

Second, smaller and independent: `sendRaw` clears both modifiers whether or not
they were used, and nine of the eighteen keys go through it — y, n, enter, esc,
tab, the four arrows, home, end. Tap ctrl, tap y, and a plain `y\r` is sent
while the ctrl key un-highlights exactly as though it had applied. The modifier
is consumed and silently does nothing.

Neither fixed, and the reason is that the repair needs per-key knowledge rather
than a rule. Alt before an arrow is an Escape prefix, which `withAlt` derives
correctly; Ctrl before an arrow is `\x1b[1;5A`, which no bit-folding produces —
applying `withCtrl` to a multi-character sequence silently returns it unchanged,
which is another thing that looks like it worked. And whether an unapplied
modifier should stay armed or be dropped is a judgement about how a thumb
expects the bar to behave: staying armed risks a stuck modifier, dropping it is
the silent loss above. Both want seeing on a real device.

Neither is covered by the harness, and the reason is worth noting: the ctrl test
verifies that the modifier *unlatches*, and it happens to use one of the seven
keys that does apply it. A test that had used `esc` would have passed too, while
proving the opposite.

## Manual order that stops being manual

`ReorderProjects` writes positions for the ids it is given and leaves the rest
alone. Its comment said those others are "left on automatic ordering". They are
not: a project that already carried an explicit position keeps it, so a partial
list can leave two projects sharing an index.

That matters because of how the ordering query breaks ties — `pinned`, then
nulls last, then `sort_index`, then `last_active_at DESC`. Two projects on the
same index are therefore ordered by *activity*, so the pair swaps places
whenever one of them does something. The user dragged one above the other and
the panel quietly stops honouring it, for that pair only, at a moment unrelated
to anything they did.

Reachable from a stale list: two viewers, one reordering while the other's idea
of the project set is out of date. Narrow, but manual ordering exists precisely
to hold still until it is changed.

Comment corrected, because it was provably wrong. Behaviour left alone: the fix
is to null the omitted ids inside the same transaction, which would make the
original comment true — but demoting a project that somebody else can currently
see to the bottom of their sidebar is a decision about their interface rather
than a defect being repaired.

## A clean read, and one small thing

`AuthGate` and the credential rules hold up. The server enforces the twelve
characters the hint promises — `auth.MinPasswordLength`, with an upper bound so
a megabyte of password cannot be used to make argon2 the denial of service —
the username is trimmed and bounded, both buttons are disabled while a request
is in flight so a double click cannot spend a throttle budget, and the
autocomplete attributes are right in all three fields.

Recording that as a result. Two rounds in a row without a finding is worth
saying plainly: if every round produces something, "it produced something"
stops carrying information.

One small defect. Errors are rendered only when there is no state yet:

    if (error && !state) { ...show it... }

After the first successful load `state` is never null again, so any later
failure is stored and never shown. The path that matters: the network drops,
sign-out is clicked, `logout()` fails and is swallowed deliberately, `refresh()`
then fails too — and the interface goes on showing a signed-in panel with the
error invisible. The user pressed sign out and nothing happened, with no
explanation.

Not fixed because the repair involves a judgement that wants seeing: whether a
failed sign-out should clear the local state anyway. Doing so is safer on a
shared machine and produces a false "signed out" once the network returns.
Small, too — it needs a network failure, and by then the user usually knows.

## doctor now says the thing the requirements list cannot

Following the previous entry's "worth doing and not done": `doctor` printed the
tmux version and compared it to nothing. A requirements list is read once, by
somebody who already installed tmux; `doctor` is read on the machine where it
is actually wrong.

`tmux.ParseVersion` and `AtLeastMinimum` handle the shapes tmux really reports
— "3.6", "3.3a" for a patch release, "next-3.6" for a development build — so a
split on "." was not enough. An unparseable version counts as new enough:
refusing to run because a version string looked unfamiliar would be worse than
the problem being guarded against.

Three outcomes rather than two, because the two existing markers do not fit.
Missing tmux is fatal. Too old is a real degradation that is not worth refusing
to start over. Neither should look like the other, so an old tmux gets the
`--` marker that passkeys already use for "works, but not here", with the
consequence spelled out underneath: allow-passthrough is not applied and agent
progress bars and notifications are lost.

The test covers both directions deliberately. A false "too old" nags about a
working install until somebody stops reading doctor's output, and a false "new
enough" leaves the passthrough problem undiagnosed on the one machine where it
matters — and that failure is invisible by nature.

## The stated tmux version is a version too low

`TestEnsureServerLoadsConfig` already understands the hazard, and says so: "A
config file with a bad option name makes tmux print an error and carry on with
defaults, so 'the server started' proves nothing on its own." It then reads
back one option per scope to prove the file was applied.

One per scope proves the file was *read*. It does not prove that the lines
whose failure is invisible survived. The ones it skipped include `bell-action`
— the setting with an incident behind it, where "none" reads like "do not
react" and actually stops tmux forwarding the bell at all, removing the only
signal the panel has that an agent wants a human. Nothing else about the panel
would look different.

Added: `bell-action`, `visual-bell`, `allow-passthrough`, `monitor-bell`,
`escape-time`, `default-terminal`. Chosen by asking which lines fail by doing
nothing observable, rather than by covering the file evenly.

Writing that turned up a requirement mismatch. `allow-passthrough` arrived in
tmux 3.3; the README asks for 3.2 or newer. On 3.2 the option is unknown, tmux
reports it and carries on, and the sequences agent TUIs use for progress and
notifications are swallowed from then on — with no error anywhere, because that
is precisely what this class of config failure does. README now says 3.3 and
why.

Worth doing and not done: `doctor` prints the tmux version but does not compare
it against a floor. It is the natural place to catch this on the machine where
it matters, rather than in a requirements list nobody re-reads.

## The runbook was wrong about the thing you read it for

Audited every claim in `docs/runbook.md` against the code, since it is the
document somebody reads at two in the morning and three of the drifted comments
found earlier were in exactly that shape.

Most of it holds up: `doctor` checks what it says it checks, `GONE` really is
printed by `session ls`, `--id` is the right flag, the database version message
is quoted correctly, and the note about a tmux server started outside the unit
escaping the unit's memory limits describes a real incident.

The memory section was wrong, and wrong in the direction that matters. It said
the panel's own memory "stays roughly flat regardless of session count" because
scrollback lives in tmux. The panel attaches to *every* session — that is what
makes state detection work for the ones you are not looking at — and each
attachment carries a replay buffer of up to 2 MiB, a PTY and a goroutine. It is
linear, not flat. Somebody debugging memory pressure was being told to look
only at tmux.

Rewritten with the actual numbers and where each lives, plus the threshold
`scale-check.mjs` uses, so the page and the check agree.

Two additions while there. The ACME section said a DNS-01 credential "must be
present in the environment" without naming the variable, which at two in the
morning is a dead end — it is `CLOUDFLARE_API_TOKEN` or `CF_API_TOKEN`, and
Cloudflare is the only provider wired up. And there is now a section for the
timestamp-preserving renewal found earlier in this log, with the one-line
workaround: `touch` the pair.

## A dependency for a renderer that was never used, and a check with no way to run

Applying the dependency guard to the frontend turned up two things and one
mistake of mine.

`@xterm/addon-webgl` is a declared dependency and is imported nowhere. The plan
called for the WebGL renderer; the implementation uses the DOM one. The
dependency stayed behind.

Reported rather than removed, and the reason matters: deleting it from
package.json without running `npm install` would leave package-lock.json
disagreeing with it, and `npm ci` — which is exactly what build-release.sh runs
— fails on that. An obvious tidy-up that would have broken every release build,
avoided only by thinking about the lock file before reaching for the edit.

The second: `check:scale` was never in package.json. The scale harness was
written, and the command that would have registered it was one of the ones
refused, so the file has been sitting there with no way to run it. I reported it
as done at the time, which it was not. Added now, and the new test asserts every
harness in `scripts/` has a script pointing at it, because a check nobody can
run is the same as a check that does not exist — the theme of this entire
stretch, arriving this time in the form of my own bookkeeping.

The guard itself mirrors the Go one: no component library, no state library, no
Jest, no Prettier, and the pieces the terminal is built on must still be there.
Both directions again, because a dependency quietly swapped out is the same
problem as one added and only the second is obvious.

## The constraint that only fails on somebody else's machine

Past the red lines, the conventions deserved the same question. One of them is
a hard constraint rather than a preference: `CGO_ENABLED=0` must keep working,
because "download one static binary and run it" is the entire reason this is
written in Go. It is why the SQLite driver is modernc's pure-Go one rather than
mattn's.

Nothing enforced it. A dependency that needs cgo compiles perfectly on a
machine with a C toolchain — which every development machine has — and only
fails when someone cross-compiles a release. Or worse, does not fail at all and
produces a binary that will not start on the machine it was copied to, which is
the one place nobody is watching.

`release-check.sh` does catch it, since it builds the archives and runs `ldd`
on the result. But that is a several-minute job nobody runs while editing, so
in practice the constraint was checked once per release rather than once per
change.

A build tag cannot express this and `go vet` will not either, so it is a list:
the packages AGENTS.md names as forbidden, the ones it names as required, and
the minimum Go directive the tests now depend on. Short, static, instant, and
it fails with the sentence explaining why the dependency is not allowed rather
than with a linker error three steps later.

The list cuts both ways deliberately. A driver silently swapped out is the same
problem as a forbidden one added, and only the second half is obvious.

## The last two red lines

Red line 5 — theme blocks redefine tokens, never component styles — is now a
static test. It reads `styles.css`, finds the `prefers-color-scheme` block and
every `[data-theme]` rule by brace counting, and asserts that each declares
custom properties and nothing else. A component's colours living inside a theme
block is the classic white-on-white: the rule exists in one theme and simply is
not there in the other, so whatever it was overriding comes back.

It checks a second thing the file's own header warns about: every token defined
in a dark block must also have a value on bare `:root`. A colour whose only
definition is inside a media query is undefined in the theme that query does not
match. Verified against the current file before writing the assertion — the dark
blocks define 36 tokens and all 36 have light defaults, with `:root` carrying
three more that are not colours and correctly have no theme variant.

The first assertion in that file is that theme blocks were found at all. A
parser that finds nothing finds no violations either, and this whole stretch has
been about checks that pass because they never ran.

Red line 4 — shape as well as hue — is now compared in the render check, at the
moment a session is waiting, htop is working and others are done. It pulls the
rendered SVG for each state and compares them pairwise. Colour is not part of
the comparison, deliberately: the point is that the shapes differ.

A state that is not on screen at that instant produces a warning rather than a
failure. It is a setup gap, not a defect — but saying so out loud matters,
because a comparison that did not happen reads exactly like one that passed.

All seven red lines now have something behind them. Three did not when this
review started: the TypeScript half of 3, exact-match targets in 7, and this
pair.

## The red line with an incident behind it had no test

Having found one red line half-fictional, the rest were worth the same
question: is this actually enforced, or only written down?

Red line 7 — exact-match tmux targets, `=name:` — was not. Five tests in the
tmux package and none of them creates two sessions with a shared prefix.
`target()` and `sessionTarget()` carry the "=" and a careful comment explaining
why; nothing would have noticed if either lost it.

Writing the test needed the failure understood properly. tmux resolves a target
by trying an exact match first and a prefix match second, so two sessions
existing at once is *not* the dangerous case — the exact one wins. The danger is
aiming at a session that has already gone while a longer name is still there,
which is an ordinary event: the panel races with sessions exiting on their own,
and a kill arriving a moment late would land on a different session entirely.

So the test creates only `vp_abcd`, then asserts that `vp_ab` is not found, that
a resize aimed at it does not move `vp_abcd`, and that killing it does not take
`vp_abcd` with it. Drop the "=" and all three fail; leave it and every other
test in the package is unaffected either way, which is exactly why this was
missing.

Red lines audited so far: 1 (socket isolation) holds by construction — every
command goes through `run`, which always prefixes `-L` — and is additionally
asserted by `doctor`. 2 (never own a session's PTY) is covered by restart-check,
which compares pane pids across a restart. 3 is now covered on both halves. 6
(validate hook input) has `TestHookRejectsGarbage`. 7 is this. Still unexamined:
4 (shape as well as colour) and 5 (theme tokens only in theme blocks).

## Half of a red line was fiction

`internal/session/state.go` opened with a rule in capitals: it is the single
source of truth, `web/src/protocol/state.ts` is generated from it by
`go generate ./...`, and the TypeScript must never be hand-edited. AGENTS.md
carries the same claim as red line 3.

Neither the generated file nor the generator exists. `web/src/protocol/` holds
api, socket, webauthn and wire; `scripts/` holds a release builder. The
`//go:generate` directive points at `scripts/gen-ts-state`, so
`go generate ./...` — the documented workflow — fails outright.

The states are in `wire.ts`, written by hand, and that file is honest about it:
"Mirrors internal/session/state.go, the source of truth" and "If this file and
the Go one ever disagree, the Go one is right". So the repository contained two
comments contradicting each other about whether a safety mechanism existed —
the second instance of that in this log, after the tmux config's second bell
signal.

This one is worse than a stale comment. A rule that reads as enforced is a rule
nobody re-examines: the reason nothing had drifted is that nothing had changed,
not that anything was checking. And the drift it describes is real — a state
the backend emits and the frontend does not know renders as a session with no
indicator at all.

Delivered the property instead of the mechanism. `TestTypeScriptStatesMatchTheEnum`
reads `wire.ts`, extracts the union members and `STATE_ORDER`, and compares both
against `AllStates` — as a set for the union, in order for `STATE_ORDER`, since
that one claims to match `SortWeight`. No build step, and it fails with the line
to edit. The comment in state.go and red line 3 now describe what is actually
there, including what they used to say.

The other half of red line 3 was true: `TestSessionOrderMatchesStateSortWeight`
exists and does compare the SQL ordering against the enum.

## A renewal that preserves timestamps is never noticed

Read after finding that the check meant to exercise this code was vacuous, so
the code was less verified than it looked. It turns out to be sound: the
constructor loads the pair synchronously and fails at startup on a bad path, so
there is no window where handshakes fail while nothing is loaded yet;
`GetCertificate` resolves per handshake, so a reload lands on the next
connection; and a failed load returns early without touching the stored pair or
its timestamps, so a half-written renewal keeps the old certificate and retries
on the next tick. The property the empty assertion was reaching for holds by
construction.

One real failure mode, reported rather than fixed. `reload` decides whether
anything changed by comparing modification times. A renewal that preserves them
— `cp -p`, a restore from backup, some sync tools — is invisible: the panel
serves the old certificate until it expires and then serves an expired one,
forever, in silence. `reload` returns nil, so nothing is logged.

The consequence is total: every browser refuses the connection, and the cause
is in a place nobody would think to look.

The fix worth making is not hashing the files every minute. It is a expiry
backstop: keep the `NotAfter` of the loaded certificate and reload regardless of
timestamps once it is within a couple of days of expiring. That targets the
outcome that actually matters rather than trying to detect every way a file can
change without saying so.

Not made here because TLS is the worst possible place for an unverified change:
a mistake is not a degraded feature, it is a panel nobody can reach.

## "It verifies" about a file it never opened

The release check runs `systemd-analyze verify` on the installed unit and
treats the absence of a complaint as a pass. With `|| true` swallowing the exit
status, a run where the tool could not start at all — no dbus, a sandbox it
cannot enter, an early error — produced empty output, no complaint, and the
line "the installed unit verifies" about a file it had never opened.

The exit status is no substitute here: this machine has unrelated units
carrying deprecated directives, so a non-zero exit says nothing about ours, and
keying on it would have introduced a failure that has nothing to do with the
panel.

A control does work. The check now writes a unit with an invented directive and
requires `systemd-analyze` to object to *that* before believing its silence
about the real one. If the tool is not functioning in this environment, that is
now what gets reported, instead of a pass.

The technique is the one that rescued the touch-selection work earlier in this
log: when a probe reports "nothing found", run it against something that should
definitely be found. Both times, the control was the difference between a
result and a reassuring noise.

## Two seconds against a sixty-second timer

The TLS check ends by writing rubbish over the certificate and confirming the
listener survives it — keeping the last good pair is the difference between a
warning and an outage during a botched renewal.

It slept two seconds. The file source polls once a minute. So the assertion ran
against a server that had not yet looked at the file: it confirmed that a
process which had noticed nothing was still working. The check passed on the
one run it has had, and would have passed just as happily against a version
that fell over the moment it read a bad certificate.

The tell was in the same file. Thirty lines above, the certificate-swap check
polls for ninety seconds precisely because the reload takes up to a minute —
the correct waiting period was already written down, in the section immediately
before the one that ignored it.

It now watches across seventy-five seconds rather than sampling once at the
end, because an outage that lasts a few seconds in the middle is still an
outage, and it additionally asserts that the fingerprint being served is still
the *previous good one* rather than merely being something.

## Comparisons against a measurement that failed

The same sweep, one level down: assertions whose *threshold* is fine but whose
*measurement* can fail silently. Every comparison against NaN is false, so a
check written as "fail if the number is too big" passes when there is no
number at all.

Three, two of them in the scale check — which is mine, and has never run:

  - `Number(attached) < COUNT`, where the client count comes from a shell
    pipeline that yields `'?'` when it fails. `Number('?')` is NaN, so a failed
    count read as "all sessions attached", which is the one thing that check
    exists to disprove.
  - `loaded - baseline > COUNT * 3`, where both come from `/proc` and are NaN
    when it cannot be read. An unmeasurable run passed.
  - In the stress check's cell-width measurement, two rows of zero width make
    the ratio NaN and the assertion vacuous. A terminal that had not been laid
    out would have been reported as rendering wide characters correctly.

All three now assert the measurement before the threshold. The memory one is a
warning rather than a failure, because `/proc` is legitimately absent on
platforms this builds for — but it says the check did not run instead of
implying it passed.

Worth noticing where these were: the two in the scale check are mine, written
in the same stretch where I was cataloguing this exact fault in other people's
code. Knowing the pattern is not the same as not writing it.

The wide-character section, by contrast, gets it right — it validates the
column count before using it and splits its marker so the echoed command cannot
be mistaken for output. Both of those were lessons from earlier failures in
this log, and they stuck.

## Three product failures that were only warnings

Continuing the sweep for checks that cannot fail, one level up: assertions that
*can* fire but are graded so that the run stays green when they do. WARN does
not affect the exit code, so those are optional.

Three were grading real product failures:

  - The terminal accepting direct keystrokes at phone width. That is the guard
    keeping an input method away from the raw PTY; without it every composition
    keystroke reaches the shell and CJK input produces garbage, which is the
    entire reason the compose box exists.
  - A narrow second viewer with no "take control" affordance. Either it is
    missing — a phone stuck scaled with no way out — or the second viewer took
    the grid merely by arriving, which is the reflow-under-somebody-else's-
    hands the arbitration design exists to prevent.
  - The page scrolling sideways. The scale check already calls this a failure;
    the same defect was a warning here.

All three are now failures. The last full run was green with no warnings, so
this changes nothing today and makes a regression visible tomorrow.

The rest of the warnings are a different thing: "could not find the row to
select", "expected two projects to drag", "the drawer did not open". Those
report that the harness could not set the scene, which is not the product
failing — but it does mean the assertion behind them silently did not run,
while the headline still reads 0 FAIL. They stay warnings because they are
printed and a person reads this output, but they belong in the same family as
everything else in this section: a check that did not happen is easy to mistake
for a check that passed.

## The reconnect check never disconnected anything

stress-check's "losing the connection" section reached for
`window.__vpSockets` — a global the application has never defined, as its own
comment admitted — and then dispatched `offline` and `online` events that
nothing in the client listens for. So the socket was never dropped, and the
section's only real assertion was that an untouched connection was still open
two and a half seconds later.

The third harness bug of this shape, after the phone section that ran with a
mouse and the drop path that was really testing a full reconnect. They are
worth counting: a check that cannot fail is worse than a missing one, because
it is subtracted from the list of things still to worry about.

It uses Playwright's own offline switch now, which genuinely closes the
connection, and asserts what a person would notice: the panel admits it is
disconnected, it comes back by itself, and the scrollback returns.

Then a correction, made before the ink dried. The first version of that section
also asserted the *duplication* fix — that a replayed snapshot replaces the
terminal's contents rather than being appended. It could never have failed:
`.xterm-screen` holds only the viewport, the duplicate copy lands above it, and
this page has just been through a twenty-thousand-line flood, so the buffer is
at its limit and even a height comparison says nothing. The assertion moved to
restart-check, where the session has five lines in it and both copies would sit
in plain view.

Writing an assertion is not the same as writing one that can fail. That is the
second time in two entries — the throttle cap bounded nothing, this counted a
region the evidence could not be in — and both were caught by asking what
values actually reach the branch, rather than by re-reading the code.

## Covering the rest of the blind changes

Two more, both for behaviour that is invisible until it is wrong.

`TestTakingControlAtTheSameSizeStillTellsEveryone` drives a real tmux session
with two subscribers, drains the events each subscription queues on its own,
then transfers ownership *without changing the dimensions* and asserts that
both viewers receive a size event. That event is the only thing carrying who
the controller is — each connection recomputes the flag when forwarding one —
so its absence is precisely the bug: the new owner still being offered a grid
it holds, the old one still believing it drives the session.

`TestHookStatusIsCachedButNotStaleAfterInstalling` pins the cache from both
sides: a second call must *not* re-read the file, and installing through the
panel must drop the cache so the "states are being guessed" notice clears at
once. A cache that is merely fast is easy; one that is fast and never lies
about something the user just did is the part worth a test.

The fourth of those — the detector cleanup on project deletion — could not be
asserted at all from outside the session package: trackers live in an unexported
map with no way to count them. Cleanup nothing can observe is cleanup nobody
notices going missing, so `Detector.Tracked` now exists for exactly that, and
the test deletes a project with two sessions and checks the count returns to
where it started.

It settles for a moment before asserting rather than polling until the number
agrees. Detaching closes the PTY, but a chunk already read can still reach
`OnSignals` after `Forget` has run, and `Observe` would recreate the tracker.
Polling until equal would tolerate that — and would equally tolerate a tracker
that is never released at all, which is the thing being tested.

Still uncovered, and honestly so: the WebSocket deregister-on-drop, the
client's clear-before-replay, and the drag-handle ref. Those need a socket or a
DOM to mean anything.

## Writing the test found the hole in the fix

The throttle change earlier in this log capped what the map may hold and, when
over the cap, dropped entries older than the forget window and then older than
half of it. Writing its test was enough to see that this bounds nothing in the
case it was written for: in a fast spray across addresses *every* entry is
newer than both cutoffs, so nothing is dropped, the map grows anyway, and —
because it is permanently over the cap — every request now walks all of it.
Strictly worse than before the change, in exactly the scenario the change was
about. The comment claiming a bounded worst case was wrong too.

It evicts by rank now: sort by last-seen, drop the oldest down to the cap. That
runs only on calls that find the map over its bound, and it actually brings it
back under. The test asserts both halves — the map stays capped, and the source
that is guessing *right now* is still remembered, because eviction that dropped
the newest end would leave an active attacker unthrottled.

Worth recording as a method rather than an incident: the fix was reasoned about
carefully, reviewed, written up, and wrong. What caught it was trying to state
the property as a test, which forced the question "what values actually reach
this branch" — and the answer was none of them.

## Tests for the changes that could not be run

Three, covering the two blind fixes most likely to be wrong in a way that
compiles: `Config.BindHost`, which decides where hook reports are posted, and
the static-directory handler, which answered 404 for every relative path.

They exist so that `make check` says something about these changes beyond "it
builds". They have not been run either — but a test that fails is a far better
outcome than a change nobody ever exercised.

One deliberate choice in the containment case: it asserts that the file above
the root is never *served*, not that a particular status comes back.
`path.Clean` against "/" folds `../` into a name under the root, so the request
ends at the SPA fallback and returns 200 with the app in it. Asserting 403
would have failed against correct behaviour — the same mistake made twice in
the upload tests earlier in this log, where "a climbing path is clamped, not
rejected" had to be learned from a red test.

## The status page does not watch anything

The brief asked for a settings page that lets you observe how the backend is
running. It fetches once, on mount, and never again.

Everything on it is a live number — uptime, sessions, how many are attached,
how many viewers, the database size. Leave the page open for ten minutes and
the uptime still reads whatever it was when you opened it, with nothing on
screen saying when the reading was taken. Not wrong data: a stale snapshot
presented as current status, which is the opposite of observing.

Reporting rather than fixing, because the interval is a judgement call.
`/api/settings` shells out to tmux twice per request — `Version` and `List` —
so polling every second is waste. The answer is a slower interval, not none.
Five seconds while the modal is open would cost little and make the page mean
what it says. Uptime could tick client-side from the fetched value and cost
nothing at all.

Smaller and concrete, in the same handler: `dbBytes` stats only
`vibepanel.db`. Under WAL there is also `-wal` and `-shm`, and the write-ahead
log is routinely larger than the database early on — 310 KiB against 4 KiB in
one screenshot from these checks. The panel under-reports its own disk usage,
on the page whose job is to report it.

## The same hot path, still reading the user's home directory

Fixing the script rewrite above only removed half of what `stateIsGuessed` was
doing on every snapshot. The other half was still there: `hooks.Inspect` reads
`~/.claude/settings.json`, parses it, and builds two JSON snippet strings with
`Sprintf` — of which the caller uses exactly one boolean, `Installed`.

So every state broadcast read and parsed a file in the user's home directory.
Twenty-odd sessions moving between working and done is several times a second,
indefinitely.

Cached now, behind a ten-second TTL, with the panel's own install and uninstall
dropping the cache so the "states are being guessed" notice does not keep
telling somebody to install hooks they just installed. Both handlers also
broadcast now, so the notice clears in every open window rather than in the one
that pressed the button.

The pattern worth naming, since this is the third instance in two entries: work
that is obviously cheap when you write it, placed on a path that turns out to
run on every broadcast. `snapshot()` is that path, and anything reaching it
should be assumed to run several times a second.

## A file rewritten on every state broadcast, while agents were executing it

Also mine, and this one was doing damage rather than merely missing.

`InstallScript` wrote the reporter script unconditionally — `MkdirAll` plus
`WriteFile`, no comparison, every call. Callers treat it as "tell me where the
script is", and the `stateIsGuessed` check added earlier in this log calls it
from `snapshot()`. Snapshots are built for every state broadcast: a session
changing state, a session created or renamed, a note saved. With a couple of
dozen sessions moving between working and done, that is the same kilobyte
rewritten several times a second, forever.

The I/O is the smaller half. `os.WriteFile` truncates before it writes, and
this particular file is executed by agents' hooks at moments nobody controls. A
shell reads a script incrementally, so a hook that fires during the truncate
can read half a file — failing in a way that would be attributed to almost
anything before the panel's own housekeeping.

Now it compares first and returns when the content already matches, which is
almost always, and writes through a temp file and a rename when it does not.
Rename swaps the name; anything already executing keeps the inode it started
with. `TestInstallIsIdempotentAndExecutable` still describes what it does: same
path twice, owner-only, executable.

Two things this is the second instance of. Rewriting a file to change nothing —
the same fault as the settings installer earlier in this log, found the same
way, one file over. And `os.WriteFile` applying its mode only on creation,
which the temp file has to be chmodded explicitly for. Worth treating as a
pattern rather than two coincidences.

Related and left alone: `handleHooksStatus` is a GET that installs the script
as a side effect. Harmless now that installing is a no-op when nothing changes,
but a GET that writes is still the wrong shape.

## The exit indicator never reaches the terminals most likely to exit

A hole in the change made earlier in this same log — worth recording as such
rather than as a discovery about somebody else's code.

`exited` and `exitStatus` were added to the session, given two shapes that a
colour-blind reader can tell apart, and wired into the sidebar row and the
header above the main terminal. Scratch terminals appear in neither.
`App.tsx` filters children out of the sidebar list, and `BottomTerminals`
renders no state at all: no glyph, no exit code, no restart.

Which means the indicator is missing from exactly the place a dead pane is most
likely to be met. A bottom terminal is where a one-off command gets run and
where somebody types `exit`. What they get is a tab that looks live, a pane
frozen on tmux's "Pane is dead" line, and no explanation anywhere in the
interface.

The restart path is unreachable for them too, which is the part that stings:
restarting in place was built to keep the pane and its scrollback, and the only
recovery available in the strip is closing the tab and opening a new one, which
throws both away.

The fix is small and entirely visual — the exit glyph beside the tab name, the
status in the tab's tooltip, and a restart control next to the close button —
which is why it is not being made blind. It needs the same render-check
treatment the sidebar version got: two shapes that differ, and a restart that
visibly brings the tab back.

While reading that file: its active tab is derived rather than stored *and* the
component is keyed by the parent session, so a switch cannot leave a selection
pointing at a terminal belonging to something else. Two defences where one
would have done, and both correct.

## Renaming does not exist on a phone

`InlineName` enters editing on `onDoubleClick` and nothing else. A phone has no
double click — a double tap is the zoom gesture, and these labels set no
`touch-action`, so the browser takes it.

The same shape as the hover-revealed controls fixed earlier in this log: an
interaction that does not exist on the platform the panel was explicitly asked
to support. It sits awkwardly against the component's own comment, which says
renaming is "the single most requested thing this panel does — the whole reason
it exists is that tabs called 'bash' are useless — so it has to be two clicks
away, not behind a dialog". On a phone it is behind an unbounded number of
clicks.

The mobile section of the render check never tries to rename anything, which is
why this survived. The touch context added for the reachability check is the
right place for it: long-press a session row's name, expect an input, type,
expect the new name in the sidebar.

Not implemented, because the fix is a new gesture — press and hold without
moving, the same shape as `touchSelect` — and an untested gesture handler is
exactly the kind of thing that quietly fights with scrolling or with the
existing double click. It wants a real touch context to be believed, which this
session could not run.

The rest of the component is sound: an empty or unchanged name is not
committed, Escape discards without the blur handler firing on unmount, and key
events are stopped so they cannot reach the terminal underneath.

## Two attaches for one session, and the invariant everything rests on

The most serious thing this read-only pass turned up. Finding only: it is core
concurrency and there was no way to run the race detector.

`Attach` checks `m.live` under the lock, releases it, then does the slow work —
`Has`, `CaptureScrollback`, `pty.StartWithSize` — and registers at the end. Two
callers arriving in that window both build a `Live` and the second overwrites
the first.

Not theoretical. There are two callers by design: the poller attaches every
session it does not already see, and a browser subscribing attaches the one it
is opening. Opening a session that was just created, or two people opening the
same one, lands squarely in the gap — and the gap is two tmux CLI executions
and a fork, so tens of milliseconds, not nanoseconds.

Three consequences, in order of how much they matter:

1. Two `tmux attach` clients on one session. `vibepanel.conf` uses
   `window-size latest` — because `manual` segfaults tmux 3.6 — and the entire
   justification for that is the comment beside it: "The panel attaches exactly
   one client per session and drives its size from Go, so 'latest' is always
   us." With two clients the window follows whichever was last active, and the
   arbitration the panel enforces in Go is being contradicted underneath it.
2. The first `Live` is orphaned and keeps running: PTY, tmux client process,
   2 MiB ring and goroutine, for the life of the process. `m.live` no longer
   points at it, so even `Detach` cannot reach it. It self-heals only on
   restart.
3. Viewers already on the orphan keep working, because it is attached to the
   same tmux session and the content is identical. So the symptom is not
   "broken" but "leaking slowly and occasionally fighting over the grid",
   which is the hardest kind to attribute.

The fix wants per-session granularity: an in-flight map keyed by session id, so
a second caller waits for the first instead of starting its own. Holding `m.mu`
across the slow work would serialise every attach at startup — two executions
each, times however many sessions — and block `Get` and `LiveIDs` along with
them.

## PublicURL says it is never used for anything security-sensitive

It is. `webAuthn()` passes it as `RPOrigins`, which is the list the library
checks an assertion's origin against — one of the three things standing between
a passkey and a phishing site.

    // PublicURL renders the address a user should open. Best-effort: it is
    // used in log lines and the setup message, never for anything
    // security-sensitive.

One of those two is wrong, and the comment is the cheaper one to trust by
mistake. It is built from `--domain` and the listen port, which is right for
the deployment this was written for — the panel terminating its own TLS on
`direct.example.com:8443`. It is wrong the moment a reverse proxy is in front:
the browser's origin is then the proxy's, `https://panel.example.com` on 443,
and every assertion is rejected against an origin nobody configured. The error
surfaces from inside the library, so the reason is findable only by reading its
source.

Worth an explicit setting rather than a derived one — the origin a browser sees
is not something the server can infer, and guessing it silently is how this
ends up being debugged at two in the morning.

## Another unauthenticated endpoint that allocates

`passkey/login/begin` has no throttle. It must be reachable without a session —
it is the sign-in path — and each call stores a challenge for three minutes and
sweeps the whole challenge map while doing it.

The same shape as the audit-write finding: something an anonymous caller can
make the panel allocate and walk, repeatedly, for the cost of one request. It
is self-limiting where the audit log is not, because entries expire in three
minutes, so the steady state is bounded by the request rate rather than growing
forever. Still worth the same treatment when that one is fixed.

## The pump guarantees half of what its comment claims

Finding only. The fix is in the hottest function in the program and there was
no way to run the race detector against it.

    ring.Write(chunk)          // ring has its own lock; l.mu is not held
    scanner.feed / drain
    terminalQueryReplies → l.ptmx.Write(reply)
    l.broadcast(chunk)         // takes l.mu

`Subscribe` snapshots the ring and registers the subscriber under `l.mu`, so
the two join up exactly — against anything else that holds `l.mu`. The pump
does not hold it while writing the ring. A viewer that subscribes between those
two lines therefore finds the chunk already in its snapshot *and* receives it
again live.

The comment says the ordering means a viewer "either sees the chunk in its
replay or receives it live — never neither". That half is true. The other half
is not, and duplication is the one that shows: a line printed twice as a
session opens, which nobody reports because everybody blames the terminal.

The window matters more than its size suggests. It spans the scanner, the
capability-query check, and `l.ptmx.Write(reply)` — a write to a PTY, which can
block when the buffer is full. So it widens under exactly the load that makes
subscribing slow.

The fix is to merge the two critical sections: take `l.mu` once, write the
ring, feed the scanner, and deliver to subscribers before releasing it.
Delivery is already non-blocking — a full queue drops the viewer rather than
waiting — so holding the lock across it is bounded. The reply write must stay
outside, because that one can block. `broadcast` would split into a locked
inner form and the existing wrapper.

## Every rejected request costs a database write

Finding only — the fix needs a policy decision and it sits in the auth path.

`RequireAuth` audits each request it refuses on the allowlist, and `audit` is a
synchronous insert. Nothing on that path is throttled: the throttle is attached
to `/api/auth/login`, and the allowlist check runs before it — and no other
endpoint has one at all. Nothing prunes `audit_log` either; `RecentAudit` reads
the newest hundred rows and the rest accumulate forever.

The asymmetry is the point. Reaching argon2 requires getting past the
allowlist; reaching a database write requires a TCP connection and a request to
any endpoint at all. SQLite takes one write lock for the whole database, and
the panel's real work — state changes, saving a note — queues behind it. That
is a cheaper lever than password guessing, and it is available to anyone who
can open the port.

Both halves want fixing:

  - Do not write a row per rejected request. Once per source per minute, with
    a count, says the same thing: "10.0.0.5 was refused 4,000 times in the last
    hour" is more useful than four thousand rows, and it is one write. The
    suppression window is a judgement call, which is why it is not made here.
  - Give `audit_log` a retention rule — a row count or an age — applied
    wherever `PurgeExpiredAuthSessions` ends up being called from, since that
    function needs a home for the same reason.

Worth saying plainly: this is not a way in. It is a way to make the panel slow
for the person who owns it, which for a tool whose whole promise is "your
sessions are always there" is its own kind of failure.

## A comment promising a safety net that was never wired up

`vibepanel.conf` said `monitor-bell on` was "required for window_bell_flag to
be maintained, which the state detector polls as a second bell signal alongside
the raw \007". Nothing polls it. `Info.Bell` in the tmux wrapper says the
opposite in as many words — always false under this configuration, do not build
on it — and the observation the poller hands the detector carries only `Dead`
and `ShellOnly`.

Worth more than tidiness. Forty lines above, the same file explains at length
that `bell-action` must not be "none" because the raw `\007` would stop
arriving. Someone who read on and found a second, independent signal described
might reasonably conclude there was a fallback. There is not: that one control
character is the only thing standing between the panel and never knowing an
agent wants a human.

Comment-only. Two comments in the same repository disagreeing about whether a
mechanism exists is a bug in the thing they are both describing.

## A fast drag could commit the wrong row

`useDragList` tracked the gesture in React state and read it back in the
pointerup handler. Those are different clocks. The handler closes over the
state from the render it was created in, and a release that lands before React
has flushed the update from the pointermove just before it sees the position
from one move ago — or `null`, if the whole drag was quick enough that no
render happened at all. A flick is precisely the gesture where those two events
arrive together.

Not observed, reasoned from the code — but the file already carries a comment
about the handler seeing "the state from the render that started it", written
when the same hazard produced a double commit, so this is the second symptom of
something that was half-diagnosed the first time.

The drag state now lives in a ref as well: state for drawing, ref for deciding.
That also takes `state` out of the release handler's dependencies, so it is
built once per gesture instead of once per pointermove.

The render check drives this with synthetic pointer events slow enough that the
race cannot happen, which is why it has stayed green. A case that moves and
releases in the same frame would be the one worth adding.

## Hooks posted to an address nothing was listening on

`report.sh` itself is fine — it exits immediately outside a panel session,
validates the state against a whitelist before sending anything, caps itself at
two seconds so it can never make an agent wait, and prints nothing whatever
happens. That last property is the one that makes the bug below invisible.

It posts to `VIBEPANEL_URL`, which `loopbackURL` hard-coded to
`127.0.0.1:<port>`. Correct for the default `--addr :8443`, which listens
everywhere including loopback. Not correct for `--addr 192.168.8.20:8443`,
which is an ordinary way to narrow what a panel is exposed on: then nothing is
listening on 127.0.0.1, every hook POST is refused, and the script swallows the
error because that is what it is built to do.

What the user sees: they press "install hooks", the settings page says
installed, and the states stay wrong — with the "states are being guessed"
notice gone, because that notice checks whether the script is installed rather
than whether anything ever arrived. Two subsystems each reporting success for a
path that does not work end to end.

The URL now follows the bind address, falling back to loopback only for the
wildcard forms. `Config.BindHost` is the new accessor, and it treats "",
"0.0.0.0" and "::" as "everywhere".

Worth doing later, and not done here: `doctor` could post to that URL and say
whether anything answers. Every failure in this chain is silent by design, so
the only way to know it works is to try it.

## The throttle walked its whole map on every attempt

The backoff itself is right: exponential, capped, per source, delay rather than
lockout, overflow-guarded. What was wrong was its bookkeeping. `sweep` ran on
every `Delay` and every `Fail`, iterating the entire map under the mutex that
every sign-in takes.

The size of that map is chosen by whoever is attacking. Keys are source
addresses, and since the `RealIP` fix they are real peer addresses — but one
host with a routed IPv6 prefix can still present a new one per request. So the
component whose job is surviving a guessing attack degraded in proportion to
the attack, and took real sign-ins down with it, because they queue behind the
same lock.

Now it sweeps on a timer (a quarter of the forget window) or when the map has
grown past a cap, whichever comes first. Over the cap it drops the oldest half
of the window as well, which does let a source that can spray addresses shorten
its own backoff — but the forget window already offers that after fifteen quiet
minutes, and the alternative is memory with no bound. The worst case is now a
few thousand map iterations rather than however many the attacker felt like.

Checked against all six existing tests by reading them: the first call always
sweeps because `lastSweep` starts at the zero time, which is what
`TestQuietSourcesAreForgotten` depends on.

An earlier draft of this entry claimed the passkey login path never records a
failure with the throttle. That was wrong: it calls `failLogin`, which calls
`Throttle.Fail` before auditing. `Delay`, `Fail` and `Succeed` are all present
on that path. Corrected here rather than deleted, because a wrong claim about a
security control is exactly the kind of thing that gets quoted later.

What is true, and is a different endpoint: `passkey/login/begin` has no
throttle at all. See below.

## There is no way to change the password

Not implemented, and not a small omission. The password set in the first-run
wizard is the only one there will ever be: no endpoint, no settings form, no
CLI command. `grep -rn "SetPasswordHash"` finds the store function and nothing
that calls it.

Three functions in `internal/store/auth.go` have no callers anywhere:
`SetPasswordHash`, `DeleteUserAuthSessions` and `PurgeExpiredAuthSessions`. The
store layer was built for this and nothing above it was. One of them documents
behaviour that does not happen — "Used when the password changes: the point of
changing it is that whoever had the old one stops having access" — which is
worse than no comment, because it reads as a description of the system.

Why it matters more here than in most projects: this panel is meant to be
reachable from the internet on a non-standard port, and the thing behind the
password is a shell on the user's machine with their keys and their dotfiles. A
password chosen in a hurry during setup, or one typed into the wrong window
once, cannot be replaced without editing SQLite by hand. Passkeys can be added
and removed in settings; the credential everything falls back to cannot.

It reads as finished because signing in works.

The shape of the fix, for a session that can run the tests:

  - `POST /api/auth/password` behind `RequireAuth`, taking the current password
    and the new one. Verify the current one with the same argon2id path as
    login, including `DummyVerify` on the failure branch, and apply the same
    throttle — an endpoint that checks a password is an endpoint that can be
    guessed at, and this one is reachable with a stolen session cookie.
  - On success: `SetPasswordHash`, then `DeleteUserAuthSessions`, then issue a
    fresh session for the caller. Changing a password whose whole purpose is
    revocation, and leaving every other browser signed in, is the failure mode
    the dead function was written to prevent.
  - Audit it. `password_changed` belongs in the same log as the sign-ins.
  - `PurgeExpiredAuthSessions` wants wiring into the same periodic work as the
    tmux poller, or deleting. Expired rows are already refused at lookup, so
    this is tidiness rather than exposure — but a maintenance function nobody
    calls is a claim that maintenance happens.
  - The settings page has the passkey list to sit next to, and the same
    "current password" field pattern it already needs for re-authentication.

## A relative --static-dir served 404 for everything

The flag exists so a frontend change does not need a Go rebuild. It is in the
README's flag table and nowhere else — no test, no Makefile target, no dev
script — and it does not work.

`spaHandler` kept the string it was given and compared it against a path that
`filepath.Abs` had already resolved:

    abs := filepath.Abs(filepath.Join("web/dist", "index.html"))
        → /home/…/web/dist/index.html
    strings.HasPrefix(abs, filepath.Clean("web/dist")+"/")   → always false
    abs != filepath.Clean("web/dist")                        → always true
    → http.NotFound

So `--static-dir web/dist`, which is what anyone would type, answers 404 for
every request while the files sit right there. An absolute path works, which is
presumably how it was tried the one time it was tried.

Resolved to an absolute path once at construction instead, and the containment
check simplified to what it was trying to say: the root itself, or something
under it, and nothing else. The check was decorative anyway — `os.DirFS` already
refuses `..` — but a decorative check that rejects everything is worse than none.

And `writeSettings` wrote 0600 unconditionally. A user whose `settings.json` was
0644 got it silently tightened by pressing a button about hooks. Tightening is
benign, but a tool that changes the permissions on your dotfile without saying
so is the same trust problem as one that rewrites its contents. It keeps the
mode it finds now, and only new files start at 0600. The temp file is chmodded
explicitly as well: `os.WriteFile` applies its mode only when it creates the
file, so a leftover temp from an earlier crash would have carried its own
permissions across the rename and onto the real file.

## What to do with all of this

The entries above are in the order they were found, which is the wrong order to
act in. This is the reading list, and it is the last thing written in this
stretch.

**First, before anything else.** Nothing above has been compiled or run — the
tooling was unavailable for the whole of it. Roughly forty source files changed
and forty new assertions were written blind.

    make check
    make render-check && make stress-check && make restart-check
    make tls-check          # slow: it waits out a certificate reload
    make release-check
    cd web && npm run check:scale

The two test files most likely to fail to compile are the newest —
`internal/ws/protocol_test.go` and `web/src/styles.test.ts` — because both
parse another file's source and neither has ever been executed.

**Then, in this order, the things that are wrong rather than missing:**

*(1, 2 and 3 were fixed in the verification pass below — each with a test that
was checked against the unfixed code. 4 and 5 are still open.)*

1. ~~The `Attach` race.~~ Fixed. It was worse than described: eight concurrent
   callers produced eight tmux clients.
2. ~~The pump's duplicate delivery.~~ Fixed, and measured — the real window is
   nanoseconds wide.
3. ~~Sessions whose tmux session has vanished still show their last state.~~
   Fixed: marked as gone, in both the poller and the startup reconcile.
4. ~~Sessions created with `vibepanel session new` can never report state.~~
   Fixed. It was two of *three* variables, and neither of the two it had was
   one that mattered.
5. ~~A renewal that preserves timestamps is never noticed.~~ Fixed, along with
   the half that detection alone could never cover: a certificate nobody
   renewed now says so, in the log and on the settings page.

**Then the two that need a product decision before any code:**

*(Both decided and implemented — see "The two product decisions, decided" near
the end of this log, which records the reasoning so either can be reversed
without archaeology.)*

- ~~What a quiet agent means.~~ Silence no longer means finished; the
  foreground process decides. The flicker went with it.
- ~~Whether there is a way to change the password.~~ Yes, from the settings
  page, requiring the current one and signing every other browser out.

**The gaps against what was asked for are closed.** Ctrl+C from a phone, with
an end-to-end assertion; renaming from a phone, by press-and-hold; identical
sidebar rows, qualified by the directory above; the settings page, polling
while open; the exit indicator on the bottom strip, along with the tab strip
reordering itself under the pointer.

What remains is the two product decisions below — what a quiet agent means, and
whether a password can be changed — and neither is a defect.

Everything else in this log is either already fixed above or small enough to
pick up when its file is next open.

---

## The verification pass, run

Everything in the list above was executed. The short version: the Go suite,
the frontend units, `make check` with the race detector, and four of the five
browser checks now pass, and getting there cost eight fixes — five of them to
code written during the stretch when nothing could be run, and two of those to
the fixes themselves rather than to what they were fixing.

That ratio is the finding. A fix written against a reading of the code and
never executed is not a fix; it is a hypothesis with good handwriting.

**`go vet` first, and it caught a compile error** in one of the new tests — an
unused variable. Nothing else in roughly forty edited files failed to build,
which is a better result than it deserved.

**The throttle cap was off by one, permanently.** `Fail` swept, then inserted,
so the map settled at `maxEntries + 1` and stayed there: the bound was checked
on entry and exceeded on the way out, every single call. Bounded, but not at
the number written down. The rank eviction moved into its own method and `Fail`
now enforces the cap after its own mutation, which is the only place the count
is final.

**The detector leak was not a missing `Forget`.** Both calls fired; the
trackers came back anyway. The poller calls `Evaluate` for every row it lists,
and a poll landing between the handler's `Forget` and its `DeleteProject`
rebuilds exactly what was just dropped. No amount of care in the delete paths
fixes that, because the race is with a different goroutine reading a list that
is still true. So cleanup is now driven by the authoritative list —
`Detector.Retain(ids)` on every pass — and `Forget` stays as the immediate
case. This is also the only thing that will ever clean up after a session that
vanished without going through a handler.

Finding it turned up something worse on the way. `pollOnce` opened with

    if len(infos) == 0 { return nil }

an optimisation to skip building an empty map, which skipped the entire
reconciliation pass instead — in the one state where reconciliation is the only
thing that can help: every session gone. A panel that had just lost its tmux
server did no reconciling at all. The loop does nothing on an empty list
anyway; it was never the loop that mattered.

**`Live.close()` did not wait for its pump.** Callers were told the attachment
had ended while a chunk already read was still in flight, so output could still
be broadcast — and `OnSignals` still fired — for a session the caller had
already deleted. It waits now, bounded at two seconds so that one wedged PTY
cannot hang a shutdown that detaches every session in turn. This was not the
cause of the leak above; it was found while looking for it, and is real.

**The render check had been looking for a session that never existed.** Six
locators searched for a row titled `scratchpad`. Nothing names it that: a shell
is named automatically after the directory it sits in, the project's path is
`process.cwd()`, and so the shell was called `web` — the name changing with
wherever the harness was run from while the name being searched for did not.
Every step after the first typed into whichever session happened to be
selected, and the failures pointed everywhere except at the cause. The session
is now named explicitly at creation, and the "could not find the shell row"
`WARN` is a `FAIL`: a run that cannot find the shell is not a run with one
thing missing, it is a run whose remaining results mean nothing.

**The panel lied about being connected.** The client pinged every 25 seconds
and never looked for the answer, and `send()` on a socket whose network has
gone is not an error — it buffers. So the status dot stayed green through a
dropped connection, indefinitely. For this application that is the worst
available failure: the list of who needs you quietly stops updating and nothing
looks wrong.

Two mechanisms, because they cover different failures:

- The `offline` and `online` events. The browser knows before we can — a phone
  leaving coverage, a lid closing — and reacting is immediate. `online` also
  reconnects at once rather than sitting out a backoff measured against a
  problem that has gone.
- A silence timeout, sixty seconds, as the backstop for a connection that dies
  without the operating system noticing: a NAT timeout, a wedged proxy. It has
  to be an application-level exchange, because the protocol ping the server
  sends every thirty seconds is answered by the browser itself and is invisible
  to script — a socket can be idle at this layer while the network under it has
  been gone for minutes.

Sixty seconds is a deliberate trade against a phone's radio, which is what pays
for noticing faster. The case that actually happens to a phone is covered by
the first mechanism, immediately.

**Ctrl+C from a phone now exists**, and the harness had been saying so every
run. The bar had no letter key, so the sticky modifier had nothing in
`0x40-0x5f` to apply to; it latched, un-latched, and could not alter a byte.
There is now a `^C` key on the row that never scrolls — one tap, not two, for
the most urgent thing in the product — and the `WARN` that documented the gap
is an end-to-end assertion: run `sleep 120`, tap it, and require the shell to
come back.

Underneath it was a second defect that the first was hiding. Every raw sequence
cleared both modifiers, so arming ctrl and tapping `y` sent a plain `y` — a
*yes* to whatever the agent was asking — while the user believed they had just
interrupted it. A modifier that disappears without doing anything is worse than
not having one. Raw keys no longer consume it.

`pageUp` and `pageDown` had been sitting in `KEY_SEQUENCES` since the file was
written with nothing rendering them. Now bound, on the scrolling row.

**The sidebar had no safe-area inset in any of its three forms.** It starts at
y=0 as a docked column, as a collapsed rail and as the narrow-screen overlay,
and only the main header was ever given one — so on a phone with the status bar
over the page, the control that closes the drawer sat under the clock. That is
the same failure as the one fixed earlier by splitting docked from drawer-open,
arriving by a different route. `vp-safe-top` could not be reused: it carries
the main header's fixed height and would have imposed it on a header that sizes
itself, so `.vp-safe-pad-top` adds padding only.

**Three things found by reading, while the checks ran:**

- `"types": ["vite/client"]` sat outside `compilerOptions` in
  `tsconfig.app.json`, where tsc ignores it. The Vite ambient types have never
  been applied; nothing has needed them yet, which is why nobody noticed.
- The theme-block test's property scan was line-first, and had two holes
  pointing opposite ways: a component rule written on one line inside a theme
  block was invisible to it, and a bare `a:hover {` on its own line would have
  been reported as a declaration named `a`. It decides by structure now —
  whether the colon is followed by `;` or by `{`. A check standing in for a red
  line has to be right in both directions or the rule is worth less than it
  looks.
- The settings modal opens with `py-8`, 32px, which is less than the ~44px
  status bar inset of a notched phone. Only the card's corner is affected, no
  control, and confirming it needs hardware — so it is reported, not changed.

**One detour worth recording because it nearly went unnoticed.** Moving the
three source-reading tests off `node:fs` and onto Vite's `?raw` looked clean
and removed a dependency. Vitest runs with `css: false`, so a CSS import — raw
or not — resolves to an empty stub, and the whole theme-block test passed
against an empty string. What caught it was the assertion written for exactly
this: *if this fails the rest proves nothing: a parser that finds no blocks
finds no violations either.* The guard cost three lines and saved the test from
becoming decorative. `@types/node` was added instead, and the guard is now the
single most valuable line in that file.

**Still open**, unchanged from the list above except where noted: the `Attach`
race; the pump's duplicate delivery; sessions whose tmux session has vanished
showing their last state (the poller now runs in that state, but the loop still
skips rows it cannot see, so the display is unchanged); CLI-created sessions
that cannot report; timestamp-preserving certificate renewal; the two product
decisions; renaming from a phone; identical sidebar rows; the settings page not
refreshing; the exit indicator missing from the bottom strip.

### Three harness defects hiding a fix that worked

The `^C` key was correct the first time it was written. It took three more runs
to see that, and none of the three failures were in the code under test.

1. The new assertion required the marker to appear **twice** — the pattern used
   elsewhere in the file, where a command and its output both contain it. But
   this marker is split across a quote precisely so the echoed command line
   *cannot* contain it, which is what makes a single occurrence mean "the shell
   really ran this". Asking for two made an assertion that could not pass
   however well the key worked.
2. The prompt was dirty. The checks above it deliberately leave things on the
   input line — a stray `1` from the modifier test, and now an Escape from the
   one this round added — and readline treats Escape as a meta prefix, so what
   follows is swallowed rather than run. The run spent its whole budget waiting
   for a `sleep` that had never been submitted.
3. `compose-newline` is a **sticky toggle**, not a one-shot. The Enter-key
   check flips it off to send without a newline and never flipped it back, so
   every send after that point in the run arrived without an Enter. Commands
   piled up unexecuted on the input line while the checks depending on them
   reported that the feature under test had failed.

The third is the one worth keeping. A harness that leaves a mode behind pays
none of the cost itself: it is paid by whatever is added after it, by someone
who has no reason to suspect the state they inherited. It is fixed where it is
caused — the toggle is restored and the restoration is asserted — rather than
worked around where it hurt.

The diagnostic that broke it open was printing the terminal's last six lines in
the failure message. `KEYBAR$ 1^[sleep 120^C` says everything at a glance: the
stray character, the escape, the command that was never submitted, and — in
that trailing `^C` — proof that the interrupt had been landing the whole time.
Failure messages that carry the state they judged are worth what they cost.

### Test debris on the machine this project exists not to disturb

`/tmp/tmux-1000/` held a tmux server from a render check that had died six
hours earlier, still holding eight sessions: several shells, an htop, and a
process called `claude` — which turned out to be the harness's own symlink to
`sleep`, made so that the automatic-naming path can be tested without spending
anybody's API quota. That much was already right.

Every check registers cleanup on SIGINT, SIGTERM and SIGHUP, which covers a
`timeout` and a Ctrl-C, and cannot cover SIGKILL. So the checks now sweep at
startup: any socket named `<harness prefix>-<pid>` whose pid is no longer
running is a server nobody owns, and it is killed before the run begins.

Safety is in the name. A candidate has to match a harness prefix *and* end in
digits — the panel's own default socket is `vibepanel`, with nothing after it —
and the pid has to be gone. A reused pid means the socket is skipped, which
errs toward leaving debris rather than killing something live.

It removed the six-hour-old server on its first run, which is the only way this
kind of fix should be reported.

### The two races, now that they can be tested

Both were on the "wrong, not missing" list, both had been reasoned about and
neither had been reproduced. With the suite running again they could be.

**The `Attach` race is real and it is not subtle.** Eight goroutines calling
`Attach` for one session produced **eight tmux clients** and seven orphaned
PTYs. `Attach` checked the map, released the lock, and then spent a hundred
milliseconds in `capture-pane` and `pty.Start` before writing its result back;
every caller walked through that window. Only the last one is in the map, and
the other seven stay attached with nothing owning them.

The consequence is not a leak, it is the size arbitration. `window-size latest`
makes the grid follow whichever client resized most recently, so a client the
panel has forgotten about turns every resize into a fight and reflows the pane
under a running TUI — the single failure the whole single-client design exists
to prevent. And the two callers are not hypothetical: the poller attaches every
live session while a subscribe attaches on demand.

Fixed by claiming the session before doing the slow part — a per-session
channel that later callers wait on, then round again to find either the
finished attachment or their own turn. The test asks tmux for the client count
rather than asking the manager, because a client the manager has forgotten is
exactly the one that does the damage.

**The duplicate delivery is real and it is very rare.** The ring was written
before the broadcast with the lock released in between, so a viewer registering
in that gap took a snapshot already containing the chunk and was then sent it
again — a line printed twice, indistinguishable from the program having done
it. The comment there called it the deliberate choice, on the grounds that the
other order would lose the chunk instead. That was a false choice: holding one
lock across the ring write and the delivery, which is the same lock `Subscribe`
already takes for its snapshot, gives neither loss nor duplication.

The test for it is worth more than the fix, and only after it was rewritten
twice. The first version printed four hundred lines — which a shell finishes
inside a second — and then subscribed forty times to a session that had gone
quiet. It passed with the bug deliberately put back. The second version keeps
output flowing and gets seven hundred subscribes in; it *also* passes with the
bug put back, because the real window is the few instructions between the ring
write and the lock. Widen that window by fifty microseconds and it fails on the
second attempt.

So the honest description, which is now the comment on the test: it catches a
regression that reintroduces the gap in any form somebody would actually write,
and it cannot certify the absence of the nanosecond-wide original — which is
also the measure of how rarely that original fired. The fix is correct by
construction; the test is there to notice if someone takes it apart.

Two rounds of mutation testing in one session, both of which found a check that
could not fail. The habit earns its keep: writing the test is the first half,
and breaking the code on purpose to watch it fail is the half that decides
whether the first half meant anything.

### A verdict that could vanish

The scale check was run three times and produced a different amount of output
each time — once nothing at all, once four lines, once five and no verdict. It
read like a crash, and two rounds were spent looking for one.

Node's stdout is asynchronous when it is a pipe, which it is under `make`, in
CI, and anywhere the output is captured. `process.exit()` abandons whatever has
not been flushed, and the findings and the verdict are the last thing printed —
so they are the first thing lost. One of those runs reported exit code 0 with
an empty output file, which is the worst possible combination: a check that
passed and said nothing, indistinguishable from a check that died.

All five harnesses ended that way. They now flush and then exit:

    await new Promise((resolve) => process.stdout.write('', resolve))
    process.exit(fails ? 1 : 0)

Setting only `process.exitCode` would also flush, but it waits for the event
loop to drain, and one stray handle from a browser or a child process would
hang the check rather than ending it. Flush, then exit deliberately.

A check whose verdict can disappear is worse than no check, because its silence
is read as success.

### Measuring the machine the measurement runs on

The scale check reported "sidebar showed 0/24 rows" three runs in a row, with a
blank screenshot, and none of it was true of the panel.

Ten of my own scale checks were running at once. Each starts a panel and
twenty-four sessions, and the earlier runs had not exited when their output
appeared to stop — the missing verdict (above) had been read as the process
dying. So the machine was carrying five panels and something like a hundred and
twenty tmux sessions while the check measured how quickly a sidebar populates.
It measured that correctly.

Two lessons, and the second is the useful one:

- `pgrep -f <pattern>` matches the shell running the pgrep, because the pattern
  is in its own command line. That is how "no processes" was concluded twice
  while ten were running, and how a later `pkill -f` killed its own shell. Use
  `ps -eo args | awk '/pattern/ && !/awk/'`, or a bracket class.
- A check that measures throughput, latency or "how long until X appears" is
  measuring the machine as much as the code. Running one while five others are
  running does not produce a weaker signal, it produces a confident wrong one —
  and the failure it invents looks exactly like a real bug, right down to the
  screenshot.

The sweep grew a second half while chasing this. Killing the stale tmux server
leaves the panel the dead run had started, still holding a port and a data
directory. It cannot be found by its command line — the harness starts a bare
`vibepanel serve` with everything in the environment, so matching the name
would put a real panel in range — but it can be found precisely by its
environment: the process to kill is the one told to use *this* socket, the one
whose owner has just been established as gone. A real panel uses the default
socket name, which never matches a harness prefix.

Verified rather than reasoned about: a tmux server on `vpprobe-999999`, a
process carrying `VIBEPANEL_TMUX_SOCKET=vpprobe-999999`, one sweep, both gone.
Cleanup code that kills processes is the last place to accept "it looks right".

### The panel was blank when nothing was happening

The scale check had never been run to completion. Run properly — on a quiet
machine, once, with its output no longer truncated — it found the most serious
defect of the session, and it found it because it is the only check whose
sessions do nothing at all.

Twenty-four `sleep 600` sessions. Log in, and the page renders **thirty-one
bytes of body**: an empty root div. No JavaScript error, no failed request, no
crash. The API had all three projects and all twenty-four sessions the whole
time.

The frontend renders from the WebSocket `state` message and has no other
source — there is no fetch of `/api/state` on mount. The server sent that
message only when something changed, and the connection handler added the new
client to the hub without telling it anything. So the panel was blank until
some session happened to do something.

Every other check misses this because they all keep something moving: an htop
redrawing, a bell, a flood of output. State changes constantly, the push
arrives within a second, and the page fills in before anyone looks.

And the case that does not move is the one the whole product is for. Coming
back in the morning to see which agents finished overnight is precisely the
moment when nothing is changing. The panel would have shown an empty page at
exactly the moment its answer mattered most.

The fix is four lines: a `Snapshot` hook on the WebSocket handler, called the
moment a client connects. A client should not depend on somebody else changing
something in order to learn what the world looks like.

Three things this is worth remembering for:

- The check that had never been run is the one that found it. Not because it
  was cleverer — because its fixtures were idle, and every other fixture was
  accidentally hiding the bug by being busy.
- "No JS error, no failed request" was read early on as "not a real bug, the
  harness must be broken". It was the opposite: silence was the symptom.
- The diagnostic that cracked it was one line — printing the body size, the
  sidebar text and the API's own counts alongside the failure. Two rounds were
  spent guessing before that was added, and none after.

### `"entries": null`

The blank page had a second cause, and it was the real one. Adding the initial
snapshot did not fix it; the page was still empty with twenty-four sessions,
with one session, and — as it turned out — with none of that mattering.

A focused probe, thirty lines outside the repo, put a browser on a panel and
printed console output every two seconds. First run:

    pageerror: TypeError: Cannot read properties of null (reading 'length')

`browse.List` builds its `Listing` with `Entries` left nil, and a nil slice
marshals to `null`. The file tree's very next move is `listing.entries.length`.
React has no error boundary, so an uncaught render error unmounts the entire
tree: no message, no controls, an empty root div, and nothing in the console
except the one line nobody was collecting.

**An empty project directory blanked the whole console.** Which is the state of
every project on the day you create it.

Why nothing caught it before: `render-check` points its project at the repo's
own `web/` directory, which is full of files. The scale check makes fresh empty
directories, and the scale check had never been run to completion. The bug was
one `mkdir` away the whole time.

Three fixes, because the incident exposed three separate weaknesses:

1. `Entries` starts as `[]Entry{}`. The line that crashed was the one rendering
   the "nothing here" message — the code for the empty case could not survive
   the empty case. A test asserts the encoded form contains `"entries":[]`, and
   it fails when the initialiser is put back.
2. An error boundary, at the root and around each right-panel tab. A file tree
   that cannot render should cost the file tree, not the terminals and the
   session list. The per-tab boundary is keyed on the tab so switching away
   from a broken one resets it.
3. The harnesses report page errors from their `finally`. They collected them
   inside the try and reported them near the end, so the click that timed out
   *because the page was blank* threw past the one piece of evidence
   explaining the blank page. `restart-check` collected them and never reported
   them at all. An uncaught error in the page is the most informative thing a
   run can produce, and it has to survive the run failing.

The sequence is worth keeping as a method. Four runs of the full check learned
almost nothing, because its diagnostics only print at the end and it spends
minutes timing out before it gets there. One throwaway probe, which I could
change and rerun in ninety seconds, found it immediately. When a slow check is
failing for reasons you cannot see, the next move is a fast thing you control —
not another run of the slow one.

### `last_active_at` was never written

The scale check's last failure was the one it calls "the point of the whole
thing": a session marked as waiting has to reach the top of the list. It did
not, and the reason was a column that nothing updated.

Projects are ordered by `last_active_at`, and `TouchProject` — whose comment
reads *"records activity, which drives automatic ordering"* — was called from
exactly one place in the whole codebase: creating a session. So "most active
first" actually meant "most recently given a new session first". A project
whose agents had been working all day sat below one where a session was created
in the morning and never touched again.

The consequence nobody would think to check for: a project containing a session
that is waiting for a human does not come to the top. The sidebar groups by
project, so a waiting session in the third project cannot be near the top of
the list however correctly the sessions within it are sorted. The one thing the
ordering exists to do was the thing it could not do.

`SetSessionState` now touches the project in the same call. State changes
rather than output: output would mean a write per chunk, while a state change
is already written only when the state actually differs, and it is the event a
person cares about.

The check is green, and its numbers are the first ones ever recorded for it:

    created 24 sessions in 238 ms (10 ms each)
    snapshot 10.9 KiB in 3 ms for 24 sessions
    tmux clients: 24 (one per session is the design)
    server RSS 82 MiB → 88 MiB (0.3 MiB per session)
    sidebar showed 24/24 rows 731 ms after opening the page
    waiting session reached the top in 308 ms
    session switches: 39, 48, 51, 43, 49 ms

Worth stating plainly: this check went from four failures to none, and three of
the four were real defects in the product rather than in the check — a blank
panel on any empty directory, a panel that showed nothing until something
moved, and an ordering rule that had never been implemented. All three had been
sitting behind a script that had never once been run to the end.

### The release check was starting a tmux server on the panel's real socket

Left behind on the machine, six hours and forty-nine minutes old: a tmux server
on the socket named `vibepanel` — the default, the one a real deployment uses —
holding no sessions and a config file in a temp directory that no longer
existed.

`SOCK="vprelease-$$"` was defined next to the one command it was passed to, and
`vibepanel doctor` runs several lines earlier. Doctor calls `EnsureServer`, so
it started a server on the default socket. On the machine running the check
that is debris. On a machine where the panel is actually deployed, that is the
user's socket — and red line 1 exists because reaching for the wrong socket is
how somebody loses a week of sessions. Nothing was lost here, since the server
was empty and a second server on the same socket is not created, but the check
had no business being there at all.

The socket is now exported once, near the top, so every invocation inherits it:
`version`, `doctor`, `serve`, and anything `install.sh` starts.

The other half is `doctor` itself. A diagnostic that starts a tmux server is
making a change, and it was making it silently — the operator learned about it
from `ps`, hours later, if at all. It now says so:

    [ok  ] tmux server        socket "vpX", 0 session(s) (started by this check; it is the panel's own socket)

and says nothing extra when a server was already there. Both halves checked.

Leaving the server running is still the right behaviour: the panel needs one
anyway, and shutting down something another process might have started in the
meantime is worse than an idle server on a dedicated socket. Saying so is the
part that was missing.

### `cmd | grep -q` under `pipefail`, again

The release check's own control — feed `systemd-analyze` a deliberately broken
unit and require it to object, so that its silence about the real unit means
something — was failing. `systemd-analyze` objects perfectly well; running the
exact command by hand printed the complaint and exited 0.

`grep -q` exits the moment it matches. That closes the pipe, the producer takes
SIGPIPE, and `pipefail` reports the pipeline as failed even though the grep
succeeded. By hand it did not reproduce, because the output was small enough to
be written before grep exited; in the check the machine's unrelated units make
it long enough to lose the race.

Capturing first and grepping a variable fixes it:

    CONTROL_OUT="$(... 2>&1 || true)"
    if ! printf '%s' "$CONTROL_OUT" | grep -q "broken.service"; then

This is the second `pipefail` trap in this project, after `ldd` exiting non-zero
for a static binary. Different mechanism, same shape: a pipeline whose exit
status describes something other than the question being asked. In a script with
`set -euo pipefail`, `cmd | grep -q` deserves the same suspicion as a bare `cmd`
whose exit code you have not checked.

The failure message now carries what the tool actually said, which is what made
the difference — a control that reports "the tool cannot be trusted here" and
offers no evidence is asking to be believed on exactly the terms it exists to
reject.

### Where the verification pass ended

All six checks and the whole suite, on a quiet machine, in one sweep:

    make check          Go with -race, plus 21 frontend tests   ok
    render-check        0 FAIL, 0 WARN
    stress-check        0 FAIL, 0 WARN
    restart-check       0 FAIL, 0 WARN
    scale-check         0 FAIL, 0 WARN   (first completed run ever)
    tls-check           0 FAIL, 0 WARN
    release-check       0 FAIL

Nothing is left running afterwards: no panels, no tmux servers on any harness
socket, and the stale empty server that had been squatting the default socket
since a release check six hours earlier is gone.

## Looking at what it renders

With the checks green, the next round was photographs: drive the panel into
states nothing had ever rendered, and read the pictures.

### The sentinel leaked into the interface

`ExitStatusVanished = -1` was chosen because a real wait status is never
negative, so the two can never be confused. In the code. On screen, the badge
beside a vanished session read **`exit -1`**, the tooltip on its restart button
promised "the process exited with status -1", and the project summary counted
it as a crash — because "exited with a non-zero status" is exactly what the
sentinel looks like to anything that does not know about it.

A status a person reads as a number has to be a number a process could have
returned. It now reads `gone`, the tooltip says the tmux session is gone, and
the project badge no longer calls it a crash.

The constant lives in `wire.ts` now, next to the other things that mirror the
backend, because three separate places put it in front of a person and the
first version of each had its own copy of the comparison.

### The button offered on exactly the rows that could not use it

Restart is shown on every exited row, and a vanished session is one of them.
`Respawn` needs a session to respawn into, so pressing it answered 500 — the
one control offered on those rows was the one that could not work. It builds a
new tmux session under the same name now: same row, same id, same title, same
notes; only the process is new, which is what restart means here.

A login shell rather than the recorded command, deliberately. `command` holds
`#{pane_current_command}` — "node" for an agent, "bash" for a shell somebody
had been using — and starting that as an argv would run something the user
never asked for.

### Sixteen pixels

Measured, in a real touch context, in the drawer that a phone user actually
uses:

    pin-session   16x16   at x=231
    kill-session  16x16   at x=255
    rows          31 tall

A twelve-pixel icon with two pixels of padding, twenty-four pixels apart, and
one of them kills a running agent. Every touch guideline says 44, and the
number comes from the width of a finger pad — about nine millimetres. Missing
kill and hitting pin is harmless; missing pin and hitting kill is not. (It does
at least ask for confirmation, which is the only reason this is a usability
defect rather than a data-loss one.)

The project header was worse and was missed on the first pass, because the
measurement only looked inside session rows: the drag grip is sixteen pixels,
and reordering is a press-and-hold drag — the hardest possible gesture on the
smallest possible target. It is driven by pointer events with
`touch-action: none`, so it is meant to work on a phone; it just could not be
aimed at.

`.vp-tap` under `@media (pointer: coarse)` gives all five controls a 44px box
without changing the icon. Desktop density is untouched: this asks the device
what kind of pointer it has rather than guessing from width or user agent.

Now:

    pin-session   44x44   at x=175
    kill-session  44x44   at x=227
    rows          56 tall

The check measures it, and the measurement is in the failure message. Removing
the CSS reproduces `pin-session is 16x16 css px on a touch screen; a thumb
needs 44`.

### Two things that looked like bugs and were not

A dark-mode screenshot showed a **white terminal** in an otherwise dark panel —
the classic theme-switch failure. It was the probe: it set
`document.documentElement.dataset.theme` directly instead of going through
`applyTheme`, so xterm's palette, which is set from JavaScript, never heard.
The app folds `prefers-color-scheme` into the key that repaints it, via a
`useSyncExternalStore` subscription, so a system theme flip at sunset does
reach the terminal. Checked rather than assumed.

The settings dialog's hook snippet is cut off at the right edge. It scrolls —
`overflow-auto` — and there is a Copy button beside it.

### The tab strip moved under the pointer

The bottom terminals are derived by filtering the session list, and that list
is ordered by urgency and recent output — correct for the sidebar, wrong for a
row of tabs. A terminal that printed something sorted to the front, so the tabs
swapped places.

The damage is worse than tabs moving, because the automatic label is
positional: `term ${index + 1}`. The names stay exactly where they are and the
terminals move underneath them. The tab you had been calling "term 2" is now a
different terminal, with the same name, in the same place.

That last part is also why the first two versions of the check could not see
it. Comparing the tab *text* before and after compares `["term 1", "term 2"]`
with `["term 1", "term 2"]` — always equal, by construction. The check now
compares session ids, and with the fix removed it prints:

    printing in a terminal reordered the tabs:
      ["6d7f0cbd1bed86f1","597bc7e9217657c1"] became ["597bc7e9217657c1","6d7f0cbd1bed86f1"]

Two other things had to be right before that assertion could fire at all, and
both were wrong first:

- It typed into the *first* tab. Under recency ordering the first tab is
  already first, so printing in it cannot move anything. It has to be the last.
- Output alone does not push a new session list; the browser keeps the order it
  was last sent. The shuffle therefore appears at the next state change, which
  is precisely when nobody is expecting the tabs to move. The check forces a
  fresh list by reloading — the first attempt patched a session's state to
  force a broadcast, picked the bell session, and broke a check three sections
  away by turning it `done`.

Four "cannot fail" assertions found by mutation testing in one session now.
The rule that keeps earning its keep: after writing a check, break the code on
purpose and watch it fail. The check that has never failed has never been
tested.

Sorted by creation, ties by id. Creation order never changes, which is the only
property a tab strip needs.

### The strip said nothing about a terminal that had died

The tabs carried a name and a close button. A build that died in the bottom
strip looked exactly like a build still running — and the bottom strip is where
builds and tests live, which makes "did it finish" the only question anybody
asks of it.

The exit glyph now appears on the tab, shape-carried as everywhere else, and
only when the terminal has actually exited: a row of identical dots for
terminals that are merely running would be noise, and noise is what stops
people looking at the thing that matters.

This was on the handover list as "the exit indicator missing from the bottom
terminal strip". It took a screenshot to make it feel like a defect rather than
a line in a list.

### Renaming, with a finger

The label said "Double click to rename", and on a phone that is not a thing
that can happen. Not because double-tap is unreliable — because on a narrow
screen the session list is an overlay that closes when you choose a session, so
the *first* tap dismisses the thing being tapped. The second tap arrives at a
terminal. Renaming from a phone was impossible rather than awkward, and the
tooltip explaining how to do it is only visible with a mouse.

A press and hold now opens the editor. Nothing else in a session row uses a
press-and-hold — only the project grip has a pointer gesture, and it is a
different element — so the gesture was free.

The awkward half is the click that follows. Releasing after a long press still
fires a click, and it lands on the *row* rather than the label, because the
element under the finger changed between down and up. The row selects the
session, which on a phone closes the drawer and takes the input with it: the
rename would open and vanish in one gesture. A one-shot capture-phase listener
on `window` swallows that click, with a timeout in case none arrives.

The check drives it through CDP, because Playwright's `tap()` cannot express
"hold still for six hundred milliseconds", and asserts both halves: the input
appears, and the drawer is still open.

### The mutation that could not fail, twice

The long-press assertion looked like the fifth "cannot fail" check of the
session. It was not. The mutation was broken.

Deleting the pointer handlers left `swallowNextClick`, `LONG_PRESS_MS` and
`LONG_PRESS_SLOP` unused, `tsconfig` has `noUnusedLocals`, so `tsc -b` failed —
and `make build >/dev/null 2>&1` swallowed the error. The build failing leaves
the *previous* bundle in `internal/webui/dist`, so the check ran against the
unmutated frontend and passed. Twice, for the same reason, before the second
attempt printed the compiler errors instead of hiding them.

Mutating a *value* rather than deleting code avoids the whole class:

    -const LONG_PRESS_MS = 500
    +const LONG_PRESS_MS = 86400000

Everything still compiles, the behaviour is gone, and the check reports three
failures. The assertion had been fine all along.

Two rules out of it, both about the same mistake:

- A mutation must be *verified to have been built*. Silencing the build during
  mutation testing turns "the check cannot fail" into an unfalsifiable claim,
  which is the exact failure mode mutation testing exists to prevent — applied
  to itself.
- Prefer changing a constant to removing a block. Deletion perturbs the type
  checker; a value does not.

### The settings page stopped observing the moment you opened it

Uptime, how many sessions exist, how many browsers are watching, whether the
hook script is installed — all of it fetched once, on mount, and never again.
"A settings page for observing the backend status" that answers a question
about the past.

It polls every four seconds while open. Four because this is somewhere you
glance to see whether things are healthy, not a monitor; the system monitor tab
has its own cadence and its own graphs.

The check reads the status block, waits six seconds and reads it again. Pinning
the interval to a day reproduces the failure, with the stale block quoted in
the message.

### Two rows called "web"

A shell is named after the directory it sits in, so a project containing
`services/web` and `admin/web` showed two rows reading "web". The sidebar
exists to answer which of these needs me, and two identical rows in one group
cannot answer it — you click one to find out, which is the thing this panel was
built to stop.

Labels are qualified with the directory above when, and only when, two sessions
in the same project would otherwise read the same: `services/web`, `admin/web`.

Three choices worth stating, because each could reasonably have gone the other
way:

- **Within a project, not globally.** The sidebar prints the project name above
  each group, so the same name under two projects is already distinguished by
  where it is. Qualifying globally would add a prefix to rows that never needed
  one.
- **One level.** Deeper qualification is longer than the row and answers a
  question nobody asked.
- **Two sessions in the *same* directory are left alone.** Nothing the machine
  knows tells them apart; inventing a suffix would only look like information.
  That is what renaming is for — and renaming now works from a phone.

Computed once by App and passed to the sidebar, rather than computed in both
places. The comment at the top of `label.ts` already describes what happens
when that rule is broken: the row and the title bar disagreeing about the name
of the session you are looking at reads as a rendering glitch rather than as
two functions.

### `includes` is not agreement

The check for that last part asked whether any sidebar row *contained* the
title bar's text. Prefixing every row label with an "x" passed it —
`"xscratchpad"` contains `"scratchpad"`. A check that two things agree has to
compare them, not ask whether one is somewhere inside the other.

Comparing the name elements exactly:

    the title bar calls the session "sleep" and no row in the sidebar does:
      ["xsleep","xhtop","xscratchpad"]

Fifth assertion this session that could not fail, and the third distinct cause:
a check that never ran, a check whose fixtures were idle, and now a comparison
loose enough to accept the thing it was meant to reject.

### A session made from the command line could never report anything

`vibepanel session new` injected two variables into the session's environment:
the session id and the project id. `report.sh` needs three, and neither of the
two it got is among the ones that make it work — it exits quietly without a
token or a URL.

So a session created from the command line installed cleanly, looked
configured, and reported nothing, forever. The script suppresses its own errors
by design — that is what makes it safe to install globally — so the only symptom
was a session whose state stayed guessed, in a panel whose settings page said
hooks were installed. Nothing short of opening a shell inside the session and
printing its environment would have shown it.

Both halves of what the CLI was missing were derivable from what it already
had. They were just in the wrong place:

- `loopbackURL` was a method on the HTTP server and depends on nothing but the
  config. It is `config.Config.LoopbackURL()` now, where its inputs live.
- The hook token was memoised on the server and is a get-or-create on a
  setting. It is `store.DB.HookToken()` now; the server keeps its `sync.Once`
  around it, because every hook report checks the token and that is a hot path.
- The variable list itself is `hooks.SessionEnv()`, in the package that owns the
  script that reads it. There were two callers and one of them was wrong, which
  is the whole argument for there being one.

The release check now creates a project and a session through the CLI and asks
tmux what is in its environment. Restoring the old two-variable list reproduces:

    [FAIL] a CLI-created session is missing: VIBEPANEL_TOKEN VIBEPANEL_URL

### A comment I invented while writing it

The first version of `SessionEnv` said all four variables were required and
explained that the project id "makes a report attributable when the session id
has been recycled". The test I wrote next asserted the script mentions each of
them, and failed on that one: `report.sh` reads three and never sends the
project id anywhere.

The rationale was invention, written in the same hour as the code it described,
and it would have read as fact to anyone who found it later. It is the exact
pattern this log has been recording all week — comments describing mechanisms
that were never built — except this one had no age to hide behind.

`VIBEPANEL_PROJECT_ID` stays: it is offered to whatever the person runs inside
the session, which is worth having. The comment now says that, and says it is
not part of the mechanism.

The test that caught it is three lines: assert the embedded script mentions each
variable the code claims it reads. A claim about another file is checkable when
the other file is right there.

### A renewal that keeps its timestamps

The certificate reloader compared modification times, which is wrong for the
one event it exists to catch. `cp -p`, `install -p`, rsync with `--times`, and
anything that restores mtime after writing all leave the panel serving the
certificate that was replaced — until it expires, and then serving an expired
one, silently, because nothing would ever look again.

It hashes the bytes of both files instead. Two files of a few kilobytes, once a
minute, and it cannot be fooled by a timestamp. Length-prefixed into one digest
so that moving a byte from the key to the certificate cannot collide.

Restoring the mtime comparison reproduces it exactly:

    common name after a timestamp-preserving renewal = "first.example",
    want second.example; the panel is still serving the certificate that was
    replaced

Detecting the file changing was only ever half of it, though. **A file that
never changes is exactly what an abandoned renewal looks like**, and nothing in
the panel could have told anyone either way. So:

- the reloader logs once — not every minute — when the certificate it is
  serving falls inside fourteen days, and again when it has actually expired.
  Fourteen because ACME attempts renewal at thirty, so anything inside that
  window means renewal has already failed more than once and there is still
  time to fix it by hand;
- the settings page carries the date and a countdown, in the warning colour
  under two weeks and the crashed colour past it. A log line on a machine
  nobody reads is not where an operator should first learn this.

The expiry is asked of the live `tls.Config` rather than of whichever source
produced it, so it works for both certificate files and ACME and stays right
when the certificate is replaced underneath.

The check opens the settings dialog and requires a date or a countdown; with
the wiring disabled it prints the whole status block and the absence of any
mention of a certificate.

    Certificate    8/26/2026 · 1 day left

## The two product decisions, decided

Both were flagged as needing a call rather than a fix, twice, and the answer
each time was to keep going. So they are decided here, with the reasoning
written down so that reversing either is a small edit rather than an
archaeology exercise.

### What a quiet agent means

**Decided: silence no longer means finished. What is running decides.**

The code asked one question — has anything printed in the last two seconds —
and answered `done` when the answer was no. So an agent thinking for four
seconds, or waiting on a slow tool call, or writing to a file instead of the
screen, was announced as finished. A green check against a session in the
middle of a task.

*Done* is a claim about completion and the panel had no evidence for it. All it
knew was that nothing had printed lately. The evidence it does have is the
foreground process, which tmux reports and which the detector was already
carrying in `Observation.ShellOnly` — used only to *demote* a busy shell, never
to promote a quiet agent. Its doc comment even said "there is nothing that could
be working"; the inverse was never asked.

So: a pane running `claude` has not finished anything. A pane back at a shell
has, because the thing that was working exited. That is the difference between
the two states, and it is the one question tmux can answer.

**Not "waiting", though the temptation was real.** A quiet agent is either
thinking or asking, and nothing available here tells them apart. Guessing
"asking" would put a triangle on every session that paused for a moment, and a
panel that cries for attention it does not need is one people stop looking at —
which costs more than the thing it was trying to buy. "Working" is true of both,
and the two signals that genuinely mean a person is needed, the bell and a hook
report, are checked before this and outrank it.

To reverse: `Evaluate` in `internal/session/detect.go`, the branch that returns
`StateWorking` for `!obs.ShellOnly`.

**The flicker went with it, and took a second bug out on the way.**
`activityWindow` and the poller's interval were both two seconds, so "was there
output in the last window" was sampled at exactly the rate the window was wide
and a session printing every couple of seconds landed on either side of the
boundary at random. The window is five seconds now — not a multiple of the
sampling rate — and a running agent no longer depends on it at all.

Widening it broke a bell test, which turned out to be the useful part: the bell
rule was using `activityWindow` to decide how long a ring stays authoritative.
Two different questions sharing one constant because they happened to want the
same number. `bellGrace` is its own two seconds now, and changing one no longer
silently changes the other.

### Whether a password can be changed

**Decided: yes, from the settings page.**

There was no way, from anywhere. The wizard sets one once and nothing could
replace it, so the answer to "this leaked" or "I typed it into the wrong
window" was to stop the panel and edit SQLite by hand.

`store.DeleteUserAuthSessions` already existed, with a comment reading *"Used
when the password changes: the point of changing it is that whoever had the old
one stops having access."* It had no callers. Another function written for a
feature nobody built, describing behaviour that did not exist.

Three properties the endpoint has that a naive one would not:

1. **The current password is required.** A stolen session cookie is then not
   enough to lock the owner out of their own panel, which is the difference
   between an intruder who can read your terminals and one who owns them.
2. **Failures are throttled**, through the same limiter as sign-in. Otherwise
   this is an unthrottled oracle for guessing the password that the login page
   refuses to be.
3. **Every other browser is signed out**, and this one is not. Leaving the old
   sessions alive makes the change decorative; signing out the browser that just
   made the change reads as the change having failed.

The check does the whole flow through the page: a wrong current password must
be refused and say so, the right one must work, this browser must stay signed
in, and the old password must stop working. Removing the server's verification
reproduces `a wrong current password was accepted, or reported nothing`.

## Rendering the parts nothing had rendered

With the handover list empty, this round was pure looking: drive the panel into
states no check produces and read the pictures.

### The error boundary, caught in the act

Route interception made the file endpoint return `{"entries": null}` — the exact
shape that blanked the whole console before it was fixed. The boundary caught
it: the files panel showed a message and a "Try again" button, and the sidebar,
the terminal, the bottom strip and the other tabs went on working.

One thing wrong with it. The error message was `truncate`d, so it read

    Cannot read properties of null (reading 'le…

which names neither the property nor the place. That line is the only thing
anybody can paste into a bug report; it wraps now.

### "0%" is not "I don't know yet"

The system monitor's CPU meter, on its first sample:

    CPU                    0%
    16 cores · sampling…

The percentage is a difference between two samples, so the first one has
nothing to compare against. The detail line said so. The number beside it said
the machine was idle, and the number is what people read.

The comment directly above the code said exactly the right thing:

    // The first sample has nothing to difference against, so it says so
    // rather than showing a zero that looks like an idle machine.
    value={sample.cpuPercent ?? 0}

The `?? 0` is the zero that looks like an idle machine. Comment and code
contradicting each other inside three lines, the comment describing the
behaviour somebody meant to write.

An unknown value reads `—` now and draws no bar. The logic moved to
`meter.ts` — a pure function, testable without a DOM, and out of the component
file that ESLint wants to contain only components.

### A third comment describing something that does not exist

The right panel is hidden on a narrow screen, which is right: a 280px column
beside a terminal on a phone leaves neither usable. The comment explaining it
ended "the panels reach mobile in their own layout rather than by being
squeezed into this one".

They do not. There is no mobile route to the file tree, the system monitor, the
notes or the todo list. The narrow layout is the terminal, the compose box and
the key bar — which is what the plan scoped, so the *absence* is a decision.
The sentence claiming they arrive by another route is what made a gap read as
work already done.

Corrected in place, and left as a gap rather than built: notes and todos were
asked for as the third and fourth tabs of the right column, which is a desktop
surface. Anyone who wants them on a phone is asking for something new, and
should get to say so.

That is three of these in one session — `SessionEnv` inventing a purpose for a
variable the script never reads, `DeleteUserAuthSessions` describing a feature
with no callers, and now two comments describing behaviour the code beside them
contradicts. The pattern is worth naming: a comment is the one part of a change
nothing verifies, so it is where an intention that never got implemented comes
to rest.

### Two probes that were wrong, not the panel

The notes tab rendered empty after the probe wrote a note through the API. That
looked briefly like the tab overwriting the note with a blank — worth checking
carefully, since it would have been the worst kind of data loss.

It was the probe. It sent `{content, rev: 0}`; the field is `baseRev`, and the
API decodes with `DisallowUnknownFields`, so the write was rejected with a 400
that the probe never looked at. A second probe wrote, read back, opened the tab
and read back again: content correct, revision unchanged, nothing lost.

The probe's fault is the same one this log keeps recording about checks — it
did not assert that its own setup succeeded. Fixtures that fail silently
produce findings about the wrong thing.

### One phone is not phones

Every mobile assertion in this project ran at 390×844. Two things were broken at
sizes just as ordinary, and one of them was broken *by the previous round's
fix*.

**320 wide.** Eight keys at the 44px a thumb needs come to 380px. The primary
key row — the one whose comment promises that nothing in it is ever hidden —
overflowed by 56 pixels, and the page does not scroll, so `ctrl` and `alt` could
not be pressed at all. Widening the touch targets did that, and measuring only
at 390 is why it shipped.

It wraps now. A second line costs 44px of a screen with room for it; a key you
cannot press costs the feature. Wrapped with `justify-start`, because
`justify-between` stretches the *last* line too and put `ctrl` against one edge
and `alt` against the other with a hand's width of nothing between them.

**A phone held sideways.** The layout switch was `(max-width: 767px)`, and a
landscape phone is 844 wide. So rotating produced the desktop layout: a 260px
sidebar, the right panel, the bottom terminal strip, and a terminal of

    35x6

Thirty-five columns and six lines, with no compose box and no key bar — so
rotating also lost the ability to type Chinese, send Ctrl-C, or press an arrow.
Turning a phone sideways is something people do to see *more* of a terminal.

The query is now `(max-width: 767px), (max-height: 500px) and (pointer: coarse)`.
The second clause is deliberately not just "is it short": a desktop window
dragged short is still a desktop, with a keyboard and a mouse, and keeps the
wide layout. Short *and* driven by a finger is a phone on its side, which is the
only thing this is trying to catch. Same terminal, after:

    104x11

Both are checked now, at both sizes, and removing either fix reproduces its own
failure with the measurement in the message.

The general lesson is the one the scale check taught in a different costume: a
check that only ever runs one fixture is testing that fixture. "It works on a
phone" meant "it works on the phone in the harness".

## A signed-out browser kept its terminals

The scenario nothing had ever rendered: a session that stops being valid while
the page is open. It happens on expiry, on signing out elsewhere, on an
administrator revoking a session — and on the password change added earlier
today, whose whole point is that whoever had the old password stops having
access.

Measured, with the session row deleted and the page untouched:

    before:  {"conn":"open","rows":1}
    session invalidated
    typing after the session was invalidated reached the shell: true
    t+3s     {"conn":"open","rows":1,"loginForm":false}
    t+8s     {"conn":"open","rows":1,"loginForm":false}
    t+20s    {"rows":0,"loginForm":true}

Two things wrong, one of them serious.

**The socket does not care that the session is gone.** Authorisation happens
once, at the handshake, and then the connection lives for hours. So a
signed-out browser kept full terminal access — reading output and sending
keystrokes — for as long as it stayed connected. "Signs every other browser
out", which this log claimed a few hours ago, was true of future HTTP requests
and false of the terminals those browsers already had open.

A live connection now re-runs exactly the checks `RequireAuth` makes, in the
same order — is this address still allowed, does this session still exist — and
closes when the answer changes. The original request is what gets re-checked,
which is the point: its cookie does not change, the row behind it does.

Every five seconds. Thirty was the first number, and it is too long for the
case that matters: somebody changing their password *because it leaked*, while
whoever leaked it still has a socket open. The cost is two indexed reads per
open browser per interval, and this panel has a handful of viewers.

**The page took twenty seconds to admit it.** A browser cannot see the HTTP
status of a failed WebSocket handshake, so a socket refused with 401 looks
exactly like one refused by a flaky network. The panel went on showing the
session list and the last frame of a terminal, with only the connection dot to
say otherwise, until an unrelated fetch happened to get a 401.

After four seconds down it asks. Asking first is the whole design: signing out
on a dropped connection would turn a bad wifi moment into a logout.

    session invalidated
    typing after the session was invalidated reached the shell: true
    nine seconds later: the panel is showing the sign-in page

### The test that destroyed what it was testing

The first version checked liveness by reading with a short context and
expecting a deadline error. In coder/websocket, **a read whose context expires
closes the connection** — so the liveness check killed the thing it was
checking, every later read returned an error, and the test passed just as
happily with the revalidation removed.

It passed the mutation. The only reason that was caught is that the mutation is
checked at all, and that the check now includes proving the mutation reached
the binary: `grep -c` on the source, and the build output not swallowed.

One read, in a goroutine, never cancelled. Removing the revalidation now prints

    the socket was still open five seconds after its session stopped being
    valid; a signed-out browser keeps its terminals

That is six assertions this session that could not fail. The causes have all
been different — a check that never ran, fixtures that were idle, a comparison
loose enough to accept what it should reject, a mutation that silently failed
to build, and now a probe that destroyed its own subject. The habit is not
optional.

### A test that was passing by catching a transient

The full sweep after all this turned up `TestPollerTracksStateFromOutput`
failing — a test the previous sweep had passed, with nothing between them that
touches the detector.

It was not flaky by accident; it was racing on purpose and had been winning.
The fixture ran `sh -c "echo started; exec sleep 60"` and expected *done*, on
the old rule that silence meant finished. For the first moment the pane's
command is `sh`; only after the exec does it become `sleep`. So the assertion
passed whenever the poller happened to look during that window and failed
whenever it looked afterwards — and once the rule changed to "what is running
decides", looking afterwards became the only correct answer.

The fixture says `exec sh` now, which is the thing that actually means nothing
is running. And the half with teeth was missing entirely, so it is there now: a
`sleep 60` left alone for several activity windows must still read as
*working*, because the process is still there and nothing has finished. That is
precisely what used to be announced as done.

Worth noting how it surfaced: not from the change that broke it, but from
running everything afterwards. A test whose result depends on when it looks
will pass the run where you would have caught it.

## Two screens nobody had rendered on a phone, and one that had never been killed

### tmux dying underneath the panel

`tmux -L … kill-server` while the panel is open and two sessions are running.
This is not hypothetical: it is what an OOM kill looks like, and what somebody
tidying up their sockets does by accident.

It behaves. Within four seconds both rows carry the dashed glyph reading *"Gone
— the tmux session no longer exists"*, the terminal shows `[server exited]`,
each row offers restart, and nothing throws. That is the `pollOnce` early-return
removal and `markVanished` doing exactly what they were built for, in the case
they were built for, which had never actually been run.

### iOS magnifies the page when you tap anything

Every input in this panel was 12 or 13 pixels. Safari on iOS zooms the viewport
when a focused field's text is smaller than 16, and it has ignored
`user-scalable=no` since iOS 10 — so the meta tag in `index.html`, which is
there for exactly this, does not prevent it. Nothing zooms back afterwards.

Nine fields, and the one that matters most is the compose box: the main way to
type on a phone. Tapping it magnified the terminal behind it and left it that
way.

    fields a finger will focus are under 16px, so iOS magnifies the page when
    they are tapped and does not put it back:
      [{"id":"textarea","px":13},{"id":"compose-input","px":13}]

One rule, on the elements rather than on a class:

    @media (pointer: coarse) {
      input, textarea, select { font-size: 16px; }
    }

A class would have held only until somebody added the tenth input. Under a
coarse pointer only, so desktop density is unchanged; the phone's fields go from
34px tall to 38 as a side effect, which is the direction they wanted to move
anyway.

The check reads the computed font size of every visible field on a touch page
and names the ones under 16. Putting the rule back to 13px reproduces it.

**Not changed:** the sign-in button is 36px tall, below the 44 established for
the icon controls earlier today. That rule was about a 16-pixel icon squeezed
between two others; a 222×36 button is a different thing and forcing every
button in the panel to 44 would be churn dressed as consistency. Said here so
the inconsistency is a decision rather than an oversight.

## Rows that can outgrow their box, the third and fourth of them

The key bar on a phone was the first. Looking for the same shape elsewhere
found two more, and the second one was not the failure I went looking for.

### The terminal strip painted its tabs over the panel next door

Eight scratch terminals in an 820px window. The tab row has no overflow
handling at all, so four tabs ended up past its right edge — and `overflow:
visible` does not clip, it *draws them over whatever is beside them*, with no
way to scroll to them.

The tabs scroll now; new-terminal and collapse stay outside the scroller,
because putting them inside means they scroll away exactly when there are
enough tabs to need them.

### The collapsed rail squashed instead of spilling

Twenty projects in a 560px window. `overflow-y-auto` did nothing, because
nothing overflowed: **flex children compress before they overflow**. Every
badge went from 36px to 17 — unreadable initials, untappable targets — and the
scroller added to catch spilling never fired because nothing ever spilled.

`shrink-0` on the badges is the actual fix; the scroller is what makes the
result usable once they refuse to shrink.

Worth stating plainly: the first fix here was wrong and the measurement is what
said so. "Add `overflow-y-auto`" looked obviously right, the check went green,
and the badges were still 17 pixels tall. Reachability was the wrong question —
everything was reachable, and unreadable.

### `scrollIntoViewIfNeeded` is not a reachability test

The scale check has asserted since it was written that the last session row
"cannot be scrolled to", using Playwright's `scrollIntoViewIfNeeded` and
treating a resolved promise as success. It resolves as long as it can scroll
*something*. Measured on the broken strip: it reported success for a tab being
painted 350 pixels past the edge of its row.

All three reachability checks now do the same thing instead — find the ancestor
that actually scrolls on that axis, scroll it to the end, and ask whether the
last item is inside its box. Removing either overflow rule reproduces its own
failure; removing `shrink-0` reproduces twenty-one badges at nineteen pixels.

That is four instances of one mistake in this codebase: a row of things that can
outgrow its container, with nothing deciding what happens when it does. Worth
naming as a thing to check for rather than a bug to fix.

## Going looking for the fifth, and not finding one

Four instances of "a row that can outgrow its box" is enough to stop fixing
them one at a time. So: a scan that walks every element on the page and reports
any whose content does not fit, whose overflow is `visible`, and which has no
scrolling ancestor to reach the rest through.

Run against a panel loaded with everything it can hold at once — a project name
that does not fit, sessions named in Chinese and emoji, five scratch terminals,
notes, todos, a file with a sixty-character name — at 1440×900, 820×560 and
320×568, with the right panel dragged to its narrowest and the settings dialog
open.

**Nothing. The panel is clean.** Which is only worth saying because the scan was
calibrated first: with the terminal strip's overflow rule removed it reports the
strip and its ancestors, by 115 to 175 pixels, at exactly the size where they
spill.

### The detector needed three attempts, and the middle one nearly cost a fix

**First version:** flag anything whose content is bigger than its box. It
reported four audit-log rows on a phone, 162px each. Plausible: those rows are
408px of fixed columns in a dialog about 256 wide.

I fixed it — `overflow-y-auto` to `overflow-auto` — and the scan still reported
it, which is what saved the whole thing. The rows were never the problem; their
*container* is, and the container was already scrolling. CSS computes
`overflow-x: visible` to `auto` when the other axis is not visible, so asking
for vertical scrolling there had quietly granted horizontal scrolling too.
Measured on a phone: `overflowX` computes to `auto`, and the box does scroll
sideways.

So the change was reverted. What is left in its place is a comment saying why
`overflow-y-auto` is sufficient there and not obvious — because the next person
to run a scan like this will find the same thing and reach for the same fix.

Without re-validating the detector I would have shipped a no-op change plus a
comment asserting a defect that never existed. Which is the pattern this log has
been cataloguing all week, produced by the process meant to catch it.

**Second version:** only report when no ancestor can scroll — and treat an
ancestor with `overflow: hidden` and no spare room as "contained, therefore
fine". That silenced *every* real finding, including the deliberately broken
strip. The app root is `overflow-hidden`, so everything has such an ancestor.
Being clipped by something four levels up is not containment; it is the content
being invisible and unreachable.

**Third version**, calibrated both ways, is what shipped.

### And it has to run where the thing exists

The first place it went was the desktop page, where it found nothing — including
nothing about the key bar, which only exists on a phone. Breaking the key row on
purpose produced a failure from the *old* targeted assertion and silence from
the new general one.

It now runs on the desktop layout, in the phone drawer, and at both phone shapes.
Broken key row, at 320:

    in a 320 wide phone, content is painted outside its container with no way
    to scroll to it: main is 52px too wide; key-bar is 52px too wide;
    key-row-primary is 56px too wide

## Every tappable thing on a phone, measured

Same move as the overflow scan, applied to touch targets: rather than fixing
the controls somebody happened to think about, measure all of them.

The five that had been given a 44px box were the five in a session row. The
scan found the rest:

- **every key on the soft keyboard was 32px tall** — the most-pressed surface
  in the product;
- the header's settings, theme and sign-out controls, 27 square;
- the compose box's send and newline buttons, 32;
- the settings dialog's close button, **23**;
- the state dot, which is a button that cycles the state, 18.

A rule about buttons rather than a class on each, for the same reason the 16px
font rule is one: a class holds only until somebody adds the next button.

    @media (pointer: coarse) {
      button, [role='button'] { min-height: 2.75rem; }
    }

`min-width` is left alone deliberately. 44×44 is the guideline, but eighteen
keys at 44 wide do not fit a phone, and a target 32 wide and 44 tall between two
other targets is a different proposition from one that is 27 square in the
corner of a dialog. Height is where the wins are, and this is written down so
the gap is a decision.

### The scan caught the consequence of its own fix, twice

Making everything taller squeezes what is above it. The overflow scan added an
hour earlier reported, immediately:

    in the phone drawer, content is painted outside its container with no way
    to scroll to it: div is 41px too tall

"div" names nothing, so the scan now reports a class fragment too — the first
thing it said was literally true of every div on the page.

`div.h-full.w-full` is the element xterm is mounted into. It re-fits its grid
asynchronously after anything changes the space it has, so it is briefly taller
than its box whenever the layout moves. Reported once, gone on the next run,
still intermittent after sampling twice six hundred milliseconds apart.

An intermittent failure is how a check stops being read: the lesson people take
from it is to run it again. So the terminal's immediate host is exempt — by
`:scope > .xterm`, not by class name — with the reasoning written where the
exemption is. Three consecutive clean runs, and breaking the tab strip still
reports it.

### One scan, two checks

Exempting the terminal host revealed that the render check could no longer see
the tab strip break at all — because it scans at 1440×900, where two tabs have
never been close to overflowing. The crowded states live in the scale check.

So the scan is `web/scripts/lib/overflow.mjs` now, imported by both, rather than
copied. This log has recorded the cost of the same rule living in two places
four times this week; it was not going to be the fifth. With the strip broken,
in the crowded state:

    with everything crowded, content is painted outside its container with no
    way to scroll to it: main… is 64px too wide; [bottom-terminals] is 64px too
    wide; div.flex.h-8… is 64px too wide; div.flex.min-w-0… is 124px too wide

### And the refactor deleted a function

Extracting the scan into `lib/overflow.mjs` cut from the start of the scan to
the next top-level `const`. Three colour helpers and one `waitHealth` sat in
between. The colour helpers were noticed and moved back; `waitHealth` was not,
because nothing referred to it in the part I was reading.

The sweep found it a minute later:

    [FAIL] harness: ReferenceError: waitHealth is not defined

Which is the argument for running everything after a refactor rather than the
thing that was refactored — and for a check whose first act is to wait for the
server, because that is the line that fails loudly when the file is broken.

Restored, with the story attached to it.

## Two more scans, and one of them found nothing

The overflow scan worked, so the same move twice more.

### Touch targets, permanently

The measurement from the previous round is a check now, in
`web/scripts/lib/tap.mjs`, run at both phone shapes and in the drawer. Removing
the 44px floor reproduces the whole list with heights attached:

    in the phone drawer, controls are too small for a thumb:
      button:Close is 27px tall; button:Sort by recent activity again is 26px;
      button:Add project is 27px; state-dot is 18px; button:Projects is 28px;
      settings-open is 27px; theme-toggle is 27px …

It also named one I had not measured by hand — `take-control`, 30px, which only
appears when another viewer owns the grid.

### Keyboard focus, and a check that had to be rewritten before it was right

Can somebody navigating with a keyboard see where they are? The first version
of the scan looked for an outline or a box-shadow on the focused element.

That would have reported **every text field in the panel as invisible**. The
panel does focus two different ways: buttons keep the browser's outline, and
inputs remove it — `outline-none` — and turn their border accent-blue instead.
Both are perfectly visible; only one of them is an outline.

So the check compares the element against itself: read the computed style while
focused, blur, read again, restore. Anything different is an indicator. Driven
with real Tab presses rather than `.focus()`, because `:focus-visible` is what
the styles hang off and it tells the two apart.

**The result is that nothing is wrong.** Twenty tab stops across the sign-in
page and the panel, every one of them visibly different when focused. Worth the
hour only because the check now exists, and because the wrong version of it
would have sent me "fixing" six inputs that were already correct — the same
near-miss as the audit-log rows, one round earlier, from the same cause: a
detector that encodes one way of doing something as the only way.

Adding `button:focus-visible { outline: none }` reproduces:

    in the desktop layout, these look the same focused as unfocused, so
    keyboard navigation is invisible: button:Collapse, button:Add project,
    project-new-shell, state-dot, pin-session, kill-session

### Where the checks stand

Three generic scans now, each in `web/scripts/lib/`, each calibrated against a
deliberate break, each run where the thing it measures actually exists:

- `overflow.mjs` — content painted where nothing can scroll to it
- `tap.mjs` — controls too small for a thumb
- `focus.mjs` — tab stops that look the same focused

Together they replace four rounds of finding the same class of defect one
instance at a time. The pattern worth keeping is not any of the three: it is
that after the second instance of a mistake, the thing to write is the thing
that finds the fourth.

## Two more mechanical checks, and three ways to write one that cannot fail

### Controls a screen reader would announce as nothing

This panel is mostly icons: pin, kill, restart, collapse, new terminal, the
theme toggle, the state dot. An icon with no text and no label is announced as
nothing, and a row of them is "button, button, button".

The scan accepts any of the four things that work — `aria-label`,
`aria-labelledby`, `title`, visible text, or a `<title>` inside the svg — and
the panel passes everywhere: sign-in, the panel, all four right-hand tabs, the
settings dialog. Removing one `title=` reproduces `settings-open` by name.

`web/scripts/lib/names.mjs`, run on the desktop layout and in the phone drawer.

### Red line 4, mechanically

*Colour is never the only carrier of meaning.* The rule has been in AGENTS.md
since the beginning and nothing enforced it — unlike red line 5, which
`styles.test.ts` has enforced since the day it was written.

The check strips colour from every state glyph on screen and requires two dots
that mean different things to still look different. Dashes count as geometry: a
dashed outline is visible without colour, which is how *gone* differs from
*exited*.

Getting it to fail took three attempts, and each failure was the check being
wrong in a way worth writing down.

**First:** it grouped labels by their first word, so "Exited" and "Exited with
status 3" were the same meaning. Those two are precisely the pair the rule
exists for — finished versus crashed — and the grouping merged them. Replacing
the crash cross with a red copy of the clean square passed.

**Second:** with the meanings separated by category, it still passed — because
it ran early in the check, before anything had exited. It was comparing working
against done, which are different shapes, and concluding the rule held. A check
for a distinction that runs where the distinction does not exist is a check
about something else.

**Third,** moved to the point where a crashed session and a cleanly exited one
are both on screen:

    "crashed" and "exited cleanly" are drawn with identical geometry, so they
    differ only by colour

Three failures, three different mechanisms — a classification that erased the
distinction, a fixture that lacked it, and (in the previous round) a comparison
too loose to see it. Every one of them produced a green check about a broken
build.

## The red lines, mechanically

Seven rules in AGENTS.md. Three were enforced by something, four were enforced
by whoever remembered them. After this round, six are enforced.

| | rule | enforced by |
|---|---|---|
| 1 | never touch another tmux socket | `TestEveryCommandNamesOurSocket` |
| 2 | never own a PTY a session depends on | `restart-check` |
| 3 | one definition of the state enum | the enum tests |
| 4 | colour is never the only carrier | the glyph geometry check |
| 5 | theme blocks redefine tokens only | `styles.test.ts` |
| 6 | validate anything from a hook | `TestHookRejectsGarbage` |
| 7 | exact-match tmux targets | `TestTargetsAreExactNotPrefixes` and `TestNoHandBuiltTargetsElsewhere` |

Three of those existed already. Four and seven-and-a-half were added over the
last two rounds.

**Red line 1** is now a property of the argument list rather than a habit: build
the argv for several commands and require `-L` followed by the configured
socket in every one. Making `attach-session` skip it reports

    no -L in "-f …/tmux.conf attach-session -t =vp_x:"; this command would use
    the user's own tmux

which is the failure that ends somebody's week, caught before it runs.

**Red line 7** already had the property tested — against a real tmux, with a
real prefix collision, which is stronger than any string comparison. What it
did not have was the *second half* of the rule: "use the helpers, never
hand-built target strings". A helper nobody is obliged to use is a convention.
So a source walk objects to a `-t` argument anywhere outside `internal/tmux`:

    tmux targets are built outside internal/tmux, where the exact-match form
    cannot be enforced:
      ../httpapi/api.go:254: func handRolled(name string) []string { … "-t", name }

### And one test deleted for being a second copy of a rule

Writing that, I also added an assertion that `target("vp_ab")` returns
`"=vp_ab:"`. It passed, it failed under mutation, and it was still wrong to
keep: `TestTargetsAreExactNotPrefixes` already proves the property against real
tmux, and catches every way of getting it wrong rather than the one way a string
comparison knows about.

Deleted, with a note where it would have gone saying why it is not there. Two
tests for one rule is the cost this codebase keeps paying elsewhere; adding
another instance while writing about the pattern would have been a poor joke.

**Red line 2** stays behavioural — `restart-check` kills the backend and
requires the sessions to outlive it, which is the only honest way to test it.

### The guard that caught its own fixture

The sweep after all that reported

    [FAIL] ui: expected eight terminal tabs to test the strip with, saw 0

which is the guard added two rounds ago — the one that fails rather than
silently skipping when the state under test was not built.

The cause: the crowded-strip section clicked the *first* session row to select
the session it had hung eight scratch terminals on. The sidebar sorts by
urgency, and a session is marked waiting a few lines above, so the first row is
that one and its strip is empty. It had been passing because the ordering
happened to put the right row first.

Selecting by name instead. Two consecutive clean runs.

Worth noting which part worked: not the assertion about the strip, which would
have been vacuously true against zero tabs, but the line above it that refuses
to proceed without the fixture. Every one of these checks needs that line, and
the pattern of writing them — assert the measurement before the threshold — is
now four rounds old and has caught three separate things.

## The migration that can never be edited again

AGENTS.md says migrations are "additive steps only, and never an edit to an
earlier one", because a released binary has already run the old version of that
step on somebody's machine. Nothing enforced it.

The obvious test does not work, and finding out why was the useful part.

**First attempt:** build a database at v1, upgrade it, build a fresh one, and
require the two schemas to match. Adding a column to `schema.sql` without a
migration would then fail — new installs would have it, upgraded ones would
not.

It passed with exactly that change made. Both paths run `migrations[0]`, and
`migrations[0]` *is* `schema.sql` — a fresh database starts at user_version 0
and applies the whole list. So the comparison was of a thing against itself and
could not fail. The seventh unfalsifiable assertion of this session, and the
first one whose premise was wrong rather than its mechanics: detecting drift
between "then" and "now" needs a copy of "then", and nothing stores one.

**What is checkable** is the rule itself. A pinned hash of `schema.sql`:

    schema.sql has changed (b54744791f20…).
    Every database in the world has already run the previous version of it, and
    none of them will run this one. Add a migration instead; if you are certain
    this file has never shipped, update the pin.

It is deliberately not a value to refresh when the file changes. If it fails,
the change belongs in a new migration. The comment says so, because the
temptation when a hash test fails is to update the hash.

The upgrade path itself was already covered — `TestMigrationUpgradesAnExisting
Database` builds a v1 database with real rows in it and requires the current
build to migrate in place rather than start over. That one is behavioural and
strong; what was missing was the guard on the thing it cannot see.

## A third race, found by hammering rather than by reading

The first two races in this package were found by reading code that looked
correct. This one was found the other way: drive every entry point on a live
session concurrently — six viewers subscribing, draining and leaving; four
clients writing, resizing and taking control — then close it underneath them
and let the race detector decide.

It passes once. Run six times, it fails:

    WARNING: DATA RACE
    Write at 0x…  by goroutine 59:
      os.(*File).Close()
      session.(*Live).close()          manager.go:690
    Previous read at 0x… by goroutine 86:
      os.(*File).Fd()
      creack/pty.Setsize()
      session.(*Live).Resize()         manager.go:603

Every user of `l.ptmx` in this file copies the pointer, releases the lock, and
then does its I/O. That is right for `Write` — a write to a full PTY buffer can
block, and holding the lock across it would stall the pump and every other
viewer — and it is *safe* there, because `os.File` reference-counts reads and
writes against `Close`.

It is not safe for `Resize`. `pty.Setsize` needs the raw descriptor, and
`File.Fd()` is documented as valid only until `Close` is called: it steps
outside the machinery that makes the other two safe. So a resize arriving as a
session detaches raced the descriptor being destroyed — and on a panel that
opens a PTY per session, a recycled fd taking that ioctl is not a theoretical
outcome.

The ioctl now happens with the lock held, and `close()` closes the descriptor
with it held too. Microseconds, and it cannot block. `Write` stays outside,
where it belongs, for the reason it was put there.

Putting the ioctl back outside reproduces the race in six runs; with it inside,
six runs are clean, and so are the two packages under `-race`.

**The lesson is about the test, not the fix.** Three races in this file now.
Two were found by reading and one by a chaos test that took twenty minutes to
write — and that chaos test would have found all three. A concurrency review is
worth doing; a concurrency review is not a substitute for running the thing
concurrently and asking the detector.

It also has to be run more than once. A single pass was green.

## The manager, hammered — and two wrong conclusions on the way

Same treatment for the manager itself: attach and detach the same names from
eight goroutines while others ask what is live, then `DetachAll` and ask tmux
what is still connected.

    vp_c1 still has 1 client(s) after DetachAll
    vp_c2 still has 1 client(s) after DetachAll

**That was not a leak.** `close()` gives a tmux client two seconds to exit on
its own before killing it — the comment right there says so — and the assertion
waited five hundred milliseconds. Half a second is not a leak, it is
impatience. With the wait past the grace period, the test passes.

Which left a fix I had already written, for a bug the test no longer showed.
The temptation is to keep it: it is small, it reads as obviously correct, and
nobody would question it. That is the shape of most of what this log has been
finding all week. So: prove it or delete it.

**The property is real.** `Attach` spends milliseconds before it installs
anything — capture-pane, then starting a PTY — and during that window the
session is in neither map. A `Detach` arriving then finds nothing, returns, and
the attach installs itself afterwards into a manager the caller has just been
told is empty. Nothing that called `Detach` can close it.

**The first test of it failed for the wrong reason.** Firing the detach
immediately after starting the attach goroutine tests the opposite thing — a
detach that arrives *before* an attach begins, where installing is correct,
because "attach after detach" is an ordinary sequence. Making the code pass that
test would have broken real behaviour.

Waiting for the claim to appear before detaching tests the window that matters.
With the fix, three runs clean; without it, attempt zero fails every time.

The consequences in the panel as it stands are bounded — every caller of
`Detach` either kills the tmux session immediately afterwards or is on its way
out of the process — and that is a property of today's callers rather than of
the manager. Which is the argument for the test: the next caller does not
inherit the reasoning.

Two self-corrections in one finding: an assertion that was too impatient, and a
test that measured the opposite of what it claimed. Both would have produced a
confident wrong answer, in opposite directions.

## The rest of the concurrent surface

Three more chaos tests, against the store, the detector and the hub. Two of
them found nothing wrong with the code and something wrong with the test.

### Concurrent writers, and a load-bearing DSN parameter

SQLite takes one write lock for the whole database, so two writers collide by
design; `busy_timeout` is what turns the collision into a wait rather than an
error. The DSN sets it to five seconds with a comment explaining exactly that,
and nothing had ever put enough writers against it to find out whether five
seconds is enough.

Eight writers — state changes, renames, project touches, notes — and four
readers, for two seconds: **zero failures**. Dropping the timeout to one
millisecond:

    946 concurrent operations failed; the first was:
      note: store: set note: database is locked (5) (SQLITE_BUSY)

So the parameter is doing real work, and now something says so.

### Two bugs in the chaos test, both of which would hang CI

**`time.After` is not a broadcast.** It delivers its value to exactly one
receiver, so a stop channel shared by twelve goroutines stops one of them and
leaves eleven spinning. The first run of this test lasted four hundred seconds
instead of two. A closed channel is the broadcast.

**Reporting failures over a buffered channel deadlocks on failure.** Once there
are more errors than buffer, the workers block on the send, never see the stop
signal, and the test hangs. Which is exactly what happened when the timeout was
deliberately broken: *the mutation meant to prove the test works proved that it
hangs*. Non-blocking send plus an atomic count.

Both are the same shape as the defects this log keeps recording, in the tool
rather than the product: a chaos test needs its own failure path exercised, or
its verdict on a broken build is "no verdict at all".

### The detector and the hub

Every entry point on the detector at once — the poller evaluating, the pump
observing, hooks reporting, a user clicking a dot, sessions being forgotten and
retained — and the hub with six browsers connecting, reading, closing and
reconnecting while two goroutines broadcast through both its paths.

Clean, four runs each, under `-race`. No finding, and that is the honest
result: the mutexes were right. What the tests buy is that the *next* change to
either one is checked by something other than reading.

## The API, with the poller running

The last of the concurrent surfaces. A `Server` carries three caches that HTTP
handlers and the poller both touch — the hook token behind a `sync.Once`, the
coalesced state snapshot, and whether the reporter script is installed — and
each has its own guard, and each looks right. So did the two races already
found here.

Four readers polling `/api/state`, `/api/settings` and `/api/health`; four
writers renaming and changing state on the same three sessions; two hook
reports arriving the way they do from inside a session; and `pollOnce` running
throughout, which is what actually happens in production.

Clean, four runs, under `-race`, with no request returning 5xx.

**And the test is exercising what it claims.** Removing the mutex around the
snapshot cache produces a data race within eight seconds — so the paths are
genuinely being hit, rather than the workers all queueing behind something and
serialising themselves.

That check matters more than the result. A concurrency test that passes because
nothing was concurrent is the same species as a check whose fixture is empty,
and this session has produced seven of those.

### Where the concurrent surface stands

Six chaos tests, all in `make check`, all verified to fail when the thing they
guard is removed:

- a live session — six viewers, four writers, closed underneath them
- the manager — attach and detach racing on the same names
- detach during attach — the window between claiming and installing
- the detector — every entry point at once
- the hub — browsers arriving and leaving while state is broadcast
- the API — handlers and the poller together
- the store — eight writers against one SQLite write lock

Two real defects came out of them: the PTY descriptor closed while an ioctl was
reading it, and a detach lost to an attach still being built. Both were in code
that read as correct, and neither would have been found by reading it again.

## Fuzzing the one function that is a security boundary

`browse.Resolve` decides whether a path the browser sent stays inside the
project directory. Everything the file panel and the download endpoint do goes
through it. Its tests cover "..", absolute paths and a symlink pointing out —
the ways somebody thought of.

**The first fuzz target was theatre.** Fuzzing the relative path alone: twenty
three million executions, half a million a second, nothing found. There was
nothing to find. The path is collapsed on the third line of `Resolve`, by
`Clean("/" + rel)`, so no string can climb out of the root textually. The
fuzzer was exploring an input space that had already been flattened.

The escape that matters needs the *filesystem* to cooperate. So the fuzzer
builds that too: it chooses where a symlink inside the root points, and then
chooses the path used to reach it.

Three and a half million executions with fuzzer-chosen symlink targets: clean.

**Calibrating it needed one more step than usual.** Removing the containment
check after `EvalSymlinks` made the *existing unit test* fail, which is
reassuring but says nothing about the fuzzer — `go test -fuzz` runs the normal
tests first, so the fuzzer never got to speak. Running it with `-run XXXnone`
to silence them:

    Resolve("link/secret") with link -> "/tmp/…/002"
      returned "/tmp/…/002/secret", which is outside "/tmp/…/001"

Thirty milliseconds.

The seeds run on every `go test`, so the regression value is there without
anybody fuzzing; the exploration is there when somebody wants it.

The lesson is about fuzzing generally, and it applies to the seven "cannot
fail" checks this session has already produced: **a fuzzer exploring an input
that the code collapses in its first three lines will report a clean result
forever**, and the number of executions makes that result look stronger the
longer it runs.

## Fuzzing the parser that reads whatever an agent prints

The OSC scanner is the only parser here fed by something nobody controls: a TUI
redrawing, a program dumping a binary to the terminal by mistake, a
half-written escape sequence split across two reads. A panic in it kills the
pump, and the pump is the session's output.

Twenty-six million executions over arbitrary bytes with arbitrary chunk
boundaries: clean. Making `handleOSC` assume a four-byte payload — the sort of
thing a small refactor does — is caught in forty milliseconds.

**The other half of the target does not work, and the honest thing is to say
so in the file.** It also asserts that the fragment carried between reads stays
under 64 KiB. Removing the cap entirely leaves the fuzz target green, because
exceeding 64 KiB needs an input larger than the fuzzer realistically produces.
`TestUnterminatedOSCDoesNotGrowForever` catches that mutation immediately; the
fuzzer never will.

So the assertion stays — it is free and it would catch a *reachable* regression
where a small input causes unbounded growth — with a comment saying which test
actually proves the property.

That is twice now that a fuzz target's stated purpose was partly unreachable:
here, and the path resolver whose input is collapsed on its third line. The
pattern is worth naming, because the failure mode is silent and flattering:
**a fuzzer reports clean on the parts of the space it cannot reach, and the
execution count makes that look like evidence.** Calibrating each property
separately — not the target as a whole — is what tells the two apart.

## A running session announced as dead because of the directory it sat in

Looking for injection through tmux's format parser turned up a real one, and it
was worse than the shape I went in expecting.

`List` asks tmux for twelve `#{...}` fields joined by 0x1f, one session per
line. Pane titles turned out to be safe — tmux strips control characters out of
them, so `a\012b`, `a\037b` and `a\011b` all come back as `ab`. Working
directories are not. A probe that made two directories, one named with a 0x1f
and one with a newline, and started a session in each:

```
List returned 1 of 3, err=<nil>
  listed vp_p0   path="/tmp/…/002"
  Get(vp_p1) path="" err=tmux: expected 12 fields, got 13
  Get(vp_p2) path="/tmp/…/c\nd" err=<nil>
```

Two of the three running sessions were simply absent from the listing. The
field count was wrong for one and the line count was wrong for the other, and
`List` drops a malformed line rather than blind the sidebar to everything else
— which is the right call in isolation and the wrong outcome here.

**Dropping the line is not where the damage is.** The poller reads a session it
cannot see as gone, and `markVanished` writes that to the database. So a
session that is alive and working gets a headstone, because of the name of the
directory it happens to be sitting in. `mkdir $'a\nb'` is the whole exploit,
and this is a panel for people who run agents in directories they did not
choose.

The fix is to make tmux do the sanitising, before the value is ever joined:
`#{s/[\x01-\x1f]/?/:pane_current_path}`, and the same for `pane_title` and
`pane_current_command`.

Two things about that format string cost time and are worth writing down.

The first is that `[[:cntrl:]]` does not work, and does not fail loudly. The
substitution modifier is terminated by a colon, a POSIX character class
contains two, so the parser splits in the wrong place and the field comes back
**empty** — not an error, not a warning, just nothing where the path was. That
is a strictly worse failure than the bug being fixed, and the only reason it
did not ship is that the probe printed the value. A literal character range has
no colon in it and works.

The second is that the first probe of the substitution syntax reported empty
for everything and nearly sent me looking for a tmux version problem. The
substitution was fine; the ad-hoc test used `-t '=abc'` where
`display-message` needs the pane form `=abc:`. The lesson is the ordinary one —
a negative result from a hand-built target says something about the target
first.

`internal/tmux/paths_test.go` pins it: three sessions, one plain, one under a
newline directory, one under a 0x1f directory, all three must appear in `List`
and none may carry a control character through. Mutation-checked by removing
the scrubbing and confirming the two named sessions go missing.

That check needed a second try, in the way this log has recorded before. The
first mutation was a `sed` whose pattern did not match; the file was unchanged,
the test passed, and "passed" read as "the mutation survived" rather than "the
mutation never happened." Printing the mutated line before running is two
seconds and turns a silent no-op into a visible one.

## Six ways the panel lost, hid, or misdelivered something

Six fixes with one shape between them: in every case the panel did something
reasonable-looking and the user's thing — a file, a directory, a name, a note,
a command — quietly went somewhere other than where they expected.

### A download that could hang the server until it was killed

`handleDownload` resolved the path, checked it stayed inside the project, and
called `os.Open`. Opening a FIFO blocks until somebody opens the other end, so
a named pipe anywhere in a project directory — and agents make them — turned
one click into a request that never returns. Graceful shutdown waits for
in-flight requests, so `systemctl stop` then waited too.

The fix is one check, `info.Mode().IsRegular()`, before the open. The test in
`internal/httpapi/fifo_test.go` carries a hard deadline, because the natural
way to write it is a test that hangs, and a hanging test tells you nothing
about which change caused it.

### A directory large enough to hide its own subdirectories

`browse` capped the listing at `maxEntries` and then sorted it. The cap was
therefore applied to whatever order the filesystem handed back, and sorting
only rearranged the survivors. In a directory with more entries than the cap,
the subdirectories — which sort first and are the only way to navigate further
— could be missing entirely, and the panel gave no sign that anything had been
left out.

Sort first, cap second, and report `Total` and `Truncated` so the tree can say
so. `Readable` also went from "we could stat it" to `Mode().IsRegular()`, which
is the question the download button actually asks.

### A filename that renders as a different filename

Session titles, pane titles and file names all come from outside — the
filesystem, or whatever the agent printed — and go straight into the DOM. A
right-to-left override reverses everything after it, so `report\u202Efdp.exe`
is displayed by every browser as `reportexe.pdf`. In a file tree with a
download button next to it, that is not a rendering curiosity.

(Written as an escape on purpose. The first draft of this paragraph pasted the
real character in, which reversed the rest of the line in every renderer that
shows this file — the documentation for the fix demonstrating the bug.)

`<bdi>` does not help, which was worth measuring rather than assuming: it
isolates the run's directionality from its surroundings, and does nothing about
an override *inside* the text. `safeText` replaces the C0 range, DEL, and the
bidi control family with U+FFFD, and everything user-visible goes through it.

### An idle panel that never stopped talking

The poller broadcasts a state snapshot only when it differs from the last one
sent, with a comment explaining that a tick which broadcasts regardless is
polling with the cost moved onto every viewer. That check had never once
suppressed a broadcast.

`LiveIDs` built its result by ranging over a map. The snapshot embeds that
list, so the serialised bytes were different on every call, so the comparison
always saw a change. Any panel with two or more attached sessions pushed a full
state snapshot to every connected viewer every two seconds, awake or not, and
nothing anywhere looked wrong.

The test asserts the property the poller depends on rather than the sort:
two snapshots taken with nothing happening in between must serialise
identically. Without the fix it fails on the *second* snapshot — not
intermittently, immediately.

Measured afterwards on the real binary, four attached sessions, ninety idle
seconds with a browser watching every frame:

```
frames received while idle: {"pong":3}
state pushes: 0        (one every 2s would be ~45)
websockets opened:  0  (no reconnect)
```

The pongs are the point of that second line. The client declares a connection
dead after sixty seconds without a frame of any kind, and until this fix the
two-second broadcast was refreshing that timer for free. Going quiet meant the
liveness path — client pings every thirty seconds, server answers — had to
carry it alone, and in an idle panel with sessions attached it had never once
been asked to. It holds, with one lost pong of margin and no more. Worth
knowing before assuming that making something stop talking is free.

### A note discarded by clicking the next tab

The notes panel saves 800ms after you stop typing, and the unmount cleanup
cancelled the pending save. Every way that component goes away hit it: the
right panel renders one tab at a time, so clicking Files unmounted Notes; the
component is keyed by project, so switching project did too; and so did closing
the page.

So typing and immediately clicking another tab — one click, nothing unusual —
threw away up to 800ms of writing, on the one panel whose stated premise is
that a note you have to remember to save is a note you lose. Reproduced against
the real binary in a real browser first: type, click Files, and the server
still had `""`.

Unmount now flushes instead of cancelling, and `pagehide` plus
`visibilitychange` cover the page going away. `keepalive` is used only on the
unload path — it is what survives a closing document, but it caps the body at
64KB, and a tab switch leaves the page very much alive.

Two harness checks, one per mechanism, because they are different mechanisms
and a single check would let either one rot. Mutating the unmount flush fails
only the first; removing the unload listeners fails only the second. Neither
masks the other.

### A command composed for one agent, run in another

The mobile compose box is rendered by position rather than keyed by session, so
switching session left the typed text in place while its send handler
re-pointed at the newly selected session. Compose a command for one agent,
glance at another, tap Send, and it runs in the wrong terminal.

Measured on a phone viewport before fixing: `echo MEANT_FOR_ALPHA`, typed while
alpha was selected, survived the switch to bravo and executed in bravo. It
never reached alpha. This is the panel's own premise turned against the user —
many agents at once is the entire product, and sending something to the wrong
one is the mistake that costs.

The obvious fix is `key={current.id}`, which stops the misdelivery by throwing
the draft away — reintroducing, one commit later, exactly the loss just fixed
in the notes panel. One draft per session fixes both: nothing reaches the wrong
terminal, and looking at another session does not cost you what you had
written. Sending clears that session's draft and no other.

The draft map lives in state rather than a ref, because the swap happens during
render — an effect would paint one frame with the previous session's command
still in the box — and the lint rule against reading refs during render is
right about why.

### And a guard that failed on its own explanation

The paragraph above went into this log with the real override character pasted
in as an example, which reversed the rest of the line in every renderer that
displays the file — the write-up of the fix demonstrating the bug, in the
document whose job is to stop that being rediscovered.

That is the Trojan Source class (CVE-2021-42574) arriving by accident rather
than by attack, which is the more likely way it arrives. The panel already
refused to *render* these characters; it had nothing to say about its own
source, where the same reordering means a reviewer reads a different line than
the compiler does.

`internal/config/source_test.go` walks the repository and rejects the
embedding, override and isolate characters in anything a person reviews. Not
the plain marks — U+200E and U+200F do not reorder a run and do appear in real
prose. The walk asserts it visited at least fifty files, because a guard whose
matcher silently stops matching passes forever.

It failed on the first run, on its own doc comment, for exactly the same
reason. Three accidents in one afternoon, every one of them while writing about
the character. A rule that is this easy to break by hand is a rule that belongs
in a test rather than in a paragraph.

## The premise of the project, undone by the unit file

Red line 2 says the panel must never own a PTY that a session's process is a
child of, because "the moment a session's lifetime depends on the Go process,
`systemctl restart vibepanel` becomes destructive and the entire premise of the
project is gone."

The code honours that exactly. The deployment did not, and arrived at the same
outcome by a different route.

tmux's server is started by the panel. It daemonises and re-parents, which is
what makes it feel independent — but cgroup membership does not change on
re-parenting. So the tmux server sits inside the panel's systemd unit, and the
default `KillMode=control-group` SIGTERMs every process in that cgroup on stop.

This had been on the suspect list for a while, marked unverified, which was the
wrong place for it: verifying it costs one transient unit and a throwaway
socket, and touches nothing else on the machine. `systemd-run --user
--unit=… --collect` leaves no files behind and disappears when it stops.

Two sessions, `systemctl --user stop`, three settings:

```
KillMode default (control-group)   2 sessions before -> 0 after
KillMode=process                   2 sessions before -> 2 after
KillMode=mixed                     2 sessions before -> 0 after
```

The tmux server's cgroup line during the run read
`…/vp-killmode-probe-1391938.service`, which is the whole explanation in one
string.

So `systemctl restart vibepanel` was killing every running agent — the exact
outcome the entire tmux architecture exists to prevent — for everyone using the
unit as shipped.

`mixed` is worth naming because it is the answer someone reaches for when told
`control-group` is wrong. It reads like the careful middle setting and it kills
the sessions too: after the main process exits, the SIGKILL phase still goes to
the whole cgroup. Only `process` leaves the tmux server alone.

### What the unit file already knew

The same file, further down:

```
# When a session's process is killed for memory, only that process should die.
# Without this systemd takes down the panel — and with it every other session.
OOMPolicy=continue
```

That comment is correct, and it is about this exact cgroup. Twenty lines above
it:

```
# Stopping the panel must not wait on anything: it detaches from tmux and the
# sessions carry on regardless.
TimeoutStopSec=20
```

Both sentences were in the file at once and only one of them could be true. The
OOM line understood that everything shares a cgroup; the stop line assumed the
sessions were independent. Neither was near enough to the other to look wrong.

### And the runbook sent you the wrong way

`## Sessions died when I restarted the panel` opened with "They should not",
then offered two causes: something owning the processes as children of the Go
process, or a mismatched `--tmux-socket`. Both are real failure modes. Neither
was this one — and this one happened every time, to everyone, on the default
configuration. The runbook page for the symptom was sending the only people who
would ever read it to check two things that were fine.

It now leads with `systemctl --user show vibepanel -p KillMode`, and keeps the
socket check as the second thing to look at.

`TestUnitLeavesTheSessionsAlone` pins the line, because a bare
`KillMode=process` in a unit file is exactly what someone tidying or hardening
it would delete. It rejects absent, `mixed` and `control-group` with the
measurement in the message.

### Why the restart harness never saw it

`restart-check.mjs` restarts the panel and confirms the sessions come back, and
it has always passed. It kills the process directly. The bug is not in what
happens when the process dies — it is in what systemd does to everything
sharing the process's cgroup, and there is no cgroup in the harness at all.

The harness was answering "does the panel reattach", correctly and usefully.
The question nobody was asking is "does the thing that stops the panel in
production stop anything else", and no amount of care inside the harness's
frame would have reached it. A check that runs the binary the way it is run in
development can only ever tell you about development.

## One viewer that stopped reading, and everybody waiting behind it

`Hub.Broadcast` took a snapshot of the connection set and then wrote to each
connection in turn, on the caller's goroutine. `sendRaw` takes the connection's
`writeMu` and writes with a ten second timeout — but *acquiring* that mutex has
no timeout at all, and the connection's own pump goroutine holds it for the
duration of its writes.

So a viewer whose TCP window had closed held up the state update to every other
viewer for as long as its in-flight write took to give up.

Measured against the real binary. A raw socket that completes the handshake,
subscribes to a session printing base64 as fast as it can, and then simply
stops reading. A second, healthy client times how long a rename takes to reach
it:

```
healthy viewer sees a rename in: 103ms, 103ms, 53ms, 103ms
one viewer has stopped reading...
with one stalled viewer:       2210ms, 103ms, 103ms, 103ms, 102ms
```

One connection, one stall, 2.2 seconds of everybody else's panel frozen. That
number is the *cheap* case: this connection tore itself down quickly. The loop
is serial, so the cost is per stalled viewer, and a client that reads just
slowly enough never to be closed pays it on every broadcast rather than once.

The fix is a single pending payload per connection, written by a goroutine of
that connection's own. A slot rather than a queue, because a state snapshot is
absolute — it describes the whole world, so a newer one makes an older one
worthless and dropping the superseded payload loses nothing.

That is the part worth stating plainly: this can be lossy where the terminal
streams cannot. Session output carries bytes that exist nowhere else, which is
why the session package drops a slow subscriber explicitly and sends
`dropped` so the viewer resubscribes and replays. State needed neither
mechanism, and had been given the *blocking* one instead.

After:

```
with one stalled viewer:       102ms, 103ms, 103ms, 103ms, 102ms
```

`TestAStalledViewerDoesNotHoldUpTheOthers` holds a connection's `writeMu`,
which is exactly what a closed window looks like from the hub's side, and fails
if `Broadcast` has not returned within three seconds. With the queue removed it
fails; with it, it passes in four milliseconds.

### What the first attempt at measuring this got wrong

The first probe killed a tmux session behind the panel's back and timed how
long the panel took to notice, on the theory that noticing is the poll loop's
job and the poll loop calls `Broadcast` synchronously.

It reported three milliseconds, before and after.

The first explanation written here was that `/api/sessions` reconciles against
tmux on read, so the answer never came from the poller. That was wrong, and
wrong in a way worth correcting rather than deleting: **there is no
`GET /api/sessions`.** The route exists only for POST, and chi answers a known
path with the wrong method with 405.

The probe polled until the session was absent from the returned list, with
`.catch(() => [])` on the parse. So every 405 became an empty list, an empty
list contained no session, and "no session" was the probe's success condition.
It would have reported three milliseconds against a server that had been
switched off.

The lesson is not about fast paths. It is that a fallback whose value means
*yes* converts every failure into a pass — the network being down, the endpoint
not existing, the JSON being malformed — and the more defensively the probe is
written, the more thoroughly it lies. The catch was added so a transient error
would not crash a long run. It made the run meaningless instead.

The second probe measured what a person actually sees: a rename made in one
window arriving in another. One path, and it throws rather than shrugging when
the request does not do what it should.

## A phone showing four-pixel text

A passive viewer keeps the owner's grid and scales it with a CSS transform,
which is the design and is right: the alternative is reflowing a running TUI
under someone else's hands. The scale was `min(fitWidth, fitHeight, 1)`. Capped
above, and nothing below.

A phone, 390 wide, watching a session a 1920 desktop owned:

```
scale 0.289, font 13px -> 3.76px on screen
```

Under four pixels is not small text, it is a grey smear with no glyphs in it.
And the screenshot showed something the number alone did not: the entire 44-row
grid sat in the top one per cent of the display, with more than a thousand
vertical pixels empty underneath it. Width was the only binding constraint, and
nothing was using the height at all.

So the panel was unusable for the thing it exists to do — read an agent's
question from a phone — while wasting the space that would have made it
readable.

There is an escape: the "take control" pill is present, correctly sized at
154x44, and tapping it gives the phone the grid. It also reflows the desktop
user's terminal to 45 columns, which the button's own tooltip warns about. An
escape hatch whose cost is somebody else's running TUI is not a substitute for
rendering something legible in the first place.

The floor is one line — never shrink below nine pixels of font — and the width
that then does not fit is panned to. `overflow` is switched on only when the
floor actually bit, because a box left scrollable when everything fits invents
a scrollbar and a way to push the terminal off the side of its own container.

```
scale 0.692, font 13px -> 9px on screen, 42% of the width visible at a time
```

Nine pixels is not comfortable. It is letters, and the question stops being "is
there anything there".

Panning does not disturb touch selection, which was the thing worth checking
before changing this: `cellFor` maps a touch through
`rows.getBoundingClientRect()`, and a bounding rect is already in viewport
coordinates with scrolling and transforms applied.

The harness check measures at phone width rather than at the second viewer's
520px, because 520 lands at 7.73px — it would have caught the regression, by
0.27 of a pixel. At 390 the same mutation reports 5.8px against a threshold of
8, which is a check with an opinion rather than a coincidence.

## A scratch shell that beeped, sitting above the agents that needed you

The bell is the only unambiguous "a human is needed" signal available without
hooks, so it outranks everything the heuristic can infer. That is right, and it
was applied to panes with no agent in them.

The rule cleared a bell when visible output arrived more than `bellGrace` after
it. Two things about a plain shell make that unreachable:

- The bell character is not visible output, so it does not move `lastOutput`.
  After a beep, `lastOutput` is still whatever it was *before* the beep, which
  is never later than the bell.
- Nothing else is going to print. Pressing TAB on an ambiguous completion is
  how readline says "I have nothing for you": it rings the bell and produces no
  output at all.

So a scratch terminal where somebody hit TAB showed an orange triangle
indefinitely. And `waiting` sorts to the top — so it sat there *above* the
sessions that really were asking for a human, which is the one thing the sort
order exists to prevent.

This file already contains the argument against it, twenty lines below the bug,
in the comment explaining why silence is not reported as waiting:

> a panel that cries for attention it does not need is one people stop looking
> at.

The fix is that the bell only outranks the heuristic when something other than
a shell is in the foreground. A bare shell has no agent that could be waiting;
if the agent rang and then exited, the pane is back at a shell and `done` is
the honest answer.

Verified end to end rather than only in the unit test, because the interesting
question was whether the scenario is reachable at all — whether a bell from a
shell really does arrive at the detector with `ShellOnly` set. It does:

```
scratch shell, before the beep: done
scratch shell, six seconds after: waiting      <- before the fix
scratch shell, six seconds after: done         <- after
the sleeping agent meanwhile:    working       <- unchanged either way
```

Three existing tests cover the bell and every one of them passes
`Observation{}`, so none of them said anything about a shell. That is the
shape of gap worth looking for: not an untested function, but a tested one
whose tests all happen to agree about a field.

### The guard for that had the same bug in it

Every harness got a shared shape: `authed` throws instead of returning when the
server says the route does not exist. A check that asks for a path the server
does not have gets an ordinary `Response` object and goes on to draw
conclusions from its body, and the more defensively it handles the parse, the
quieter it is about having learned nothing.

The first version tested `res.status === 404`. It was written specifically to
catch `GET /api/sessions`, and it did not catch it, because chi answers a known
path with an unregistered method with **405**. Injecting the exact bogus call
into a harness produced a clean `0 FAIL`.

Which is the whole argument for mutation-testing a guard rather than reading
it. The reasoning behind the guard was right, the code did not implement the
reasoning, and nothing about looking at it would have said so — the number 404
is exactly what "route does not exist" looks like when you are writing it from
memory.

It now covers both, and injecting the bogus call fails the run:

```
[FAIL] harness: GET /api/sessions -> 405; this server has no such route and
method, so whatever this check concluded from the answer was meaningless
```

Throwing aborts the rest of that harness run, which is the right trade: an
answer from a route that does not exist poisons everything downstream of it,
and a check that keeps going is a check reporting on nothing.

### And a survey that came back clean

Since the failure mode is "a fallback whose value means yes", the obvious
question is where else the harnesses do that. Of roughly sixty
`catch(() => …)` sites, nearly all are the safe direction: `catch(() => '')`
feeding an assertion that the text *must contain* something, so an error
becomes a FAIL rather than a pass.

The dangerous shape — `if (await x.isVisible().catch(() => false)) FAIL` —
appears a handful of times, and each one is paired with a positive check
elsewhere that the same element does appear when it should. Delete the element
and the positive check fails. Those pairs hold.

Worth recording as a negative result. The sweep was motivated by a real bug and
found nothing, which is information about the harnesses rather than about the
sweep.

### `waitHealth is not defined`, for the second time

Applying that guard to four files was done with a regex, whose optional
leading group for "any doc comment immediately above" matched lazily from a
`/**` much further up. It ate `waitHealth`, `USERNAME`, `PASSWORD`,
`NEW_PASSWORD` and `cookie` on the way down to `const authed`.

The deleted comment above `waitHealth` read, in full:

> Restored after being deleted by accident: extracting the overflow scan into a
> shared module cut from the scan to the next top-level const, and this sat in
> between. The sweep caught it a minute later as
> `ReferenceError: waitHealth is not defined` — which is the argument for
> running everything after a refactor, not only the thing that was refactored.

Same function, same kind of over-wide edit, same error message, and caught the
same way — by running all four harnesses rather than only the one the change
had been tested against. The note recording the first occurrence was deleted by
the second.

Two things follow, and only one of them is "be careful".

The comment did its job: it made the repeat legible the moment the diff was
read, and turned "why is waitHealth gone" into a known failure mode with a
known cause. That is what it was for.

The other is that a regex with an optional greedy-ish prefix is not an edit,
it is a guess. Reapplying the change by exact literal match — the whole
original block, asserted to occur exactly once — cannot do this, and takes the
same amount of time to write.

## The phone took the desktop's grid by reloading

Grid arbitration is careful about the case it was designed for. A second viewer
arriving does not take the grid; when the owner's connection ends the grid is
*frozen* rather than handed over, and the comment explaining that says exactly
why:

> giving it to the phone glancing at the session from across the room …
> immediately reflows a 147-column agent view down to 13 and leaves the
> returning desktop stuck watching it.

The freeze protects the instant of departure and nothing after it. The
controller is cleared, and the next `Subscribe` — from anyone — takes the grid.
So the phone does not need to arrive at the wrong moment. It only needs to
reconnect, which on a phone is what happens when the browser feels like it.

Measured against the real panel, tmux's own `#{window_width}`:

```
1. desktop alone                     112x34
2. phone joins, passive              112x34   <- arriving does not steal
3. desktop's tab closes              112x34   <- frozen
4. phone merely reloads               46x34   <- taken
5. desktop returns                    46x34, and is passive
```

Step 4 is a reload. Not a click, not a resize — a page load. The agent's view
went from 112 columns to 46, and the desktop came back to find it that way with
no idea why.

### Why the rule could not have been written correctly

`Subscribe` claims an unowned grid because of this, which is also right:

> An unowned session goes to whoever opened it. Without this the first viewer
> is told it is passive … the only way out is a "take control" button the user
> has no reason to think they need.

Both rules are correct and they contradict each other, because "unowned" was
being asked to mean two different things: *nobody has ever driven this*, and
*the person driving it stepped away*. The frozen grid is evidence of the
second, and nothing in the code could tell them apart.

Worse, nothing *could* tell them apart, because the server minted a client id
per connection. A returning viewer was a stranger to the arbitration. The old
test says so in its own variable name:

```go
// The desktop comes back and reclaims it by subscribing.
back, _ := live.Subscribe("desktop-2")
```

`desktop-2` — the author knew the returning connection had a different
identity, and the rule was written to accommodate that. The test asserted the
mechanism that causes the bug, two comments below the comment describing the
bug.

### Identity that survives a reconnect

The browser now supplies its own id, in `sessionStorage`, and the rules can say
what they mean: `lastController` remembers who stepped away, a subscribe claims
an unowned grid only if nobody has ever owned it or the subscriber is the one
who left, and pressing "take control" always works regardless.

`sessionStorage` rather than `localStorage` is the substance of it. It survives
a reload and a dropped socket; it does not leak across tabs. Two tabs of the
same browser are two viewers at possibly two sizes and must not claim each
other's grid, and a closed tab is a viewer that is not coming back.

The id is client-supplied and therefore not trusted, which costs nothing: it
grants no capability that pressing the button does not already grant.

Afterwards, same probe:

```
4. phone reloads                     112x34, still passive
6. desktop resizes its window        offered "take control"
7. after taking it                   tmux follows its window
8. same tab reloads                  reclaimed without asking
```

Step 6 matters as much as step 4. A viewer returning in a *new* tab is a new
identity and does not reclaim — and if its window happens to fit the frozen
grid exactly, it is offered nothing, because there is nothing to change. The
moment it resizes, the affordance appears. It is never stuck; it is asked once,
which is the cost the freeze comment already accepted:

> a lone remaining viewer keeps scaling until it taps "take control" once. That
> is a deliberate, visible action rather than a surprise.

### A dead line in the fix, found by mutating it

The first version also cleared `lastController` inside `TakeControl`, with a
comment about the previous owner having been overruled. Mutating that line away
changed no test, and then changed no behaviour either: `Unsubscribe` sets
`lastController` to whoever was controlling as they leave, so it is always
overwritten before it could matter. Dead code with a comment claiming an
effect it did not have.

The test written to pin it passed with and without — a check that cannot fail,
which is the thing two entries above this one were about. Both are gone. What
replaced them pins the property that is actually load-bearing: `TakeControl`
must work on a grid frozen for somebody else. Guarding it with the same
identity check that guards `Subscribe` would leave a viewer whose colleague
shut their laptop stuck scaling a grid it cannot have, pressing a button that
does nothing. That mutation fails the test.

## A panel that forgot the size you gave it, behind a handle you could not grab

Two bugs in the same twenty pixels, and the second one hid the first.

### The size

The right panel and the terminal strip each stored one number, with zero
meaning collapsed. The comment above both said:

> Height doubles as the collapsed flag: 0 means hidden. One value to store, and
> reopening restores the size the user last chose rather than a default.

The second sentence cannot be true given the first. Collapsing writes 0 over
the only copy of the chosen size, so there is nothing left to restore, and the
code two hundred lines away says so plainly:

```jsx
onClick={() => setRightWidth(RIGHT_DEFAULT_WIDTH)}
```

Drag the notes panel out to 480 to read something, glance at the terminal,
open it again: 280. Every time. Both panels.

Size and openness are now two stored things, and `bottomHeight` / `rightWidth`
stay derived so nothing else changed. A stored 0 is the old encoding and is
honoured once — the panel comes back collapsed for anyone who left it that way,
and reopens at the default. Zero is never written again.

Measured before and after, including the migration:

```
1. width the user chose              480
3. reopened                          280  -> 480
4. collapsed survives a reload       yes
5. and reopens at                    480
6. old stored "0" reads as collapsed yes
7. and reopens at the default        280
```

### The handle

Writing the harness check for that meant dragging the divider, and the drag did
nothing. `elementFromPoint` across the eight-pixel grip:

```
offset  0 1 2 3 | 4 5 6 7
        grip    | content
```

Half the target, and the wrong half. The grip has a negative right margin so it
straddles the panel border rather than sitting beside it — which is the right
idea — but the content is the later sibling and, with no stacking order, paints
over the four pixels they share. Those four are the ones on the visible edge,
which is where a person aims. What was left was a four-pixel strip in the empty
space *outside* the panel.

`relative z-10` on the grip, and all eight pixels hit it.

This one had survived every run of every check, because no check had ever
dragged that divider. It was found by writing a test for a different bug — the
resize was a *step* in the check, not the thing being checked, and it failed as
a step.

### The WARN that was worth writing

The check needs a non-default width before collapse, or it cannot tell a
remembered width from a coincidence. So it verifies the drag actually moved the
panel, and if it did not, says:

```
[WARN] panel: dragging the divider moved the panel from 280 to 280; the restore
check below cannot tell a remembered width from the default
```

Which is what it printed with the grip covered again — the check declining to
report on something it could not observe, rather than passing. Both mutations
give distinct signals: covering the grip is the WARN, forgetting the width is

```
[FAIL] panel: a panel dragged to 424px reopened at 280px; closing it threw the
width away
```

## The replay that was drawn on a screen nobody could see

The cold-start path had a mechanism, a confident comment, a test, and a browser
check. It did nothing at all.

`Attach` primed the ring from `capture-pane -S - -E -1` — the history above the
visible screen — reasoning that a panel restart empties the ring, and that
without it "the first person to open a session sees a blank terminal attached
to a live process". The comment even explained why the range stops one line
short: "attaching repaints the visible screen, so only the history above it is
taken; the two compose exactly."

They do compose exactly. Onto a buffer that is then hidden.

Dumping the first bytes the attach actually sends:

```
\x1b[?1049h \x1b[22;0;0t \x1b[?1h \x1b= \x1b[H \x1b[2J …
```

`ESC[?1049h` is the switch to the alternate screen, and the alternate screen
has no scrollback — that is its definition. The primed history is written into
the normal buffer, and a millisecond later tmux draws everything else somewhere
the normal buffer cannot be seen from.

Measured in a browser after a real backend restart: `.xterm-viewport`
`scrollHeight` 414, `clientHeight` 414. Nothing above the screen, on a session
where `tmux capture-pane -S - -E -1` still returned 68 lines of history. Same
on a live page that had never seen a restart, because the ring replays the
`1049h` too.

### Deleting it changed nothing

Priming disabled, everything re-run:

- the rendered screen after a restart: identical, same 22 rows
- the scrollback: still absent, same numbers
- `restart-check`: still `0 FAIL`, including its own "cold replay" check

Which is the test that names this exactly, written in this file a few hundred
lines above, about this very harness:

> The rule: a test that exercises a nearby path is not a test of this path. Ask
> what would have to be deleted for the fallback to run, then delete it.

Nobody had run it against the entry that states it. The thing that had to be
deleted was the priming, and deleting it left every check green.

`TestReconnectReplaysScrollback` was the other half of the illusion. It prints
one line. One line fits on the visible screen, and the visible screen comes
from the repaint, so the test never touched scrollback in its life. It is now
`TestReconnectReplaysRecentOutput`, which is what it checks and is worth
checking.

### What replaced it

The priming and `CaptureScrollback` are gone — with the priming removed the
helper had no callers at all, which is the same defect one level up.

In their place, `TestAFreshViewerIsFilledWithoutWaitingForOutput` pins the
guarantee that was actually being delivered the whole time: a session that has
finished printing and is sitting idle must still fill a terminal that opens on
it, and what delivers that is the repaint. If the repaint ever stops arriving,
every session opened after a restart is a blank rectangle attached to a live
process — the exact failure the priming was written to prevent, and the one it
was never preventing.

### The part that is not a bug, and the part that might be

None of this is the panel mishandling tmux. tmux uses the alternate screen in
every terminal it runs in; scrolling back is copy-mode's job, not the outer
terminal's. Byte-perfect replay of the *screen* is what the ring promises and
what it delivers.

The lever, for anyone who wants the browser's own scrollbar to work, is
`terminal-overrides ',*:smcup@:rmcup@'` in `vibepanel.conf`, which keeps tmux
out of the alternate screen. Its cost is well known and not small: every full
redraw lands in the scrollback as another copy of the screen. Not taken here.

Worth writing down alongside it: on a phone there is no way into copy-mode at
all. The key bar has `pgup` and `pgdn`, which in a pane running a shell do
nothing, so they read as scrollback controls that are inert. That is a real
gap, and a product decision rather than a fix.

## Three lines typed on a phone, three instructions to the agent

The compose box exists because typing into a raw PTY on a phone is unusable
with an input method, and its comment says what it is for:

> Composing first and sending once is the only way this works.

It did not send once. Shift-Enter makes a new line in the box, Send wrote the
whole thing into the PTY as bytes, and a newline in the middle of a byte stream
is a newline: indistinguishable from someone pressing Enter. Measured against a
reader that echoes one submission at a time —

```
composed: "please refactor the auth flow\nkeep the passkey path working\nand
           do not touch the tmux config"

GOT<please refactor the auth flow>
GOT<keep the passkey path working>
GOT<and do not touch the tmux config>
```

Three submissions. An agent starts refactoring the auth flow before it has read
the sentence telling it what not to touch. On the one control whose entire
premise is composing a long instruction before sending it, and on the platform
that control exists for.

### Bracketed paste, and who is allowed to decide

`ESC[200~ … ESC[201~` is how a terminal says "this block arrived at once, do
not act on it line by line". Wrapping the text is the fix, and it cannot be
done unconditionally: an application that never asked for bracketed paste
receives the markers as literal characters in the middle of the message.

tmux tracks that mode per pane. `paste-buffer -p` brackets only when the target
pane asked. So this input goes through the tmux command socket rather than
through the PTY — not because the PTY is wrong, but because the PTY does not
know the answer to the question and tmux does.

Only for text with a line break in it. A single-line message has no ambiguity
to resolve, and no reason to leave the fast path.

### The return has to be the server's job

The paste travels by the tmux command socket and a carriage return would travel
by the PTY. A client sending one after the other is racing two roads to the
same pane. `MsgPaste` therefore carries `submit`, and the return is written
once the paste has been accepted.

### What the measurement looked like, twice wrong before it was right

The first reading said the fix had made things *worse* — two submissions
instead of three, with the first line missing. The line was not missing; it had
wrapped in the pane, and the probe filtered rows with `startsWith("GOT<")`.

The second reading said the paste was happening twice: `^[[200~ … ^[[201~`
appeared in the capture twice over. That was the tty echoing its own input, and
ECHOCTL renders ESC as `^[` exactly the way `cat -v` does. `stty -echo` and one
copy remained.

The Go test then read half a paste — `^[[200~` and the first line, no closing
marker. Canonical mode holds a line until it sees a newline, and the closing
marker arrives after the last line and before any newline, so it was still
sitting in the tty buffer. `stty -icanon min 1` and it completes.

Three misreadings of the same measurement, none of them in the code being
measured. The instrument needed more care than the change did, which is worth
remembering the next time a probe reports something surprising.

### What the fix does not do

An application that never asked for bracketed paste still receives three lines,
because there is no way to tell it otherwise — and the test asserts that it is
sent no markers, since garbage in the middle of the message is worse than the
problem. The fix helps exactly the applications this panel exists to drive.

## The panel told every link where it lived

The terminal loads `WebLinksAddon`, so any URL an agent prints becomes
clickable. That is a good affordance and it means the panel navigates to
addresses chosen by whatever an agent read, echoed, or was told to print.

A listener standing in for somewhere-else-on-the-internet, an agent printing
`See http://…/docs for details`, and a click:

```
requests the third party saw: [{"url":"/docs","referer":"http://127.0.0.1:38475/"}]
```

The panel's exact origin. In the deployment this project is for, that is
`https://panel.example.com:8443/` — handed to an arbitrary host, on a
click, by a panel whose entire exposure story is a non-standard port and a
password.

There were no security response headers at all:

```
referrer-policy              (absent)
x-frame-options              (absent)
content-security-policy      (absent)
x-content-type-options       (absent)
cross-origin-opener-policy   (absent)
```

`Referrer-Policy: no-referrer` closes it, and the same listener afterwards sees
`referer: null`.

### The thing next to it that was already right

`window.opener` looked like the same bug — a page opened by a click on
agent-controlled text, able to navigate the tab it came from. It is not. The
addon's default handler opens a blank window, sets `opener` to null and only
then navigates:

```js
const n = window.open()
if (n) { try { n.opener = null } catch {} ; n.location.href = t }
```

Worth writing down, because it is the sort of thing that gets "fixed" twice —
and because the referrer leak survives that handler exactly: a navigation from
`about:blank` inherits the opener's referrer, which is why the leak was there
despite the opener being cleared.

### The other three headers

Added in the same pass because they cost nothing and each one is a separate
errand otherwise: `nosniff`, `frame-ancestors 'none'`, and
`Cross-Origin-Opener-Policy: same-origin`.

`frame-ancestors` rather than a real Content-Security-Policy. A full policy
would have to survive the inline styles xterm and Tailwind generate at runtime,
and a CSP that breaks the terminal is a CSP somebody turns off within a day.
This one directive restricts nothing the panel does.

The session cookie was already `HttpOnly; SameSite=Strict`, checked while
measuring, which is why framing was a hardening rather than a hole.

`TestSecurityHeaders` asserts all four on the API, on the page, and on a
refused request — an attacker's browser is still a browser. Removing the
middleware fails it seven times.

## The one endpoint that has to be public, and allocates

`/api/auth/passkey/login/begin` cannot require a session — it is how you get
one. It creates a WebAuthn ceremony, stores it for three minutes, and returns
the challenge. `login/finish` is behind the login throttle. `begin` was behind
nothing.

The throttle would not have helped anyway: it counts *failed attempts*, and
starting a ceremony has no notion of failure. What was missing was a bound.

One laptop, a local panel, twenty-five seconds:

```
70,238 requests, 0 refused
rss 49 -> 70 MiB
rate  31k in the first 5s, then 13.6k, 10.9k, 7.9k, 6.5k
```

The decay is the real finding. Every `put` and every `take` sweeps the whole
map for expired entries, so the cost of a request grows with the number of
requests already made. Roughly 6,300 a second down to 1,300 in twenty-five
seconds, with the panel spending the difference on scanning a map an anonymous
caller controls the size of. It self-limits in the sense that a fire
self-limits when it runs out of house.

`maxChallenges = 4096`, checked after the sweep so a burst that has aged out
costs nothing. Same flood afterwards:

```
389,344 requests: 4,095 accepted, 385,249 refused with 503
rss flat at 31 MiB
rate constant at ~15,600/s
health latency unchanged at 4ms
```

The point is not the refusal. It is that the cost of an attack became flat
instead of quadratic — the panel now answers faster under the flood than it did
before, because scanning four thousand entries is cheap and scanning two
hundred thousand is not.

### 503, not 500

A full store is a temporary refusal, and the two existing `put` call sites both
mapped any error to 500. A 500 here sends whoever is on call looking for a
panel fault that is not there. The message names the way through:

> too many sign-ins in progress; use your password, or try again in a moment

Which is true, and is the design: passkeys are an addition, never the only
door. Refusing every passkey ceremony while under attack is a real cost, and it
is smaller than the alternative, where nobody's panel responds.

### The cap has to be a queue, not a wall

Checked after the sweep rather than before, so that once a flood stops the
entries age out and sign-in works again without restarting anything. That is a
separate property from the cap itself, and it has its own test: fill the store,
backdate every entry, and the next ceremony must be accepted. Moving the check
one line earlier passes the bound test and fails that one.

## The throttle counted addresses, and addresses are free

The login throttle keys on the client address, which is the obvious thing and
is only correct for IPv4. The smallest IPv6 allocation anyone is given is a
/64: eighteen quintillion addresses, all belonging to the same person, each a
different key. Fifty failures from one /64, never throttled once.

A password guessed at that rate is a password guessed, and the panel is
deliberately on the public internet.

Bucketing to a /64 is the fix, and the /64 is the right width precisely because
it is the unit that gets handed out — narrower is evadable, wider throttles
somebody's neighbours. Done inside the throttle rather than at the four call
sites, because forgetting one call site is exactly this bug again.

### The half the comment did not say

`evict()` bounds the map at four thousand entries by dropping the oldest, and
its comment already conceded the cost:

> This does let a source that can present many addresses shorten its own
> backoff.

*Its own.* It also flushed everybody else's. An address with six failures
recorded against it was forgotten because somebody else arrived from eight
thousand addresses — so rotation was not merely evasion, it was a reset button
for the whole throttle, including for an attacker being throttled on IPv4.

Bucketing does not fix that. A /48 is sixty-five thousand /64s, still well past
the bound. What fixes it is ranking: evict fewest failures first, then oldest.
An entry with one failure against it is close to noise; an entry with six is
the thing the structure exists to remember. Displacing it now costs an attacker
six real failures per address instead of one request, and with /64 bucketing
those are bounded per allocation.

Two fixes for what looked like one bug, and the tests are orthogonal: removing
the bucketing fails only the /64 test, removing the failure ranking fails only
the eviction test. That was worth checking, because the first version of this
entry assumed bucketing would fix both.

`ClientIP` is untouched. The audit log and the CIDR allowlist still see the
exact address, which is what they are for.

## Plaintext on every interface, by default, quietly

`--addr` defaults to `:8443`, which is every interface. `--tls` defaults to
`off`. So `vibepanel serve`, with no arguments, serves a terminal and the form
you type your password into, unencrypted, to anything that can route to the
machine.

The startup banner's entire statement of this was one letter:

```
  url          http://localhost:8443
```

on the same screen as the one-time setup token, which is the line the operator
is actually reading.

The README knew the shape of the problem and only in one form — it explains
that a misspelled `VIBEPANEL_TLS` "used to mean a panel serving plaintext on a
public port while its operator believed otherwise". Spelling it correctly and
leaving it at the default meant the same thing, and nothing said so.

There is now a warning in the same place as the misspelled-variable one, above
the setup token where it cannot be scrolled past:

```
  WARNING: TLS is off and the panel is listening on every interface on this machine.
           A terminal, the password you type into it and the session
           cookie all cross the network in the clear, and anyone who
           can see that traffic can replay the cookie.
           Use --tls acme or --tls files, put a proxy that terminates
           TLS in front, or bind to 127.0.0.1 if this is only for you.
```

Not a refusal. Plaintext on a trusted LAN, or behind a proxy that terminates
TLS itself, is a legitimate way to run this; a panel that refused to start
would be worked around inside a minute and would have taught the operator to
ignore it. Bound to loopback it says nothing, because that is genuinely
private.

### What was already right

The session cookie's `Secure` flag is conditional on the TLS mode, with a
comment explaining that setting it unconditionally would break a plain-HTTP
deployment entirely — every request silently unauthenticated, nothing on
screen to say why. Checked while measuring this, and worth recording as a
non-finding: the cookie is not carelessly insecure, it is deliberately
conditional, and the missing piece was only ever the warning.

## The clock icon that deleted your arrangement

The sidebar lets you drag projects into an order. Once you have, a clock icon
appears with the tooltip "Sort by recent activity again". It is an icon, in a
header, with no confirmation — every signal says *view toggle*.

It ran `UPDATE projects SET sort_index = NULL`.

Four projects arranged `delta bravo alpha charlie`, one click:

```
4. after one click:           alpha bravo charlie delta
5. the button is now:         gone
6. order mode reported:       auto
```

The button removes itself, because it only renders in manual mode. So the
arrangement is destroyed, there is no undo, and there is not even a control
left to press. The way back is to drag every project again.

### The same shape as the panel width

The mode was *inferred* from the data: "manual" meant at least one project
carried a `sort_index`. One value doing two jobs, so the only way to express
"use automatic ordering" was to erase the positions — exactly the bug that made
a collapsed panel forget its width a few entries ago, in a different file, with
a different author's reasoning behind it.

Two things now. `sort_index` holds the arrangement; a setting holds which
ordering is in use; `ListProjects` picks. Switching costs nothing in either
direction, and the sidebar offers the way back — a second button that appears
when automatic ordering is showing and an arrangement is stored.

A database from before this reads its mode the old way, from whether any
project carries a position, so an existing manual arrangement stays manual on
upgrade.

### The check that admitted it could not tell

The harness compares the two orderings around the round trip, and in its
fixture they agree: the project it drags to the top is also the one it has been
clicking into, so it is the most recently active as well. Comparing what is on
screen proves nothing there.

So the assertion that carries the check is against the server —
`/api/state`'s `hasProjectOrder` must still be true after switching to
automatic — and the on-screen comparison runs only when the two orderings
actually differ, with an INFO line saying so when they do not. Making
`hasProjectOrder` always false fails the run; the on-screen comparison alone
would not have.

### And a check I had been running that was not the check

`npx tsc --noEmit` passed on a change that `npm run build` then rejected: a
second construction of `PanelState`, in `socket.ts`, missing the new field.
The build runs `tsc -b`, which follows the project references in
`tsconfig.app.json`; the bare invocation does not, and had been quietly
covering fewer files all session. Verify with `npm run build`.

`make lint` already runs `npx tsc -b`, so the gate was never the thing with the
blind spot — only the ad-hoc check standing in for it was. Which is its own
small lesson: a faster substitute for the real check is a different check.

## The screen every check reached past

Five harnesses, and all five begin the same way: complete the setup through
`POST /api/auth/setup`, because they need a session cookie to seed with. Which
means the one screen a new user cannot avoid — paste the one-time token, choose
a password — had never been driven in a browser. Neither had adding the first
project, which goes through a `window.prompt` and is the first thing anybody
does after signing in.

Driven by hand, it works, all of it: the setup form appears rather than the
sign-in form, a short password is refused with "password must be at least 12
characters" and leaves the token in place, completing it lands in the panel,
the empty panel says "No projects yet. Add one to point the panel at a
directory.", a directory that does not exist is refused with `cannot open
directory: stat /definitely/not/here: no such file or directory`, and a real
one appears in the sidebar.

A negative result, and the reason to write the check anyway: this is the screen
where a regression costs the most and would be noticed the latest. Every other
harness would stay green through it.

`first-run-check.mjs`, and `make first-run-check`.

### The token has to survive a refusal

One assertion in there is not obvious and is the one worth having. The setup
token is printed once, at startup, and a failed attempt must not clear the
field it was typed into — otherwise a mistyped password costs a restart of the
panel to get a new token. It survives; now it has to keep surviving.

### Three mutations, two of which did not mutate anything

Checking the new harness meant breaking things on purpose, and two of the three
attempts silently did nothing:

- The build was run as `make build >/dev/null 2>&1`. Had it failed, the harness
  would have exercised the *previous* binary and passed, which is precisely the
  `.catch(() => [])` shape from a few entries above: a step whose failure is
  indistinguishable from success.
- A Python `replace(..., 1)` hit the first `setError(...)` in the file, which is
  in a different handler, so `guard`'s error path was never touched. The
  harness passed, correctly, and looked like a check with no teeth.

Only when the mutation was aimed at the right line did it report what it
should:

```
[FAIL] project: a directory that does not exist was refused with nothing on
screen to explain it; the first thing a new user does is accept the suggested
path
```

This log already carried the lesson once, about a `sed` whose pattern did not
match. The general form is worth stating plainly: **print the line you changed,
and never silence the build that carries the change.** A mutation that did not
happen and a mutation that was survived look identical from the outside, and
the wrong conclusion from that pair is "the check is useless", which is exactly
the conclusion that deletes a working check.

### And two claims of mine that were wrong

Killing a session was on the list as "one click, no confirmation". It is not:
`killSession` opens `window.confirm("Kill …? The process is terminated.")`.

And `make lint` runs `npx tsc -b`, the one that follows the project references
— so the gate never had the blind spot described in the previous entry. Only
the bare `npx tsc --noEmit` standing in for it did.

## A diagnostic that stopped diagnosing

`doctor` runs nine checks and returned at the first failure. So a machine with
three problems took three runs to find them: fix the data directory, run again,
discover the database, run again, discover the isolation. A tool whose job is
"tell me what is wrong here" was asking the operator to bisect their own
environment.

It also returned the error it had just printed, and `main` prefixes whatever
comes back with `vibepanel:`, so every failure appeared twice:

```
[FAIL] data dir           /tmp/…/data: config: create …: permission denied
vibepanel: config: create …: permission denied
```

Which reads like a crash rather than a report.

Every check that can run now runs, the ones that cannot say why, and the
returned error is a count so the exit code still means something to a script:

```
[ok  ] tmux binary         3.6
[FAIL] data dir           /tmp/…/data: config: create …: permission denied
[--  ] database           skipped: the data directory is not usable
[--  ] tmux server        skipped: the data directory is not usable
[--  ] isolation          skipped: there is no session list to check
[--  ] passkeys           disabled; password login only
[ok  ] environment        no unrecognised VIBEPANEL_* variables

vibepanel: 1 check(s) failed
```

The dependencies are real and are stated rather than assumed: the socket and
the generated tmux config both live under the data directory, so a data
directory that cannot be created means there is no tmux server check to run
either.

`release-check.sh` covers it: an unwritable data directory must exit non-zero,
name the directory, say which checks were skipped, still reach the later ones,
and print the failure once.

### Five instrument errors in one session

This turn produced three of them, and they are all the same shape — a
verification step that quietly did not verify:

- A read-only-database probe that `chmod`-ed the files *after* the panel had
  opened them. `chmod` governs `open()`, not descriptors already held, so
  SQLite kept writing and the panel's cheerful "saved" was the truth. The probe
  simulated nothing.
- `go build ./...` before running the binary. That compiles and writes nothing;
  the run used the previous `./vibepanel`. Worse, the old and new versions of
  the line under test printed *identical text*, so the output looked like a
  fix that had not taken.
- A mutation that returned `fmt.Errorf("%s: %w", …, err)` where `err` was not
  the error I meant — the real one was scoped to its `if` statement, so the
  wrapped value was nil. It compiled, it ran, and the assertion it was supposed
  to exercise never saw a duplicate.

Added to `sed` that did not match and a build silenced with `>/dev/null`, from
earlier entries, that is five. The common form is worth naming once:
**every one of them made a check appear to pass.** None made anything fail.
That asymmetry is not luck — a broken instrument reports the absence of a
signal, and absence is what "pass" looks like.

The habits that catch them are cheap and specific: print the line you changed,
build with the command that writes the binary, and make the mutation fail the
check *before* trusting the check.

## Two comments that would have licensed deleting the recovery

A coverage sweep with `-coverpkg=./internal/...` — the flag matters, per-package
coverage counts none of what the httpapi tests exercise in `internal/auth` and
made a dozen live functions look dead — turned up four functions with no
callers and no tests: `GetSessionByTmuxName`, `SendKeys`, `ClearBell` and
`PurgeExpiredAuthSessions`.

`ClearBell` is the interesting one, because pulling on it found a contradiction
between two comments about the signal this whole product exists to surface.

`tmux.Info.Bell`:

> Always false under the panel's configuration … The real signal is the \007 in
> the PTY stream … but **do not build on it**.

`vibepanel.conf`:

> Nothing polls window_bell_flag — under bell-action "any" tmux forwards the
> bell to its client instead.

And `Reconcile`, at startup, building on it:

> A bell that rang while the panel was down is still latched … Read it before
> attaching, which clears it: otherwise restarting the panel loses every "this
> needs you" raised while it was gone.

They cannot all be true. Measured under the embedded config, session ringing
with no client attached:

```
clients attached: 0
window_bell_flag: 1
… after a client attaches: 0
```

So `Reconcile` is right and the two comments are wrong. Both halves of its
claim hold: the flag latches when there is nobody to forward to, and attaching
spends it, so the recovery consumes each bell exactly once.

The comments are the defect. Either one, read by someone tidying up, is
permission to delete the only thing that recovers a "needs you" raised while
the panel was restarting — which is precisely when nobody was watching.

`TestTheBellFlagLatchesWhenNobodyIsAttached` pins both halves, because they are
properties of tmux rather than of this code and a version bump could take them
away silently.

### The test that was measuring the wrong tmux

It passed. It also passed with `monitor-bell off` pasted into the config, and
with `bell-action none`.

`newTestClient` builds a `Client` but never calls `EnsureServer`, and
`EnsureServer` is what writes the embedded config to disk. The server was
coming up on tmux's defaults, so the test was pinning tmux's behaviour and
saying nothing about the panel's configuration — while its comment claimed to
measure exactly that.

With `EnsureServer` in the test, `monitor-bell off` fails it.

`bell-action none` still does not, and that turned out to be correct rather
than a hole: `bell-action` decides whether a bell is forwarded to an attached
client, not whether the flag latches when there is none. What it breaks is the
live signal, and the render check catches it —

```
[FAIL] state: a session that rang the bell never showed as waiting
```

— which is worth having established rather than assumed. Two options in the
same file, two different checks, and neither covers the other.

### The three that were simply dead

`ClearBell` is gone. Its own comment claimed the panel depended on it —
"without this the flag latches on … the waiting badge would never clear" —
and that is false: attaching is what clears the flag, which is why nothing
ever called it. `SendKeys` and `GetSessionByTmuxName` are gone too, uncalled
and untested.

`PurgeExpiredAuthSessions` stays for now and is worth naming as an open thread:
nothing calls it, so expired rows accumulate forever. Not a security hole —
`AuthSessionByToken` filters on `expires_at > ?`, so an expired session cannot
authenticate — just a table that only grows, one row per sign-in.

## A table that only grew, and two promises that turned out to be kept

`PurgeExpiredAuthSessions` was the last of the four functions the coverage
sweep found with no callers. Unlike `ClearBell`, its reason for existing was
true: nothing removed expired sign-ins, so the table grew by one row per
sign-in and never shrank.

Not a security hole. `AuthSessionByToken` filters on `expires_at > ?`, so an
expired row cannot authenticate — it is dead weight, not a way in. Worth
stating, because "expired sessions are never cleaned up" reads like something
much worse than it is.

Called at startup now, in `serve` rather than in `openApp`, which the admin
subcommands share: `vibepanel project list` should not quietly write to the
database. Not on a ticker either — a sign-in is rare enough that a panel
restarted every few weeks accumulates nothing worth a goroutine, and a purge is
easier to reason about when it happens at a moment somebody chose.

```
level=INFO msg="purged expired sign-ins" rows=2
rows left: 1
```

The test pins the half that would be the louder bug: a purge that took the live
session with it would sign everybody out at every restart. Widening the
`WHERE` to match everything fails it on both counts.

### Two things that were already right

`RecordSize` also had no coverage, and it carries a promise worth checking: "a
reconnecting browser starts at the grid the session was last used with". If the
stored size were wrong or ignored, every panel restart would reflow every
agent's TUI to the 120x32 attach default — with nobody watching, which is the
worst time for it.

```
1. the grid the session was used with: 132x40
2. while the panel is down:            132x40
3. after the panel restarted:          132x40
```

Kept. And the earlier sweep's other suspicion — that `Info.Bell` might be a
field nobody could rely on — turned out the same way once measured: the
mechanism works, only the comments were wrong.

Three negative results in a row is worth noticing rather than being
disappointed by. The sweep's value was not that it found broken code; it is
that "no test touches this" and "this does not work" are different claims, and
the only way to tell them apart is to go and look.

## Two of the three signals cannot arrive

`handleOSC` treated `OSC 9` and `OSC 777` as "this session wants attention",
and the state table has always listed them beside the terminal bell. Pulling on
one of them found a parsing bug, and then found that the parsing bug could
never fire.

### The parsing bug

`OSC 9` carries two things that have nothing to do with each other.
`OSC 9 ; <text>` is the iTerm2 desktop notification, and reading it as
attention is right. `OSC 9 ; 4 ; <state> ; <percent>` is the ConEmu progress
indicator, which anything drawing a progress bar emits over and over during a
build, an install or a download.

Both were read as somebody asking for a human. Four progress forms, all
mis-read as attention. `waiting` sorts to the top, so a build reporting
progress would have sat above the agent that really had stopped and asked.

The guard is a prefix test on `"4;"` rather than a leading `4`, so a
notification reading "4 tests failed" is still a notification. `OSC 777` got
the same treatment: only `notify;` counts, because other subcommands live under
that number.

### Then the reachability check

Two attempts to reproduce it end to end both came back `working`, never
`waiting`. The first because the probe ran a shell loop, so `ShellOnly` made
the detector ignore the bell for a different and entirely correct reason — an
earlier fix in this same session masking this one. The second, with `python3`
in the foreground, still would not fire.

Because tmux never delivers it:

```
OSC 9;plain notification   bell_flag=0 activity=0, nothing reaches the client
OSC 777;notify;t;b         bell_flag=0 activity=0, nothing reaches the client
BEL                        bell_flag=1
```

tmux consumes both sequences and drops them. It does not forward them to its
client, and does not convert them into a window bell or an activity flag.

So the fix is a latent correctness fix, not a live bug fix, and saying which is
the point. What the measurement is actually worth is the other half: **the
terminal bell is not the most reliable of three signals available without
hooks, it is the only one that exists.** An earlier entry concluded the
heuristic was weak because Claude Code rings no bell — that is about one agent.
This is about every agent, including a well-behaved one that sends a
notification sequence instead. It would be swallowed by the multiplexer before
the panel could see it.

`TestTmuxSwallowsDesktopNotificationSequences` pins that, and its failure
message says what it means: a tmux that starts forwarding OSC 9 fails the test,
and the failure is good news — the parser is already right for that day.

### A mutation that corrupted the source

Verifying the guards was done in a shell `for` loop over strings containing
`||`, `&&`, `!` and quotes. Word-splitting cut one pattern at the `||`, the
assertion for the next one failed before its restore ran, and the *restore*
step of the following iteration then replaced the first `false` it found — in
an unrelated function — with a fragment of Go containing backslashes. Three
separate lines of `osc.go` were damaged, in two functions that had nothing to
do with the change.

Loudly, at least: it did not compile. That is the one thing distinguishing it
from the five earlier instrument errors, every one of which made a check
quietly appear to pass.

The replacement is a Python script that holds the original text in a variable,
applies one mutation at a time, and ends with

```python
assert P.read_text(encoding='utf-8') == orig
```

which is the assertion the shell loop could not make. Three mutations, each
caught by exactly one test, and the file byte-identical afterwards.

## The one part running outside Go, and the only path through it nobody had run

Every piece of the hook mechanism had a test. The path did not.

`report.sh` is the file that gets merged into somebody's `~/.claude/settings.json`
and run by the agent on every state change. Two tests execute it, and both are
negative: it no-ops with no environment, and it refuses an unknown state.
Nothing established that the positive case works — that a real script, with the
environment the panel injects, actually moves a session's state.

That is a strange thing to have left untested, and it became stranger this
session. Without hooks the panel infers state from the byte stream, and the
only signal that reaches it is the terminal bell: tmux swallows OSC 9 and OSC
777 before the panel can see them, and the agent most people run here does not
ring. So the script is not an optional enhancement to state detection. It is
most of state detection.

`TestTheReporterScriptActuallyReportsState` walks all of it: the installed
file, the injected environment, curl, the endpoint, the token check, the
detector, and the state the sidebar reads. Report `waiting`, see `waiting`.
Report `done`, see `done` — both directions, so the test cannot pass on a value
that happened to be the default.

Two properties beyond the happy path, and both are about the fact that this
runs inside somebody else's program:

- **It must not print.** The script's own comment promises "never fail, never
  block, never print", because an agent surfaces whatever its hooks say. Adding
  an `echo` to the script fails the test twice.
- **A wrong token must change nothing.** The script is installed globally and
  runs wherever an agent runs; the token is the only thing stopping a session
  on somebody else's panel from being driven by it.

Pointing the script at a path that does not exist fails with
`state is "working", want "waiting"` — which is the heuristic's answer when
nothing reports, and exactly the failure this test is for.

## A comment that told the next person not to bother

`CodexNotify` writes `notify = ["<script>", "waiting"]` into
`~/.codex/config.toml`, and its comment explained the design:

> Codex has a single notify command rather than per-event hooks, so it can only
> say "something happened that wants you".

codex-cli 0.147, on this machine, carries `hooks/src/legacy_notify.rs`, a
`Stop` hook event, `bypass_hook_trust`, and a `--dangerously-bypass-hook-trust`
flag. `notify` living in a file named *legacy* is the whole story: Codex has
per-event hooks, and `working` and `done` are reachable for Codex the same way
they are for Claude.

The setting itself is not broken. `codex doctor` with exactly that line reports
`config.toml parse ok` and no deprecation, which is the cheap check — it reads
configuration and contacts nothing, so it costs no quota.

Nothing was changed beyond the comment. The hooks schema here is known only
from strings scraped out of a binary, and confirming it means running a real
Codex turn, which is the user's to spend. Implementing an integration against
guessed schema and untestable without spending somebody's money is how you get
code that looks finished and reports nothing — which is precisely the failure
mode `SessionEnv`'s comment already records from the last time.

The defect is the sentence. "Codex cannot do better" is the kind of claim that
stops the next person looking, and it was wrong. It now says what is true, what
was measured, and what is left undone — and the runbook gained a section for
the symptom, because a Codex session showing a guessed state most of the time
is behaving as designed rather than misconfigured, and that is not obvious from
the panel.

## "Reporting 4 events", to nobody

The settings page installs the state-reporting hooks and then says what it
believes:

```
Claude Code    reporting 4 events
```

It has read a file. It has not heard from anything, and for every session that
was already open the claim is false — an agent reads its hooks when it starts.
In a panel built for a dozen long-lived agents, that is all of them.

So the honest sequence for the person who has been living with guessed states
is: click Install, watch the status turn green, and watch nothing else change.
Nothing on screen connects the two.

Claude Code's own instruction to itself, in the 2.1.241 binary, says what has
to happen and why the agent cannot do it:

> Tell the user to open `/hooks` once (reloads config) or restart — you can't
> do this yourself; `/hooks` is a user UI menu and opening it ends this turn.

So the panel is the only thing that can say it, and it did not.

Two changes. The status reads `installed for 4 events` rather than
`reporting 4 events`, because the panel knows the file and nothing more. And a
notice appears after installing or removing:

> Sessions that are already running will not pick this up. In each one, open
> `/hooks` once to reload, or restart the agent.

Both are pinned in the render check, and each mutation fails it alone: hiding
the notice, or putting the word "reporting" back.

### Verified without spending anything

Both Claude and Codex are installed on this machine, and every question here
was answerable without running a turn.

The event names the panel installs — `Notification`, `Stop`,
`UserPromptSubmit`, `PreToolUse` — are all present in the 2.1.241 binary, so
that mapping is still current. The reload requirement came out of the same
binary. `codex doctor` confirmed the Codex snippet still parses.

One attempt of mine reported all four event names as "not found", which was
`grep` declining to print matches from a binary rather than the truth. The
same shape as the instrument errors from earlier entries, caught the same way:
absence is the answer a broken instrument gives, so it is the answer to check
twice.

## The notice that left at the worst moment

`stateIsGuessed` ended with `return !s.hooksAreInstalled()`, so the sidebar's
explanation cleared the instant the hook file was written.

Which is the worst possible moment to stop explaining. An agent reads its hooks
when it starts, so every session already open keeps guessing after the install
— in a panel built for a dozen long-lived agents, all of them. The sequence a
user actually gets:

1. See "States are being guessed from output… Turn on state reporting →"
2. Click it, install
3. **The notice disappears**
4. Every state stays guessed, with nothing on screen saying why

The panel removed the one thing telling them what was wrong, on the click that
was supposed to fix it.

Guessed now means what it says: an agent is running and nothing has reported.
Whether the hooks are installed decides *which way out the notice offers*, not
whether it appears — so the payload carries `hooksInstalled` separately and the
sidebar picks:

> States are still being guessed. Sessions that were open before state
> reporting was installed keep guessing until they reload — open `/hooks` in
> each, or restart the agent.

The other branch was corrected while it was open. It said "Claude Code does not
ring the terminal bell", which frames the problem as one agent's quirk and
invites the wrong conclusion — use a different agent. It now says the bell is
the only signal that reaches the panel at all, because tmux swallows the
notification sequences, which is what the earlier measurement actually showed.

### A flake, and what to do with one you cannot reproduce

The render check failed once on the notes flush and passed on the re-run. Eleven
runs of the isolated probe, including one rewritten to match the harness's
preamble exactly, would not reproduce it.

At that point guessing at causes is worse than useless, because each guess
costs a change to code that might be fine. What is cheap is making the next
occurrence explain itself.

The flush swallowed its own failure:

```js
.catch(() => { /* nothing on screen is left to tell */ })
```

The comment is true and the conclusion does not follow. There is no component
left to show it in, which is not the same as there being nowhere to put it —
and swallowing it made a failed flush indistinguishable from one that had
nothing to send, which is the entire question when a note goes missing. That is
the same shape as every instrument error recorded above: a fallback that erases
the difference between "no signal" and "nothing to report".

It now warns to the console, the harness collects warnings as well as errors,
and the check quotes them. Verified by breaking the flush on purpose:

```
[FAIL] panel/notes: leaving the tab mid-edit threw the edit away; the server
still has "remember: NOTE_PERSIST_OK", the panel last reported status "?", and
it said: "vibepanel: a note could not be saved on the way out not found"
```

The flake is still unexplained. It will explain itself next time, which is the
most that could honestly be done today.

## A machine with no memory, using none of it

The system monitor, fed what a machine without a readable `/proc/meminfo`
actually sends:

```
CPU     42%   8 cores · load 1.20 0.90 0.70
Memory   0%   0 B of 0 B
Disk     0%   0 B free
```

`readMem` returns zeroes when `/proc/meminfo` cannot be opened, and the disk
read does the same when statfs fails. The frontend turned that into
`memTotal ? … : 0` — a measurement nobody made, and the measurement it claimed
was "nothing is using any memory". "0 B of 0 B" is nonsense on its own; the
`0%` beside it is the part that lies convincingly.

Reachable, not theoretical: every `darwin/arm64` build the release script
produces has no `/proc`, and so does any container that masks it.

The CPU meter one line above already knew. Its comment, and `meter.ts`'s, both
say exactly this — a first sample has nothing to difference against, so it
renders `—` rather than a zero that looks like an idle machine. The lesson was
learned, written down twice, and applied to one of the three meters.

Now:

```
Memory   —    unavailable
Disk     —    unavailable
```

Swap was already right: `sample.swapTotal > 0 &&` hides the meter entirely
rather than drawing a zero. Three meters, three different treatments of the
same question, and only one of them wrong.

### Served rather than provoked

The machine running the check has a readable `/proc`, so the honest way to see
that payload is to send it: the harness intercepts `/api/system` once and
fulfils it with the zeroes, then unroutes. Restoring either fallback fails the
check, and the failure message quotes the whole meter strip, so it says which
one broke and what the other two were doing at the time.

### And the other two zeroes in the same component

Finishing the sweep found two more of the same thing in the same panel, and the
release script decides whether they matter: `build-release.sh` builds
`darwin/arm64`, so this is what every macOS release showed.

`up {duration(sample.uptime)}` with an unread `/proc/uptime` renders **"up
0m"** — a machine that just booted. Hidden now rather than shown as `—`,
because a line of prose has nothing to draw.

And `cpuPercent` being null meant two things the panel could not tell apart:
"no sample yet, one is coming" and "there is nothing here to sample". The
second rendered `8 cores · sampling…`, a promise that renewed itself every two
seconds and was never going to be kept. `CPUReadable` now says which, and the
detail reads `8 cores · unavailable`.

A machine with no `/proc` at all, rendered:

```
CPU     —   8 cores · unavailable
Memory  —   unavailable
Disk    —   unavailable
```

`cores` is still a number, and correctly so: `runtime.NumCPU()` works
everywhere and is the one thing the monitor can always say.

Four fields, four different shapes of the same mistake, in one component whose
own helper file already carried the rule in a comment. Which is the part worth
remembering: the lesson was written down, and written down well, next to one of
the five places it applied.

## The checks that find the bugs were the ones nobody was told about

`make check` describes itself as "everything a change should pass before it
lands". It runs vet, gofmt, eslint, the Go tests and the frontend units, and it
never starts a browser.

The browser checks have found most of the defects in this project. A note
discarded by clicking the next tab. A phone rendering a desktop's grid at four
pixels. A panel telling every link where it lived. A clock icon that deleted an
arrangement. None of them are reachable from a unit test, and none of them are
in `check`.

They were also hard to find. `scale-check.mjs` had an npm script and no Makefile
target, so `make help` did not list it at all. Neither `README.md` nor
`AGENTS.md` mentioned any of them — a contributor reading the Tests bullet
learns about `testing` and `vitest` and stops there.

Three changes, all small:

- `make scale-check` exists now.
- `make verify` runs everything, in the order that fails fastest, and takes
  about twenty minutes.
- `check`'s help line says what it is: the fast gate.

`verify` is deliberately not merged into `check`. A gate people stop running is
worse than a slow one they run on purpose, and twenty minutes on every save
would make `check` something to skip.

`AGENTS.md` gained a table naming each harness and what it covers, ending with
the sentence that is the actual point: **a change that only passes `check` has
not been looked at.**

First run of the new target:

```
=== first-run check: 0 FAIL, 0 WARN ===
=== render check: 0 FAIL, 0 WARN ===
=== stress check: 0 FAIL, 0 WARN ===
=== restart check: 0 FAIL, 0 WARN ===
=== scale check: 0 FAIL, 0 WARN ===
=== tls check: 0 FAIL, 0 WARN ===
=== release check: 0 FAIL ===
all checks passed
```

The notes flake did not recur, and now has one more run of evidence against it
being frequent — and diagnostics waiting if it is not gone.

### Two more zero-means-unknown checks that came back clean

The sweep that produced the monitor fixes was carried through the rest of the
UI. The passkey list already guards `lastUsedAt` with `? … : 'never used'`, and
the certificate expiry is `omitempty` on the wire *and* skipped when the time
is zero *and* guarded again in the component — three layers where one would
have done, which is the opposite failure and a much better one to have. There
is no relative-time rendering anywhere else to get wrong.

## A sweep that mostly found things already handled

`restart-current` was one of six testids no harness referenced, so it looked
like an untested affordance on a failure path. Driving it turned up a series of
things that were already right, which is worth recording because the *reason*
they were right is not obvious from the testid list.

A session whose process exits with status 3 renders:

- a red cross and `exit 3` in the sidebar row
- a red cross and a `restart` button in the header, tooltipped with the status
- the failure output, and tmux's own `Pane is dead (status 2, …)` line, still
  readable

And the harness already checks the hardest part of that: the crash glyph, the
clean-exit glyph and the running glyph must have *different geometry*, not
merely different colours — red line 4, checked by stripping the fills and
comparing the shapes. There is a comment there about somebody "simplifying" the
crashed cross into a red copy of the clean square, which is exactly the change
that looks tidier in a diff and is invisible at 2am.

The restart *mechanism* has a Go test that uses a die-once script, so "it
restarted" and "it crashed again immediately" are distinguishable — the
ambiguity my own probe walked straight into, twice, before I noticed the
command I was restarting failed every time by construction.

### What was actually missing

Two restart buttons exist, in the sidebar row and in the session header, and
only the sidebar one had ever been pressed. The header one is the one you reach
for after reading the stack trace that just scrolled past, which is the whole
reason it is there.

The first attempt to check it broke a later check by reviving the session it
needed. It now creates its own die-once session with its own flag file, after
the sidebar check has finished with `dies`.

Detaching the button's `onClick` fails it. Removing the `current.exited` guard
does not — TypeScript refuses to compile it, because `current` is only narrowed
to non-null inside that branch. Worth knowing which layer is holding which
line.

### Three testids down, three to go

Of the six unreferenced ones: `file-truncated` renders correctly and its
server side sorts before it caps; `restart-current` is now clicked;
`drop-note` turned out to be the only unchecked part of a drag-and-drop that is
otherwise covered end to end. `file-tree`, `key-row-secondary` and
`passkey-note` are container and label elements whose contents are checked
through other selectors.

An unreferenced testid is a hint, not a finding. Following all six cost one
afternoon and produced one real gap, which is a fair rate for a lead that cheap
to check.

## A reachability scan that could not see the thing it was for

The scale check visited 900px and 1440px and never a phone, so the
intersection of the two things this panel is for — a lot of sessions, and
reading them from a phone — had never been rendered. Twenty-four sessions in
the phone drawer turned out fine: every row present, the last one scrollable
into view, no overflow, no small tap targets.

Then the check was mutated, and the interesting part started.

### `overflow: hidden` does not stop a script

Turning the session list's `overflow-y-auto` into `overflow-y-hidden` — a
one-word diff that hides the tail of a list from a person — changed **nothing**
that any of the measurements could see. The last row still scrolled into view,
`findUnreachable` still reported clean.

Because `overflow: hidden` stops *people* scrolling, not scripts. `scrollTop`
still moves, `scrollIntoViewIfNeeded` still works, and every script-based
reachability measurement therefore says the content is fine. The check was
proving something a person cannot do.

`findUnreachable` had the same blind spot from the other direction: it only
examines boxes whose `overflow` is `visible`, so a box that clips is skipped
entirely — and "a scroller changed into a clipper" is exactly how content
becomes unreachable.

It now flags a box whose `overflow-y` is `hidden` while holding more than it
can show. Vertical only: `overflow: hidden` with wider content is how
`text-overflow: ellipsis` works, and every truncated label in the panel would
be a finding.

### Three refinements, each forced by the thing failing

The first version fired on the terminal wrapper: hundreds of pixels of layout
overflow while every glyph was on screen, because a passive viewer scales the
owner's grid with a CSS transform and **a transform is not layout**.

So the rule started asking where children were actually *painted*, which fixed
that — and immediately stopped catching the mutation it was written for,
because by then the list had been scrolled to its end and its overflow was
above the box rather than below. A list made unscrollable hides its head, not
its tail.

Looking both ways caught both. Each of those three states was found by running
the mutation rather than by reading the rule, which is the entire argument for
mutating a check before trusting it — the rule read correctly all three times.

### The one thing excluded, and said plainly

After the painted-geometry fix the wrapper still reported its host painting 8px
past the bottom, in the harness's touch page, after the long-press tests:
persistent enough to survive the double scan, invisible on the screenshot, and
not reproducible in isolation across a dozen attempts at three device pixel
ratios. Eight pixels is less than one row of a scaled grid.

That box is excluded now, one level above the terminal host which was already
excluded for the same asynchrony. Excluded rather than explained, and the
comment says so: a new check whose first finding is a sub-row overhang nobody
can see is a check people turn off, and the failure it exists for is caught
regardless.

## The second thing a script can see and a person cannot

`overflow: hidden` was one. Here is the other, found by asking the same
question of a different measurement.

Playwright's `isVisible()` means: the element has a bounding box with a size,
and `visibility` is not `hidden`. An element at `opacity: 0` satisfies both. So
does one inside a container at `opacity: 0`.

Adding `opacity-0` to the "take control" pill — the only way out of a passive
viewer's scaled grid, and something the render check has a dedicated FAIL for
when it is *missing* — left the entire run green.

That check reads:

> the small second viewer shows no "take control" affordance; it may have
> silently taken the grid

It cannot tell "the affordance is not there" from "the affordance is
invisible", and on that screen those are the same outcome for the person
looking at it.

`findFadedControls` reports any element with a `data-testid` whose effective
opacity — multiplied down the ancestor chain, because that is how it composes
and because a faded container is the likelier accident — is under 5%, while
being large enough and not `display: none`. It runs everywhere the other scans
run: the desktop layout, the passive viewer, the phone drawer, two phone
shapes, and the crowded and phone states of the scale check. The mutation fails
four of them at once, naming the testid and the opacity.

### One exclusion, and it names itself

`.vp-reveal` — a row control that appears on hovering its row. That is a
deliberate `opacity: 0`, and `styles.css` already explains why it is switched
off below the hover media query:

> `opacity-0` plus `group-hover` is invisible for the whole life of a touch
> session: a phone fires no hover, so "pin to the top", "kill", "close this
> tab" and "delete this todo" were controls you had to know the pixel position
> of.

One class, one reason, written down before this scan existed — which is what
made a generic scan affordable. A separate check already asserts those controls
are opaque on a touch screen, so the exclusion does not create a hole.

### The general form

Two blind spots, same shape: `overflow: hidden` is still scrollable by script,
and `opacity: 0` is still visible to one. Both let a check assert something
about a person's experience using a measurement that is not about people at
all.

The question worth carrying: *what would this measurement say about a build
where the thing is technically present and practically absent?* It is cheap to
ask — one mutation each — and both times the answer was "nothing".

## The third one, and what the three have in common

A control can be present, opaque, correctly sized — and completely underneath
something else.

Playwright does notice, but only for controls a check actually clicks, and only
by timing out. Lifting the terminal area over the header produced:

```
[FAIL] harness: TimeoutError: locator.click: Timeout 30000ms exceeded.
```

Thirty seconds, then the run aborts. The call log does name the culprit —
`<div class="over"> intercepts pointer events` — so the information is there,
but you get one covered control per run and nothing about the ones no check
clicks.

`findCoveredControls` takes each named control's centre point, asks
`elementFromPoint` what is actually there, and reports anything that is neither
the control, inside it, nor around it. Same mutation, before the timeout:

```
[FAIL] ui: in the desktop layout, controls are on screen with something else on
top of them: [state-dot] is under div.xterm-screen, [session-title] is under
div.xterm-screen
```

Two controls rather than one, named, with the thing covering them, in a
millisecond.

It runs only where nothing is deliberately covering the screen — the desktop
layout and the passive viewer. A drawer covering what is behind it is the point
of a drawer, and a scan that fires on every control behind an open dialog is a
scan somebody deletes.

### The three together

| what a script measures | what it misses |
|---|---|
| `scrollIntoViewIfNeeded`, `scrollTop` | `overflow: hidden` is still scrollable by script |
| `isVisible()` | `opacity: 0` is still visible to one |
| `isVisible()` again | so is a control underneath something else |

Each was found by asking one question of an existing check: *what would this
say about a build where the thing is technically present and practically
absent?* Each answer was "nothing", each cost one mutation to establish, and
each fix is a scan that runs everywhere the other scans already run.

The pattern is not about Playwright. It is that a check written in terms of the
DOM is a check about the DOM, and the thing being defended is somebody looking
at a screen. The gap between those two is where a check quietly stops meaning
anything — and it does not announce itself, because a check with a blind spot
looks exactly like a check that is passing.

## Editing a todo, checked at three levels for the first time

`SetTodoText` came out of the coverage sweep with one caller and no tests. The
caller is the API's todo patch handler; the API is called by an inline editor on
every todo row; nothing tested any of it. The render check only ever added an
item and ticked it.

Driven by hand, all of it works: double-click a todo, type, Enter, and the new
text persists. Clearing it and pressing Enter leaves the old text — because
`InlineName` refuses an empty commit locally, with a comment saying why:

> An empty name is a mistake, not an instruction to clear it: committing it
> would leave a row the user cannot identify or click back into.

And the API refuses it too, with a message that suggests the right action —
"text must not be empty; delete the item instead".

So this entry is coverage rather than a fix. A todo list you cannot correct is
a todo list you rewrite, and the path a person uses to correct one had no test
at any level.

`TestEditingATodo` pins the API: an edit sticks, whitespace is refused with a
400 and changes nothing, and ticking an item does not rewrite its text — done
and text share an endpoint and must not interfere. Removing the empty guard
fails it three times over, including with `a refused edit changed the text to
""`, which is what a blanked row looks like from the outside.

The browser check pins the same outcome through the editor. Making the commit a
no-op fails it; removing the *local* empty guard does not, and that is correct
rather than a hole — the API still refuses, the row still keeps its text, and
the check is about what the person ends up with rather than which of the two
layers held.

## An agent in your home directory, filed under a project it is not in

Delete a project's directory — a git worktree pruned, a mount gone, a rename —
while the panel is running, then open a session in that project.

```
the project directory has been removed: /tmp/…/worktree
create returned:                        201
the session is actually running in:     "/home/jmr"
the project it is filed under is:       "/tmp/…/worktree"
```

tmux falls back to `$HOME` when `-c` names a directory that is not there, and
says nothing about it. The panel passed the project's path straight through, so
the session was created, started in the user's home directory, and listed in
the sidebar under the project it was not in. The row even recorded the
requested directory rather than the real one until the poller corrected it.

Discovered by accident, in the sidebar of a screenshot taken for a different
question: three sessions where there should have been two, and the third was
titled **`jmr`** — the automatic title, taken from the directory the pane was
actually in.

For a panel whose whole purpose is running coding agents, "refactor this"
starting in somebody's home directory is the wrong kind of surprise. The panel
already refuses to *create a project* whose directory is missing; it just never
asked again, and a directory checked once can be gone by the time a session is
started in it.

### Refused, except where there is somewhere sensible to stand

A scratch terminal opens in its parent's working directory, which an agent may
have `cd`'d into and which may since have gone. The project root is still a
useful place to be and is not a lie about where you are, so that case falls
back rather than refusing. Only when there is nowhere left to stand does the
request fail:

```
400 {"error":"the project directory is not there any more: /tmp/…/worktree"}
```

and the panel shows it, because `guard()` puts an API error into the banner.

Two tests, one per branch, each failing under its own mutation: removing the
guard lets the session be created *and* drops the scratch terminal into a
directory that no longer exists; turning the fallback into a refusal breaks
only the second.

### Two things that were already right

The file panel says `no such directory` in the same situation — my probe looked
for the wrong container and reported nothing, which was the probe. And the
running session in the deleted directory is untouched: still `working`, still
in tmux, which is the whole point of the architecture. tmux holds the working
directory open; the directory being unlinked does not disturb a process already
inside it.

## Two seconds per session, and the timer that took the blame

`systemctl restart vibepanel` was slow in a way that scaled with how much of
the panel was in use. Measured through the real binary:

```
sessions attached: 1    shutdown took  2025 ms
sessions attached: 4    shutdown took  8030 ms
sessions attached: 8    shutdown took 16033 ms
```

`deploy/vibepanel.service` sets `TimeoutStopSec=20` and says, in a comment,
that stopping "must not wait on anything: it detaches from tmux and the
sessions carry on regardless". Past ten sessions it waits longer than systemd
is willing to, and the panel is SIGKILLed on every stop — on a setup built for
a couple of dozen agents. `KillMode=process` means the tmux sessions live
through that, so nothing is lost, but the unit still reports failed: 137 is not
the `SuccessExitStatus` it was told to expect.

The same cost sat on the delete paths, where it is in front of a person waiting
for a request:

```
DELETE /api/sessions/{id}                 2015 ms
DELETE /api/projects/{id}, 5 sessions    10029 ms
tmux kill-session on its own                 6 ms
```

### The wrong two seconds

`Live.close()` ends with a goroutine that gives the tmux client two seconds to
exit on its own before killing it, under a comment saying that killing it
immediately "is usually unnecessary". A tmux client does not exit when its PTY
is closed, so that fallback is the normal path — and two seconds per session is
exactly what the measurements say.

It was the wrong two seconds. `awaitPump` is bounded by `pumpDrain`, which is
*also* two seconds, so the arithmetic came out right against a cause that was
not the cause. The first fix built on that reading — kill the tmux session
before detaching, so the client exits by itself — changed nothing at all:

```
TIMING kill    3.19 ms
TIMING detach  2.000515532 s
```

An isolated test had said that same reordering made detach instant. It had
measured a session that was never properly attached, and 0 ms is what a
measurement of nothing looks like. Four configurations, varying session count
and settle time independently, all came back at 2.001 s.

### The pump was waiting for itself

`pump`'s deferred cleanup calls `l.close()`. `close()` calls `awaitPump()`,
which waits on `l.pumped`. `l.pumped` is closed by a defer in that same
goroutine — registered first, so it runs last, after the one that is waiting
for it. The pump waited for itself on every teardown and was released only by
the `pumpDrain` timeout.

`closeFromPump` skips the wait, because it *is* the pump. With that gone the
earlier reordering starts paying, and the two fixes turn out to be one each for
two independent two-second costs that had been hiding behind one another:

| | before | after |
|---|---|---|
| `DELETE` one session | 2015 ms | 14 ms |
| `DELETE` a project with five sessions | 10029 ms | 25 ms |
| shutdown, 16 sessions attached | ~32 s | 2027 ms |

Shutdown keeps two seconds because a plain detach leaves the tmux session
alive, so the client really does have to be killed on the timer. `DetachAll`
now closes attachments concurrently, which is what makes it two seconds
whatever the count instead of two seconds each.

The comment on `pumpDrain` said the wait was "immediate in practice". It was
never once immediate, in any code path, since the pump was written.

### Three tests, each failing under its own mutation

Serial `DetachAll` → 12.01 s for six sessions. `closeFromPump` back to
`close()` → the detach test hangs on its own timeout. The kill/detach order
swapped back → 8.016 s to delete a project with four sessions, which nothing
had been watching before: reverting that ordering built and passed everything.

## A project you could add and not remove

Adding a project is a prompt you type a path into, so the first mistake anybody
makes is a path that is wrong and happens to exist. The panel had no way back
out: `DELETE /api/projects/{id}`, the `project rm` CLI command and
`api.deleteProject` were all there, and nothing in the sidebar called any of
them.

The control is on the project row, revealed on hover like the others. It
confirms, because removing a project kills every session in it — which is not
what "take this off the list" looks like — and the confirmation counts them and
says what survives:

```
Remove zzz-second from the panel? Its 3 sessions will be killed.

The directory itself is left alone.
```

### The count is the part worth checking

Three, not two: the scratch terminal under one of those sessions dies with it,
and a confirmation that leaves it out understates what the click destroys.
Mutating the count to exclude child sessions is caught:

```
[FAIL] projects: the confirmation offered 2 sessions, the project had 3:
       ["doomed-one","doomed-two","doomed-scratch"]
```

The render check drives the button rather than calling the endpoint, because a
control that exists but cannot be reached is the shape of defect that harness
is for. It also asserts the directory is still there afterwards. A panel that
deleted somebody's working tree because they tidied their sidebar would be the
worst bug in this project, and nothing else was watching for it.

### The probe was wrong twice first

`GET /api/projects` returned 405 — there is no such route, the list comes from
`/api/state` — and the harness's own guard against 404/405 caught it before the
check could conclude anything from the answer. Then the hover timed out,
because earlier sections leave the page at whatever viewport they finished
with and the sidebar only lists projects at desktop width. Both were the probe.
The section now reports the rows it can see when it cannot find the one it
wants, instead of spending thirty seconds hovering over nothing.

## Turning the hardening on opened a hole

`--allow-from` is the option you set because the panel is on a public port. The
allowlist check sits in `RequireAuth`, ahead of authentication and outside the
login throttle, and it wrote an audit row every time it refused a request.
Nobody had to be logged in, and nothing limited the rate. Measured against the
real binary, one loopback client running `curl` in a loop:

```
400 requests from an address that is not allowed
audit_log rows: 400
db: 4096 bytes -> 159744 bytes     (237 rows/sec)
```

`curl` spawning 400 processes is the slow way to do this. The disk it fills is
the one holding the projects.

The login path was already safe: the throttle returns 429 before `failLogin`
runs, and the allowlist rejection there does not audit at all. Only the
middleware had it.

### Gate the row, keep the line

Every refusal still goes to the journal, because that is what a fail2ban rule
reads and a ban decision needs the individual requests. Only the database write
is gated, to one per source per minute:

```
journal lines saying blocked: 400
audit_log rows: 1
db: 4096 bytes -> 4096 bytes
```

What the settings page needs from fifty identical rows is that an address is
hammering, and one row a minute says that just as well. A source that keeps
going does not refresh its own entry, so an hour of it reads as sixty rows
rather than one row and then silence.

The source map is keyed by `/64` for IPv6, like the login throttle: keying on
the full address would let anyone with a prefix write a row per request by
counting up. It is capped at 4096 sources, because the map is otherwise the
same unbounded growth moved into memory — and worse, since restarting the panel
cannot sweep it when the panel is what dies. Past the cap it stops gating
rather than stops recording: the row is the only evidence the request happened,
and the trim below is what bounds the disk.

### Nothing had ever removed an audit row

Separately from any attack. The settings page reads fifty; the table only grew,
one row per refused sign-in, for as long as the panel ran — and the real one
has been up for over a week at a time. `TrimAuditLog` keeps the newest 50,000
and runs at startup, next to the sign-in purge that was added for the same
reason.

Five mutations, each caught by its own test: the gate removed, the window
ignored, the `/64` bucketing dropped, the source map unbounded, and the trim
taking the newest rows instead of the oldest.

### The same shape, twice more

Two other endpoints take a request from someone who has not authenticated. The
hook endpoint answers a bad token with 401 and writes nothing, which is right.
`/api/auth/setup` audited every rejected setup token, unthrottled — reachable
only while no account exists, which is precisely when nobody is watching a
freshly deployed panel. It is recorded through the same gate now.

The token itself is 32 bytes of `crypto/rand`, so the guessing was never the
problem; the rows were.

The gate buckets per event as well as per source. One address can produce both
kinds of noise in the same minute, and a shared window would have recorded
whichever arrived first and lost the existence of the other.

## A title is whatever the agent printed

A session's automatic name comes from `#{pane_title}`, which tmux sets from
`OSC 0` or `OSC 2` — so its length is decided by whatever is running in the
pane. Nothing bounded it anywhere. Measured against the real binary, a pane
emitting a 200,000-character title:

```
pane_title tmux kept:  200001
title stored in the row: 200000
state snapshot:  705 bytes -> 200710 bytes
```

The snapshot is the part that costs. It is rebuilt every two seconds, compared,
and broadcast to every connected viewer whenever it changed. A couple of dozen
sessions doing this is megabytes pushed at every browser watching, including a
phone on mobile data.

None of it needs malice. `cat` a file with an escape sequence in it and this is
what happens, which is a thing agents do to files they did not write.

Bounded at 256 runes, in `internal/session` because `internal/store` already
imports it and a second copy of the number is the "two lists that have to
agree" this codebase keeps finding. Applied in two places: the store, which
covers both the automatic title and the rename field, and the OSC scanner,
whose copy does not go through the store — it is broadcast as a title event the
moment it arrives. Runes rather than bytes, so truncation cannot split a
character and leave invalid UTF-8 in the row, which renders as nothing.

After: 705 bytes -> 966.

### The browser said it was fine

The render check drives a pane that emits the sequence and looks at the sidebar
and the page width. With the bound removed and a 5,000-character title in the
row, that half still passed:

```
[PASS] title: a title the pane chose does not widen the page
[FAIL] title: a title of 5000 characters reached the snapshot
```

CSS clips it, so nothing looks wrong. What CSS hides is still in every snapshot
pushed to every browser — a check that only looked at the rendering would have
called this build clean.

### The probe reported a bound that did not exist

The first attempt pasted the escape sequence into the pane with `paste-buffer`,
where the shell took it as text to run rather than output to render, and
`pane_title` came back as 13 characters. That reads as "tmux bounds titles",
which is a reassuring answer and a wrong one. Delivering the sequence from
inside the pane gave 200,001.

## Twenty thousand lines of history nobody could see

tmux keeps 20,000 lines per session. Not one of them could be reached from the
panel, on any device. Measured out of xterm's own buffer, on a session that had
printed 300 lines:

```
tmux history_size: 267
xterm buffer length: 34   baseY: 0
```

34 rows: the visible screen and nothing behind it. Wheel, touch drag and the
phone's `pgup` key all moved it by nothing.

### Two lines of tmux config, both of them

The first thing a tmux client writes to its terminal is the alternate screen:

```
"\x1b[?1049h\x1b[22;0;0t\x1b[?1h\x1b=\x1b[H\x1b[2J..."
```

That buffer has no scrollback by definition, so nothing the panel ever rendered
could accumulate any. And tmux scrolls with `CSI Ps S`:

```
"\x1b[12S\x1b[HROW_12\r\nROW_13\r\n..."
```

which discards what goes off the top — only a line feed at the bottom margin
hands a line to the terminal to keep.

`smcup@:rmcup@` and `indn@` remove both, on the panel's own socket. After:
`buffer length 303, baseY 269`. The alternate screen matters because this PTY
is created for one tmux client and dies with it; there is nothing behind it to
restore. Programs *inside* tmux are untouched — tmux emulates the alternate
screen per pane and sends the composed result, so leaving vim still puts the
shell back, which stress-check has always pinned and still does.

This was the item recorded as a decision for the user. The measurement made it
a defect: 20,000 lines of history unreachable in a tool whose purpose is
reading what agents did.

### The check that answered the same question three different ways

The browser check flapped — one run in three, always all-or-nothing — and on
one of those runs it reported that `indn@` was unnecessary. It nearly took a
necessary line out of the config.

The cause was the check, not the panel: it scrolled 1500ms after selecting the
session, while the replay was still arriving and the terminal was still
following output to the bottom. Both the before and the after were being read
out of a moving picture. Waiting for the last line of the burst to appear turns
it into a measurement, and it has passed fourteen consecutive runs since.

The lesson is the one this project keeps relearning, in a new form: an
instrument that is merely *usually* right will eventually be confidently wrong,
and the answer it gives will be the one that costs the most to act on.

### A second instrument, for seconds rather than minutes

`TestTmuxDoesNotTakeTheAlternateScreenOrDiscardScrolledLines` asserts the same
two facts against the bytes tmux writes to the PTY. It runs in eight seconds
instead of twenty minutes, needs no browser, and names which of the two config
lines broke:

```
tmux put this PTY on the alternate screen, which has no scrollback
tmux scrolled with CSI Ps S 25 times, which throws the lines away
```

### Still missing on a phone

Scrollback now exists and the desktop wheel reaches it. A touch drag does not,
and neither does the `pgup` key — it sends `ESC[5~`, which a shell ignores.
Measured with scrollback present: wheel 269 → 268, touch drag 268 → 268, pgup
268 → 268, `scrollLines(-60)` 268 → 208. The buffer is there and the phone has
no gesture that reaches it. That is the next thing.

## A finger that could not reach the scrollback

With the alternate screen gone there are hundreds of lines behind the screen,
and on a phone nothing could get to them. Measured with 269 lines of scrollback
present:

```
wheel                269 -> 268
touch drag           268 -> 268
the panel's pgup key 268 -> 268
term.scrollLines(-60) 268 -> 208
```

The buffer is there and xterm scrolls when asked; the phone has no gesture that
asks. xterm's scrollable element listens for wheel events, which a touchscreen
never sends, and `pgup` sends `ESC[5~`, which a shell ignores — it is a key for
the application, not a way to look backwards.

`touchSelect.ts` already saw every touch: it waits for a long press to start a
selection, and on movement it cancelled the timer and returned. Now the same
movement scrolls, once the gesture is clearly vertical and there is something
behind the screen to show. Vertical-only, because a horizontal drag changes
view; and only with scrollback present, because `preventDefault` on a terminal
with nothing behind it stops the page moving for no reason.

The pixels-to-rows arithmetic carries its remainder. A row is around eighteen
pixels and a finger arrives in ones and twos, so truncating each event on its
own throws most of the movement away and the terminal crawls behind the finger.

### The mutation that passed because the build had failed

The first attempt to prove the drag handler was load-bearing said it was not:
with the handler disabled, the browser check still reported a finger scrolling
back — and reported *the same line numbers*, 368 to 330, as the unmutated run.
Identical numbers are the only reason this was caught.

The mutation left `dragRows`, `carried` and `scrolling` unused and `startPoint`
possibly null, so:

```
make build exit code: 2
   src/components/mobile/touchSelect.ts(166,39): error TS18047: 'startPoint' is possibly 'null'.
embedded frontend changed: False
```

`internal/webui/dist` was untouched, the binary kept the previous frontend, and
the check measured a build that had never contained the mutation. The harness
had captured the build output and never looked at the exit code.

Redone with a mutation that compiles — `scrollLines(-step.rows)` to
`scrollLines(0)` — the drag is load-bearing: 268 -> 268, nothing.

### Refusing to measure yesterday

`lib/fresh.mjs` compares the binary against the Go sources and the embedded
frontend, and the embedded frontend against `web/src`, and throws before the
browser starts:

```
/home/…/internal/session/manager.go is newer than /home/…/vibepanel.
This check would measure the previous build. Run `make build`.

/home/…/web/src/App.tsx is newer than the built frontend in internal/webui/dist.
The binary embeds the previous one, so this check would measure a build that
does not contain the change. Run `make build`.
```

Every browser harness calls it. `npm run check:*` rebuilds nothing, so "edit,
forget to build, measure yesterday" was always one step away — and it is the
one failure that looks exactly like a pass.

## The committed history did not build

Every check in this project drives a binary built from the working tree. None
of them can tell you anything about what was *committed*, and the two had
drifted apart: a worktree at HEAD did not compile.

```
internal/httpapi/api.go:321:36: s.DB.HookToken undefined
  (type *store.DB has no field or method HookToken)
```

The caller was committed. `store.HookToken`, which it calls, was sitting
untracked in the working tree, along with forty-four other files — the flag
precedence fix, the FIFO guard on the download path, the hook installer that
merges instead of replacing, the certificate reload keyed on bytes, half the
frontend and the tests for all of it.

Nothing was wrong with any of that work. What was wrong is that commits were
made by naming paths, and a path that was not named stayed behind. Everything
kept passing, because everything was measuring the tree where the missing
pieces were.

So my own reports of "all green" in this log describe a working tree, not the
repository. Anyone cloning it got something that did not compile.

### Repaired, and then guarded

The tree is committed in three parts — backend, frontend, packaging — and a
worktree at HEAD now builds, vets, passes the Go tests, and passes `npm ci`,
`tsc`, `eslint` and 36 frontend tests.

`make head-check` does exactly that, from a temporary worktree with nothing of
the working tree in it. It is second in `verify`, right after `check`, because
everything below it is blind to this.

It was verified against real history rather than a synthetic mutation: pointed
at the commit before the repair, it reports the failure above and exits 1.

### The shape of it

This is the third instrument failure in as many sessions, and the same shape
each time: a measurement that answers a question next to the one being asked.
`.xterm-viewport` instead of xterm's own scroll element. A build that failed
while its output was captured and its exit code ignored. And now a checkout
that nobody was ever checking. All three reported success.

## The bundle that does not match its own sources

`internal/webui/dist` is committed, because the binary embeds it and `go build`
has to work on a machine with no npm. That makes a second kind of drift
possible, and a quieter one than the missing file: commit a frontend change
without rebuilding, and everything passes. The Go build compiles, the tests
run, `head-check` is satisfied — and the binary serves the previous UI. The
browser checks cannot see it either, because they build first.

`head-check` now rebuilds the frontend in its worktree and asks git whether
anything moved:

```
[FAIL] the committed frontend bundle is not what these sources build:
        D internal/webui/dist/assets/index-XgnNSXlN.js
        M internal/webui/dist/index.html
       ?? internal/webui/dist/assets/index-UC4TH9xD.js
```

HEAD was current when this was written, so this closes a hole rather than
fixing a break.

### The probe was too weak first

The first attempt appended `// drift probe` to `main.tsx` and committed it
without rebuilding. The check passed, correctly: comments do not survive
bundling, so the output really was identical and there really was no drift.
A probe that changes nothing observable proves nothing, and it reads exactly
like a working check. Changing a string that has to reach the bundle —
a `title` attribute — produced the failure above.

## A spinner erased the one state the panel exists for

Without hooks, a bell is the only unambiguous "an agent wants a human" signal
there is. The rule was that a bell means waiting until output arrives more than
`bellGrace` after it — two seconds, meant to cover the prompt redraw that
follows the ring.

An agent whose TUI keeps moving defeats any finite grace, because the test is
"has anything printed since" and something always has. Measured against the
real binary, with panes whose command is not a shell:

| pane | 3s | 8s | 15s |
|---|---|---|---|
| rings, then silent | waiting | waiting | waiting |
| rings, then cursor moves only | waiting | waiting | waiting |
| rings, then animates a spinner | waiting | **working** | **working** |
| rings, then ticks a seconds counter | waiting | **working** | **working** |

An agent asking a question with a live "esc to interrupt" line under it reads
as busy: a blue circle instead of an orange triangle, sorted below the sessions
that are merely working. On a phone at 2am that session is invisible.

### The line feed

What separates "went back to work" from "is redrawing while it waits" is
whether the screen moved forward. Read off the PTY the panel actually sees,
three seconds of steady state each:

```
spinner  480 bytes  LF=0     \r| over the same line
lines    430 bytes  LF=22    the agent producing output
box      105 bytes  LF=0     a full-screen repaint, cursor-addressed
```

So `Signals.Advanced` — the chunk contains a line feed — and the bell is
cleared by that rather than by any output at all. No timer has to guess.

Both directions hold. The four cases above stay `waiting` for as long as they
run, and an agent that rings and then prints goes to `working`, including a
full-screen one that scrolls: tmux turns a scroll into line feeds on this
socket, which is the `indn@` from the scrollback work, so a scrolling TUI
advances.

### The rule was tested and the signal was not

`TestWorkResumingClearsTheBell` encoded the old contract and failed, correctly:
its fixture said `Visible` where an agent resuming work also advances. Updated
rather than deleted — the intent was always right.

Then a mutation inverting the *computation* of `Advanced` in the pump passed
every test in the package. The detector tests build `Signals` by hand, so none
of them touched the line that decides what Advanced means.
`TestTheAdvanceSignalIsComputedFromWhatTmuxSends` drives two real panes through
the real pump into the real detector; both mutations fail against it now.

A rule with a well-tested consequence and an untested premise is the same
blind spot as a check that drives the previous build.

## Restarting the backend forgot who was asking

The detector keeps its evidence in memory: when a bell last rang, what a hook
last said, what the user last chose by clicking the dot. A restart threw all of
it away, and `Reconcile` then re-derived every session from live facts — which
for anything that is not a shell means "working".

Measured against the real binary, with a pane sitting on
`Do you want to proceed? (y/n)`:

```
before the restart:  asking   waiting  (heuristic)
(backend stopped)
after the restart:   asking   working  (heuristic)
six seconds later:   asking   working  (heuristic)
```

The session was untouched, the question was still on its screen, and the panel
had stopped saying so — permanently, because nothing was ever going to ring a
second time.

`systemctl restart vibepanel` is the operation this entire architecture exists
to make safe. It silently destroyed the one state the panel is for, for every
waiting session at once.

### Only what cannot be re-derived

`Detector.Restore` seeds a session from the row that was written down. A bell,
a hook report and a manual choice are events that happened and that nothing on
the wire will repeat. "Working" and "done" are read off the pane's foreground
process, which is still true after a restart and is better taken fresh —
restoring those would pin a stale answer over a live one.

Stale evidence corrects itself. A session answered at the tmux socket while the
panel was down advances its screen within moments of being re-attached, and
that clears the bell exactly as it would have:

```
after the restart:   asking   waiting   resumed  working   manual  done (manual)
```

### The harness reported a warning that did not exist

Adding a `PASS` finding to `restart-check` turned its summary into
`0 FAIL, 1 WARN`, with no warning printed anywhere. The count was
`findings.length - fails`, which is right only while `FAIL` and `WARN` are the
only severities a file uses. `scale-check` and `first-run-check` had the same
line; `render-check` and `stress-check` did not. All three now count `WARN`.

A summary that invents warnings is one people stop reading, which is the same
failure as a check that never fails — just from the other side.

### And the test that introduced a race

`TestRestoreStateReadsWhatWasWrittenDown` replaced `srv.Detector` with a fresh
one to stand in for a restart. `Server.Detector` is set once at startup and
read from the pump's goroutine without a lock, so reassigning it under a
running server is a data race — and the full `-race` run said so, after the
test had passed on its own without `-race` and been committed.

Production is unaffected: the field is never reassigned outside that test. The
test now calls `Forget`, which is what a restart leaves for a session and is
mutex-protected. It still fails when the restore is removed.

Running one test on its own is not running the suite, and `-race` is not
optional for a package whose subject is a server with goroutines in it.

## An agent the OOM killer took looked like one that finished

tmux reports the two ways a pane can end differently. `#{pane_dead_status}` is
the wait status of a process that returned; for one that was *killed* it is
empty, and `#{pane_dead_signal}` holds the signal instead. Measured:

```
after SIGKILL:  dead=1  dead_status=[]  dead_signal=[9]
```

The panel read only the first, so it stored 0 — the number a task that finished
its work has. An agent killed by the OOM killer, a segfault, or a stray
`kill` was recorded as a clean exit, and the sidebar's "something failed here"
indicator, which tests `exitStatus !== 0`, never fired for any of them.

On a machine running a couple of dozen agents, the OOM killer is not a rare
visitor, and "your agent was killed" versus "your agent finished" is the whole
question.

`Info.ExitStatus()` now returns 128+signal for a kill, which is what every
shell does and makes the common ones recognisable: 137 is SIGKILL, 139 a
segfault, 143 a SIGTERM. The restart button's explanation reads them back out
in words — "The process killed (SIGKILL). Run it again in this pane." — because
137 on its own is not something anybody decodes at 2am.

### The fixture had to load the real config

The first version of the tmux test failed with "the pane never went dead".
`newTestClient` does not call `EnsureServer`, so its server runs without the
embedded config — and `remain-on-exit` lives there. Without it the session is
destroyed the moment the process ends and there is no dead pane left to ask
about. A fixture that does not load what production loads is testing a
different program.

### And a bug that was not one

The same probe appeared to show the restart button doing nothing: `204` from
the endpoint, and twelve seconds later the pane still `dead=1` with the same
pid. Both halves were the probe.

The fixture's command was `sh -c 'echo something broke; exit 3'`, so respawning
it re-runs a command that exits immediately — correctly dead again, with the
same status. And the pid was compared across three readings *after* the
restart, never against the one before it. Driven directly against tmux, the pid
goes 2407602 → 2407612 on respawn, and a pane whose command survives comes back
`dead=0` with `sleep` running.

Two wrong conclusions in one measurement, both pointing at working code.

### The stale-build guard earned its keep

Editing `App.tsx` and then running `make check` followed by the browser
harnesses: `make check` does not rebuild the frontend into
`internal/webui/dist`, so all three refused to start.

```
Error: web/src/App.tsx is newer than the built frontend in internal/webui/dist.
The binary embeds the previous one, so this check would measure a build that
does not contain the change. Run `make build`.```

Without it they would have driven the previous UI and reported three clean
runs, which is what I would have written down.

## Every tick was a broadcast

The poller builds the state snapshot, compares it to the previous one, and
pushes only when they differ. The comment says why: "a tick that broadcasts
regardless is polling again, just with the cost moved onto every connected
viewer."

`last_output_at` was in the payload. It moves for any session that is printing,
so one busy agent made every tick differ. Measured, six sessions each printing
ten lines a second:

```
idle   0 of 10 ticks would broadcast    2889 bytes
busy  10 of 10 ticks would broadcast    2907 bytes   85 KiB/min per viewer
```

At two dozen sessions the scale check puts it at **329 KiB/min per viewer** —
about 20 MB an hour, onto a phone, for as long as anything is running. The
optimisation had never once applied to a panel in use.

Nothing read the field. It was declared in `wire.ts` and used nowhere; the
ordering it drives is applied in SQL, so the array already arrives in the right
order. The column stays, the field leaves the wire, and busy drops to 1 tick in
10 — that one being a real change.

A display of "last active" should carry a value chosen for display: bucketed,
so it changes when the words would.

### Two checks, because the cheap one cannot see the cost

A store test pins that two sessions differing only in last output serialise
identically — fast, and it says exactly which field came back. The scale check
respawns six of its twenty-four sessions as printers and counts how many ticks
push a snapshot, which is the number that actually matters and the one that
names the price:

```
[FAIL] scale: 7 of 7 ticks broadcast a full snapshot while sessions were merely
       printing — 329 KiB/min to every viewer.
```

It proves the sessions are printing before it measures. A respawn that silently
did nothing would report the idle figure and read as a pass — which is how the
first version of the CPU probe in this same investigation reported "0.1% busy,
0.1% idle" and I nearly wrote it down.

### What the CPU probe found, once it was measuring something

Nothing wrong, which is worth recording. Fifteen sessions idle: 0.10% of one
core for the panel, 0.03% for the tmux processes. Fifteen sessions each
printing ten lines a second: 1.5% and 1.1%. A panel that attaches to every
session so it can see what they are doing costs about two and a half per cent
of one core to do it.

## Nothing compared the rest of the shape

`wire.ts` is written by hand, and one red line already covers it: the state
enum is compared against `AllStates` by a test. Nothing compared any other
field.

The drift that allows is silent in the direction that matters. Panel data
arrives as `JSON.parse` cast to the interface, so a field the server has
stopped sending is still declared, still type-checks, and is `undefined` at
runtime — with `strict` on and no error anywhere.

This came up while removing `lastOutputAt` from the wire. Editing `wire.ts` to
match was something I had to remember to do, and nothing would have said a
word if I had not.

`TestTypeScriptRowsMatchWhatIsSent` marshals `Session`, `Project`, `Note`,
`Todo` and `AuditEntry`, reads the matching `export interface` out of
`wire.ts`, and compares the two as sets. Both directions are errors, and they
are different errors:

```
wire.ts declares [lastOutputAt] on Session, and the server does not send them.
They are undefined at runtime and nothing type-checks that.

the server sends [titleSource] on Session, and wire.ts does not declare them.
The frontend cannot read a field it has not been told about.
```

Renaming a Go json tag is caught too, as both at once.

### Two probes that proved nothing

Before this, two investigations ended in nothing, and both are worth the note.

`chmod 444` on the database to see what the panel does when it cannot write:
the writes went through and returned 200. Permissions are checked at `open`,
and the panel had the file open read-write long before the `chmod`. Simulating
a full disk needs a filesystem I can size, which needs root. The question — a
panel that silently stops persisting — is still open.

And the two harnesses I had not run all session, `tls-check` and
`release-check`, both pass. Five of seven is not "everything passes", and I had
been writing the latter.

## The panel that could not write, and said it was fine

The open question from the last session: what happens when the panel cannot
persist. `chmod` proved nothing — permissions are checked at `open` and the
file was already open. User namespaces are not permitted here, so no tmpfs
either. `ulimit -f`, applied to a restart after the database exists, does work:
Go turns SIGXFSZ into a write error, which is close enough to a full disk to
answer the question.

Three findings, in order of how quietly they fail.

### Startup was already right

With the limit below what the database needs, the panel refuses to start and
says why:

```
vibepanel: store: ping .../vibepanel.db: disk I/O error (4874)
stopped; tmux sessions keep running
```

### Running, it said nothing at all

With the cap just above the database's size, the panel starts. The eleventh
rename returns `500 {"error":"store: exec: disk I/O error (778)"}` — so the
person who pressed a button is told, and `guard()` puts it in the banner.

Everyone else saw a working panel. `/api/health` answered `"ok": true`. The
snapshot said nothing. The terminals kept working, because they belong to tmux
— which is the architecture doing exactly what it promises, and exactly why
nothing else looked wrong. Every state change, every derived title, every note
was being dropped.

`ok` is a claim, and it was an unconditional one. Health now reports the
failure, and so does the state snapshot, so every viewer gets a banner rather
than only whoever happened to press something.

### It also signed everyone out

`currentUser` collapsed "no session" and "the database cannot say" into the
same answer, so the first thing a viewer saw was `401 sign in required`. The
sign-in goes to the same broken database, so they try again — and the login
throttle locks them out of a panel that was only ever short of disk space.

Refusing either way is right and stays. It answers 503 now, and says which of
the two it is.

### The first version of the signal never fired

It cleared on every successful poll. A database capped at its current size
still lets the poller rewrite pages it has already allocated while a request
needing a new one fails, so the poller kept succeeding and erased the evidence
every two seconds. Failures now have to have *stopped* for thirty seconds, not
merely paused between two of them.

Three ticks before it says anything, because one failed poll is a blip — a tmux
call that lost a race with a delete — and a banner that comes and goes is one
people learn to ignore.

### And the two shapes that had to agree

`stale` reached the WebSocket snapshot and not `/api/state`, because the two
built `stateResponse` separately from the same six fields. One builder now, and
a test that the socket and the REST answer carry the same keys.

That is the second time this session that duplicated construction has cost
something, and both times the duplicate was three lines away from the original.

### Driven, and the worry was wrong

The banner had to be seen, not reasoned about, so the capped-database setup was
put in front of a real browser. It appears, six seconds in, and the connection
stays open:

```
+5s   stale-notice=null                                          connection="open"
+10s  stale-notice="The panel has stopped recording what the
      sessions are doing. The terminals are unaffected."         connection="open"
```

The reasoning that led to running it was wrong, and worth writing down. The
WebSocket revalidates every five seconds through `stillAuthorized`, which
discards the error from `currentUser` — so a database failure looked like it
would close every socket, which would mean the client never receives the
snapshot carrying `stale` and the banner never appears. The fix would have been
to weaken revalidation.

It does not, because `currentUser` only reads, and a database that cannot be
*written* reads perfectly well. `TouchAuthSession` is the one write and its
error is already ignored as best-effort. Measuring saved an unnecessary and
security-relevant change to the auth path.

The server half is pinned by tests. The banner's rendering is not: it was
verified by this probe and nothing guards it, which is worth knowing rather
than implying otherwise.

## A project name is in the snapshot too

Session titles were bounded because they are whatever an agent printed. Project
names were not bounded at all, on either create or rename — and they are in the
same state snapshot, broadcast to every viewer.

The way this one goes wrong is a paste into the rename field rather than an
escape sequence, and it is self-inflicted, but the cost on every phone watching
is identical. Same constant, applied in the store, which is the single point
both paths go through.

Notes and todos are fetched per project rather than pushed, so a large one
costs only the person who opens it. Left alone.

## The command the runbook sends you to said everything was fine

`doctor` opens the database, reads its schema version, and prints `[ok]`.
Opening a database says nothing about writing to one. Against a database that
could not accept a single row it printed:

```
[ok  ] database           schema v6 at /…/vibepanel.db
```

and exited 0 — on the failure the runbook sends people to `doctor` to find.

It now attempts a real write, inside a transaction it rolls back so a
diagnostic leaves nothing behind:

```
[FAIL] database writes    store: write check: attempt to write a readonly database (8)
vibepanel: 1 check(s) failed
```

And it reports free space where the database lives, failing under 64 MiB and
warning under 512 MiB. The number was already being sampled for the monitor
panel and was not being shown to the one person asking what is wrong.

### What the simulation could not show

The `ulimit -f` trick does not make this fail. A cap on file size stops the
database *growing*; a one-row update to an existing page still succeeds, and
the probe correctly says so. What it reproduces is "cannot grow", and a real
full disk is "cannot append to the write-ahead log".

Making the file read-only does reproduce it, and that is what the measurement
above is.

### The test that would have passed on a SELECT

The first version checked a healthy database and a closed one. A closed
database fails at `BeginTx`, so both halves pass just as well if the "write" is
a `SELECT` — which is what the probe would become if somebody made it cheaper.

The test now builds a handle that can read and not write (`mode=ro`), proves it
can read, and requires the check to fail. Turning the INSERT into a SELECT now
fails, and so does swallowing the error.

## An npm package's Go code was part of this project

`go test -cover ./...` listed a package nobody here wrote:

```
github.com/jiangmuran/vibepanel/web/node_modules/flatted/golang/pkg/flatted
   coverage: 0.0% of statements
```

`flatted` ships `golang/pkg/flatted/flatted.go`, so `go build ./...`,
`go vet ./...` and `go test ./...` were all compiling and checking a
third-party file that arrives, changes and disappears with `npm ci`. Nothing
imports it, so it never reached a binary — but it was inside every gate, and a
vet finding in somebody else's file would have failed `make lint`.

The Makefile already filtered it out of one command:

```
@out="$$(gofmt -l . | grep -v '^web/' || true)"
```

Which is the same symptom, noticed once and patched in one command out of four.

Go has no exclude directive; a nested `go.mod` is the mechanism. `web/go.mod`
makes everything below it another module, and the root's `./...` stops there.
Fifteen packages instead of sixteen, and `npm ci` cannot put it back.

### The guard was measuring its own directory

The test that asserts nothing under `web/` is in the module ran
`go list ./...` — which is relative to the working directory, and the test
lives in `internal/webui`. It listed three packages that were never going to
include anything from `web/`, and passed with `web/go.mod` deleted.

It runs from the module root now, and asserts it can see `internal/session`
before it concludes anything from what it did not see. Deleting `web/go.mod`
fails it, naming the package.

That is the fifth instrument in this session to answer a question next to the
one being asked, and the second where the tell was a check that passed while
the thing it checked was absent.

## Two panels, one data directory

Starting a second `vibepanel serve` on the same data directory and the same
tmux socket worked. Measured: one session, one panel, one attached tmux client;
the second panel started and there were two, with nothing logged by either.

Two clients on a session is not mainly a memory problem. The panel is meant to
be the *only* tmux client — that is what makes one authoritative grid and one
place that decides its size possible, and the mobile story is built on it. With
two panels the arbitration means nothing.

And each keeps its detector in memory. A bell one of them saw is invisible to
the other, so the "waiting" it set is overwritten by the other's "working" on
the next tick — the two findings from earlier in this session, reintroduced by
an operator mistake rather than by a bug.

It is an easy mistake: the unit is running and you run `vibepanel serve` to try
something.

`flock` on a file in the data directory, taken by `serve` only — the admin
subcommands read and write briefly and have to keep working while the panel is
up. A lock file rather than a pid check, because a pid in a file is a guess
about a process that may have died and been replaced, and the kernel releases
an flock however the holder exits. Verified: SIGKILL the holder and a
replacement starts immediately and finds its sessions still in tmux.

`doctor` reports the holder rather than failing over it — a running panel is
the normal state, and `doctor` is often run because somebody is not sure
whether it is up.

### The test seeded a pid the same length as its own

The lock file is rewritten by each holder, and without a truncate a short pid
written over a long one leaves the tail of the long one behind. The test seeded
`1234567` — seven digits, which is exactly what pids look like on this machine
— so removing the truncate changed nothing and it passed either way.

Seeded with thirty digits it fails, and says what the next operator would have
been told to look for:

```
the lock file says "2589214\n9012345678901234567890", want 2589214
```

## The binary never mentioned what it could do

```
$ vibepanel --help
vibepanel — a web console for many parallel coding sessions.

Usage:
  vibepanel [flags]

Flags:
  -acme-directory string
  …
```

Six commands exist — `serve`, `project`, `session`, `hook`, `doctor`,
`version` — and the help mentioned none of them. Somebody who unpacked the
release archive and asked the binary what it does got a flag list, while the
runbook opens by telling them to run `vibepanel doctor`.

The list was already written, in the error for an unknown command. It was
simply never shown to anyone who asked politely.

### And then there were three

Adding it to the usage made three copies of the same six words: the `switch`,
the error text, and the help. That shape has cost this session twice already —
`stale` reaching the socket and not `/api/state`, and before that the two
builders of `stateResponse` — both times with the duplicate a few lines from
the original.

So the switch became a map, the error joins its keys, and the help text is the
one place the names and descriptions are written. A test compares the two that
remain: every name `--help` offers is dispatched, and every name dispatched is
offered. Deleting `doctor` from either side fails it, and so does misspelling
it in one:

```
--help offers [doctor hook project serve session vresion] and the binary
answers to [doctor hook project serve session version]
```

A second test requires each line to say what the command is *for*. A list of
six words is only marginally better than the error message it was copied from.

`cmd/vibepanel` had no tests at all before this; it was the 0.0% at the top of
the coverage list, next to the npm package.

## A download button that always answered 403

The file panel offers a download when the entry is `readable`, and `readable`
meant "the thing it points at is a regular file". For a symlink it stat'd the
target and stopped there.

The download does more than that: it resolves the link and refuses anything
that leaves the project. So a link to `/etc/passwd` in a project — one
`ln -s`, or anything at all under `node_modules` — listed like this:

```
{"name":"link-to-passwd","symlink":true,"readable":true}
```

and the button it produced answered `403 outside the project` every time.

Nothing was leaking: the four hostile cases all refuse correctly, including
through a symlinked directory. What was wrong is that the panel advertised a
file it would not hand over, and a control that cannot do what it offers
teaches people the panel is unreliable rather than that the file is out of
bounds.

`readable` now means what the download will do. The entry still appears —
hiding a file is its own kind of lie — with `escapes` set, and the row says
`outside` next to it, because a file with no download among files that have
one otherwise reads as the panel failing.

### The wire check caught the person who wrote it

Adding `escapes` to the Go struct and forgetting `wire.ts` is exactly the
silent drift the field comparison exists to catch. It covered `Session`,
`Project`, `Note`, `Todo` and `AuditEntry`; `FileEntry` was not in the list, so
it was added — and it failed immediately:

```
the server sends [escapes] on FileEntry, and wire.ts does not declare them.
```

First time a guard here has caught its own author, in the same sitting.

### What the probe found by being wrong first

The upload probe pointed at `/api/fs/upload`, which does not exist, and every
one of its eight hostile cases answered `404`. Eight refusals reads as a
thoroughly defended endpoint. It now uploads one ordinary file and refuses to
interpret anything unless that returned 200.

Against the real endpoint everything was already right: traversal and absolute
paths in the multipart filename are stripped to the base name, `..` is refused,
a traversing `path` is 404, a colliding name is 409 and the existing file
survives. The frontend shell-quotes what it types at the prompt, with the
`'\''` idiom, and only when it needs to.

### Where this entry stops

Written after the fact and without a commit behind it. The tooling that runs
commands became unavailable partway through this change, so what is described
above was verified first — `go test -race ./...`, `make lint`, three mutations
each failing on their own, `render check: 0 FAIL, 0 WARN` — and then could not
be committed. If `git log` does not show it, the working tree is where it
lives.

## Found by reading, in a sitting with nothing to run

The tooling that runs commands stayed unavailable, so this is analysis rather
than measurement, and is labelled as such wherever it landed.

**`vibepanel session kill` leaves orphaned panes.** The HTTP path lists
`ListChildSessions` before killing anything, and says why: "Children cascade
away in the database, but their tmux sessions do not. Deleting the row first
would leave processes nothing in the UI can reach." The CLI kills one tmux
session and deletes one row — so the scratch terminals under it lose their
rows to the cascade and keep running on the panel's socket with nothing
pointing at them. Two paths doing the same job, one of them updated: the shape
that has cost this project more than anything else. Not fixed, because a fix
here could not be tested; recorded at the call site.

It is one step to confirm, and the panel already reports the symptom: kill such
a session with the CLI, start the panel, and `Reconcile` should log `tmux
sessions on our socket with no database row count=N`. If it does not, the
finding is wrong and the comment should go — which is the point of writing the
prediction down rather than the conclusion.

Narrowed, before anybody widens it: deleting a *project* is fine on both paths.
`ListProjectSessions` filters on `project_id` and nothing else, so scratch
terminals come back with everything else — which the render check measured
earlier today, counting three sessions in the confirmation with the scratch
terminal among them. Only `session kill`, which takes one session and never
asks what is under it, has the gap.

**A CSI fragment beginning with `[` is never trimmed.** `trimPartialEscape`
exists to stop a wrapped replay buffer printing the tail of an escape sequence
as text, and it handles `31m` and `1m` but not `[31m` — `allParameterBytes`
rejects `[` (0x5b). Worked out by hand against the function, not run. Left
alone with the reasoning written down, because every obvious fix is worse:
allowing a leading `[` eats `[warn] something`, and requiring a parameter byte
after it still eats `[2024-01-01] …`. It needs a measurement, and measurement
is what was unavailable.

**`outputSeen` is never pruned.** Recorded at the field with an estimate rather
than a measurement, and left for the next change to that file.

**And one this session introduced.** `stillAuthorized` exists to ask the same
two questions `RequireAuth` asks, so that an open WebSocket cannot outlive a
rule an ordinary request would now fail. `currentUser` was given a third
outcome earlier today — "the database cannot say" — and `stillAuthorized`
discards it. So a lookup that errors closes every connection, while the same
failure gives an HTTP request a 503 and leaves its session intact.

A database that cannot be *written* never reaches it: `currentUser` only reads,
and that case was measured — the banner arrives, the socket stays open. A
database that cannot be *read* was not measured, and there the two paths part,
with the storage banner unable to reach anyone to say why.

Closing is the conservative half and not obviously the right one: nothing is
revoked by closing, since a client reconnects the moment the database answers
and a genuinely revoked session is caught by the next check that succeeds. The
cost is the panel going dark during a hiccup, which is when somebody most wants
to look at it. Recorded at the function, unfixed, because both directions have
been argued convincingly here before and the arguments were wrong.

**And one that was changed, because the risk of changing it is a log line.**
`FileSource.warnIfExpiring` said so once. One flag gated both messages, so a
certificate warned about at fourteen days and then never renewed passed its
expiry in silence: the flag was already set, and the error saying the panel is
now serving something no browser will accept never fired. The single scenario
the function exists for — "a panel that has been serving a certificate nobody
renewed", in its own comment — was the one it went quiet for.

Two flags now, one per state, both reset when a new pair loads. Changed rather
than merely recorded because the blast radius is a log line, and because
nothing could break: every test in that package passes a nil logger, so the
body returns on its first line and the whole warning path is uncovered. That
absence is most of the missing coverage in `tlsmgr`, and it is why a flag could
be wrong for as long as it liked.

A test comes with it, and it is marked NOT RUN in its own doc comment. It loads
a certificate with a day left, checks the warning was said, moves the leaf's
`NotAfter` into the past without touching the file — which is what time passing
without a renewal looks like — and requires the expiry to be reported. Whoever
runs it should also put the single `warned bool` back and watch it fail: a test
that has not been seen to fail is a decoration, and this one has not been seen
to do anything at all.

**A gap in `safeText`, and a reason not to close it from here.** The character
set covers the directional family thoroughly — embeddings, overrides, isolates,
the standalone marks, and U+061C, which is the one everybody forgets. It does
not cover the invisible family: U+200B, U+FEFF, U+2028, U+2029 and the C1
controls. By the function's own definition — characters that make a name lie
about what it is — two rows rendering identically is a lie, and in this panel
the thing beside a name is a download link.

The obvious widening is wrong in the same way `trimPartialEscape`'s was. U+200D
holds an emoji sequence together and U+200C is required orthography in Persian,
Arabic and Hindi, so a blanket zero-width rule corrupts real names: a directory
called with a joiner in it comes apart into pieces with a replacement character
between them. Any fix has to name the characters individually and say why two
of their neighbours are absent.

Not attempted here for a reason worth recording. The file writes these
characters as `\u` escapes on purpose — "invisible in an editor, in a diff and
in a code review, the same property this function exists to defeat" — and the
editing available in this sitting turned the escapes into the characters
themselves. Writing the real bytes into that file would have created exactly
what it exists to remove, in the one place nobody would see it. The tooling
failed into the hazard the code documents.

**And a list that reshuffled itself.** `hooks.Inspect` builds `Status.Events`
by ranging over the `events` map, and Go randomises map iteration, so the list
of installed hook events reached the settings page in a different order every
time it was asked for. `encode` is unaffected — `json.Marshal` sorts map keys,
so the file written to the user's settings is deterministic — but the list sent
to the browser was not.

Sorted. Safe against the existing tests by construction rather than by hope:
the order was already random, so nothing could have been asserting one. This
project calls a self-reordering tab strip hostile in a comment two directories
away; a self-reordering list of four events is the same species, smaller.

**A passkey cannot be renamed, and a store method waits for a caller.**
`store.RenameCredential` exists and is written correctly — scoped to
`user_id`, with the reason in its comment — and `registerPasskeyRoutes`
registers five routes: login begin and finish, list, register begin and finish,
delete. There is no rename. So the name a passkey is given at registration is
the name it keeps, and the settings page lists it: whatever was typed on a
phone at the moment of registering, usually the default "Passkey", forever.

Established in three steps rather than asserted, because asserting absence has
gone wrong here before: not in `passkey.go`, which ends; not in
`registerSettingsRoutes`; and not in the authenticated group of the main route
table — where following the trail led to `registerPasskeyRoutes` in the public
group, which lists all five.

Either the endpoint is missing or the method is. Adding one is a feature and
removing the other could not be verified from here, so neither was done.

**`--static-dir` is guarded by a textual prefix check, and the repo already
says that is not enough.** `spaHandler` resolves the request path, joins it to
the root, and requires the result to have the root as a prefix. That collapses
`..` and is correct as far as it goes. It does not resolve symlinks, and
`os.DirFS` does not either — its own documentation says it "only guarantees
that the Open calls it makes to the operating system will begin with the
prefix: it does not ensure that the file returned is actually within" it, and
that it is not a substitute for chroot.

So a symlink inside the served directory pointing outside is followed, and
static assets are served before authentication, because the login page has to
load. The comment directly above the check says this handler "is one URL away
from reading the user's home directory" — the concern is stated and the
implementation stops one layer short of it.

The severity is low and worth saying precisely: `--static-dir` is a
development flag pointed at a build output directory the operator chose, and
vite does not emit symlinks. It is the operator's own directory. Compare
`internal/browse`, which serves the *project* directory — whatever an agent
wrote, symlinks included — and which resolves them properly. `handleFiles`
even states the principle: `browse.Resolve` refuses anything that leaves the
root "including through a symlink, which a textual prefix check would happily
follow." One repository, one sentence, two handlers, and only one of them
follows it.

Not fixed, and the obvious fix is wrong: `filepath.EvalSymlinks` on the
resolved path fails for paths that do not exist, which is the single-page
fallback — the most common branch this handler takes. `os.Root` from Go 1.24
enforces containment properly and is the thing to look at, and choosing it is
a design decision that needs a machine to check.

## The seven red lines, checked

A separate pass, and worth recording because three times in this session a
comment turned out to describe something the code did not do — `pumpDrain`
saying the wait was "immediate in practice" when it was never once immediate,
the unit file saying stopping "must not wait on anything" while it waited two
seconds per session, and `spaHandler` saying it is "one URL away from reading
the user's home directory" above a check that stops at the lexical layer. Rules
deserve the same suspicion as comments.

All seven hold.

- **1, one socket.** `args()` is the only place a command is built and it
  prefixes `-L` unconditionally. `-f` goes on every call too, so whichever
  command happens to start the server brings it up correctly — the invariant is
  in the constructor rather than in a rule callers have to remember.
- **2, no PTY parenthood.** Measured every time `restart-check` runs: the
  backend is killed and the sessions and the login outlive it.
- **3, one state enum.** `TestTypeScriptStatesMatchTheEnum` pins the TypeScript
  and `TestSessionOrderMatchesStateSortWeight` pins the SQL ordering. The field
  comparison added this session extends that from the enum to every row the
  server sends.
- **4, shape as well as colour.** Five states, five shapes, and the sixth
  distinction — a session that vanished versus one that exited cleanly — is a
  dashed square against a solid one: the same shape family, told apart without
  hue. Every glyph carries a `<title>` and an `aria-label`, which is the case
  shape alone does not cover.
- **5, tokens only in theme blocks.** Every line inside both dark blocks is a
  `--vp-*` assignment. Three states are covered, including the one that is easy
  to miss: "system" stamps nothing on the root, so `[data-theme]` alone cannot
  tell it from light.
- **6, validate what a hook sends.** Constant-time token comparison, `Valid()`
  on the state, and the session must exist.
- **7, exact-match targets.** `target()` renders `=name:`, and `sessionTarget()`
  deliberately omits the `:` so that asking whether a session exists fails with
  "can't find session" rather than "no current target". Two forms because the
  rule needs two. `id.TmuxName`'s fixed length is the second line of defence:
  no generated name can be a prefix of another.

### One risk the rules do not cover

The dark palette is written out twice — once under `prefers-color-scheme: dark`
and once under `[data-theme='dark']` — about thirty tokens, duplicated
verbatim. Both blocks obey red line 5 completely; nothing there is a component
style. The rule simply does not reach this shape.

Changing one and not the other gives a different panel to somebody on "system"
than to somebody who chose dark by hand, and probably only half of them would
say so. Merging the selectors is the fix and it was not attempted: collapsing
CSS selectors changes matching and precedence, which is the exact mechanism red
line 5 exists to prevent, and it has to be seen in a browser rather than
reasoned about.

Eight things, none of them verified, all written where somebody working on that
code will see them rather than only here.

## This log stopped preventing the thing it recorded

Re-reading it end to end, which had not been done, turned up this from an
earlier session:

> The restart *mechanism* has a Go test that uses a die-once script, so "it
> restarted" and "it crashed again immediately" are distinguishable — the
> ambiguity my own probe walked straight into, twice, before I noticed the
> command I was restarting failed every time by construction.

This session walked into it a third time. Probing whether the restart button
worked, the fixture was `sh -c 'echo something broke; exit 3'`; respawning
re-ran a command that exits immediately, the pane was dead again within
milliseconds with the same status, and it read as "restart does nothing" — and
it was written up here as a fresh discovery, in the entry about killed agents.

The warning was already in this file. It was not read, because the file is now
long enough that nobody reads it before probing — including the person adding
to it.

Two things follow, and only the second is worth acting on.

The lesson does not belong in prose. It belongs where the mistake is made: the
Go test that uses a die-once script cannot be walked past, and a probe written
against a command that exits by construction should have been caught by asking
"what would this fixture do if the code were perfect?" — the same question that
caught the CPU probe reporting 0.1% for both idle and busy in this session, and
the upload probe answering 404 to eight hostile cases.

And an append-only log of a hundred entries is a record, not a defence. It is
worth keeping as one — the reasoning in it is why several of this session's
fixes were possible — but nothing here should be relied on to stop a repeat.
The things that actually stopped repeats this session were all executable: the
stale-build guard, the freshness check, the mutation harness that asserts the
file is restored, `head-check`. Every one of those is a program. None of them
is a paragraph.

### And the same reading found two of my own

Further down, an afternoon spent following six `data-testid`s that no harness
referenced, concluding that an unreferenced one "is a hint, not a finding" —
one real gap out of six, a fair rate for a lead that cheap.

Measured against this session: three testids were added and one of them is
driven. `project-remove` has a render-check section that clicks it, reads the
confirmation and counts what dies. `stale-notice` has none, which was said at
the time and is repeated here so it is not only in a chat log. `file-escapes`
has none either — the `escapes` field is covered by a Go test in `browse`, and
the label that field exists to render is not looked at by anything.

So the standard this project set for itself was met once out of three, by the
person who had just read the entry describing it. Both gaps are cheap to close
next to a run of `render-check`: the file panel already has a fixture with
symlinks in it, and the storage banner has a reproducible setup in the entry
above.

### And the guard against a dead fixture is itself soft

The broadcast-rate check added to `scale-check` respawns six sessions as
printers and then, before measuring, confirms they are printing — because a
respawn that silently did nothing would report the idle figure and read as a
pass. That guard exists because the CPU probe in this session did exactly that.

The guard's own failure is a `WARN`. If the fixture cannot be established the
check skips the measurement and the run reports `0 FAIL`, which is a passing
run that measured nothing: the same shape, one level up. `stress-check`'s
scrollback precondition makes the identical choice, so it is a consistent
mistake rather than a slip.

Not changed, and the reason is in this file: "an intermittent failure is how a
check stops being read". Turning the guard into a `FAIL` on a fixture that is
at all flaky on a slower machine trades a silent skip for a red run people
learn to re-run, which is worse. The right form is to poll until the fixture is
up, with a deadline, and fail hard only after that — and writing a retry loop
for a check that cannot be run repeatedly here is how one gets a check that
hangs.

The other half of the same block: those six sessions keep printing for
everything downstream. The tmux client count, the reachability scan and the
session-switch timings that follow were calibrated with the fixture idle and
now run under load. They pass, so this is a coupling rather than a fault — but
it was introduced without noticing, and whoever next moves a threshold there
should know why the machine is busy.

### A refused clipboard write, handled on one path and not the other

`SelectionCopy` calls `navigator.clipboard.writeText` and answers a rejection
with `.catch(() => setCopiedText(''))` — the button goes from "Copy" back to
"Copy". Somebody who pressed it cannot tell a refusal from a missed tap.

The panel already knows this call gets refused. `App.tsx` keeps `blockedClip`
for exactly that, on the OSC 52 path, with the reason written down: the text a
pane copied that the browser would not take, kept so it can be offered behind a
click, "which is the activation the write needs". Two paths through one API,
one failure mode, and only the path an agent uses has a way out of it.

Not fixed. What a clipboard rejection looks like depends on the browser, the
security context and whether the gesture counted as user activation, and
guessing a fallback is what this session has repeatedly shown to be the
expensive move. `blockedClip` is right because somebody watched it fail; this
needs the same and a real phone. Three are pre-existing findings left
unfixed, one is a defect this session introduced, and two are changes made on
the grounds that a log line and a display order are the cheapest things to be
wrong about.

The middle one is the useful half of the exercise: the class this session kept
finding — a second path that was never updated — was reproduced by the person
who had just spent the day finding it. That argues the next worthwhile thing is
not another bug but a check that makes the class visible, and writing that check
needs a machine that can run it.

## The machine came back

Everything above was written in a stretch where nothing could be executed.
Three functional changes were left marked NOT RUN, on the grounds that a change
nobody has run is a claim, not a fix.

All three were judged first.

`tlsmgr`'s split of `warned` into `warnedExpiring` / `warnedExpired` passed, and
then was mutated back to the single flag to see the new test fail. It does, and
says why in the sentence that matters: "the certificate expired and the panel
said nothing: every browser now refuses it and the log does not say why."

`hooks`' `sort.Strings` passed and **nothing was testing it**. Removing the sort
changed no test result across five runs. The comment beside it had reasoned its
way there and stopped one step short — "it cannot break an existing test: the
order was already random, so nothing could have been asserting one" is true, and
is also exactly why nothing would ever catch its removal. A test that asks
twenty times now pins it; removing the sort makes it fail on the first call, and
three consecutive runs printed three different orders, which is the randomness
made visible rather than argued about.

### The check that makes the class visible

The previous entry ended by saying the next worthwhile thing was not another bug
but a check for the class this session kept finding — a second path that was
never updated — and that writing it needed a machine.

`TestTypeScriptRowsMatchWhatIsSent` was that check, and it was covering six
types out of thirteen. It lived in `internal/store`, which cannot import
`internal/httpapi` without a cycle, so it pinned the rows and left unpinned the
envelope that carries them: `stateResponse`, which is what the socket pushes on
every change and the largest hand-written interface in `wire.ts`. Moved to
`internal/httpapi`, which can see both, and moved rather than copied because a
second copy of the comparison is what the file says it exists to prevent.

Then the check itself turned out to have the defect it was written to find.
`jsonKeys` marshalled a **zero value**, so every `omitempty` field was absent
from what it believed the server sends. Demonstrated before touching it, by
adding `omitempty` to `Session.CWD`: the test failed with

```
wire.ts declares [cwd] on Session, and the server does not send them.
```

which is wrong in the way that costs the most — the server does send `cwd`,
whenever it is not empty, and the remedy the message names is to delete a
correct line from `wire.ts`. Reading the struct tags instead fixes it and lifts
what was blocking the other six interfaces, so `AuthState`, `SettingsInfo`,
`HookStatus`, `FileListing`, `SystemSample` and `Passkey` are covered now.

Thirteen subtests, all confirmed to run rather than assumed to. The shape was
already consistent, which is the outcome worth stating plainly: the check found
nothing and that is the result, not a disappointment.

### The wire has three hops and now all three are pinned

Worth writing down because the middle one is invisible and the third was doing
work nobody had credited it with.

```
Go stateResponse ──(Go test)──▶ wire.ts PanelState ──(tsc)──▶ socket.ts, App.tsx
```

`socket.ts` re-lists every field by hand when it hands a snapshot to its
listeners, which is a third place to forget. It is safe, and measured to be:
adding a required field to `PanelState` fails the build in `socket.ts:298` and
`App.tsx:130` both. The first hop was the one with nothing on it — a field added
to `stateResponse` and forgotten in `wire.ts` used to be silent in all three
places at once. Note the limit: an *optional* TS field is not caught by tsc, so
the Go test is the only thing standing there.

### The scratch terminals really were being orphaned

Recorded earlier as a prediction, from reading the CLI and the HTTP handler
against each other in a sitting where neither could be run. Confirmed: kill a
session that has two scratch terminals with `vibepanel session kill`, and both
of their tmux sessions are still there afterwards, while their rows cascade
away with the parent's. The panel then reports them at every startup without
saying who made them.

Fixed by pulling the teardown into `killSessionTree` — which is where the test
can reach it — rather than adding four lines to a switch. The extraction is the
point: the comment beside the bug asked for "the test that would have caught
the two paths drifting apart", and a test that can only reach one of them
would not have.

### A sweeper whose allowlist had drifted from what it sweeps

`sweepStaleSockets` kills tmux servers left by harness runs that were SIGKILLed
before their cleanup could run. It decides what is safe to touch from the socket
name, against a hand-written list of prefixes:

```
vp(render|stress|restart|scale|tls|clip|probe|check|release)-<pid>
```

Seven harnesses build socket names. `vpfirstrun` is not in that list and never
was, so first-run-check is the one check whose interrupted runs nothing could
ever clean up. Thirteen of its sockets were in `/tmp` when this was noticed —
all dead files, which is the lucky version; the sweeper exists because one
interrupted run was found six hours later still holding a live `htop`.

Three of the list's entries — `clip`, `probe`, `check` — belong to harnesses
that no longer exist. Left in place: dropping one strands whatever it made.

The fix is a word. The guard is the point: `harness.test.ts` reads the socket
names out of the harnesses and asserts each is sweepable, and refuses to pass
if it finds fewer than six — a reader that silently stops matching would
otherwise be a test that passes by finding nothing, which is the failure this
project has hit more than once.

Not part of the repository, but worth writing down: the machine this was found
on had 245 stale socket files and three live tmux servers, one still attached
to a session created hours earlier. Almost all of it is debris from one-off
probe scripts written during development, which use ad-hoc socket names no
sweeper knows about. The lesson for the next probe is to name its socket like a
harness does.

### Three names on screen, one of them sanitised

`safeText` exists because a filename carrying U+202E renders its own extension
backwards next to a download link. Its docstring names the second source in the
same breath: "session titles come from `pane_title`, which any program sets with
a two-byte escape sequence."

Four places render a name that came from outside. One of them called it.

- `sessionLabel` sanitised, and the sidebar and header both use it.
- The scratch-terminal tab strip had its own `label()` in BottomTerminals, with
  its own reasoning about fallbacks and no `safeText`. Same `pane_title`, one
  row lower on the screen.
- A project's name defaults to `filepath.Base` of its directory, and an agent
  creates directories. It went to a sidebar row, a tooltip, and the text of the
  `window.confirm` that asks before killing every session in the project. The
  equivalent confirm for a *session* used `labelOf` and was safe. Two confirms,
  in the same file, one of them updated.

All three are funnels in `label.ts` now, which is where that file's own
docstring already said the answer was: "one definition ... because it was two."

The character set grew at the same time, and the interesting part is what was
left out. Added: C1 (only C0 and DEL were covered), U+00AD, U+200B, U+2060,
U+FEFF — invisible, and with no role in a name. These hide a *difference*
rather than a suffix: "deploy" and "dep<U+200B>loy" are the same pixels in a
sidebar, and picking the wrong one means typing into the wrong agent.

Not added, deliberately: U+200C and U+200D. Completing the range to
`​-‏` is the obvious next edit and it was measured first:

	👨‍👩‍👧  ->  👨�👩�👧      U+200D is what makes it one glyph
	می‌خواهم  ->  می�خواهم      U+200C is what makes Persian join

Both are load-bearing in text people put in filenames. Breaking a Persian name
to defeat a lookalike is the worse trade, and the test that pins this fails with
the broken emoji printed in it, so the next person sees the cost rather than
reading about it.

### The leak next to the one that was fixed

`tearDownSession` calls `Detector.Forget(id)`, with a comment saying why: the
detector would otherwise keep a tracker per session for the life of the
process, "small, but it is the kind of asymmetry between two paths that doing
the same thing eventually turns into a real bug."

`outputSeen` is one field over in the same struct and holds one timestamp per
session so `last_output_at` is not written on every chunk. Nothing ever removed
an entry. Recorded as a prediction while nothing could be run; confirmed and
closed in the same place as the tracker.

The test asserts the entry exists before deleting the session. Without that it
would pass against an implementation that never records anything — which is the
shape of every probe this project has been fooled by.

### The measurement the comment asked for

`stillAuthorized` carried a paragraph saying its behaviour was "deliberate but
untested", and refused to argue the point: "both directions have been argued
convincingly in this file's history and the arguments were wrong."

Measured. With the database unable to answer a read, a live WebSocket closes
5.1 seconds after it stops answering — one revalidation tick. Every viewer
disconnects at once, and the storage banner the snapshot carries is exactly
what cannot be delivered to explain it.

The timing is part of the assertion, not an observation beside it. The state
poller runs every two seconds and also touches the database, so a close at 2s
would mean something else was doing it and the test would be reading a
coincidence; revalidation is the only five-second timer on that path, and the
test fails if the socket dies too early.

Not changed. Closing is the conservative half and the cost is the panel going
dark during a hiccup, which is when somebody most wants to look at it — a real
decision either way. What is different now is that changing it is a decision
rather than an accident, and the comment no longer claims something nobody
checked.

### A guard against a flood that the flood could turn on the panel

`Cooldown` exists because enabling the IP allowlist opened an unauthenticated
write into the database: one row per rejected request, measured at 237/sec from
one loopback client. It gates that to one row per source per minute, and caps
the map at 4096 so the growth does not simply move from disk to memory.

Reviewing it turned up the case the cap creates. When the map is at its cap and
every entry is still inside its window, `Allow` sweeps, finds nothing to free,
lets the event through and — deliberately — records nothing, because a
distributed flood that goes unaudited is worse than one that writes rows. The
next call then finds the map full again and sweeps again.

Measured, cap reached and every entry fresh:

	ordinary  :    82ns per call
	map full  : 25.19µs per call   (307x)

with the mutex held for all of it, on a path that runs before any
authentication. The flood that fills the map pays nothing; the panel pays. That
is the shape of the original bug, one layer up: a hardening option that hands
an unauthenticated caller a lever.

The fix takes no arbitrary interval. A sweep can only remove entries whose
window has passed, so nothing can expire before the oldest survivor does, and a
sweep before that moment frees precisely the same set as the last one. The
sweep records `oldest + window` and Allow skips until then. 25.19µs became
179ns.

Pinned as a scan count rather than a duration, with a counter on the struct for
the purpose. The defect is "it scans on every call"; a stopwatch measures the
machine as much as the code, and a flaky test in front of a security path is
worse than none. Reverting the guard makes it read: swept 500 times for 500
calls at the same instant.

### A bound that only existed while nobody needed it

`TrimAuditLog` was called once, from `main`, at startup. The panel is built to
run for months — the install instructions turn on lingering precisely so it
survives a logout — so the only thing cutting the table back was somebody
restarting the process.

That is worse than untidy. `Cooldown`'s overflow case deliberately lets an event
through unrecorded when a flood is too widely distributed to gate, on this
reasoning: "the row is the only record that it happened at all, and the trim on
the table is what bounds the damage from here." Written believing the trim ran.
Under a sustained flood, "restart the panel" is not a bound, and the flood is
the thing writing the rows.

Now on a five-minute ticker inside `Poll`, which is already the loop that keeps
the database in step. An hour would have been the obvious interval and is too
long: at the rate measured for the ungated path — 237 rows a second from one
client — an hour of overshoot is most of a million rows.

The tick was measured rather than assumed, because "one indexed delete, it costs
nothing" is the kind of claim this session has been wrong about. Worst case for
the no-op is a table sitting exactly at the cap, where the subquery still walks
50,000 index entries to find there is no boundary row: **1.03ms**, against 6.6ms
for a trim that removed 10,000 rows. Once every five minutes.

`TrimEvery` and `AuditKeep` are overridable on the server, following
`RevalidateEvery` in the ws handler. A periodic job no test can drive is how
this one came to run only at startup and stay that way.

### The second row of the soft keyboard was never driven

The primary row of the phone key bar is checked in a browser: every key on
screen without scrolling, at a 320px viewport, because eight keys at a thumb's
44px came to 380px and `ctrl` and `alt` were once simply unreachable.

The second row has a `data-testid` that nothing referenced. It is *allowed* to
scroll — "losing sight of `~` costs far less than losing sight of Escape" — but
allowed to scroll and actually scrollable are different things, and the
difference is precisely what the row above already got wrong once.

Measured by breaking it: with `overflow-x-hidden` on that row, the check now
reports

	the secondary key row overflows by 237px with overflow-x: hidden —
	the keys past the edge cannot be reached

237px is `/`, `-`, `|`, `~` and the digits. So the `overflow-x-auto` is
load-bearing rather than decorative, which is worth knowing before somebody
tidies it away.

What the check asserts: the four arrows are in the row without scrolling — they
are what it is mostly for — and the last key really arrives when the row is
scrolled to its end, rather than the row merely claiming to be scrollable.

Tap-target height is not asserted here. `findSmallTargets` already scans every
button on the page, which is how the whole key bar was found at 32px in the
first place; a second check would be a second place to update.

### Coverage, read twice

`go test -coverprofile ./...` listed `auth.SetCookie`, `HashToken` and
`TokenFromRequest` at 0.0%, which is false: they are exercised by every
authenticated request in the httpapi tests. Without `-coverpkg`, the profile
only records what a package's *own* tests reach, so anything called across a
package boundary reads as dead. The first list was almost entirely that
artefact, and acting on it would have meant writing tests for code that is
already covered.

With `-coverpkg=./...` the module is at 66.0%, and one entry survived that is
neither passkeys (driven in a browser instead) nor `main`:

**`handlePatchProject`, 0.0%.** Renaming a project, pinning it, and returning it
to automatic ordering. The browser checks rename a todo and rename a session;
nothing renamed a project. "Tabs cannot be named" is the first line of the
problem statement this project was started from.

The case worth having is the last one. `clearSortIndex` and `sortIndex` can
arrive in the same request and the handler takes the clear — the flag exists
because a null `sortIndex` is indistinguishable from an absent one after
decoding. Invert that precedence and "sort by activity" writes a position
instead of removing one, which is the failure the sidebar comment describes:
switching to automatic used to erase the arrangement, so there was never
anything to return to.

### Three guards on one door, and a gap between them

Following the empty-name question through the paths that can set one:

- `InlineName` trims and refuses an empty result, and says why: "an empty name
  is a mistake, not an instruction to clear it".
- `project add` substitutes the directory's base name for an empty `--name`.
- `RenameProject` truncates but does not reject, and neither does the session
  equivalent — consistently, so not a drift.

`--name "   "` walks past all of it, and the display had no floor:
`sessionLabel` falls back to the command and then to the word "session", while
`projectLabel` — added earlier this session — copied the sanitising and not the
fallbacks. A blank name left a sidebar row with no text, which is a row nobody
can identify or click back into to fix the name that made it.

Fixed at the funnel rather than at the three entrances, for the reason the
funnel exists: covering every entrance one at a time is how one of them stays
open.

### Two methods nobody called

`(*Live).Replay()` and `(*Live).Subscribers()` had no caller anywhere — not in
the package, not in the ws handler that looks like it would want them, not in a
test. The socket's replay comes back from `Subscribe`, which takes it under the
same lock that registers the subscriber; `Replay()` was the version of that
without the subscription, and nothing ever needed it.

Removed. Two exported methods on a type with a mutex read as API, and the next
person reasoning about `Live`'s locking has to account for them.

### Three scenarios that turned out to be handled

Written down because a negative result is the answer to the question, and the
next person to wonder should not have to run the probe again.

**A project's directory is deleted while the panel runs.** An agent running
`rm -rf`, or a directory renamed. The path stays in the database. Measured: the
file listing answers 404 "no such directory", creating a session answers 400
"the project directory is not there any more: /path", and the project stays in
the state snapshot, which is right — it has to be visible to be removed.

**Somebody runs `tmux -L vibepanel kill-server`.** `pollOnce` returns no error,
the live list empties, and the sessions are marked `exited` with
`exitStatus = -1`, which is `ExitStatusVanished` — the one status that is not a
wait status. The header then offers "The tmux session is gone. Start it again in
a new one." rather than a number no process could have returned. The row's
`state` stays `done`, which reads wrong at a glance and is not: `Exited` is
orthogonal to `State`, and the exit status is what carries how it ended.

That last one was nearly written up as a defect on the strength of a probe that
printed the state and not the exit status. The panel's whole argument about
*done* is that a crash and a finished job must not look alike; concluding it had
broken that, from a probe that could not see the field carrying the difference,
would have been the same mistake in miniature.

**A partially-covered function is not an uncovered one.** `RecordSize`,
`truncateForLog` and `Paste` all show 0.0% and all have live callers — the
resize message, the trace path behind a debug flag, and the paste the compose
box sends. Coverage says what the tests reach, not what the program runs.

### Seven of thirty-seven

`TestEverythingRequiresASession` listed seven paths by hand. The router has
thirty-seven. Among the thirty it did not name: `DELETE /api/projects/{id}`,
which kills every session in a project; `GET /api/settings/audit`, which is a
list of usernames and addresses; the upload endpoint; and `/ws`, which is the
terminal itself.

All of them were behind `RequireAuth`. Nothing said so, and nothing would have
noticed a route registered above the group rather than inside it — one line's
difference, invisible in a diff of a file that size. Now it walks the router, so
a new endpoint is covered the moment it exists rather than when somebody
remembers to add it to a list.

The interesting part was the allowlist. Written first as prefixes, and
`/api/auth/` looked obviously right — those are the sign-in routes. It exempted
four that are not: listing passkeys, deleting one, and both halves of
registering one. They answer 401, because each handler asks `currentUser`
itself; but a prefix exemption is a promise that they always will, made on their
behalf by the one test that would otherwise notice. Exact paths now, one line of
reason each, and the four are checked like everything else.

So the first version of the guard reproduced, in its own allowlist, the class of
defect it was written to find. That is twice in this session — the wire mirror
test also had to be fixed before it could be trusted — and the pattern is worth
naming: a guard is code, and nothing guards the guard except making it fail on
purpose.

Also measured, and refuted, on the way:

**A client can set the authoritative grid to any size.** `Live.Resize` and
`tmux.Resize` both check only `> 0`, and the grid is shared by every viewer, so
a bad viewport measurement becomes everyone's problem. Measured against a real
tmux: 5000x2000 is accepted with RSS unchanged at 5.1MB — the grid is sparse —
and 60000x60000 is refused outright with "width too large". The panel's scaling
already has a legibility floor that pans rather than shrinking text to a smear.
Three layers, each with its own bound; nothing to fix.

### Mutating the guards

Following the pattern from the route sweep — a guard is code, and nothing
guards the guard — each of the standing checks was made to fail on purpose.

**Red line 5's stylesheet guard holds.** A component style inside
`@media (prefers-color-scheme: dark)` written over three lines, the same thing
written on one, and a declaration inside a real `[data-theme]` rule: all three
caught, each naming the property.

The third mutation passed at first, and it was the mutation that was wrong, not
the guard: the first `[data-theme=` in the file is inside a comment block, and
the test strips comments before parsing. A mutation that does not reach the code
proves nothing about the check — the same trap as a probe pointed at an endpoint
that does not exist, which this session has already fallen into once.

**`CGO_ENABLED=0` was not in head-check.** `make build` and the release script
both set it; `scripts/head-check.sh` — which exists to be what somebody cloning
the repository gets — ran the default build. So a dependency needing cgo would
pass the check whose whole purpose is to be honest about a clean checkout.

Fixed, and then the fix's own justification turned out to be wrong. The first
version of the comment said a cgo dependency "compiles here and fails at
release". Measured: with `CGO_ENABLED=0` the toolchain does not reject cgo, it
*excludes* the files that use it, so a cgo file nothing references is silently
dropped and both builds pass. It is a cgo symbol that is **referenced** — which
is what adding a real dependency looks like — that fails under `CGO_ENABLED=0`
and builds fine under the default. Same conclusion, different mechanism, and the
mechanism is the part somebody will rely on later.

### Auditing the guards

Every standing check in the repository, against one question: would it notice?
Two were confirmed by making them fail; the rest by reading, in a stretch where
nothing could be run, and marked as such.

| guard | what it pins | verdict |
|---|---|---|
| `styles.test.ts` | red line 5: theme blocks declare only tokens | **failed on purpose**, three ways |
| `TestSessionOrderMatchesStateSortWeight` | SQL `CASE` vs `State.SortWeight` | read: sound both ways |
| `TestBinaryFrameLayoutMatchesTheClient` | frame bytes, header length, byte order | read: sound both ways |
| `TestMessageTypesMatchTheClient` | the JSON message-type unions | read: sound |
| `TestTypeScriptRowsMatchWhatIsSent` | thirteen wire shapes | fixed and failed on purpose earlier this session |
| `TestEverythingRequiresASession` | every route needs a session | fixed and failed on purpose earlier this session |
| `harness.test.ts` | the sweeper's socket allowlist | failed on purpose earlier this session |
| `deps.test.ts` | no component or state library | read: emptiness covered, but indirectly |
| `scanTapTargets`, `scanUnreachable` | thumb-sized controls, unreachable content | **hole** |

Two are worth writing down.

`TestSessionOrderMatchesStateSortWeight` derives its expectation from
`SortWeight` and compares it to what the SQL actually returned, so a change to
either side disagrees with the other. The hand-written `CASE` appears exactly
once in the tree, which is the other half of why it holds.

`TestBinaryFrameLayoutMatchesTheClient` checks the constants, the header length,
**and** both ends of the byte order — `getUint32(1, false)` and
`setUint32(1, ref, false)`. Checking only the read would have left the client's
own writes free to disagree, and those carry every keystroke.

`deps.test.ts` is protected against reading an empty `package.json` only because
a different `it()` in the same file asserts that react and xterm are present.
Delete that block for an unrelated reason and every "has no component library"
assertion goes vacuous. Not a defect; a thread worth knowing about.

The hole is recorded at its call site in `render-check.mjs`. Both scans report
only when they find something and neither asserts it looked at anything, so a
button refactored into a `<div onClick>` — or a scan run before the view
renders — makes them blind and silent. The two checks written most recently
assert a floor on what they saw for exactly this reason; these predate that.

One assumption checked rather than carried: the passkey surface is 0.0% in the
Go coverage profile, which is fine **because** `render-check` registers a
passkey, signs out, and signs back in with it. That was believed on the strength
of a line in AGENTS.md before it was looked up.

### Everything on the wire that is written twice

The hooks finding came from one question — what mirrors a source of truth by
hand — so the question was asked of the whole file. `wire.ts` declares four
constants:

| constant | mirrors | pinned |
|---|---|---|
| `STATE_ORDER` | `session.AllStates` / `SortWeight` | yes |
| `FRAME_DATA` | `ws.FrameData` | yes |
| `FRAME_REPLAY` | `ws.FrameReplay` | yes |
| `EXIT_VANISHED` | `store.ExitStatusVanished` | **no** |

The gap is recorded in `TestBinaryFrameLayoutMatchesTheClient`, which pins the
other three and reads the same file. Drift there reproduces a bug that already
happened: the frontend treats any non-zero status that is not `EXIT_VANISHED` as
a crash, and `Sidebar.tsx` counts those for the project summary — so a session
whose tmux session merely disappeared would be reported as having crashed, with
a badge reading a number no process could have returned. Adding it needs the
`declared` helper to accept a leading `-` and an int64 comparison, so the better
home is probably `internal/httpapi/wire_test.go`, which already imports store.

Two more enums are mirrored by hand and pinned by nothing: `titleSource`
(`'auto' | 'manual'`) and `stateSource` (`'heuristic' | 'hook' | 'manual'`).
Both agree with Go today. Unlike states, there is no `AllSources` slice to
compare against, which is why no test could have been written the same way.

### Two fields nothing reads

Checking what the UI does with those two turned up something else: across all of
`web/src`, `stateSource` and `titleSource` appear **only in a test fixture**. No
component reads either.

Both are enforced where they matter, server-side. `title_source` is what stops
an automatic title overwriting a manual one, and that lives in the SQL — `AND
title_source = 'auto'`. `state_source` is the detector's, and the panel-wide
"this state is guessed" hint the UI does show is `stateGuessed`, computed on the
server.

This is the shape of a field already removed once: `lastOutputAt` was taken off
the wire because "nothing read it. It was declared in wire.ts and used nowhere",
and it was costing 85 KiB a minute per viewer. These two are small by comparison
and the case is not identical — a *per-session* "guessed" indicator would need
`stateSource`, and `stateGuessed` is panel-wide — so this is recorded rather than
acted on. Whoever decides has both halves in front of them now.

### The fix that was applied to one of three rules

`Signals.Advanced` was added earlier in this session to separate a screen that
moved forward from one being redrawn where it stood. The reason is measured and
sits in `detect.go`: three seconds of a spinner is 480 bytes and **zero** line
feeds, three seconds of an agent working is 430 bytes and twenty-two.

`Evaluate` has three rules that ask "has this session done something since". The
bell rule was changed to `lastAdvance`. The manual rule and the hook rule were
not, and still read `lastOutput`.

Both are recorded at their call sites rather than changed, because nothing could
be run in the sitting that found them.

**The manual rule** fails in the case the feature exists for. Somebody overrides
the state precisely when the automatic one is wrong, and the automatic one is
most often wrong while a TUI is animating — so the next chunk arrives in
milliseconds, `lastOutput` passes `manualAt`, and the dot snaps back. The click
reads as having done nothing.

**The hook rule** is worse. `hookGrace` is three seconds; the same measurement
says a spinner emits 480 bytes in three. So a hook that reported "waiting for
you" is discarded, the fall-through reads the foreground process, and the panel
says *working* — its precise source overridden by its guess, in the one case the
precise source exists for. The README says the bell and a hook report "are
separate and outrank it"; this rule makes that true for three seconds.

The paragraph that explains the bell fix applies to both without a word changed:
"an agent whose TUI keeps moving defeats any finite grace, because the test was
'has anything printed since', and something always has." It is in the same
function, a few lines below the two rules it was never applied to.

That is the shape this session kept finding in other people's code, produced in
the session's own. It was found by reading the file end to end and asking which
rules consume the field that was added — `lastAdvance`'s field comment names the
bell rule as its only reader, and that was the thread.

### State of the tree when execution went away

Nothing below is committed. HEAD is sound and was last verified in full — `make
check`, `head-check`, and all seven browser and system checks green. Everything
here came after that, from reading only.

**Run `make check` first.** One of these files has never been compiled.

Fourteen files. Every one is a comment or a document except
`internal/hooks/states_test.go`, which is a test that has never been compiled.

Reconciled against the findings recorded before this stretch, which used other
wording and so were invisible to a sweep for "KNOWN GAP". Of the eight: five
were fixed and committed — the orphaned scratch terminals, the escape-trim blind
spot, the unpruned output debounce, the socket-closing behaviour of an
unreadable database, and safeText's missing invisibles. One, the textual
containment of `--static-dir`, is a decision rather than an open item: left
alone deliberately, with the reason and `os.OpenRoot` named as the fix if it
changes. One, that a passkey cannot be renamed, is a missing feature the UI
agrees with — registration prompts for a name and the list renders it as plain
text with no edit affordance. The eighth is 27 below, which had never been
carried into this table.

**The rows are ordered by what it would cost to leave alone. The numbers are
labels, not positions** — each was assigned when the finding was written, and
the row order changed as later ones were slotted in beside their peers. So the
numbers run out of sequence and nothing is missing; a row that says "sits with
9" is telling you why it is where it is. Renumbering on every insert is how a
reference by number stops meaning anything, which is the same reason a bug
tracker does not renumber.

| # | finding | where | fix |
|---|---|---|---|
| 1 | **Fixed, and measured — see the last section.** A hook that reported "waiting for you" is discarded after three seconds of a spinner, and the panel says *working* — its precise source overridden by its guess. The panel-wide "guessed" notice does not cover for it, in two independent ways: one other session holding a fresh hook report suppresses that notice everywhere, and — per 7 — the notice only fires at all when a session's polled `pane_current_command` is literally `claude` or `codex`, so on a machine where Claude Code reports `node` it never appears regardless. Fixing either does not restore the mitigation; both have to hold. | `internal/session/detect.go` | `lastAdvance` for `lastOutput`, after reproducing an agent that reports and then animates |
| 37 | **Fixed, and measured — see the last section.** The client drops every `error` frame the server sends. `ServerMessage`'s union declares ten members and `handleControl`'s switch has eight cases; `pong` needs none, because any frame refreshes `lastSeenAt`, but `error` is simply absent — no case, no default, and `'error'`, `MsgError` and `msg.message` appear nowhere in `socket.ts`. Six server sites send one, and three of them are `write failed` twice and `paste failed`: you type into a terminal, the write fails server-side, the server says so, and nothing reaches the screen. This is the hazard `TestMessageTypesMatchTheClient` was written about — "the server says 'dropped', the client hears nothing, and the terminal sits frozen looking like a network problem" — fixed for `dropped` and not for `error`. That test compares the server's sends against the *declared union*, which `error` is in, so it passes; nothing pins the union against the switch. The mirror direction is sound today and unpinned in the same way: `ClientMessage` declares six types and the server's `switch msg.Type` has a case for all six, with no `default` and no exhaustiveness check Go could offer. Worth fixing together, and in that order — a `default` on the server would answer an unknown type with an error frame, which is the frame the client currently drops. | `web/src/protocol/socket.ts` | a case for `error`, and a `default` with a `never` check so the third hop is pinned by the compiler |
| 38 | **Fixed, and the finding as written was wrong — see the last section.** A fourth session state would render as *working*, silently. `renderGlyph` is an if-chain — `waiting` → triangle, `done` → check — ending in an unconditional return of the breathing circle. That is exhaustive today, because TypeScript narrows the union to `'working'` by then. Add a member to the enum and to `wire.ts`, which red line 3 already forces you to do together, and the type widens while the final `return` keeps catching everything: the new state gets working's shape and working's colour. Red line 3 names three mirrors of the enum; this is a fourth, and the only one whose failure is a wrong answer rather than a missing one — "a confident wrong answer to the only question it exists to answer", in the project's own words. Red line 4's premise is three shapes for three states, with no room for a fourth. Same structural class as 37. The right pattern is forty lines above it in the same file: `LABEL` is a `Record<SessionState, string>`, so adding a member to the union makes that object literal fail to compile. One function is pinned by the type system and the other is not, in the same component, for the same enum.

There is a second site, and it is the more visible one. `summarise` in `Sidebar.tsx` is a priority chain — any `waiting`, then any `working`, then any crash — ending in `return sessions.length > 0 ? 'done' : null`. A project whose sessions are all in the new state matches none of the tests and comes out as *done*, so the badge in the collapsed 42px sidebar, which is the panel's whole at-a-glance surface, shows a green check. Its own docstring is about precisely that outcome: returning a crash as a crash "is what stops a project whose every session died from wearing a green check." Both sites are triggered by the same edit, so whoever adds a state wants both in front of them.

Two is all of them. Swept by type rather than by literal — everything that takes a `SessionState` — which finds five uses in two files: `LABEL`, the two callback props that only pass a value along, and these two. Searching for `'waiting'` would have been the natural way and the wrong one, for the reason 17 records: a grep shaped by what you already imagined finds only that. This list being complete is the point, since the whole failure mode here is stopping at a list somebody wrote down. | `web/src/components/StateDot.tsx` | `if (state === 'working')` and a `never` check, so adding a state is a compile error |
| 2 | **Fixed, and measured — see the last section.** Two of the four callers of `currentUser` silently discard its third outcome, "the database cannot say", and collapse it into "not signed in". `/api/auth/state` returns that to a client whose rule is `onSignOut()` — and an unreadable database is exactly what closes the socket that makes the client ask. `handleChangePassword` returns 401, which the frontend turns into a return to the sign-in screen, which reads the same database. `RequireAuth` was fixed to answer 503; `stillAuthorized` drops it deliberately and documents why. | `internal/httpapi/auth.go` | 503 when the error is non-nil, at both sites |
| 3 | **Fixed: the test exists, was seen to fail, and is committed.** The state strings `internal/hooks` writes into the reporter script, the Codex `notify` line and `~/.claude/settings.json` are bare literals; the package does not import `internal/session` at all (verified: the only such import is the new test itself). Partly covered by accident — `TestTheReporterScriptActuallyReportsState` posts a hard-coded `"waiting"` through the real script and waits for it, so renaming the enum's *value* makes that round trip fail. What nothing covers is the mapping: a typo in `events` (`"Notification": "wating"`), a state in the `ClaudeSettings` snippet that the map does not know, or a new enum member the hooks should report. Narrower than it first looked, twice over. `report.sh` carries a fourth copy in its `case working\|waiting\|done` guard, and that one self-heals: `hooksAreInstalled` calls `scriptPath` on the way to every state snapshot, which rewrites the script whenever its content differs, so an upgraded binary refreshes it as soon as anyone opens the panel. What does not self-heal is the Codex line — `CodexNotify` writes the literal `"waiting"` into `~/.codex/config.toml` and nothing repairs a TOML file. **Correcting what this row said earlier:** Claude does not recover by itself either. `ClaudeSettings` writes the state into the user's `settings.json` as an argument — `"command": "<script> waiting"` — and nothing rewrites that file on its own. `Inspect` asks only whether *an* entry points at our script, not what it passes, so a stale argument still counts as installed and `InstallClaude` runs only when somebody presses the button. So after a rename the self-healing script accepts only the new names while both agents keep sending the old one from files that do not self-heal, the `case` falls to `*) exit 0`, and every session goes quiet with the settings page reporting all four events installed. The script healing itself is what makes this worse rather than better: it is the half that changes, so the halves stop agreeing. | `internal/hooks/states_test.go` (new), `AGENTS.md` | run the test, then rename `StateWaiting`'s value and confirm it fails |
| 4 | **Fixed with 1 — the manual rule reads `lastAdvance` now.** A manual state set by clicking the dot is cleared by a spinner, so the click reads as having done nothing in the case the feature exists for. | `internal/session/detect.go` | same as 1, and more of a judgement call |
| 5 | **Retracted — the premise was measured and is false. See the last section.** The poller rewrites `cwd` and `command` for every live session on every tick whether they changed or not — about twenty-four writes a second at idle, at the scale this is built for. Two of the four writes in the same loop compare first. | `internal/httpapi/api.go` | `if info.Path != row.CWD \|\| info.Command != row.Command`; provably a no-op on content |
| 6 | **Fixed, and the finding named the wrong character — see the last section.** An uploaded filename containing a newline is **typed** into the shell, so the line editor reads it as Enter and leaves the user at a `>` they cannot explain. This is the same bug `pasteText` was introduced for — its docstring records a three-line instruction typed at an agent arriving "as three separate GOT<> lines. An agent acts on the first sentence before it has read the third." The compose box was moved to a paste; the upload path still types. A filename reaching the *screen* goes through `safeText`; this is the one place its bytes reach a shell. | `web/src/App.tsx` | `pasteText` instead of `writeText`, with the caveat from its own docstring — the server brackets a paste only if the pane asked for bracketed paste |
| 7 | **Confirmed by measurement, and made diagnosable — see the last section.** `agentCommands` is matched against `#{pane_current_command}`, which is somebody else's packaging. If Claude Code reports `node` on a machine, the "states are guessed" notice never appears there. | `internal/httpapi/api.go` | `tmux -L vibepanel list-panes -a -F '#{pane_current_command}'` settles it |
| 8 | **Fixed: pinned in `internal/httpapi/wire_test.go`, and seen to fail at -2.** `EXIT_VANISHED` is the one constant of four on the wire that nothing pins. Drift reproduces a bug that already happened: a session whose tmux session merely vanished counted as a crash. | `internal/ws/protocol_test.go` | needs a leading `-` in the regex and int64; `internal/httpapi/wire_test.go` is the better home |
| 22 | **Fixed: `scripts/verify.sh` collects the summary lines under the verdict.** `make verify` prints "all checks passed" over any number of warnings. A WARN does not change a check's exit code — deliberately, since it separates "the thing under test failed" from "its setup did not happen" — but several of them mean a section was skipped: no second project to drag, no uploaded file in the tree, no dead session for the header check. render-check has twenty-four WARN sites. So six sections can be skipped and the run still ends with the word "passed", twenty minutes after the warnings scrolled past. Sits with 9, and is the same shape head-check was written to remove. | `Makefile` | every check already ends with `=== name: N FAIL, N WARN ===`; collect those and print them under the verdict |
| 28 | **Fixed: `check` ends with a notice when tmux is missing, both branches driven.** `make check` passes on a machine with no tmux, having run almost nothing. Four test helpers call `t.Skip("tmux not installed")` and each guards a whole suite — `internal/tmux`, `internal/session`, `internal/httpapi`, `cmd/vibepanel`. `go test` without `-v` does not summarise skips, so every one of those packages prints `ok`. The Makefile never names tmux as a prerequisite. Worse than 22 in one way: `check` is the gate people run before committing, and AGENTS.md's warning that "a change that only passes `check` has not been looked at" is about the browser checks, not about `check` itself being hollow. | `Makefile` | look for tmux in the target and say loudly what will not be tested without it |
| 29 | **Worse than it said, and now labelled with the evidence — see the last section.** stress-check's escape-fragment check looks for the shape that is already trimmed. `trimPartialEscape` handles `31mhello` and, as pinned by `TestTrimPartialEscapeKnownFalseNegative`, does not handle `[31mhello` — the case where only the ESC was evicted. The check's regex is `^\d+;?\d*[a-zA-Z]\s*$` with `length < 8`, which matches the first and cannot match the second: `[` is not a digit and the string is nine characters. So the one browser check aimed at that defect is aimed past it. It is also a WARN, while the two assertions above it in the same block — nothing recognisable after a reload, the replay injecting terminal responses — are both FAIL. Checked the other suspicious WARNs in that file and they hold up: the alternate-screen one is a weaker observation beside a FAIL, and the rest are setup that did not happen. | `web/scripts/stress-check.mjs` | match the leading `[` too, and decide whether a literal fragment on screen is a failure |
| 45 | **Fixed: `restart-check` squeezes a running panel and watches the banner. See the last section.** Nothing re-runs the one fault the stale banner exists for. A full disk is what that whole path is built around — `CheckWritable`, `noteStale`, the three-tick grace, `/api/health` answering `"ok": false`, and the banner itself — and no harness injects it, so `stale-notice` is driven by nothing. Both halves that are usually hard are already done and recorded above: the injection is `ulimit -f` applied to a restart after the database exists, since "Go turns SIGXFSZ into a write error, which is close enough to a full disk" (chmod proves nothing on an already-open file, and no tmpfs is available here), and the banner was watched appearing in a real browser six seconds in with the connection staying open. What is missing is only the wiring into a check. Sits with 9. | `web/scripts/render-check.mjs` | `ulimit -f`, restart, assert the banner appears and the socket stays open |
| 9 | **Fixed: both return what they examined, and a floor is asserted. Seen to fail blind.** `scanTapTargets` and `scanUnreachable` report only when they find something and never assert they looked at anything. A button refactored into a `<div onClick>` makes them blind and silent. | `web/scripts/render-check.mjs` | a floor at zero candidates, which cannot false-positive |
| 21 | **Fixed: events have their own queue beside the snapshot slot.** Panel notifications and state snapshots share one coalescing slot. `notifyPanel` goes through `Hub.Broadcast` → `queueState`, which *replaces* `statePending` — correct for a snapshot, because the newest contains the older ones, and wrong for an event. A `panel` message saying "your notes changed, fetch them again" is dropped if anything else queues while `stateWriter` is inside `sendRaw`, which is a network write that can block. The other viewer's notes or todos then do not refresh until something else wakes them. Sits with 5 and 6. | `internal/httpapi/api.go`, `internal/ws/conn.go` | a second slot, or a small queue for messages that are not snapshots — not dropping the coalescing, which exists for a measured reason |
| 16 | **Fixed, after the first version of the check proved nothing — see the last section.** The compose box's multi-line branch is exercised by nothing. `text.includes('\n')` routes to a paste — the fix for a measured failure, "three lines in, three separate submissions out" — and render-check fills the box only with `true` and an `echo`, both single-line. The chain behind it, `pasteText` → `MsgPaste` → `Manager.Paste`, is 0.0% under `-coverpkg` and named by no Go test. The branch that *had* the bug is driven five times; the one that fixes it, never. Sits with 9 and 15. | `web/src/components/mobile/ComposeInput.tsx` | one multi-line fill in render-check's mobile section; the assertion is already written in the comment above the branch |
| 15 | **Fixed: `reorder` extracted and tested; the mutation is caught by two cases.** The drag-reorder adjustment in `useDragList` — `overIndex > from ? overIndex - 1 : overIndex` — is the downward-drag branch, and nothing takes it. render-check drags the second project *above* the first, which is upward; `web/src/hooks` has no test file, in a frontend where every other pure-logic module has one. Not a reported bug: the untested half of a classic off-by-one, in a gesture used constantly, failing silently — a project dropped one position from where it was aimed reads as having aimed badly. Sits with 9. The empty `hooks/` directory is not the finding, though — the other module there is `useMediaQuery`, whose logic is the string `(max-width: 767px), (max-height: 500px) and (pointer: coarse)`, and a unit test for that would only copy the string back. Its four cases were checked by reading and are right: a phone in portrait, a phone in landscape at 844×390 (the case that once produced a six-line terminal), a desktop window dragged narrow, and a touchscreen laptop with a mouse, which reports `pointer: fine`. So the gap is one function, not one directory. | `web/src/hooks/useDragList.ts` | a unit test (it is a pure function of ids, from, overIndex) and a downward drag in render-check |
| 10 | **Fixed, and it found a second check worth more than the one asked for — see the last section.** `doctor` never asks whether the panel answers on the loopback URL its own hooks post to, which is the one check that would turn the bind-address trap into a line of output. | `cmd/vibepanel/main.go` | one GET, only while a panel holds the lock |
| ~~11~~ | **Retracted.** Claimed that `passkey/login/finish` consults the login throttle and never feeds it, on the strength of `grep Throttle` in that file returning only `Delay` and `Succeed`. It does feed it: the refusal path calls `s.failLogin`, the shared helper that does `Throttle.Fail` *and* writes `login.failed` with the detail `"passkey: …"`. Prefixing the shared event is better than the separate `passkey.login.failed` the finding asked for, because one fail2ban rule then catches both. The grep was true and incomplete — the third interaction goes through an abstraction, which is what a grep for the concrete name cannot see. | — | — |
| 14 | **Fixed: both detach before destroying anything, both halves mutation-checked.** Both delete paths run on the request's context, which Go cancels when the client disconnects, and both loop over sessions. A tab closed just after pressing delete leaves some tmux sessions dead with their rows intact — a batch of GONE from doing nothing wrong. The comment on `tearDownSession` reasons about which half should fail first and not about the half being cancelled; `notifyState`'s writers already detach with a 5s background context for exactly this reason. Found after the ordering above was fixed; it belongs beside 5 and 6. | `internal/httpapi/api.go` | detach the teardown the way notifyState does |
| 12 | **Left open deliberately, and the code says so.** The comment in `handleCreateProject` already states the trade and names the answer if the decision goes the other way ("the useful answer names the project already there rather than a UNIQUE constraint failure"). Refusing removes a capability somebody may want; disambiguating two projects that share a name *and* a path needs an ordinal, which is its own product decision. Not a defect to fix in passing. Nothing stops the same directory being added twice, so the sidebar can show two entries that are the same pixels with separate notes behind them. `disambiguatedLabels` solves this for sessions and not for projects. Arguable rather than wrong. | `internal/httpapi/api.go` | if refused, name the project already there rather than a UNIQUE failure |
| 30 | **Fixed: both halves, and both seen to fail.** Removing `safeText` from `sessionLabel` would pass everything. Four places sanitise a name: the function itself is tested; `projectLabel` and `terminalLabel` assert `.not.toContain('\u202E')` — both added this session; `sessionLabel` is tested only for its fallback chain; and `FileTree`'s inline `safeText(e.name)` is asserted nowhere. The browser checks do exercise the rendering path — they locate rows by `hasText: 'scratchpad'` — but every name in them is plain ASCII, so nothing there touches the sanitising either. The unguarded one is the original funnel, the one safeText's docstring is written about: "session titles come from `pane_title`, which any program sets with a two-byte escape sequence." | `web/src/components/label.test.ts`, `web/scripts/render-check.mjs` | one assertion for sessionLabel; for FileTree, a file whose name carries an override — the transfer section already writes files |
| 31 | **Half wrong, and the other half is now covered — see the last section.** `TruncateTitle`'s tests cannot tell runes from bytes. The constant's comment gives the reason for the whole design — "runes rather than bytes so that truncation cannot split a character and leave invalid UTF-8 in the database" — and both tests feed it `strings.Repeat("A", …)` and `strings.Repeat("x", …)`, pure ASCII, where a byte slice and a rune slice agree. Rewriting it as `s[:MaxTitleRunes]`, which is the obvious way to drop an O(n) `RuneCountInString`, passes both and cuts a CJK title mid-character. Implementation is correct today; the property is unasserted. Same shape as 30, different file and fix. | `internal/session/title_test.go` | one case with multi-byte runes |
| 17 | **Fixed: `passkeyLabel`, the fourth funnel.** A passkey's name is rendered raw in two places in `Settings.tsx`, one of them the `window.confirm` that asks before deleting it — the same shape as the project-name fix made earlier in this session, one file over. Low severity: the name comes from a query parameter the user types, defaulting to "Passkey", with no external default like the directory basename that made project names dangerous. It is the fourth name-rendering site and the only one not funnelled. | `web/src/components/Settings.tsx` | `safeText` at both, or a `passkeyLabel` beside the other three |
| 18 | **Fixed at the source, and the TypeScript that agreed with the bug is corrected too.** Two endpoints send `null` for a JSON array, bypassing `emptyIfNil` — whose own comment says it exists "so the frontend never has to guard a map over a missing list". `hooks.Status.Events` is nil until a hook is installed, which is every fresh panel; the upload's `paths` is nil if no part is named `file`. Neither crashes today, and the reason is the tell: `Settings.tsx` reads `(status.events ?? []).length`. That guard is the symptom patched at the reader, in the one place the helper was written to make unnecessary — so the next reader added without it throws. | `internal/httpapi/settings.go`, `internal/httpapi/panels.go` | `emptyIfNil` at both, and the `?? []` can then go |
| 19 | **Fixed, with a migration for the rows already written.** Eleven of the thirteen audit events are dot-separated — `login.failed`, `setup.rejected`, `passkey.register.failed`, `hooks.installed`. Two are not: `password_changed` and `password_change_refused`. The field is grouped by the `GROUP BY event` the runbook hands the operator, listed on the settings page, and is what a fail2ban rule matches on, so one separator is worth having. Not free, though: rows already exist with the underscore spelling, so renaming the emitted value either leaves history mixed or wants a migration. Related and smaller: an entry in this log refers to "the fail2ban story the README advertises" and the README does not mention fail2ban — left alone, because a chronological record describes what was true when it was written. | `internal/httpapi/auth.go` | one separator, and decide what happens to the rows already written |
| 20 | **Fixed: `release-check` builds it and runs it, and it turned out to work.** Nothing builds the container image. No Makefile target, no script, none of the seven checks — while `Dockerfile` pins `node:24-alpine`, `golang:1.26-alpine` and `alpine:3.21`, and `deploy/docker-compose.yml` builds from it. It is a shipped artifact with the property `head-check` was written to remove: nothing tells you whether what was committed works. Lower stakes than the binary, since the Dockerfile itself says the container "is the awkward way to run this and is offered second". Smaller half: the compose file, which is what a compose user opens, does not repeat or point at the caveat. | `Dockerfile`, `deploy/docker-compose.yml` | a `docker build` in release-check, or an honest note that the image is unverified |
| 23 | **Fixed: `hook.rejected`, gated the same way its two siblings are, and seen to fail without it.** Three unauthenticated failure paths, two audited. The allowlist refusal and a bad setup token both write a row through `auditFromOutside`, cooldown-gated because they are unthrottled; a bad token on `/api/hook/state` writes nothing at all. The runbook's diagnosis for "somebody is hammering this panel" is `GROUP BY event` over `audit_log`, where a hook probe is invisible. The tell that it was not decided rather than chosen: the other two carry a comment explaining the audit choice, and this one explains only its constant-time compare. Low severity — the token has full entropy, so the value is noticing the attempt, not preventing it. | `internal/httpapi/api.go` | `auditFromOutside`, the same as its two siblings |
| 24 | **Fixed at the three render points; the browser drive was attempted and abandoned — see the last section.** Server error strings are a name-carrying channel nobody funnels. `safeText` is applied to fields the frontend knows are names; an error message is not one, and several echo the same values — `base+" already exists"` on an upload conflict, `"writing "+base+": "`, `abs+" is not a directory"`, `"unknown state "+req.State`. The frontend renders them raw, as `{error}` in the banner and `setDropNote(err.message)`. So a file whose name carries a directional override produces a conflict message that reverses the text around it, at the moment you are deciding whether to rename and retry. Lower severity than 17 — you dropped the file — but the threat model in safeText's own docstring is "whatever an agent or a download wrote to disk". Sits with 17.
| 46 | **Fixed: it polls now, after failing a second time. See the last section.** `render-check`'s mobile scrollback assertion is flaky. "dragging down did not scroll back: top line was TOUCH_368, now unreadable" fired once in six runs today, on a tree where the five runs around it were clean and the change under test was in the upload path. Re-running passed. That is the shape that trains people to re-run rather than investigate, and this project's whole argument for the browser checks is that a FAIL there means something. Worth pinning down before it is seen twice more and starts being ignored. | `web/scripts/render-check.mjs` | the gesture is dispatched and then read after a fixed sleep; make it poll for the line to move, and report what it saw if it never does |
| 47 | **Fixed in the same sitting that found it: the section is rewritten with the measured mechanism.** The runbook's "hooks say they are installed and no state ever arrives" section explained the wrong mechanism, and so did the comment the fix was written from: binding one interface does not strand the hooks, because `LoopbackURL()` follows `BindHost()` and the sessions are told the bound address too. Measured. Rewritten with what actually does it. | `docs/runbook.md` | done |
| 48 | **Fixed in stress-check, and narrower than it looked: `render-check` reads `innerText` and does not have it.** Terminal text read out of the DOM comes back with U+00A0 where the spaces are, depending on how many style runs the row has. xterm emits one `<span>` per run, and `textContent` joins them with a non-breaking space. A line printed with two escape sequences reads back with ordinary spaces; the same line with eight does not, and every `includes(...)` in the file silently stops matching. Normalised in `rows()` in stress-check; `render-check` reads terminal text in about thirty places and was not touched. | `web/scripts/render-check.mjs` | the same one-line normalisation where it reads rows |
| 49 | Every colour in the light terminal palette's *bright* row is below WCAG AA on white, and the normal row is comfortably above it. Measured: normal red 5.38, green 5.39, yellow 5.31, blue 7.56, magenta 6.24, cyan 5.82; bright red 3.55, green **2.22**, yellow 3.46, blue 4.02, magenta 4.13, cyan 3.16. The palette's own comment says it is "tuned for contrast on white rather than lifted straight from a dark theme", and the normal row was; the bright row is the Apple system colours unchanged. Nothing had ever driven coloured terminal output through the contrast check, which is why it never said so — the same shape as 29. Not fixed here because it is a real design trade rather than a defect: bright cannot be both brighter than normal and above 4.5:1 on white when normal is already 5.4, so tuning it collapses the two rows into each other. The alternatives are inverting the relationship (bright = stronger, not lighter, which several light terminal themes do) or deciding terminal output is exempt, as most terminal palettes assume. | `web/src/styles.css` | a decision, then values |

The same channel has a better instance, and it needs nothing from the owner. `handleLogin` does not call `validateCredentials` — that runs on setup and on a password change — and `Audit` does not truncate, so a failed login is recorded with whatever username was sent. The settings page renders it as `{e.username \|\| '—'}`, raw. So an unauthenticated stranger chooses text that appears, unsanitised, in the one view its owner opens to find out who has been attacking them. Half of it is contained: every audit cell has `truncate` and a fixed width, so a 10 KB username cannot break the layout. `truncate` does nothing to U+202E, and the override's reach is its own `<span>`, so the damage is confined to the username cell — limited, not absent. | `web/src/App.tsx`, `web/src/components/Settings.tsx` | `safeText` where server strings and audit fields are rendered, which covers the channel rather than each message |
| 25 | The same destructive call is confirmed on one path and not the other. Killing a session asks `window.confirm("Kill …? The process is terminated.")`; closing a scratch terminal is `onClose={(t) => api.deleteSession(t.id)}` — the same endpoint, the same killed process, no prompt. The X sits inside the tab, next to the tab's own click target, which is a mis-tap on a phone. Arguable rather than wrong: scratch terminals are framed as cheap and numbered, and a tab strip that asks on every close is one people stop using. What is missing is the sentence saying which of those was chosen — the sibling path has a confirm and this one has neither a prompt nor a note. | `web/src/App.tsx`, `web/src/components/BottomTerminals.tsx` | decide, and write down which |
| 26 | **Fixed: a live session is refused, and the pid is asserted.** `POST /sessions/{id}/restart` kills whatever is running. `Respawn` is `respawn-pane -k`, and nothing on the server asks whether the session has exited — the comment there reasons carefully about the tmux session being *gone* and not about it still running. The only guard is the frontend, which renders the button only when `current.exited`. The reachable case is the one the panel is built for: two viewers, A restarts an exited session, B's tab still shows the old snapshot and offers the button, and B's click kills the agent A just started. One round trip wide, since `notifyState` follows the restart. Possibly the capability is wanted — restarting a stuck agent is a real need — in which case the gap is the opposite one, that the UI never offers it deliberately. | `internal/httpapi/api.go` | refuse a live session, or offer it on purpose with the confirm that implies |
| 27 | A refused clipboard write is recoverable on one path and silent on the other. `App.tsx` keeps `blockedClip` for the OSC 52 case — the text a pane copied that the browser would not take, held so it can be offered behind a click, "which is the activation the write needs". `SelectionCopy` answers the same rejection with `.catch(() => setCopiedText(''))`, so the button goes from "Copy" back to "Copy" and nobody can tell a refusal from a missed tap. It is the touch path, where browsers are stricter about the clipboard and a missed tap is likelier, so the ambiguity lands where it costs most. Predates this phase — described in an entry above, never carried into this table, which is why the index was swept for what it might be missing. | `web/src/components/mobile/SelectionCopy.tsx` | the `blockedClip` treatment, or any message that separates the two outcomes |
| 32 | Every build prints a warning nobody has decided about. Vite reports "some chunks are larger than 500 kB" for the 644 KB bundle, on `make build` and again inside each of the seven checks that build first. There is no `chunkSizeWarningLimit` in the vite config and no note anywhere about it. The answer is almost certainly to raise the limit — the bundle is xterm and React, embedded in the binary, served locally, loaded once — but the reason is the part worth having, because this project argues elsewhere that output people learn to ignore is worse than no output: `doctor` reports `--` rather than FAIL precisely so it does not "train the reader to ignore doctor output". Housekeeping, not a defect. | `web/vite.config.ts` | raise the limit with the sentence saying why, or split the bundle |
| 33 | The dev server proxies to a port nothing documents. `vite.config.ts` sends `/api` and `/ws` to `127.0.0.1:7788`, and 7788 appears nowhere else in the tree — not in AGENTS.md, the README or `docs/`. So `npm run dev` requires the panel to have been started with `--addr :7788`, which a contributor has to learn by reading the vite config; without it every request 502s and nothing on screen points at a port. AGENTS.md is the file whose job this is — it carries the red lines, the conventions, the layout and the table of checks — and has no dev-server section at all. | `AGENTS.md`, `web/vite.config.ts` | a line saying how to run the two halves together |
| 34 | The README's "Try it" never opens the panel. It builds, runs `doctor`, adds a project and lists sessions — three CLI commands and then it stops, without `./vibepanel` (serve is the default with no command), without a URL, and without mentioning that the first run prints a one-time setup token to the console. The Install section does cover the systemd path end to end, token included; the gap is the local-build path, which is the one a reader trying the product takes. A web console whose quickstart never opens the console. Sits with 33 — same class, different audience.

The same file stops early at the other end too: it never mentions that a runbook exists. `docs/runbook.md` is fourteen sections of what to check when a running deployment misbehaves, it is not in the release archive, and the shipped README does not point at it — only AGENTS.md does, and that is not shipped either. Nothing dangles, so this is discoverability rather than a broken link: the project's best operational asset is invisible to the people it was written for. | `README.md` | the two lines that start it and say where to look for the token; a third pointing at the runbook |
| 35 | Nothing says how to remove it, and removing it wrongly is easy in two ways the design makes non-obvious. `install.sh` has no uninstall path and no document mentions one. Stopping the unit leaves the tmux server running with every agent in it — that is the property the project is built on, and `enable-linger`, which the README tells you to turn on, means logging out does not collect it either; the panel is gone, so nothing shows you they are still there. Then deleting the data directory removes the reporter script while `~/.claude/settings.json` still points at it, so the user's own agent config is left referencing a path that no longer exists. The runbook has the kill command already; what is missing is the section that connects "I am removing this" to it. | `deploy/install.sh`, `docs/runbook.md` | four lines: disable the unit, kill the server, uninstall the hooks *before* deleting the data dir, then delete it |
| 36 | **Fixed: it restarts when the unit is already active, and says which happened.** Upgrading with `install.sh` does not take effect, and the script says it did. The env file is protected — there is a guard and a "kept … left alone" line — but the last step is `systemctl --user enable --now vibepanel`, which is a no-op on a unit that is already enabled and active. So the new binary is on disk and the old one keeps serving, while the script prints "started. the one-time setup token is in: journalctl …" — a start that did not happen and a token consumed at first install. `restart` is the whole fix, and this is the one project where that is free: sessions survive it by design, which is what restart-check exists to prove. release-check does run the installer twice, and asserts the one upgrade property it can see there: an edited `VIBEPANEL_DOMAIN` survives a reinstall. The restart is structurally unobservable in that harness — the throwaway HOME has no systemd user session, so the installer takes its "no user systemd session here" branch and never reaches the line in question. Not sloppiness, then; and worth knowing that adding the restart will not be covered by any check that exists. | `deploy/install.sh` | `restart` rather than `enable --now` when the unit is already active, and say which happened |
| 39 | `escapeLength` treats four sequence families as two bytes, and the fall-through lands the wrong way. It handles CSI, OSC and the character-set forms; everything else returns 2. DCS (`ESC P`), APC (`ESC _`), PM (`ESC ^`) and SOS (`ESC X`) are unbounded and end at ST, so their payload — ASCII, by convention — is scanned as content, and `hasPrintable` says the chunk was visible. A pure control burst then moves `lastOutput`, which clears a manual state and eats into the hook grace window. That is the same failure the tmux re-initialisation case in `osc_test.go` was written for, for a different introducer. **Reachability is not settled by reading this repository:** `allow-passthrough` is on deliberately, but on that path tmux unwraps `ESC Ptmux;…` and hands the client the inner sequence, which is usually an OSC and already handled. Whether tmux forwards any other DCS to its client is a question about tmux. The structural gap stands either way, and the fix is cheap — treat the four like OSC, running to ST. | `internal/session/osc.go` | four more cases, and a test case per family beside the tmux one |
| 40 | **Fixed: `-wal` and `-shm` are counted. Its pairing with 5 is stale — that finding was measured and retracted.** The settings page's database size omits the WAL. It is `os.Stat(DBPath())` — the main file only — while the panel runs in `journal_mode=WAL`, so recent writes live in `vibepanel.db-wal` until a checkpoint, and a checkpoint can be held off by a long-lived read, which this panel has: four pooled connections and a poller reading every two seconds. So the figure shown can be well under what is actually on disk, at the moment it is being read to answer "why is this growing". The runbook's own instruction, `du -sh ~/.local/share/vibepanel`, is right and covers it, and `doctor` measures free space rather than this, so the incomplete number is only on the settings page. It pairs with 5, which is what fills the WAL in the first place: two unconditional row updates per live session per two-second tick, about 1,440 a minute at two dozen sessions, each dirtying a page, with the WAL growing to roughly 4 MB before an automatic checkpoint folds it back. So the panel's most frequent write is precisely the one the growth figure cannot see, and an operator comparing the settings page against `du` finds numbers that disagree for a reason neither screen explains. Low severity, three more `os.Stat` calls. | `internal/httpapi/settings.go` | add `-wal` and `-shm`, or say it is the main file |
| 42 | **Fixed: `doctor` detects it and the runbook names all three causes.** Turning TLS on kills hooks in every session that already exists, and the README tells you to do it. `LoopbackURL()` builds the scheme from `TLSMode` and the port from `Port()`, and that string is baked into each session's environment at creation, where it cannot be changed. The documented sequence is: install with the shipped default of `--tls off`, work, then "edit it before exposing the panel" and restart — at which point the sessions survive by design, still holding `http://127.0.0.1:8443`, and post plaintext at a listener that now speaks only TLS. Changing the port does the same. The symptom is the one already documented twice, and it is silent because the reporter suppresses its own errors. The comment directly above `LoopbackURL` describes that symptom for a different cause — binding to one interface — so the failure mode was known and this second route into it sits in the value that function returns. Same class as 41, far likelier: it is the setup flow. Detectable, too — `tmux show-environment` can be compared against the current config. | `docs/runbook.md`, `internal/httpapi/api.go` | name it in the runbook; a startup check comparing each session's injected URL against the live one would turn it into a line of output |
| 43 | **Fixed: the server records what it started with, and `doctor` compares. All three branches driven.** A changed `vibepanel.conf` never reaches a running tmux server. `EnsureServer` writes the embedded config to disk every time, so the file is always current, and then returns early if the server is up — `-f` is only read at `start-server`. The panel never kills that server, which is the premise of the project, so a config change takes effect at the next reboot or not at all. That covers `allow-passthrough`, the reason tmux 3.3 is required, and the `smcup@:rmcup@` and `indn@` overrides added during this session. It compounds with 36: an upgrade leaves a new binary that is not running and a new tmux config that is not loaded, and both look installed. Detectable — stamp a hash of the config into a tmux user option at `start-server` and compare it on the next start. | `internal/tmux/tmux.go`, `docs/runbook.md` | say it in the runbook beside the kill-server command that is already there, and consider the stamp |
| 41 | **Fixed: `doctor` compares each session's token against the stored one, both branches driven.** A restored database silently kills hooks in every session that survived it. `hook_token` is created on first use and never rotated, which is right; it is injected into each tmux session's environment at creation, and a running session's environment cannot be changed. So if the stored value ever goes away — a restore from a backup taken before it was created, which the runbook's "database will not open" section tells operators to do, or the settings row being cleared — a new token is generated while the sessions, which outlive the database by design, keep presenting the old one. Every report is then rejected, permanently for those sessions, and silently, because the reporter script suppresses its own failures. It is a third cause for the symptom the runbook already documents twice, and that section does not name it. | `docs/runbook.md` | add it beside the bind-address cause, with `vibepanel session ls` and "recreate them" as the remedy |
| 44 | **Fixed: the reverse direction is pinned, both ways mutated.** The settings page shows a snippet built separately from what installing writes. `ClaudeSettings` composes four hardcoded `entry(…)` calls for display; `InstallClaude` ignores it and iterates the `events` map through `mergeEvent`. Two producers of one mapping, and nothing compares them — `TestInstallIsIdempotent` asserts four events, but from `events` on both sides. A disagreement means the page shows one thing and the button writes another, against the promise that you see what will be merged before agreeing to it. Half-covered already by the uncommitted `states_test.go` (see 3), which asserts every event and state in `events` appears in the snippet; the reverse — a snippet entry the map does not know — is still unpinned. | `internal/hooks/hooks.go` | run 3's test, then add the other direction |
| 13 | **Fixed: both lines deleted.** `panels.go` ends with `var _ = func() []store.Todo { return emptyIfNil[store.Todo](nil) }` under a comment that calls it `emptyTodos` and says it "keeps the compiler honest". It is neither: the variable is `_`, and `emptyIfNil` has four real call sites in this package. Two dead lines under a comment that misnames its own variable and misstates its purpose. | `internal/httpapi/panels.go` | delete both lines |
| — | red line 3's third mirror, `doctor`'s eleven lines as a table, the wire inventory, this handoff | `AGENTS.md`, `docs/*` | reading is the whole of it |

The order that matters:

1. `make check`. It compiles the four Go files and lints the one JS file, and it
   runs the new test for the first time.
2. If `states_test.go` passes, change the value of `session.StateWaiting` and
   confirm it fails. A test that has not been seen to fail is a decoration, and
   this one was written without the machine that could have shown it failing.
3. The two gaps in `detect.go` are the substantive findings. Neither should be
   changed on the strength of the comment: reproduce a pane that reports through
   a hook and then animates while it waits, which is what the measurement beside
   the bell rule was taken against.

What was checked and found sound, so nobody repeats it:

- **tmux `-F` parsing.** The separator is `\x1f` and the three free-text fields
  are scrubbed of `[\x01-\x1f]`, which includes it, so a pane title or a path
  cannot shift the fields.
- **The *done* rule** against the README's definition of it, and the binary
  frame mirror in both directions, read and write.
- **The replay path, end to end.** The server sends no replay frame for an
  empty ring, so the client's armed-but-not-acted-on reset cannot blank a live
  terminal; `onReset` fires on the *second* `subscribed` confirmation, which is
  what a reconnect produces, so the buffer replaces rather than appends; a
  dropped viewer resubscribes instead of freezing.
- **The state broadcast.** A connection is registered with the hub before its
  snapshot is built, `queueState` coalesces to the newest payload rather than
  dropping it, and every writer goes through one `writeMu` — so a slow viewer
  skips intermediate states instead of keeping a stale one, and the two
  goroutines that write state cannot interleave.
- **Uploads.** Size cap, streamed parts rather than `ParseMultipartForm`,
  basename-only filenames with `.`/`..`/separator rejected, `O_EXCL` so a
  symlink at the target cannot help, and shlex-equivalent quoting of the path
  typed at the prompt. The one gap is the newline, recorded above.
- **OSC.** Payloads over 64 KiB are dropped rather than buffered, the byte
  stream reaches the browser untouched, and OSC 52's `?` read request — which
  would let a pane exfiltrate the clipboard — is explicitly refused.
- **Every union crossing the Go/TS boundary was checked for an exhaustive
  consumer**, which is what 37 and 38 came out of. The two that matter are
  recorded there. `projectOrder` is consumed as `=== 'manual' && …`, and a
  third value would behave like `auto` — safe, because the semantics are binary
  and hiding the control is the harmless default. `titleSource` and
  `stateSource` are read by nothing at all. `SocketStatus` ends in a ternary
  that falls through to `closed`, the same shape as 38, but it never leaves
  `socket.ts`: whoever adds a status is editing the file its consumer lives in.

  The generalisation is worth stating, because it is counter-intuitive. A
  pinned mirror makes a non-exhaustive consumer **more** dangerous, not less.
  Red line 3 tells you what to change when the enum changes, a test enforces
  the list, and the list is where you stop — so the consumer nobody wrote down
  keeps compiling and answers wrongly. An internal union has no such list and
  no false assurance, which is why the risk sits with the enums that look best
  guarded.
- **The `Source` enum needs no equivalent of 38, and the contrast says why.**
  Its consumers are non-exhaustive too, and every one of them falls the safe
  way: `Precedence` ends in an explicit `return 0`, so an unknown source ranks
  lowest and loses every conflict; `Restore`'s switch ignores what it does not
  recognise, which is right, since restoring a state whose provenance you
  cannot read is worse than not restoring it; and `stateIsGuessed` tests
  `== SourceHook`, so anything new counts as guessing. `SessionState`'s two
  fall through to a *particular wrong answer* — working, and a green check.
  Non-exhaustive is not the defect on its own; where the fall-through lands is.
- **The reverse direction of the same drift is clean.** A testid a harness
  names but the source no longer defines is the half that can disable a check,
  so all 87 references in `web/scripts` were resolved against the 83 literals in
  `web/src`. Four families matched nothing as literals and each turned out to be
  built at render time: `key-${label}` in `MobileKeyBar`, `panel-tab-${id}` in
  `RightPanel`, and the `setup ? 'setup-form' : 'login-form'` ternary in
  `AuthGate`. `key-1` and `key-~` were the two that looked strongest — neither
  appears in a `label="…"` prop — and both come from the two `.map()` rows,
  `['1','2','3']` and `['/','-','|','~']`. Nothing dangles. Worth knowing that a
  rename inside those four fails as a 30-second Playwright timeout rather than a
  readable message, but it does fail: none of them is a silent-skip risk.
- **The unreferenced-testid heuristic is closed out.** Two audits used it, and
  the standard it earned is stated in the log: an unreferenced testid "is a
  hint, not a finding". The first pass found one real gap in six — `restart-current`
  looked like an untested affordance and turned out to be covered by the glyph
  geometry check that enforces red line 4, just not through that name. The
  second pass, over the three testids added this session, found two: `file-escapes`,
  closed here by the symlink check in render-check, and `stale-notice`, which is
  45. The four still unreferenced are each fine for a reason worth not
  re-walking: `file-tree` is a container, `file-truncated` has the Go test for
  `Truncated` plus the wire mirror and only a two-line conditional left over,
  `drop-note` is a transient status message beside assertions that check the
  file actually landed, and `passkey-note` renders only when passkeys are
  unavailable, which the check's own TLS configuration rules out.
- **`doctor` is tested in both directions, with a real fault injected.**
  release-check runs it on a healthy machine and asserts exit 0, then
  `chmod 555`s the data directory and asserts four things about the broken run:
  a non-zero exit, that the output names the unusable directory, that it says
  which checks were skipped, and that it still reaches the later ones — the
  last being the property whose absence once meant "a machine with three
  problems took three runs to find them". The fault injection is the part worth
  noting: it works because doctor opens the directory fresh, unlike an earlier
  probe in this session that chmod'd a database already held open and proved
  nothing.
- **`SessionEnv` injects four values and two of them can go stale**, which is
  the whole of that class: the session id follows the row that owns it, and the
  project id cannot change because nothing offers to move a session between
  projects. The URL is 42 and the token is 41.

  Both are unrepairable for a session that already exists, which bounds what a
  fix can be. `tmux set-environment` rewrites the *session* environment and
  reaches only panes spawned afterwards; the reporter runs as a child of the
  agent, whose environment was fixed when its pane was created. So nobody
  should go looking for a way to update a running session — detection and a
  clear message is the whole available remedy, and the sessions have to be
  recreated.
- **The OSC 9 and OSC 777 handlers are dead, deliberately, and it raises the
  stakes of 1.** Both are implemented and correct, and the comment on them says
  nothing reaches either: tmux does not forward desktop-notification sequences
  to its client under this configuration and does not convert them into a bell
  or an activity flag — measured, and pinned by
  `TestTmuxSwallowsDesktopNotificationSequences`. The code is kept so that a
  tmux which starts forwarding them "would otherwise arrive with the bug below
  already in place", which is the right call. What it means for 1 is that when
  a hook report is discarded by an animating TUI, the heuristic it falls back
  to has exactly one attention signal, the terminal bell — so an agent that
  did the polite thing and sent a notification instead of ringing has, at that
  moment, no way at all to say a human is needed.
- **tmux's latched bell flag cannot re-fire.** It stays set until a client
  views the window, so reading it on every tick would keep pushing `lastBell`
  forward and pin a session at *waiting* for good. It is read once, in
  `Reconcile` at startup, and the ongoing signal comes from the PTY byte
  stream instead. The ordering is load-bearing and easy to miss: the read has
  to happen *before* the attach, because attaching is what clears the flag —
  "otherwise restarting the panel loses every 'this needs you' raised while it
  was gone, exactly when the user was not watching." Swap those two lines and
  the recovery silently returns nothing.
- **The connection dot cannot go on lying**, which took four checks because
  each layer only covers one failure. The browser's `offline` event catches
  local loss — a phone leaving coverage, a lid closing — and the comment on it
  names the stakes: without it "the panel goes on claiming to be live while the
  list of who needs you quietly stops updating. That is the worst way for this
  particular application to fail, because nothing looks wrong." That event says
  nothing about a server that died or a middlebox that dropped the connection
  silently, and those are caught by a watchdog instead: any frame refreshes
  `lastSeenAt`, and the ping timer declares death past `DEAD_AFTER_MS`. The
  tuning is the part worth checking and it is right — 60s against a server that
  pings every 30, so two consecutive misses, and a healthy idle connection
  cannot false-positive. `onclose` and the backoff handle the ordinary case.
- **Some updates check rows-affected and some do not, and the split is the
  right one.** `exec1` returns `ErrNotFound` when nothing was updated, and it
  is used for what a person asked for — rename, pin, reorder, delete, and
  `RecordSize` — so renaming a session that no longer exists answers 404. The
  poller's writes go through plain `Exec` instead: `UpdateSessionRuntime`, and
  `SetSessionTitle`'s automatic branch, which carries its own
  `AND title_source = 'auto'`. So a session deleted between the poller reading
  the rows and writing them back is a silent no-op rather than an error, which
  matters more than it looks: `pollOnce` returning an error calls `noteStale`,
  and the banner it raises says the panel has stopped recording what the
  sessions are doing. A routine race must not be able to say that, because a
  banner that cries once is one nobody reads the second time. `RecordSize` is
  the one `exec1` on a background path, and its caller logs at Warn and carries
  on.
- **`hasPrintable`'s test is the shape 30 and 31 should copy.** Whoever fixes
  those has an example in the same tree. Its cases are chosen so that a wrong
  implementation cannot pass: `"\x1b]0;a title\x07"` is false and
  `"\x1b]0;a title\x07real output"` is true, which is the whole "an OSC
  payload's ASCII is not content" property in two lines; cursor moves alone are
  false; and one case is the literal sequence tmux emits five seconds after a
  client attaches, carrying its own history — "the terminal being reconfigured,
  not the session doing anything, and counting it as output reset every
  session's state." Inputs that can tell the right answer from the wrong one,
  which is what `strings.Repeat("A", …)` and a plain-ASCII session name cannot.
- **The browser check's visibility guards do not hide assertions**, checked
  because a guard with no else is a worse version of 22 — coverage that
  disappears without even a warning. All eight `isVisible().catch(() => false)`
  guards are one of two shapes: a permissive setup step, where the button not
  being there means the panel is already open, followed by a strict assertion
  with its own FAIL; or an inverse assertion, where the true branch *is* the
  failure — "a viewer the same size as the grid owner is still offered take
  control". Setup permissive, assertions strict, which is the right way round.
  The two paths where coverage does vanish are 22 and 28.
- **The copy bar reads two selections on purpose.** xterm's own selection and
  the browser's `window.getSelection()` are both consulted, terminal first —
  "on a phone the terminal is what fills the screen, so a stale DOM selection
  behind it must not shadow the thing the user just dragged over." Either alone
  is wrong: xterm's DOM renderer paints its selection with overlays rather than
  a native range, and text outside the terminal has no xterm selection at all.
  Worth knowing before anyone simplifies it to one source.
- **A touch selection survives output arriving under it.** The obvious way to
  build this anchors the selection where the finger was, so an agent printing
  while the bubble is up leaves you copying whatever scrolled into that patch
  of screen. `touchSelect` converts before handing over —
  `term.select(run.col, run.row + term.buffer.active.viewportY, run.length)` —
  so the anchor is a buffer row and xterm keeps the highlight on the text. One
  addition, and it is the one that makes the feature work on the platform it
  exists for.
- **The Manager needs no `Retain`, and the asymmetry with the Detector is the
  reason.** A detector tracker is passive data with nothing behind it to notice
  its own end, so only a sweep against the authoritative list can clean it up —
  which is what `Retain` is for, and its comment says why `Forget` at each
  removal site cannot be enough. Every `Live` has a goroutine instead, and the
  pump's deferred cleanup removes itself from `m.live` under an identity check
  (`m.live[l.ID] == l`, so a late exit cannot evict the attachment that
  replaced it) *before* closing, because the other order "leaves a window where
  a caller woken by Done() still sees it listed as live — which is exactly long
  enough for a reconnect to attach to a corpse."
- **A note edited in two windows loses nothing.** The `rev` column is
  optimistic concurrency end to end: the client tracks the revision it loaded,
  a 409 arrives as a typed `ConflictError`, and the handler adopts the server's
  revision and content for its own bookkeeping while leaving the textarea
  untouched — "keeping the local text on screen is the only safe move; it is
  the thing that has not been stored anywhere" — under a status line that says
  "This note changed in another window. Yours is still here, unsaved." A manual
  state override is likewise written to both places that hold it, the database
  and the detector, because "the detector is what the poller consults, so an
  override recorded only in the database is undone two seconds later."
- **There is no HTML injection sink in the frontend**, which bounds 17 and 24
  and is the first question anyone will ask about them. `dangerouslySetInnerHTML`
  and `innerHTML` appear nowhere in `web/src`; notes are plain text in a
  textarea rather than rendered markdown; terminal bytes go to xterm.js, which
  parses escape sequences without producing markup from them. So every finding
  about raw-rendered strings in this log is a *deception* problem — a name or a
  log line that reads as something it is not — and none of them can become an
  *execution* problem.
- **The setup flow fails closed at every branch**, which is worth having
  written down because it is the one place an error could hand the panel to a
  stranger. `CountUsers` failing answers 500 and returns rather than falling
  through; `n > 0` answers 409 and the endpoint is shut for good; the token is
  compared with `subtle.ConstantTimeCompare`; and `handleSetup` refuses
  outright when no token is set, which is the state `serve` leaves behind via
  `defer` once it returns. The token is `auth.NewToken()` — the session-token
  generator, `crypto/rand` through base64url — so the comment's admission that
  this path is "unauthenticated and unthrottled" costs nothing, and repeated
  refusals are audit-rate-limited by the same cooldown as the allowlist.
- **The status check that writes to disk is safe, and knows why.**
  `hooksAreInstalled` reaches `InstallScript` on the way to every state
  snapshot, so a GET rewrites a file — one that agents execute at moments
  nobody controls. It compares content first, so the write almost never
  happens; and when it does it writes beside the file and renames, because
  "`os.WriteFile` truncates before it writes. A shell reads a script
  incrementally, so overwriting one mid-execution can fail in ways that are
  almost impossible to attribute." Both halves of the hazard are named in the
  comment, in that order.

  The pattern is applied at the other site too, with its own reasoning:
  `writeSettings` writes beside `~/.claude/settings.json` and renames, "so a
  crash halfway through must not leave the user with a truncated settings file
  and an agent that will not start", keeps whatever mode the user's file
  already had rather than tightening it silently, and handles `os.WriteFile`
  applying a mode only when it creates the file. Those are the only two files
  the panel writes into the user's world — the Codex TOML is shown as a snippet
  to paste, never written, which is also why nothing repairs it (see 3).
- **All six harnesses fail on an uncaught browser exception**, checked one by
  one after the severity split above raised the question. restart-check reports
  its `pageErrors` through `if (len)` and the category `console` rather than a
  `for` loop and `js`, which a pattern built from render-check's spelling
  misses — the fifth time a narrow grep nearly produced a false finding here.

  The obvious next question, whether anything asserts the *server* logged no
  errors, has no answer to give: the whole codebase contains one `Log.Error`,
  for a certificate that has expired. Failures are returned to the caller,
  raised in the stale banner and `/api/health`, or logged at Warn and Debug.
  There is no server-side error channel to assert on, by design, so the
  asymmetry with the browser side is not a gap.
- **The browser check's severities are split where they should be.** A
  `console.error` is a WARN, which looks too lenient until you find the other
  two collectors: an uncaught exception arrives on `pageerror` and is a FAIL,
  and so is a failed request. So a crash fails the run and React's key and
  prop-type complaints do not, which is the right way round. `consoleWarnings`
  looks collected-and-never-read and is not — it is filtered for one specific
  message in the note-saving assertion. The eleven bare `catch(() => {})` are
  tab clicks whose failure a later assertion would catch, except the one
  swallowing a twenty-second wait for the bottom tabs, which feeds the skipped
  section recorded as 22.
- **The SQLite setup avoids all three of its classic traps.** The pragmas are
  in the DSN — `journal_mode(WAL)`, `busy_timeout(5000)`, `foreign_keys(ON)`,
  `synchronous(NORMAL)` — so they apply to every connection the pool opens,
  rather than to whichever one happened to run a `PRAGMA` statement. Each
  migration step is its own transaction with `PRAGMA user_version = N` moving
  *inside* it, so a crash cannot leave the schema and the version disagreeing.
  And every step is `ADD COLUMN` with a default, `CREATE TABLE IF NOT EXISTS`,
  or one `DROP TABLE` — no table rebuild, which is the case that would have
  needed foreign keys turned off for the duration.
- **"Flags win" is implemented the way it has to be.** `envOverlay` runs before
  the flags are declared, and each is declared as
  `fs.StringVar(&c.Field, name, c.Field, …)` — so an unset flag writes back the
  value the environment just supplied and changes nothing, while a given one
  overwrites. The two `[]string` settings cannot use that idiom and are gated
  on `fs.Visit`, which reports only the flags actually typed. Neither of the
  two classic ways to get this wrong is present.
- **`deploy/` is guarded where it needs to be.** The unit's load-bearing line
  is pinned by `TestUnitLeavesTheSessionsAlone`, which fails if `KillMode` is
  absent or anything but `process`, and says why a comment could not do the
  job: "anyone tidying the unit, or hardening it, would delete a bare
  `KillMode=process` without knowing it is the load-bearing one." release-check
  runs `systemd-analyze verify` on the installed copy.

  Nothing pins `deploy/vibepanel.env` against the config, which looked like the
  same gap one file over — it uses four legacy names that only work because
  `config.go` lists them as aliases, and losing one would drop a user's TLS
  settings to their defaults. It announces itself instead: every alias is
  entered into the `seen` map, so removing one makes the variable "set but
  never read" at startup and in `doctor`. A runtime warning doing what a test
  would is the difference between this and the `KillMode` line, which had
  nothing.
- **No client-supplied path reaches the filesystem without `browse.Resolve`**,
  swept the second way after the sanitising sweep turned out to have been
  scoped by variable name. Grepping for what actually touches the disk —
  `os.Stat`, `os.Open`, `os.OpenFile`, `filepath.Join` — rather than for
  path-shaped identifiers gives nine sites in `internal/httpapi`, and every one
  is accounted for: the project root validated at creation, `expandHome`,
  the download and upload targets from `Resolve`, config paths, and
  `isDirectory`, whose two callers pass either the stored project path or the
  parent session's cwd as tmux reported it. Never the request. The name-based
  sweep had reached the same answer, which is the reassuring case — its
  exclusions were hiding nothing.
- **The TypeScript frame codec has no unit test**, and that is proportionate.
  `encodeData`/`decodeData` are called only from `socket.ts`, by no test — but
  every keystroke in render-check and every byte in stress-check goes through
  them, so a breakage fails loudly; the constants and both ends of the byte
  order are pinned by `TestBinaryFrameLayoutMatchesTheClient` reading the
  TypeScript source; and the edge payloads the Go round-trip test enumerates
  are unreachable here, since the server skips an empty replay and nothing
  sends a zero-length write. Not the situation the Go half was in, where the
  comment was "nothing exercised it".
- **Every other pure-logic module in `web/src` has a test**, checked by
  listing them: label, keys, touchSelect, meter, text, and theme — whose test
  is at `src/theme.test.ts` rather than beside the module, which a
  co-location check reports as a gap and is not one. The real gap is
  `hooks/`, recorded above as 15.
- **The absent HSTS header**, which is correct and now says why: the policy is
  host-scoped and port-blind, so sending it from `:8443` would pin every port
  of that hostname to HTTPS in every visitor's browser.

### Why the sanitising sweep missed a site

The sweep that found the scratch-terminal strip and the project name searched for
`{p.name}`-shaped JSX with the variable spelled out:

	{\(s\|sess\|session\|e\|p\|proj\|project\|c\|cred\)\.\(title\|name\)}

`Settings.tsx` renders a passkey as `{k.name}`. `k` was not on the list, so two
sites — including a `window.confirm` — were invisible to a sweep whose stated
purpose was to find every one of them, and the fix that followed read as
complete.

A grep scoped by variable name finds only the sites you already imagined. The
second sweep dropped the allowlist and matched any single-word receiver, which
is what turned it up. Cheap to have done first.

### The container caveat is documented; the image is not built

Going looking for whether the compose deployment quietly voids the project's
stated most important property — "restarting the panel does not touch the
sessions" — turned up the opposite of a gap. `Dockerfile` opens with it, in
capitals:

	IN A CONTAINER, RESTARTING THE PANEL KILLS EVERY SESSION.

with the reason, the three commands that do it (`docker restart`, `compose up
-d` after a rebuild, anything that recreates the container), and the honest
conclusion: "Nothing can be done about that from inside the image; it is what a
container is. Run it this way only if the sessions are cheap to lose."

Recorded here so nobody investigates it twice.

What the search did turn up is 20 above: no target, script or check ever builds
that image, so the one shipped artifact outside the binary has exactly the
property `head-check` exists to remove.


## The third outcome, at the two endpoints that were still collapsing it

2 in the table above, fixed. `currentUser` answers "signed in", "not signed in",
or "the database cannot say", and the third exists because collapsing it into
the second once told every viewer on a full disk to sign in — into the same
broken database, again and again, until the login throttle locked them out of a
panel that was only short of space. `RequireAuth` was taught to answer 503 for
it at the time. `/api/auth/state` and `/api/auth/password` are registered
outside the authenticated group and do their own check, so neither was covered,
and both still wrote `_` where the error goes.

`/api/auth/state` is the worse of the two. It is what `App.tsx` asks four
seconds after the socket goes down, precisely to tell "the session ended" from
"the network went away", and its rule is `if (!state.authenticated)
onSignOut()`. An unreadable database also closes every open socket after one
revalidation tick, so this endpoint is asked at exactly the moment it is most
likely to fail. `/api/auth/password` answered 401, which the frontend's
`UnauthorizedError` guard turns into a return to the sign-in screen.

Both now answer 503 with the message `RequireAuth` uses, and both call
`noteStale`, so the storage banner gets the same evidence an ordinary request
would have given it. The client already does the right thing with that: its
`catch` treats a failed probe as "unreachable rather than refusing us" and
leaves the socket to reconnect, and `AuthGate` shows the message rather than a
sign-in form nobody can satisfy.

**The injection is the part worth keeping.** The obvious one is wrong. Closing
the database makes `CountUsers` at the top of `handleAuthState` fail too, so the
endpoint answers 500 — which the client already treats as unreachable, so the
bug never appears and a test built that way passes with or without the fix. It
would have been a decoration. The fault that matters is the split one the
finding actually named: CountUsers succeeds and the session lookup does not.
Dropping `auth_sessions` from a second connection to the same file produces
exactly that and leaves `users` intact.

Measured both ways, which is the only reason to believe any of it.
`TestAStorageFaultDoesNotSignAnybodyOut` passes against the fix; against HEAD it
fails four times, and the first line is the one that matters:

    /api/auth/state answered 200 authenticated:false while the database could
    not say — the client signs the user out on that, into a login form that
    reads the same database

A 200 is what makes it dangerous. Every other answer, including the 500 the
naive injection produces, lands in the client's catch and does nothing.


## The spinner that outranked the agent

1 in the table above, fixed, and it was the most expensive thing in it: the
panel's precise source being overridden by its guess, in exactly the case the
precise source exists for.

`Advanced` was introduced to separate a screen that moved forward from one
redrawn where it stood — the measurement is a spinner sending 480 bytes in three
seconds with no line feed among them, against an agent producing output sending
430 bytes with twenty-two. Only the bell rule was changed to read it. The manual
rule and the hook rule both still asked `lastOutput`, and `lastAdvance`'s own
field comment named the bell rule as its only reader, which is the shape of the
omission: the field knew who used it.

So `hookGrace` was three seconds and a spinner emits 480 bytes in three. A hook
that said "waiting for you" was discarded, the fall-through read the foreground
process, and the dot said working — with a circle instead of a triangle, sorted
below the sessions that were merely busy, on a phone, at 2am. The manual rule
failed the same way and worse in feel: somebody clicks precisely when the
automatic state is wrong, the automatic state is most often wrong while a TUI is
animating, so the next chunk arrived in milliseconds and the click read as
having done nothing.

Both now read `lastAdvance`. Reproduced first, in the detector's own units
rather than by running an agent — twenty seconds of `\r|` at 200ms intervals
after a report, and after an override:

    state = "working" from "heuristic" after twenty seconds of animation,
    want "waiting" from "hook"; the agent said it was waiting and a spinner
    overruled it

**Why this is not a trade of one wrong state for another.** `Advanced` is
`chunk contains "\n"`, so an agent that resumed work inside a full-screen TUI
would never advance and its report would stand forever. It cannot get stuck,
because `hookState` is non-empty only when the hooks are installed, and an agent
with hooks installed reports its other transitions too — UserPromptSubmit and
PreToolUse arrive the moment it starts again. The grace stops being a timer and
becomes "until the agent says otherwise, or the screen actually moves", which is
what `hookGrace`'s own comment always said it was for: "the prompt itself, not
the agent resuming work". The manual rule has no such backstop, so stickiness is
a real cost there and only a phrasing here, and the comment now says so.

**Two existing tests had to be amended, which is the move that hides
regressions, so it is worth being explicit.** `TestOutputWellAfterAHookWins` and
`TestManualOverrideSticksUntilActivity` both drove non-advancing output, so
under the new rule they asserted the opposite of the new tests. Setting
`Advanced: true` in both fixtures preserves what each was written to say — an
agent that was told to carry on is working again — and the pair now expresses
the rule rather than one half of it.

Amending them was checked rather than asserted. With the grace turned into a
permanent lock, and with the manual rule turned into one that never clears, each
amended test still fails:

    detect_test.go:207: state = "done" from "hook", want "working" from "heuristic"
    detect_test.go:228: manual override survived new output: "done" from "manual"

So the amendment narrowed them to the advancing case; it did not remove their
teeth. A test changed to accommodate a change, and then not mutated, is how a
suite quietly stops testing anything.


## A drift that ran three hops

37 in the table above, fixed at both ends and pinned at all three hops.

The client's `handleControl` had a case for eight of the ten members of
`ServerMessage['t']`. `pong` needs none — any frame refreshes `lastSeenAt` —
but `error` was simply absent, with no case and no default, so every one of the
six senders was dropped on the floor. Three of those six are `write failed`
twice and `paste failed`: you type into a terminal, the write fails
server-side, the server says so, and nothing reaches the screen. It looks
exactly like a network problem, which is the failure `TestMessageTypesMatchTheClient`
was written about — "the server says 'dropped', the client hears nothing, and
the terminal sits frozen looking like a network problem" — fixed for `dropped`
and not for `error`.

That test could not have caught it. It compares the server's sends against the
*declared union*, and `error` is in the union. Nothing pinned the union against
the switch, which is the third hop.

All three are pinned now, each by the cheapest thing that can hold it:

- **Server sends against the union** — the existing Go test, unchanged.
- **Union against the client switch** — the compiler. The switch is exhaustive
  with a `never` default, so a new member without a case stops the build.
  Measured by adding `'nudge'` to the union: `src/protocol/socket.ts(337,15):
  error TS2322: Type '"nudge"' is not assignable to type 'never'`.
- **The case actually delivering** — `socket.test.ts`, three cases, all three
  failing with the `error` case removed. The compiler cannot say whether a case
  does anything, only that it exists.

The mirror direction had the same shape and no exhaustiveness check Go could
offer: every `ClientMessage` type had a case and there was no `default`, so a
stale or misspelled type from a client was indistinguishable from one being
handled. It answers with an error frame now, which is only worth sending
because the other end finally shows them — fixing it in the other order would
have been another silent drop. The type is truncated to 40 characters on the
way back: it arrives from the client, is unbounded, and is rendered. React
escapes it, so the hazard is length rather than markup.

That test's first version failed by hanging until its own ten-second deadline
and reporting `context deadline exceeded`, which says nothing. It now uses a
three-second per-read deadline and fails as a sentence:

    nothing answered an unknown message type within three seconds: the server
    ignored it silently, which is indistinguishable to a client from having
    handled it

**KNOWN GAP, and worth stating rather than leaving to be found.** The banner
itself — `socket-error` in `App.tsx` — is not driven by any browser check, and
not for want of trying. There is no deterministic way to make the *app's own*
socket receive an error frame from the harness. `Live.Write` is a PTY write, so
a dead pane does not fail it; the attach has to go away, which means killing
the tmux session behind the panel's back and typing before the poller notices.
Every other sender is reached the same racy way. A flaky FAIL in `render-check`
would be worse than this paragraph. What is covered is that the frame reaches
the listeners; what is not is that App draws it.


## 38 was right about the bug and wrong about why, which the measurement caught

The row says a fourth session state "would render as *working*, silently",
because `renderGlyph` was an if-chain ending in an unconditional return of the
breathing circle, exhaustive only because TypeScript had narrowed the union to
`'working'` by then.

The first half of that is true and the word *silently* is not. Adding a member
to `SessionState` stops the build today, four lines above the chain:

    src/components/StateDot.tsx(4,7): error TS2741: Property 'blocked' is
    missing in type '{ waiting: string; working: string; done: string; }' but
    required in type 'Record<SessionState, string>'

`LABEL` is a `Record<SessionState, string>`, and it has been there all along.
Reading the function without reading the file above it produced a finding about
an unguarded fall-through that was in fact guarded — by something else, several
lines away, that the reading never reached.

**What survives is worse than a wrong finding and better than the one written.**
The guard exists, but it asks for the wrong thing. It says a *label* is
missing, adding the label is the obvious way to satisfy it, and then — measured,
with both the union member and the label added to the tree as it stood —
the build is clean and the new state renders as the breathing working circle.
So the compiler stops you one step away from the thing that actually breaks and
tells you the wrong thing is missing. A developer who does exactly what the
error asks ships a state wearing another state's shape, which is precisely what
red line 4 exists to prevent, not a smaller version of it.

`renderGlyph` now ends in a call whose parameter is `never`, so the same
experiment fails where it should:

    src/components/StateDot.tsx(162,23): error TS2345: Argument of type
    '"blocked"' is not assignable to parameter of type 'never'

and it cannot be satisfied by adding a label.

The fall-through draws a hollow dashed ring rather than nothing, because these
values can also arrive at runtime — a row written by a newer build, or an older
one — and a missing glyph is a hole in the sidebar that reads as nothing at all.
It is deliberately in the vocabulary of none of the three.

**The lesson is the one this log keeps relearning.** The earlier version of it
was "a grep for a concrete name cannot see an interaction that goes through an
abstraction". This is the same shape with a smaller radius: a function read on
its own cannot see a constraint declared four lines above it. Both findings that
turned out to be wrong this session were wrong that way, and both were caught by
running the experiment rather than by reading more carefully — which is the
argument for running things, not for reading harder.

## 5 was a plausible finding about a database that does not behave that way

The row says the poller rewrites `cwd` and `command` for every live session on
every tick whether they changed or not, "about twenty-four writes a second at
idle", and that the fix is four tokens and provably a no-op. The reasoning was:
`UPDATE sessions SET cwd = ?, command = ?` has no value comparison, and "SQLite
does not skip a write because the values match — the row is rewritten and
appended to the WAL either way".

That last sentence is the whole finding, and it is false.

    1000 identical UpdateSessionRuntime calls: data_version 4 -> 4 (+0 commits),
      10.147ms total, 10.147µs each
    WAL: 1000 identical writes 0 -> 0 (+0), then 1000 changing writes
      0 -> 4120032 (+4120032)

Two instruments, chosen because the first one was useless. Measuring the WAL
naively read `0 -> 0` after a thousand writes and looked like a result; it was
auto-checkpointing truncating the file underneath the comparison, and it would
have read `0 -> 0` for a thousand *changing* writes too. The fix was a positive
control — one write that really changes the values, asserted to move the
instrument before anything is concluded from it not moving. `data_version` moved
2 → 3 for that one and not at all for the thousand. With auto-checkpointing
switched off the WAL agrees, and the changing writes cost a page each.

So SQLite elides an update that changes nothing, and what is left is the
statement: 10µs, twenty-four times a second, a quarter of a millisecond per
second of one core. The guard is deliberately not added, and the comment in
`api.go` now says so with the numbers, because a future reader will otherwise
find the same four tokens and the same argument.

The inconsistency that made the finding look strong — two of the four writes in
that loop compare first and two do not — reads the other way round once the
measurement is in: those two need the comparison for their own reasons, and it
is not a convention these lines break.

`TestAnUpdateThatChangesNothingDoesNotWrite` keeps the premise honest. It pins a
property of SQLite rather than of this project, which is normally not worth a
test, and is worth one here for exactly one reason: a comment in another package
now reasons from it. If it ever stops being true, the guard is worth adding, and
that test is the only thing that would say so.

**Second finding this session that was wrong in the same direction.** 38 read a
function without the four lines above it; this one reasoned about a dependency
without running it. Both were caught by executing rather than by reading more
carefully. Two of forty-five, which is a rate worth knowing when reading the
rest of that table: it was written in a stretch where nothing could be run.


## 6 was a real bug wearing the wrong character's name

The row says an uploaded filename containing a newline is typed into the shell,
that the line editor reads the 0x0A as Enter, and that the user is left at a
`>` they cannot explain. The mechanism is right and the character is wrong, and
the character is the whole of whether it can happen.

Three measurements, in the order they were taken.

A browser check was written to drive it: drop a file named `two\nlines.txt`,
then assert the shell still runs the command after it. It failed, and the
failure text was the finding's obituary — the path at the prompt read
`two%0Alines.txt`, and the file on disk is literally named that. The HTML spec
has multipart percent-encode LF, CR and the double quote in a filename, so a
newline never leaves the page.

Then the other control characters, asked of the server directly:

    sent "ZULU\x15KILO.txt" -> 400 malformed MIME header line
    sent "esc\x1bx.txt"     -> 400 malformed MIME header line
    sent "tab\tx.txt"       -> 200 OK, landed as "tab\tx.txt"

Go's `textproto` refuses a Content-Disposition line carrying a control
character, and makes an exception for tab, which is ordinary header whitespace.

So exactly one character travels the whole way, and at a prompt readline reads
it as "complete this". The browser check now drops `ZULU\tKILO.txt`, and with
the quoting removed it fails — not with a tab visible in the path, but with
`KILO.txt` never appearing at the prompt at all. Completion ate the second half
of the name. The user is invited to press enter on a path that is missing its
end, which is worse than the `>` the finding described, because a `>` at least
looks wrong.

`shellQuote` escapes the whole control class rather than tab, because the set
that can arrive is decided by two parsers this project does not own, and both
have already surprised it once.

**Three findings in this table were now wrong in the same direction** — 38
reasoned about a function without the four lines above it, 5 about SQLite
without running it, and this one about a browser and a MIME parser without
asking either. All three were real problems; all three named the wrong cause,
and the fix that follows from a wrong cause is a different fix. They were
written in a stretch where nothing could be run, which is the common factor and
the thing to weigh when reading the rest of the table.

**Two smaller notes from the same afternoon.** The first attempt at the browser
assertion compared against `ZULU\x09KILO.txt` and failed on a passing tree: the
terminal wraps, and `innerText` reports a space at the wrap, which split `\x09`
down the middle. It strips whitespace now, which cannot hide the failure it
looks for, because a raw tab is whitespace too and an unquoted name collapses
to `ZULUKILO.txt`. And the first two attempts at writing all of this were
refused outright for containing literal control characters in the command —
the same class of byte, in the tooling, while fixing it in the product.


## 7 is real, and the fix it wanted does not exist

The row says the agent match is somebody else's packaging, and that if Claude
Code reports `node` the "states are guessed" notice never appears. The fix
column says one command settles it. It does, and the answer is yes:

    probe1: node      (a script with a #!/usr/bin/env node line)
    probe2: sleep     (a /bin/sh wrapper that execs)

Measured on a throwaway socket rather than by running an agent. Both agents on
this machine are native ELF binaries, so they report `claude` and `codex` here
and the bug is invisible from this desk -- which is why it was worth measuring
the mechanism instead of the machine.

**The structural fix turns out not to be available, and finding that out is the
useful part.** The obvious repair is to match what the session was *asked* to
run rather than what it happens to be running, since the launch command is not
somebody else's packaging. But the panel never launches an agent. Both places
the frontend creates a session pass an empty command:

    api.createSession(project.id, [])
    api.createSession(current.projectId, [], { parentSessionId: current.id })

Every session starts as a shell and the user types `claude` into it. There is no
launch command to store, and the poller overwrites the row's `Command` with
`#{pane_current_command}` on every tick regardless. The live process name is all
there is.

Widening the list is not the answer either, and the existing comment already
says why: `node` would fire on every node process, and "any non-shell process"
fires on htop and on a build, which makes it a notice people stop reading.

So what was actually wrong was not the match but that a mismatch was
undiagnosable. `doctor` now prints what tmux reports for every live pane, with
both branches driven:

    [--  ] agents             none recognised; tmux reports: node
           1 session(s) running something that is not a shell...
    [ok  ] agents             1 recognised; tmux reports: claude node

`agentCommands` moved to `session.IsAgentCommand` so that doctor and the state
snapshot cannot disagree about what an agent is -- the third place a definition
would have been copied.

`TestAScriptIsReportedByItsInterpreterNotItsOwnName` pins the premise against a
real tmux, portably: a `#!/bin/sh` script reports `sh`, not its own name. It
uses a shell builtin on purpose, because a script that runs `sleep` puts sleep
in the foreground process group and tmux reports that instead -- the same lesson
by a route that would have made a confusing fixture.


## Three about the checks themselves, and one of them caught in the act

**28, and it is the one that mattered most.** `make check` is the gate people
run before committing, and without tmux it passed having run almost none of the
Go code: four helpers call `t.Skip("tmux not installed")` and each guards a
whole suite, `go test` without `-v` does not summarise skips, so every one of
those packages prints `ok`. Nothing in the Makefile mentioned tmux as a
prerequisite. `check` now ends -- last, so it is the final thing on screen
rather than something that scrolled past -- with a notice saying what was not
tested. Both branches driven by hand, the absent one by putting a PATH with no
tmux in it in front of make.

**22.** `make verify` ended in `@echo "all checks passed"`, printed over any
number of warnings, twenty minutes after they scrolled past in the middle of
eight checks' output. It is `scripts/verify.sh` now, which tees every check into
a log and prints what each one reported under the verdict:

    ── what each check reported ──
      === head check: HEAD builds and passes ===
      === render check: 1 FAIL, 1 WARN ===

    verify: FAILED — render-check

A WARN still does not fail the build -- that trade is right, and making setup
flakiness fail a gate is how a gate stops being run -- but the verdict now says
how many sections did not run, next to the word "passed". `VERIFY_TARGETS` makes
the collection logic exercisable without a twenty-minute run, which is how the
above was produced.

**46, and it turned up in that very output.** The mobile scrollback assertion
had failed once earlier in the day on a tree where the change under test was in
the upload path, and re-running passed. It was written down rather than chased,
with the observed rate. Then the two-target run above failed it again: twice in
nine runs, both times reporting the top line as "unreadable".

That word was the diagnosis. `lineNo` returns NaN when the row it reads does not
match `TOUCH_(\d+)`, and it read once after a fixed 700ms sleep -- so a repaint
in flight at that instant is indistinguishable from a scroll that never
happened. It polls for five seconds now and keeps the last readable value
separately, so a real failure says "last readable TOUCH_368" -- the scroll did
not move -- and a broken fixture says "never readable". The old message could
not tell those apart, which is why the first occurrence was filed as flakiness
rather than fixed.

A check that fails one run in five teaches people to run it again instead of
looking, and the entire argument for these browser checks is that a FAIL there
means something.


## The fault the stale banner exists for, finally run

45, fixed. A full disk is what that whole path is built around -- `CheckWritable`,
`noteStale`, the three-tick grace, `/api/health` answering `"ok": false`, and the
banner -- and nothing had ever re-run it. The harness injects an unwritable data
directory, a killed backend, a dead session, a wrong password, an offline cycle,
floods and a certificate swap. Not this. It had been driven by hand once, and a
thing driven by hand once is a thing that worked on the day it was written.

It lives in `restart-check`, because the injection is a restart and restarting
the backend under a browser is what that file already does.

**Two attempts, and the first one answered a different question.** Applying
`ulimit -f 1` at exec, which is what the finding proposed, does not produce a
panel with a full disk. It produces no panel at all:

    vibepanel: store: ping .../vibepanel.db: disk I/O error (4874)

It exits inside `store.Open`. Worth knowing, and not what the banner is for: the
banner is for a disk that fills under a panel that is already up. So the check
boots normally and squeezes the running process with `prlimit`.

**Then nothing happened, for a good reason.** Under the limit, an idle panel sat
at `"ok": true` for twenty-four seconds. Nothing probes writability on a timer,
and that is defensible -- a panel with nothing to record has lost nothing, and
you find out when a write actually fails. But a check that only waits would have
passed against a panel that never noticed. So it makes the most ordinary write
there is, and asserts the write fails first, so that a failed injection cannot
read as a pass.

    [PASS] stale: the banner appeared: "The panel has stopped recording what the
    sessions are doing. The terminals are unaffected."

with `/api/health` answering `"ok": false` and the connection still `open` --
which is the half that is easy to lose. The banner travels *in* the state
snapshot, so a socket that closes cannot deliver the explanation. That is the
difference from a database that cannot be *read*, where the socket closes one
revalidation tick later and every viewer is told nothing at all.

One measurement from the teardown, because it cost a run: `prlimit --fsize=1024`
sets the soft *and* hard limit, and raising a hard limit back needs
CAP_SYS_RESOURCE. `--fsize=1024:unlimited` lowers only the soft one. The run
whose assertions had all passed still ended in a FAIL, from the cleanup.

## 15, and the branch nothing took

`reorder` is now a pure function of `(ids, draggingId, overIndex)`, out of the
release handler and tested. `web/src/hooks` had no test file at all, while every
other pure-logic module in the frontend has one.

The correction it carries -- `overIndex > from ? overIndex - 1 : overIndex` --
is the downward-drag arm, and render-check only ever drags the second project
*above* the first, which takes the other one. Removing the `- 1` fails exactly
two of the eight cases: the downward drag, and the drop-just-below-itself that
must be a no-op. The drop-at-the-end case passes either way, which is worth
knowing about a fixture that looks like it covers the same ground.

## 16, and a check that had to be wrong twice before it was right

The compose box routes a block containing a newline down the paste road rather
than typing it, because typed newlines are Enters -- "three lines in, three
separate submissions out", measured, fixed, and then exercised by nothing. The
chain behind it, `pasteText` to `MsgPaste` to `Manager.Paste`, was 0.0% in a
`-coverpkg` run and named by no Go test either. The branch that had the bug was
driven five times over.

**The obvious check failed, and the failure was mine.** Fill the box with three
lines, send, assert they did not run. It failed: two of the three had run. The
evidence pointed at the client, then at tmux, and both were innocent. A pane
driven directly with `paste-buffer -p` brackets correctly -- with my shell, with
an empty HOME, and with the panel's own tmux config, all measured. The compose
box really was holding the newlines, measured too, by reading the textarea back
before concluding anything from what the terminal did.

What was wrong was the pane. `render-check` creates its scratchpad session as

    mkSession(['sh', '-c', 'echo RENDER_CHECK_MARKER; exec sh'], 'scratchpad')

and dash never asks for bracketed paste, so tmux correctly does not bracket, and
the newlines are Enters no matter which road the block travelled. The product
did exactly what `ComposeInput`'s own comment promises -- "better rather than
airtight" -- and the check was asserting a guarantee that does not exist for
that shell. The prompt was in the screen tail the whole time: a bare `$ `, not
this machine's bash prompt.

Along the way the first version of the message was also useless: it collapsed
whitespace, so "the three lines sit unrun on the input line" and "each was
submitted in turn" produced the same string -- the two outcomes the check exists
to tell apart.

So the check now makes its own session, `cat -v` after `printf` of `ESC[?2004h`,
which is the fixture `internal/tmux` uses and for the same reason: the markers
have to be text, because a terminal swallows them as sequences. It asserts what
the client is responsible for -- that the block went down the paste road -- and
leaves what the receiving program does with it to the receiving program. Removing
the branch fails it with `open=false close=false`, and the screen tail shows
three bare lines.

**A third thing, from my own tooling again.** The session command contained
`printf '\033[?2004h'`, and in a JavaScript string `\0` is an octal escape,
illegal in strict mode. ESLint said so, node refused to parse the file, and the
whole run exited without printing a single finding -- which is exactly the
"green because nothing ran" shape that `check`'s new tmux notice was added for,
one file over.


## Two that were about a context and a slot

**21: an event in a slot built for snapshots.** `queueState` holds one payload
and replaces it, which is exactly right for a state snapshot -- it is absolute,
so the newest contains every older one -- and the reason it exists is measured:
sending from the caller's goroutine let "one viewer that stopped reading delay
every other viewer's state update by 2.2 seconds".

`notifyPanel` went through the same slot, and a panel notification is not a
snapshot. It says "your notes for this project changed, fetch them again" and
carries nothing else, so replacing it drops the only copy. The poller queues a
snapshot every two seconds, so this is not a narrow race; the symptom is a note
saved in one browser not appearing in another until something unrelated woke it,
which reads as flaky sync rather than as a dropped message.

Events now have their own queue beside the slot, deduplicated by payload rather
than bounded by a count. That works because the message carries no content: two
identical notifications are one notification, so the queue's length is the
number of distinct (project, kind) pairs with something unsent -- small for the
same reason the message is small, and it cannot grow however hard a writer
stalls. The snapshot slot keeps coalescing, which is the half that must not be
lost.

Tested through `takePending` rather than a socket, because that is where the
property lives. Routing events back through `queueState` fails it with "got 0
events, want 2".

**14: a delete that stopped when the tab closed.** Both delete paths ran on the
request's context, and Go cancels that the moment the client disconnects. Both
loop over sessions killing them one at a time, so a tab closed just after the
click left some tmux sessions dead with their rows intact -- processes nothing
in the panel can reach, produced by doing nothing wrong. `tearDownSession`'s
comment reasoned carefully about which half should fail first and not at all
about the half being cancelled, while `notifyState`'s writers already detached
for this exact reason.

Both now detach before they destroy anything, with a thirty-second bound. The
user pressed delete; closing the tab is not a reason to do half of it.

The test calls the handlers directly with an already-cancelled context, because
a real disconnect mid-loop is a race and a race is not a test. It is a stricter
version of the same fault: if anything in the path still reads the request's
cancellation, nothing is deleted at all. Both paths are mutated separately --
put the session path back and two sessions survive with their row; put the
project path back and three of three survive. "Both" was what the finding said,
and one of two passing is how half a fix ships.


## 9: a scan that found nothing and a scan that looked at nothing said the same thing

Both scanners reported only when they found something, so silence meant either
"the layout is clean" or "this measured an empty set", and nothing in the output
told them apart. `findSmallTargets` walks `button, a[href], [role="button"]`; a
control refactored into a `<div onClick>` leaves that set, and enough of them
would leave it empty while the check went on passing.

Both now return what they examined alongside what they found, and the wrappers
assert a floor: the phone drawer must have at least eight controls in it,
because the key bar alone has sixteen, and the overflow scan must have measured
at least twenty elements, because it walks every element on the page and a
handful means the page was not there yet.

The counts are printed on the way past, which is the other half of the value:

    [PASS] mobile: 47 tap targets measured in the phone drawer
    [PASS] mobile: 31 tap targets measured in a 320 wide phone
    [PASS] mobile: 31 tap targets measured in a landscape phone

A floor cannot catch one control going missing -- that needs an expected count,
which is brittle -- but a number in the output makes a drop from 47 visible to
anyone comparing two runs, and the floor catches the scan going blind. Pointing
the selector at `marquee` fails all three call sites with "looked at 0 controls".


## 10, and the check it turned up on the way

The finding asked for one GET: does the panel answer on the URL its own hooks
post to, while a panel holds the lock. `doctor` does that now, with
`InsecureSkipVerify` because `report.sh` uses `--insecure` and for the reason
its comment gives -- "the destination is 127.0.0.1, and when the panel is
serving TLS its certificate is issued for the public hostname". Probing more
strictly than the hook does would report a problem the hook does not have.

**The mechanism the finding gave for it is wrong.** It says `--addr
192.168.8.20:8443` "binds one interface and leaves nothing on 127.0.0.1", and
that much is true -- but `LoopbackURL()` returns `BindHost()` when one is set, so
the sessions are handed `192.168.8.20` as well and reach it perfectly well.
Measured, by starting a panel on `127.0.0.2` and asking `doctor`:

    [ok  ] hook endpoint      http://127.0.0.2:18470/api/health answers

The runbook carried the same wrong explanation, and its `curl` line checked
127.0.0.1 regardless of what the panel was bound to. That is 47.

**What does do it is worth more than what was asked for.** `VIBEPANEL_URL` is
injected with `-e` when a session is created, and tmux's `set-environment`
reaches only panes started after it -- a live session's environment cannot be
updated. So changing `--addr` and restarting the panel leaves every session made
before the change posting to the old address forever, while new ones work. The
symptom is "some sessions report their state and some do not", which reads as
flakiness rather than as a configuration change, and the loopback probe cannot
see it: it asks about the URL the *current* configuration produces, and that one
answers.

`doctor` now reads what each session actually holds and compares:

    [FAIL] hook url           1 of 3 session(s) still post to http://127.0.0.9:9999,
                              not http://127.0.0.1:8443
    [ok  ] hook url           2 of 2 session(s) post to http://127.0.0.1:8443
    [--  ] hook url           no session carries one; hooks are not in use

All four branches driven by hand against a real tmux server.

**And tmux corrected me on the way.** `SessionEnvValue` first treated a variable
that was never set as the `-KEY` line the manual's output format suggests. A
real tmux answers `unknown variable: X` as an *error*, so a session created
before hooks were installed would have been reported as broken. The test caught
it on its first run, which is the whole argument for AGENTS.md's rule that this
wrapper is tested against a real tmux and not a mock: "the bugs worth catching
there are tmux's, and a mock reproduces none of them."

**A third thing fell out of the same afternoon.** The runbook opens with "Eleven
lines" over a table of ten rows, and `doctor` prints thirteen -- `agents` was
added earlier in the day without a row, and the two above would have made three.
That table is the only place that says what each line *means*, so a line missing
from it is a line people read once and guess at.

The count and the rows are corrected, and pinned:
`TestTheRunbookExplainsEveryDoctorLine` reads the labels out of `main.go` and
looks for a row for each. Both directions were mutated -- deleting the `hook url`
row and inventing a `new thing` line each fail it by name. It also refuses to
run at all if it finds fewer than ten labels, because a regex that has stopped
matching would otherwise report perfect agreement about nothing, which is the
failure mode `jsonKeys` already had once in this project.


## 17 and 18: the fourth funnel, and a type that agreed with the bug

**17.** A passkey's name was rendered raw twice in `Settings.tsx`, one of them
the `window.confirm` that asks before deleting the credential. `passkeyLabel`
now sits beside `sessionLabel`, `projectLabel` and `terminalLabel`, so all four
name-rendering funnels are in one file and the sweep that finds three finds four.

Lower stakes than the others -- this name is typed rather than taken from a
directory basename or a `pane_title` an agent set -- but a dialog is the last
thing between a credential and being deleted, and a name carrying an override
can make it ask about a different key than the one it removes.

**The test was wrong first, and the code was right.** It expected a name made
only of control characters to fall back to "Passkey". It does not:
`safeText` *replaces* with U+FFFD rather than stripping, deliberately, so that a
name which contained something deceptive looks wrong instead of looking short.
Falling back there would hide exactly what the sanitising exists to show, in the
dialog where it matters most. The test now pins that behaviour with the reason.

**18.** `[]string(nil)` marshals to `null`, and `hooks.Status.Events` is nil
until something is installed -- which is every fresh panel, and the state the
settings page reads first. The upload's `paths` had the same shape when no part
of the body was named "file".

The tell was the guard: `Settings.tsx` read `(status.events ?? []).length`. That
is the symptom patched at the reader, in the one place `emptyIfNil`'s own comment
says it exists to make unnecessary -- "so the frontend never has to guard a map
over a missing list". And the type had been changed to agree: `wire.ts` declared
`events: string[] | null`, so the frontend was *told* to expect the bug.

All three are corrected: the server sends `[]`, the type says `string[]`, the
guard is gone. `TestTheEventListIsAnArrayEvenWhenNothingIsInstalled` marshals
the struct and looks for `"events":[]`; removing the normalisation fails it with
the whole payload printed, which is the form that makes it obvious.

## 30: the funnel nothing pointed at, and the one place outside it

Removing `safeText` from `sessionLabel` passed every test in the project. Its
own tests were all about the fallback chain -- title, then command, then the
word "session" -- which is the other half of what it does. Three cases now
assert the half it is named for: an override in a title, a zero-width space
between two rows that would otherwise be the same pixels, and an override in the
`command` it falls back to, which is no safer than the title because
`#{pane_current_command}` is a process name and a process can be called anything
a filesystem allows. Taking the sanitising out fails all three.

`FileTree` was the one site outside the funnel, sanitising inline with
`safeText(e.name)` and asserted nowhere. The browser checks do drive that
rendering path -- they find rows by `hasText` -- but every name in them was
plain ASCII, so nothing there touched the sanitising either.

`render-check` now writes a file called `invoice\u202Egnp.pdf`, which any list
honouring the override displays as `invoicefdp.png`, and reads the row back:

    [PASS] files: a bidi filename is neutralised in the tree: "invoice<U+FFFD>gnp.pdf"

Dropping the `safeText` fails it with the raw name in the message -- where it
reverses its own suffix, in the failure text, which is as good a demonstration
as the check could ask for.

**A note about writing this down.** That failure message contains a literal
U+202E, and pasting it here would have been caught by
`TestNoBidirectionalOverridesInSource` -- the guard that already caught this
author once today, in the entry describing the sanitising work. It is escaped
above.


## 31: right about a gap, wrong about which one

The row says both of `TruncateTitle`'s tests feed it pure ASCII, where a byte
slice and a rune slice agree, so rewriting it as `s[:MaxTitleRunes]` would pass
them and cut a CJK title in half.

There are three tests, not two, and the third has been there since the commit
that introduced the function: `TestTruncateTitleDoesNotSplitACharacter` drives
中, é and 🙂 and asserts both `utf8.ValidString` and the rune count. Rewriting
the truncation the byte way fails it immediately. The property was asserted; the
reading missed the test.

**The gap it points at is real, one line up.** The guard is
`utf8.RuneCountInString(title) <= MaxTitleRunes`, and changing *only that* to
`len(title) <= …` -- leaving the rune walk intact -- passes all three:

    ### the three pre-existing tests:
    ok  	github.com/jiangmuran/vibepanel/internal/session	0.004s
    ### and mine:
    --- FAIL: title_test.go:102: a title of exactly 256 runes was cut;
        only its byte length is over

The ASCII exact-limit case has `len == runes` so it never notices, and the
multi-byte case feeds 2000 runes, which is over the limit whichever way it is
counted. A title of exactly 256 CJK characters is 768 bytes, so that version
truncates a title it should leave alone -- and nothing said so.

The test is narrowed to the three things the others do not reach: that
exact-limit multi-byte case, the marker on a multi-byte title, and that the text
kept before the marker is the text that went in.

**Fourth premise this session that did not survive being run**, after 38 (a
function read without the four lines above it), 5 (a database reasoned about
rather than measured), 6 (a browser and a MIME parser never asked), and 10's
mechanism. Every one of them pointed somewhere real. None of them was right
about why, and the fix that follows from a wrong why is a different fix.


## 29: four ways of measuring a check that cannot fail

The row says the escape-fragment check is aimed past the defect -- its regex
matches `31m` alone and cannot match `[31mhello`. True, and not the half that
matters.

**The fixture could not produce the defect at all.** The flood was
`echo "line $i of noise"`: twenty thousand lines of plain text with no escape
sequence anywhere in them, so the ring buffer had nothing that could be cut in
half and the check could not fire whatever its regex said.

So: colour the flood, fix the anchor -- `^\d+;?\d*[a-zA-Z]\s*$` requires the row
to be *only* `31m`, while a regression produces `31mline 4021 of noise` -- and
measure it with `trimPartialEscape` replaced by `return b`. Four times:

    the original plain-text flood              0 FAIL
    two sequences a line, ~1/3 escape bytes    0 FAIL
    eight sequences a line, mostly escapes     0 FAIL
    the same, scrolled to the top of the       0 FAIL
      scrollback, since `rows()` reads the
      visible grid and a reload leaves the
      viewport at the bottom

Deleting the function it guards does not fail it. Something between the ring
buffer and the rendered page is not carrying the fragment through, and what that
is has not been identified -- so the comment now says exactly that, lists the
four measurements, and points at `TestTrimPartialEscape*`, which fails
immediately for the same mutation. The check stays, because a literal escape
tail at the top of a replay is a real defect whatever produces it; it stays
labelled, because believing it covers the trimming is worse than not having it.

**And it produced a finding of its own, 48.** Making the flood emit eight
sequences a line broke an unrelated assertion on a passing tree, in the most
confusing possible way -- it printed the very text that satisfies it:

    the tail is missing the last line: ["line 19998 of noise","line 19999 of noise", ...]
    codepoints: ["6c 69 6e 65 a0 31 39 39 39 39 a0 6f 66 a0 ..."]

`a0` is U+00A0. xterm emits one `<span>` per run of styling and `textContent`
joins them with a non-breaking space, so how a row reads back depends on how
many escape sequences produced it. Two sequences a line gave ordinary spaces;
eight gave NBSP, and `includes('line 19999')` stopped matching. Normalised once
where `rows()` reads them, which is cheaper than remembering it at thirty call
sites -- and render-check has about thirty of those and has not been touched.


## 48 was narrower than it looked, and it uncovered 49

The NBSP hazard is real and it is `textContent`-only. Removing the
normalisation from `render-check`, which reads `innerText`, does not fail the
styled-runs check that was added to catch exactly that -- measured, not assumed,
after the comment had already been written claiming otherwise. The helper stays
because the decision then lives in one place rather than at ten call sites, and
the comment now says which of those two things it is.

**The fixture that was supposed to demonstrate it found something else.** A line
printed with `\033[31m\033[1m` -- bold red, which xterm maps to the *bright*
palette -- left a standing contrast warning behind:

    [WARN] contrast/light: 3.55:1 (need 4.5) — "STYLED" rgb(255, 59, 48) on white

That is not the fixture's fault. Measuring the whole light palette against its
white background:

    colour     normal          bright
    red        #d70015  5.38   #ff3b30  3.55
    green      #0f7b32  5.39   #34c759  2.22
    yellow     #a05a00  5.31   #c77700  3.46
    blue       #0040dd  7.56   #007aff  4.02
    magenta    #a316b8  6.24   #af52de  4.13
    cyan       #00707c  5.82   #00a0b0  3.16

Every normal colour clears AA comfortably and not one bright colour does. The
pattern is unmistakable: the normal row was tuned -- those are darkened, not
system colours -- and the bright row is Apple's palette unchanged, which is
precisely what the comment four lines above it says was avoided. Bright green at
2.22:1 is close to unreadable on white.

**Not fixed, and the reason is the finding.** Bright cannot be both brighter
than normal and above 4.5:1 on white when normal already sits at 5.4 -- tuning
it to pass collapses the two rows into each other, which costs the six
distinctions the palette exists to make. The real options are inverting the
relationship on light backgrounds (bright means stronger, not lighter, which
several light terminal themes do) or deciding that terminal output is exempt,
which is what most terminal palettes assume. That is a design decision, and this
loop does not get to make it. 49.

The fixture uses normal-intensity colours instead, because a standing WARN is
how warnings stop being read -- which is the thing `make verify` was changed to
count two days of work ago.


## 23 and 24, and a check that would not converge

**23.** Three failure paths an unauthenticated caller can reach are worth
recording, and two of them wrote a row. A bad token on `/api/hook/state` wrote
nothing, so the runbook's diagnosis for "somebody is hammering this panel" --
`GROUP BY event` over `audit_log` -- could not see a hook probe at all. It goes
through `auditFromOutside` now, like its siblings, for the reason that helper's
own comment gives: the endpoint is not behind the login throttle, and an ungated
write turned 400 requests into 400 rows at 237 a second. The journal line still
goes out every time, which is what fail2ban reads. Removing the call fails the
new test.

The value is noticing, not preventing: the token has full entropy, so nobody is
guessing it. What was previously unanswerable is "has anyone been trying".

**24.** Server error strings echo names back -- `base + " already exists"` on an
upload conflict, `"writing " + base + ": "`, `abs + " is not a directory"` --
and the frontend rendered them raw in three places: the error banner, the drop
note, and the socket-error banner added earlier in this session. `safeText` is
applied to fields the frontend knows are names, and an error message is not one,
so a file whose name carries a directional override reversed the text around it
at the moment you were deciding whether to rename and retry. All three render
points now go through the same funnel.

**The end-to-end drive did not converge, and that is worth writing down.** The
plan was to upload a bidi-named file twice, force the 409, and read the note.
Four runs, roughly ten minutes each, and the note was empty every time. What was
learned: the drop overlay was not visible when measured -- after the drop, which
makes that datum useless -- and the directory listing I printed to check whether
the file was there was cut with `.slice(0, 8)`, so the name I was looking for
sorted past the end of my own evidence. That is the `head -10` mistake this log
already records once, made again by the person who wrote it down.

Abandoned rather than left half-driven, because each failed attempt leaves a
standing WARN, and a permanent warning is how warnings stop being read -- the
argument made two entries above about the palette. The fix stands on `safeText`'s
own tests and on the three call sites being one line each. That is weaker than a
browser check and it is what there is.


## 19 and 20: a rename with history behind it, and an artifact nobody had built

**19.** `password_changed` and `password_change_refused` were the two audit
events that did not share a prefix with their pair, in a field that `GROUP BY
event` groups on -- the query the runbook hands an operator asking whether
somebody is hammering the panel -- and that a fail2ban rule matches. They are
`password.changed` and `password.change_refused` now, and migration v7 renames
the rows already written: a history spelled two ways is worse than either
spelling, because the group-by the rename exists to fix would still return two
rows for one thing.

The convention is not "no underscores" -- `passkey.clone_warning` has one inside
a segment and is fine. It is that a pair shares a prefix, the way login /
login.failed and setup.completed / setup.rejected do.

Pinned by an explicit list rather than a pattern, for the same reason
`openRoutes` is a list: a pattern says the shape is plausible, and a list makes
adding an event an edit somebody has to look at. The migration is driven
directly rather than by opening an old database, because an `UPDATE` whose
`WHERE` matches nothing succeeds -- a migration with the spelling wrong is
indistinguishable from one that worked. Misspelling it fails the test.

**20.** Nothing had ever built the container image. No target, no script, none
of the seven checks, while the Dockerfile pins three base images and
`deploy/docker-compose.yml` builds from it -- exactly the property `head-check`
exists to remove.

Built, and it works. Run, and that works too:

    [ ok ] the container image builds
    [ ok ] the container answers /api/health ({"ok":true,...,"tmuxVersion":"3.5a","version":"docker"})

tmux 3.5a from `alpine:3.21` rather than the 3.6 on this host, which is past the
3.3 `doctor` calls the floor. `release-check` does both now, and skips with `--`
rather than failing on a machine with no docker, since building release archives
does not require a container runtime.

**The first version failed, and the failure is the useful part**: `docker build
-q -t "$IMG" .` from a script that does not run in the repository root, so it
reported "open Dockerfile: no such file or directory" about an image that built
fine by hand a minute earlier. `$REPO` is right there at the top of the file.

The smaller half is done too. The Dockerfile's caveat -- restarting the panel in
a container kills every session, which is the opposite of the premise everywhere
else -- is now repeated at the top of the compose file rather than pointed at,
because that file is the one a compose user opens, and it says what
`restart: unless-stopped` does and does not buy: the panel comes back, the
sessions do not.


## A correction, and the two findings the last change had already half-fixed

**The correction first.** The previous entry ended by saying the handoff table
was complete. It was not, and the way it went wrong is worth more than the
claim. The command used to list what was left filtered on three markers and then
took rows 8 to 14 of the result -- and the first seven rows of that filtered
list belong to a *different* table, the red lines at the top of AGENTS.md, which
have numbers of their own. So the window skipped past the same seven every time
and never reached the end. Fourteen findings were unhandled while the report said
none were.

A count would have caught it in one line, and the count was never taken until
the table looked finished. The same shape as `.slice(0, 8)` two entries ago and
`head -10` before that: the evidence was cut, and the cut was invisible in the
output.

**42 and 41 were both partly answered by work already done.** 42's fix column
asks for "a startup check comparing each session's injected URL against the live
one", which is what the `hook url` line added while fixing 10 already does. What
was missing was the runbook naming the causes, and there are three: `LoopbackURL`
builds the scheme from the TLS mode and the port from the configured port, so
turning TLS on -- which the README tells you to do after installing, since the
shipped default is `--tls off` -- moves every *new* session to `https://` while
the ones you have keep posting plaintext at a listener that now speaks only TLS.
Changing the port does the same.

41 is the token rather than the URL, and it travels the same way for a different
reason: created once, never rotated, so it only changes when the row holding it
goes away -- a restore from a backup taken before it existed, which the runbook's
own "database will not open" section tells operators to do. `doctor` compares it
now, both branches driven against a real tmux server:

    [ok  ] hook token         1 of 1 session(s) hold the current token
    [FAIL] hook token         1 of 2 session(s) hold a token this panel no longer accepts

Read with `GetSetting`, not `HookToken`: that one *creates* the token when there
is not one, so a diagnostic run on a fresh panel would generate a credential as a
side effect. Compared and never printed, because it is a credential and doctor
output ends up in bug reports.


## 36 and 43: two halves of an upgrade that looks installed

**36.** `install.sh` ended in `systemctl --user enable --now vibepanel`, which
is a no-op on a unit that is already enabled and active -- exactly the state an
upgrade finds. The new binary went to disk, the old one kept serving, and the
script printed "started. the one-time setup token is in: journalctl ...": a
start that did not happen and a token consumed at first install. It restarts
when the unit is already active now, and prints which of the two it did, because
"started" and "restarted" are different facts and the setup token exists for only
one of them.

The finding already said no check will cover this, and it is right: release-check
runs the installer twice, but its throwaway HOME has no systemd user session, so
the installer takes its "no user systemd session here" branch and never reaches
the line. Recorded rather than pretended.

**43.** tmux reads `-f` once, at `start-server`. `EnsureServer` rewrites the
config on every call, and the panel never kills its server -- the premise of the
project -- so the file is always current while the running server uses whatever
it read at boot. Nothing could see the difference.

The server records a hash of the config it started with now, in a server option,
and `doctor` compares:

    [ok  ] tmux config        the running server has the config this binary carries
    [--  ] tmux config        the running server started with a different config
    [--  ] tmux config        the running server predates this check; restart it to know

All three driven by hand. A `--` rather than a FAIL, because the remedy costs
every session on the socket and that is the reader's decision.

**Two measurements corrected the implementation on the way.**

The first: I expected `EnsureServer` to restamp a server it did not start,
because `ServerRunning` is `list-sessions` and I assumed that fails on a
session-less server. It does under tmux's defaults -- and this project's config
sets `exit-empty off`, under which it succeeds. Measured both ways rather than
reasoned about. The hypothesised bug does not exist, and the test now asserts
`ServerRunning` directly so the assumption is pinned rather than believed.

The second is worse and is mine. The test's "a second EnsureServer must not
restamp" case changed the config *file* and asserted the stamp was unchanged --
which it would have been either way, because `ConfigStamp` hashes the embedded
bytes and not the file. It passed for a reason with nothing to do with what it
was testing. It sets a value no hash can produce now, which is decisive.


## 26: the button that killed what somebody else had just started

`Respawn` is `respawn-pane -k`, which kills whatever is in the pane, and the
handler never asked whether there was anything to kill. Its comment reasons
carefully about the tmux session being *gone* -- killed from a shell, lost with
the server, gone in a reboot -- and not at all about it still working. The only
guard was the frontend, which renders the button only when the session has
exited.

The reachable case is the one this panel is built for. Two viewers: A restarts a
dead session, B's tab still holds the snapshot from before and still offers the
button, and B's click kills the agent A just started. The window is one round
trip wide, because `notifyState` follows the restart -- and "one round trip" is a
description of a race, not of a safe interval.

409 now, with the reason. Not 400: nothing about the request is malformed, the
state it assumed has changed underneath it.

**The pid is the assertion, and it is why the test is worth having.** Removing
the guard produces this:

    restarting a running session answered 204 No Content, want 409
    the process was replaced anyway: pid 3814339 -> 3814346

A 409 with the process replaced anyway would be a worse bug than no 409 at all,
because the message would say nothing happened.

Restarting a *live* session is a real need -- an agent wedged rather than exited
-- and if it is ever wanted it is a different affordance with a confirm on it,
not this one silently doing more than its name.

## 44: two producers of one mapping

`ClaudeSettings` composes four hardcoded `entry(...)` calls for the settings page
to show; `InstallClaude` ignores it and iterates the `events` map through
`mergeEvent`. Nothing compared them. `TestInstallIsIdempotent` asserts four
events and reads `events` on both sides, so it agrees with itself.

A disagreement means the page shows one thing and the button writes another,
against the one promise the install flow makes: you see the exact JSON that will
be merged before agreeing to it. The forward direction -- every event in the map
appears in the snippet -- was already covered. This is the reverse, which is the
half that lets the page advertise a hook nothing installs.

Both mutations caught, and the messages are the point:

    the settings page advertises a "SubagentStop" hook and the events map has no
      such event, so pressing install writes nothing for it
    the snippet's "Stop" entry does not carry "/tmp/report.sh done"; the page
      would show one state and the installer write another


## 40 and 13, and a test server that was reading the wrong file

**40.** The settings page reported `os.Stat(DBPath())` -- the main file only --
while the panel runs in `journal_mode=WAL`, so recent writes live in the `-wal`
file until a checkpoint, and a checkpoint can be held off by a long-lived read,
which this panel has. The number shown could sit well under what is on disk, at
the moment somebody was reading it to answer "why is this growing", and it
disagreed with the runbook's own `du -sh` for a reason neither screen explained.
It counts `-wal` and `-shm` now, because agreeing with `du` is the whole job.

**The finding's second half is stale and is corrected rather than repeated.** It
paired this with 5 -- "two unconditional row updates per live session per tick,
each dirtying a page" -- as the thing that fills the WAL. 5 was measured and
retracted: SQLite elides an update that changes nothing, so those writes dirty no
pages at all. The reporting gap stands on its own; what fills the WAL is
something else.

**13.** Two dead lines under a comment that misnamed its own variable and
misstated its purpose. Deleted.

**And the test for 40 found something in the harness.** `newTestServer` opened
the database at `filepath.Join(dir, "test.db")` while its `Cfg` had `DataDir:
dir`, so `cfg.DBPath()` pointed at `vibepanel.db` -- a path with no file at it.
Anything reading the database *through the config* was therefore looking at
nothing and saying so without complaining: the settings page's size has been
reporting zero in every test that ever touched it. It opens at `cfg.DBPath()`
now, which is what a real server does.


## A service worker turned every stub in the harness into a no-op

Adding a service worker for installability and notifications broke every
`page.route` interception in `render-check`, silently, in the same commit that
added it.

The worker passes requests through with `respondWith(fetch(event.request))`.
Playwright's `page.route` does not see requests a service worker makes — so the
one stubbed endpoint in that file, the fake `/api/system` used to render a
machine the panel cannot measure, stopped being requested at all. The
assertions under it went on running against the real machine, where every meter
reads fine, and the check failed with a message about the *product* being wrong.

Found by putting a counter on the route handler rather than by reasoning about
it. It read zero:

    [FAIL] panel/monitor: the fake /api/system was never requested, so every
      assertion below it is about the real machine rather than the payload
      under test

That line is the fix as much as the counter is. A stub that stops being reached
looks exactly like a product that stopped working, and the difference is one
number nobody was printing.

Every context in `render-check` blocks service workers now. Which leaves the
worker driven by nothing, so there is one context that allows it and checks the
two things that decide whether a browser will offer to install at all: the
manifest's `name`, `start_url`, `display`, `icons`, a 512px icon, and that
`navigator.serviceWorker.ready` actually resolves.

**Two fixture failures wearing product failures' clothes, in one afternoon.**
The pasted-screenshot check reported the feature broken twice: first because
Playwright's `dispatchEvent` does not carry a `DataTransfer` across the boundary
as `clipboardData` — it has to be built and dispatched inside the page — and
then because the terminal wrapped the uploaded path and `includes('pasted.png')`
missed `pasted\n.png`. That second one is the NBSP lesson again, one file over.

---

## Publishing it

**A README with no picture, for a program whose whole complaint was that it
looked bad.** The old one was a good document and the wrong document: it opened
with a status paragraph that had been wrong for weeks, explained the design
before saying what the thing does, and never once showed the screen. Somebody
arriving from a link decides in about four seconds, and there was nothing for
them to look at.

So `README.md` leads with the panel, then with what you get, and the design
material — which is the best part of it — moved down to where a person who has
already decided they are interested will find it. `README.zh-CN.md` is a real
translation rather than the English with the words swapped; the two files say
the same things but neither reads like the other's shadow.

**The screenshots come out of `shots.mjs`, which meant fixing what it seeds.**
The hero picture was 80% empty terminal: four lines of fake agent output at the
top and a thousand pixels of background under them. That is not just a bad
photograph, it is a bad *test* — the line gap, the wrapping, the dim-vs-bold
contrast and the whole palette are invisible in an empty terminal, and every
rendering problem this project has found was found in a full one.

The seed now writes a ~28-line transcript to a file and `cat`s it, rather than
building it out of `printf`. The command crosses JS, JSON, Go's argv and `sh` on
its way to the pane, and every quote in it would otherwise have to survive all
four. The bottom scratch terminal got real log lines for the same reason.

Then, three times in a row, the tool refused the patch:

    command contains control characters that would be hidden in the approval dialog

Each time because I had typed a literal ESC byte into `const e = '…'` instead of
writing an escape for it. Third attempt: `String.fromCharCode(27)`, which cannot
be typed wrong. This is the same class of byte the product spends effort
sanitising before it reaches a React node, and I could not stop emitting it into
my own shell.

**One personal hostname and one session lock file were about to become public.**
`docs/build-log.md` carried `direct.<the operator's domain>` from the entry about
a link click telling a third party where the panel lives, and
`.claude/scheduled_tasks.lock` — a pid and a session id belonging to the editor,
not the project — had been swept in by a bulk commit. Both are in the working
tree's fix and both are still in history, which is worth saying plainly rather
than quietly rewriting 106 commits under somebody's repository.

**CI now exists, and it installs tmux on purpose.** `.github/workflows/check.yml`
runs `make check` and a `CGO_ENABLED=0` build. The tmux install is not
boilerplate: without it, four packages skip their entire suites and `go test`
prints `ok` for each, so the run goes green having checked almost none of the Go
code. The Makefile warns about that locally; the workflow makes it impossible.

The browser checks stay out of CI. They boot real tmux servers and a real
Chromium and take twenty minutes, and a gate that slow attached to every push
becomes a gate people route around.

---

## Which session is eating the machine

The side panel could say the box was at 90% and not which of a dozen agents was
doing it, which is the question a person actually has at that moment.

`sysmon.TreeSampler` walks `/proc` **once** per tick and attributes usage to
each pane pid by summing its whole process tree. Once, not once per session:
a panel with two dozen sessions would otherwise walk the process table two
dozen times a tick, and the first walk and the last would disagree with each
other about the same instant.

Three decisions, each of which could have gone the other way:

*The percentage is a share of the whole machine, not top's.* top and htop mean
"one core saturated" by 100%, which is more informative in isolation. But the
machine meter is an inch above this list on the same panel, and a session
reading 310% beside a machine reading 31% invites exactly one wrong conclusion.
`cores` is in the payload for anyone who wants to convert.

*A session whose pane has gone is absent, not zero.* Zero is a real reading —
a shell sitting at a prompt — and a dead session drawn at 0.0% looks like an
idle one.

*RSS is summed across the tree and over-counts pages shared with forks.* The
honest alternative is walking `smaps` for every process on every sample, which
costs more than the number is worth. Every process viewer that shows a tree
total has the same footnote.

The parse of `/proc/<pid>/stat` starts after the **last** `)`, not by splitting
the line. Field 2 is the executable name in parentheses and it is not escaped:
a process can be called `(weird) name)` and Chromium's renderers routinely
carry spaces. Splitting on whitespace shifts every field after it and the
numbers that come out are still numbers, so it fails silently with a plausible
ppid belonging to nobody. Mutating `LastIndex` to `Index` fails the test.

The tree walk keeps a visited set, which is not tidiness either. `/proc` is
read without a lock, so a process can be reparented between reading its stat
and reading its children's, and the graph assembled from two instants can
contain a cycle the kernel's real tree never had. Removing the set hangs the
walk; the test has a five-second deadline so it reports that rather than
hanging the suite.

Not part of the state broadcast. That snapshot goes to every viewer whenever it
differs from the last one, and a number that moves every tick would make every
tick a broadcast — the same reasoning that keeps `LastOutputAt` off the wire.
It is polled by whoever has the monitor open, and by nobody otherwise.

**And the monitor panel turned out to be the one surface that was never
translated.** CPU / Memory / Disk / "Reading…" were English on a Chinese page,
one tab away from a strip that says 内存 and 磁盘. Found by looking at the
screenshot rather than by anything failing.

## CI found a bug on its first run, and it was ours

`.github/workflows/check.yml` went green on nothing: fourteen tests failed
across `internal/tmux` and `internal/session` on Ubuntu 24.04, which ships tmux
**3.4**. The symptoms all named something else — sessions missing from the
listing, panes that never started their command, `expected 13 fields, got 1` —
and the cause was one line in the embedded config:

    invalid option: allow-set-title

That option does not exist below tmux 3.6. The README claimed 3.3 as the floor
and `MinMinor` agreed, so the panel was advertising support for a version its
own config could not load on. Debian 12 ships 3.3a and Ubuntu 24.04 ships 3.4:
between them that is most of the machines anybody would run this on.

Measured before fixing: on 3.6, `allow-set-title` **defaults to on**, verified
against a server started with `-f /dev/null`. So the line is an assertion
against a future default flipping, not configuration, and an assertion is not
worth breaking a supported tmux over. It is now guarded by
`%if "#{>=:#{version},3.6}"` — 3.6 rather than the version it actually arrived
in, because 3.6 is the one that could be verified here and skipping the line
below it changes nothing.

The general fix matters more than the specific one. Nothing was checking that
the embedded config is valid on the tmux that is installed, and tmux makes that
hard to notice: it does not report config errors on stderr at `start-server`,
does not put them in `show-messages`, and instead draws them in the pane and
carries on. `source-file` is the one command that does report them — non-zero
exit, message on stderr — so `TestTheEmbeddedConfigLoadsWithoutComplaintOnThisTmux`
sources the file into a throwaway server and every option in it becomes a
checked claim about the local tmux rather than about the author's.

A per-option assertion would not have caught this. The assertion for
`allow-set-title` was passing on the machine it was written on.

## Three bugs behind one CI failure, and a tmux 3.4 to reproduce them on

The `allow-set-title` guard was not the end of it. CI on Ubuntu 24.04 kept
failing, with `expected 13 fields, got 1` everywhere and no clue in it, so the
first move was to stop guessing: `dpkg-deb -x` the noble tmux and its three
libraries into a directory, and add `TEST_TMUX_BIN` to the four test helpers
that build a tmux client. `TEST_TMUX_BIN=… go test ./...` then runs the whole
suite against 3.4 in a minute instead of five minutes per CI round.

It is not `VIBEPANEL_`-prefixed, which the first attempt was. The panel reports
every unrecognised variable under that prefix at startup, on purpose, and
`internal/config` has a test that says so — which promptly failed. Correctly.

**1. The field separator.** Measured, same format, same session:

    3.6:  "vp_p\x1f/some/path\x1fEND"
    3.4:  "vp_p\\037/some/path\\037END"

tmux before 3.5 escapes non-printable bytes in its own output as octal, so the
0x1F separator arrived as four characters and every record parsed as one field.
Unescaping is not available as a repair, because the escaping is not
reversible: 3.4 leaves backslash alone, so a directory genuinely named
`lit\037here` and an escaped separator are the same eight characters. `mkdir`
is all it takes to make a running session vanish — which is the exact failure
the separator was chosen to avoid in the first place.

The separator is U+241F SYMBOL FOR UNIT SEPARATOR now: the printable picture of
the character rather than the character. Multi-byte UTF-8 passes through both
versions untouched, verified on both. Being printable, a directory can be named
with it — so it joins the control characters in `scrubbed()`, and a session
whose path contains it is now a case in the paths test.

**2. A repaint is not progress.** `Advanced` was `chunk contains "\n"`. On 3.4,
a pane running a spinner produces this several times a second:

    "\x1b[H- waiting\x1b[K\r\n\x1b[K\r\n\x1b[K\r\n\x1b[K\r\n…"

Every line feed there is tmux stepping down the screen erasing as it goes, and
counting them as progress clears the bell on an agent that is still waiting for
an answer — the one state this panel exists to report. A line feed now counts
only when the line it ends was not erased first. Measured on 3.4: eighteen
chunks of spinner produce no advance, eighteen chunks of real output produce
seventeen. The unit test uses the captured bytes.

**3. A race the faster tmux kept winning.** `TestTheAdvanceSignalIsComputedFrom
WhatTmuxSends` rang its bell in the same millisecond the pane started, which is
a race against the panel's own attach. tmux latches a bell that rang with
nobody watching, and this test has no poller to read the latch back — that
backstop belongs to reconciliation, not to the Manager. 3.6 won the race often
enough for it to be invisible; 3.4 lost it every time, and the failure read as
"the advance signal is wrong", which was the one thing that was not broken. The
scripts wait a second before ringing now.

The whole Go suite passes on 3.4 and on 3.6.

**What this says about the checks.** Every browser check in this project builds
from the working tree and runs against the tmux on this machine. That is one
version, and three of the bugs above are things a *different* version does. CI
was worth its keep on the first run, and the reproduction is worth more than
the CI: the loop went from "push and wait five minutes for a line that names
the wrong thing" to "run it locally against the tmux that fails".

## The last two CI failures were not the same kind of thing

**One was a test that did not attach the way the panel does.**
`TestTheBellFlagLatchesWhenNobodyIsAttached` starts a real tmux client on a PTY
and asserts the bell flag is spent. The panel sets `TERM` on its own client;
the test did not, and a non-interactive CI step has no `TERM` to inherit.
Without one, tmux attaches to a terminal it cannot drive and the flag survives.
Reproduced locally with `env -u TERM` in one run, which is the whole argument
for having a way to run the suite the way CI does.

**The other is tmux losing a wait status, and nothing can be done about it.**
`TestAKilledAgentIsNotRecordedAsACleanExit` failed about one run in ten on
tmux 3.4. The diagnostic that settled it printed three things the assertion had
been throwing away: what tmux says *now*, the pid that was killed, and whether
it is still in `/proc`.

    stored exit status = 0, want 137 … killed pid 390132 (still present: true);
    tmux now says dead=true status=0 signal=0 pid=390132 cmd="sh"

Still present, and a zombie: the process was killed and tmux had not reaped it
yet, but tmux had already marked the pane dead because its pty closed. Both
`pane_dead_status` and `pane_dead_signal` read 0, permanently — the number
never existed to be collected later.

So there are two claims in that test and only one of them is the panel's. The
panel's claim is faithfulness: the row says whatever tmux says. That is now
asserted on every run. The other claim — that a SIGKILL reads as 137 — belongs
to tmux, and when tmux loses it the test skips and says so, naming the version.

Treating "dead with status 0" as a kill would be the wrong repair: it would
misreport every agent that genuinely finished cleanly, which is the common
case. `docs/runbook.md` records the symptom for anyone on an older tmux who
notices a killed agent shown as a clean exit.

## A repaint is not a duplicate either

`TestSubscribingDuringOutputDoesNotDuplicate` failed on CI and nowhere else:

    attempt 13: "LINE-1\x1b[K\r\n" was in the replay and sent again live

The `\x1b[K` is the tell. tmux repaints the screen when it falls behind, and a
repainted line arrives carrying the same text the pane printed earlier — which
is the terminal doing its job, not the same bytes delivered twice. A two-core
runner falls behind far more than a sixteen-core desktop, so the repaints were
constant there and absent here.

Reproduced locally with `taskset -c 0,1` on the fourth attempt, which is the
only reason it could be identified as a repaint rather than as the race the
test is named for. `markers()` now ignores any token with an escape sequence
inside it, and the plain-line comparison that catches the real thing is intact:
reintroducing the gap between the ring write and the broadcast still fails on
attempt 2, with a plain `LINE-304\r\n`.

That is three findings in a row where an older or slower machine did something
this one does not, and the loop that made each of them tractable was the same:
reproduce it locally first — an extracted tmux 3.4, `env -u TERM`, two pinned
cores — and only then read the code.

## `make verify` found four things the translation had broken, and one it had not

The full run had been green before the language work landed. Afterwards:

**A selector on user-visible text.** `first-run-check` clicked
`button[title="Add project"]`. The i18n pass renamed that string to "Add a
project" and the check timed out. Both add-project buttons carry a
`data-testid` now. Translated text is not a selector, and this is the general
form of it — the same file also had `header button[title="Projects"]`, which
is worse: the title is not "Projects" when something is waiting, it is
"Projects — 2 waiting for you", and the check guarded it with
`isVisible().catch(() => false)`. So the drawer-opacity check underneath had
been skipping itself, silently, for as long as the fixture has had a waiting
session. A check that stops checking looks exactly like one that passes; that
branch is a FAIL now.

**A separator changed for typography.** `stress-check` read the grid chip and
split on `"x"`. Making it a chip changed the text from `130x46` to `130×46`,
so the parse produced NaN and the check reported that it could not read the
grid — about a change to the font of the thing it was reading. It splits on
non-digits now.

And the failure cascaded: the click that focuses the terminal was inside the
`else` of that same test, so the emoji and font measurements after it typed
into an unfocused page and reported "output never arrived" as two warnings.
Three findings, one cause, and the two downstream ones named the wrong thing.
The click is unconditional now.

**A whole modal that was never translated.** `DirectoryPicker` had six
hardcoded Chinese strings — 上一层, 取消, 使用这个目录 — while the dictionary
already carried `dir.up`, `dir.cancel` and `dir.use` for exactly those. An
English reader got a Chinese modal. Nothing caught it; it was found by grepping
the components for CJK. The mobile menu button and the header's "No session
selected" were English-only in the other direction.

**And one that was not about language at all.** Rewriting the first-run check
for the picker exposed the flow: picking closed the modal *before* the project
was created, so a path that did not exist took the modal away and left an error
in a banner at the top of the app. The way to retry was to reopen the picker
and type the whole path again, with the field that was wrong already gone.
`onPick` returns a promise now; the picker stays open on rejection and puts the
reason in the error slot it already had.

The two duplicate monitor namespaces — `mon.*` added beside the existing
`monitor.*`, both defining `cpu`, `memory`, `disk` — are merged. A dictionary
with two names for one string is how a translation gets updated in one of them.

## The translation was half a translation

`make verify` going green said nothing about language, because a string in the
wrong language still renders. Grepping the components turned up the rest of it:

- **The sign-in screen** — the first thing anybody sees — was English only.
  Title, both hints, all three field labels, the submit button, the divider,
  the passkey button and the "passkeys unavailable" line.
- `DirectoryPicker` was the mirror image: six hardcoded Chinese strings with
  `dir.up`, `dir.cancel` and `dir.use` sitting unused in the dictionary.
- Eleven soft-key tooltips, the empty-project line, the empty-terminals line,
  the "Add" button on todos, the clipboard-refused tooltip, the settings
  password-changed line, the file-tree escaped-link warning, the sidebar's
  sort-by-activity and restart tooltips, and the storage-fault banner.

None of it broke anything. That is the point: this class of defect has no
symptom a test can see, so it needs a test that reads the source instead.
`i18n.untranslated.test.ts` scans every `.tsx` for the two shapes a literal
takes — an attribute (`title`, `placeholder`, `aria-label`) and a line of prose
between tags — with comments stripped first. Crude on purpose: a checker that
needs a TSX parser this project does not depend on would not exist. Mutating a
button's title back to English fails it.

Its first run found the storage-fault banner, which the eye-grep had missed.

**Two self-inflicted wounds worth recording.** `git checkout --` was used to
undo a deliberate mutation and took the real work in the same files with it —
the whole sign-in translation, silently, because the mutation and the work were
in one file. And a blanket `s.replace("t(", "tr(")` to restore an alias turned
`preventDefault(` into `preventDefaultr(` and `useEffect(` into `useEffectr(`.
The alias exists for a reason the edit did not know: both of those files map
over items bound to `t`, so the translator cannot also be `t` in that scope.
Reverting and applying the two intended edits by hand took less time than the
clever version had already cost.

Also dropped `uppercase` from the project name in the sidebar. It is an
editable field: you type `my-app`, it shows `MY-APP`, and clicking to rename
shows something different again from what you typed. Case is information, and
the section already reads as a section from its size and colour.

## Hot upgrade: the parts that were assertions and the parts that were tests

Three things were true about upgrading and only one of them was checked.

**The panel now says when the tmux config is stale, where a person looks.**
tmux reads its `-f` file once at start-server, and the panel never kills its
server -- the premise of the project. So an upgrade that changes the config
leaves the new file on disk and the old settings in memory, and both look
installed. `doctor` reported it and nobody runs doctor after a `systemctl
restart`. It is on the settings page now, with the remedy *and its cost* in the
same sentence, because the remedy is `kill-server` and that ends every session.

Three states, not two: current, stale, and *unknown* -- a server that predates
the stamp cannot answer, and a page that renders "unknown" the same as "current"
is guessing on the reader's behalf. The test drives all three by writing the
stamp option from outside, which is exactly the shape an upgraded binary meets.
Set from outside rather than through a method added for the purpose: production
API that exists only for a test is API somebody will later find and use.

**The rollback refusal had a careful comment and no test.** `migrate` refuses a
database whose `user_version` is above what the binary knows, and that is the
one place where being permissive loses data silently -- an old binary would read
the tables it knows, ignore the columns it does not, and write rows back without
them. Nothing looks wrong until the next upgrade finds the values gone. The test
also asserts the message names both numbers and says what to do, because whoever
reads it is mid-rollback with the panel down. Removing the guard opens a
database from the future.

**And there was no upgrade procedure written down at all.** The README said the
browser notices a new build; it did not say how to install one. Both READMEs now
carry the three-line procedure and the three things that look like nothing: the
config that only applies at the next start-server, the rollback that is safe but
loud, and the older installer that did not restart the service.

One thing removed on the way: a claim that installers "older than v0.4" did not
restart. There is no v0.4 -- the binary reports `dev` -- and inventing a version
number in a public README is the kind of detail somebody later builds a support
answer on.

## A screenshot of an empty panel is a screenshot of a broken fixture

The notes tab was photographed empty, with its placeholder showing, in every
round of screenshots for as long as there have been screenshots. It looked like
a feature that did not work.

`shots.mjs` seeds a note with `PUT /api/projects/{id}/notes`, and the server had
been answering `400 unknown field "rev"` the whole time. Nobody looked: `authed`
returned the response and every caller ignored it. It throws on a non-2xx now,
and the very first run named the failure in one line.

The 400 was correct and the *documentation* was wrong. `PUT` notes decodes
`baseRev`; `docs/api.md` said `rev`, which is what the response calls the same
number. Anyone building against that page got a 400. `TestTheAPIDocCoversEveryRoute`
could not see it — it compares routes, and this was a field name inside one.

The asymmetry is worth stating rather than smoothing over, because it is what
went wrong: you **read** `rev` and **send it back as** `baseRev`. Different names
for one number because they are different claims — "this is the revision" versus
"this is the revision I was looking at". The doc says so now, and a test asserts
the documented name against the struct tag rather than against a string written
twice.

## Two more places the language never reached, and the rule that finds them

The untranslated-string test had two rules — attributes, and prose between tags.
Neither can see a *lookup table of labels*, and there were two:

- `Notes` kept six: `saved`, `unsaved`, `changed elsewhere`. The panel said them
  in English under a Chinese heading for as long as the translation existed.
- `StateDot` kept three, which are what a screen reader announces for the state
  indicator, and what the tooltip says.

The third rule is narrow on purpose: an object value that is a *phrase* — it
contains a space or trails an ellipsis. `method: 'POST'` and `kind: 'output'`
are not phrases. On this codebase it fires on exactly the two real cases and
nothing else, and mutating a label back to English fails it.

`StateDot`'s table also had to stop being a constant. A module-level map is
evaluated once at load, so it keeps whichever language was active when the tab
opened and a switch never reaches it.

## Two more UI things, both visible only in a photograph

**The todo counter was at the foot of the panel.** It was a sibling of the
scroller, so with three items in a tall column it floated nine hundred pixels
below the last one, alone against the background — a summary of something that
was no longer on the same part of the screen. It sits under the input now, next
to the list, and stays visible when the list is long enough to scroll.

**The checkbox tooltip said the same thing in both branches.**
`title={t.done ? tr('session.done') : tr('session.done')}` — copy-paste, and it
told you the item was "done" whether it was or not. Mark done / mark not done.

## The installer asks now, and the one path that needed root was the one path nobody shipped

`install.sh` did one thing: copy the binary, drop a user unit under `$HOME`,
enable lingering, print what to run next. Everything else was a comment at the
bottom pointing at `deploy/vibepanel-system.service` — the unit that exists
because a *user* unit asking for `OOMScoreAdjust=-500` measurably gets `100`.

Two things were wrong with that pointer. The README told people to `sudo cp
deploy/vibepanel-system.service /etc/...`, and **`build-release.sh` never copied
that file into the archive**. So the instruction worked from a git clone and
from nowhere else — for anyone who had done what the README's own Install
section says, `deploy/` held two files and neither was the one being named. It
ships now, and `release-check` asserts the archive contains it, which is the
assertion that would have caught it.

The other thing is that installing a service is a decision with consequences and
the script made none of them out loud. It now asks, when there is somebody to
ask: which unit, whether to start it, and then it prints the whole plan and
waits for a yes. At the end it reports what it *did* — which unit, started or
restarted, where the token is, what URL to open — rather than what the script
generally does.

**Interactive by default, and the definition of "by default" is the whole
trick.** Both stdin and stdout have to be terminals. stdin alone is not enough:
`make release-check` runs the installer with its output redirected to a log
file, from a make invocation that may well still have a terminal on stdin, and a
prompt written into a log file is a script that waits forever for an answer
nobody can see it asking for. `curl | bash` falls out of the same rule for free,
because there stdin is the script.

`--yes`/`--non-interactive` says it explicitly, `--interactive` forces prompts
on without a terminal, and every flag that existed before still means what it
did.

**Never both units.** A user unit and a system unit are two panels on one tmux
socket and one database. They do not corrupt anything and they do not collide
loudly — SQLite serialises them — they take turns, and the symptom is a panel
that forgets things: a project added in the morning gone by the afternoon,
`doctor` clean, because from inside either panel everything is as it should be.
The installer detects the other kind and refuses to create the second one;
`--migrate` is how you say you meant it, and it stops and removes the old one
*before* installing the new, so an interruption leaves nothing running rather
than two things running. `docs/runbook.md` has the symptom, because machines
where both were installed by hand already exist.

A bare re-run keeps the kind you already have and does not ask again. An upgrade
that offers to change the unit kind is an upgrade that changes it for whoever
pressed return.

**No root is a sentence, not a failure.** Root is available if you are root, if
`sudo -n true` works, or if sudo exists and there is a terminal to type a
password at. Sudo that *would* work but needs a password nobody is watching
counts as no root, because an installer that hangs in CI is worse than one that
installs the user unit. When it cannot, it says why and installs the user unit —
which is the right default anyway; the only thing given up is the OOM score.

### Testing shell without sudo, and the two bugs that came out of it

The bar is that a fix must be seen to fail when removed, and there is no test
framework here — so `scripts/install-check.sh` runs the real script down every
branch with three overrides that exist for no other reason, and say so in a
comment where they are defined: `VIBEPANEL_DESTDIR` (a DESTDIR-style prefix, so
"the system unit" lands in a temp directory instead of `/etc`),
`VIBEPANEL_SYSTEMCTL` (a recorder), and `VIBEPANEL_ROOT_CMD` (`none` produces
the no-root path, which cannot otherwise be produced on a machine where sudo
works). Seventy-odd assertions, no sudo, seconds to run.

`VIBEPANEL_SYSTEMCTL` is not only for assertions. **`systemctl --user` talks to
the manager for the logged-in user, which read its own `$HOME` at login and does
not care what `HOME=` the script was handed.** The migration path runs
`systemctl --user disable --now vibepanel`; a check driving it with a throwaway
HOME would have stopped the panel of whoever ran the check.

The same fact was a bug in the installer, not just the check. "Is it already
running?" decides restart-versus-start, and asking it on a *first* install into
a different HOME can get "yes" about somebody else's panel — which the new
restart-without-being-asked behaviour would then have restarted. It only asks
once a unit of that kind exists in the HOME it was pointed at. `release-check`
passes `--no-enable` on top of that, for the same reason stated at the call.

**Mutating the script proved each guard.** Removing the TTY test, the conflict
refusal, the plan confirmation, the no-root fallback, the restart-on-upgrade
branch and the `__USER__`/`__HOME__` substitution each failed the check, between
two and eight assertions apiece.

And it found a bug in the check itself. `has "$log" -- "--user restart …"` —
the `--` was meant for `grep`, but `has()` takes the pattern as `$2`, so it was
grepping for `--`, which every log contains. Four assertions were passing on a
string nobody had written on purpose; one of them was still green under a
mutation that removed the behaviour it named. Found because the mutation run
reported three failures where four were expected, which is the only reason to
count them.

**One real defect in the first draft, found the same way:** `read -p` prints its
prompt *only when stdin is a terminal*. Driven from a here-doc, the installer
consumed the answers and asked nothing — the questions, which are the thing
under test, were invisible in the transcript. Prompts are printed with `printf`
and the answer comes back in a variable now, and the answer is echoed back when
stdin is not a terminal so a piped run reads as a conversation rather than a
monologue.

The pty case is checked separately: every other case forces the mode with a
flag, so none of them can say whether a person at a terminal is actually asked
anything. `script -qec` gives it a real pty, with no flag at all.

## Two ways to send a title, and the panel read one of them

A session running Codex never got a useful name, and setting the title from
inside the process did not reach the panel either. The obvious suspects were all
innocent, and it is worth writing down that they were measured rather than
reasoned about, because three of them look guilty:

- `allow-set-title` is guarded by `%if "#{>=:#{version},3.6}"`. On tmux 3.6 the
  option reads `on` under the embedded config, and it defaults to `on` wherever
  it exists, so the guard changes nothing.
- A plain `OSC 0/2` from inside a pane does set `#{pane_title}` — with a client
  attached and without one, on the primary screen and from inside the alternate
  screen, which is where a full-screen agent TUI lives.
- The whole chain then works. A session created with no command, sent
  `printf '\033]2;set from inside\007'` the way a person would type it, is
  renamed by the next poll. That was written as a test before anything was
  changed, and it passed.

What does not work is the other way of sending a title, and it is the way a
program that knows it is inside tmux sends one. The title is meant for the
terminal a human is looking at, not for tmux, so the sequence goes in the
passthrough DCS:

```
printf '\033]2;X\007'                        pane_title becomes X
printf '\033Ptmux;\033\033]2;X\007\033\\'    pane_title unchanged
```

Passthrough is defined as tmux not looking. It hands the bytes to its client —
which is the panel — and `#{pane_title}`, the only source `deriveTitle` had,
never moves. Measured on tmux 3.6 with a real client attached: the client PTY
receives `ESC ] 2 ; X BEL` verbatim, and `pane_title` still reads the hostname.
`TestWhereAPaneTitleEndsUp` pins both halves, and each half fails silently on
its own.

**The panel was already parsing that title.** `internal/session/osc.go` reads
OSC 0/2 off the client PTY, bounds it with `TruncateTitle`, and the manager
broadcasts it as a title event; `internal/ws` forwards it as `MsgTitle`;
`socket.ts` calls `onTitle`. Nothing in `web/src` passes `onTitle` — it is
declared on `Terminal` and supplied by no caller — and nothing wrote it to the
store. So a title sent the only way a tmux-aware program sends one was parsed,
truncated, delivered to the browser, and dropped, and the session kept the name
of the directory it was sitting in. That is the whole bug: two channels, one
reader.

`Live` now keeps the last title the PTY saw, and `deriveTitle` takes it as its
second source, behind `#{pane_title}` and ahead of the command. Behind, because
`pane_title` is live and the PTY title is the last one ever seen; in practice
they never both hold, because a program picks a route and the route it picks is
the one tmux is not watching. Kept on the attachment rather than written from
the pump: naming a session is one decision in one place, and a write on the pump
goroutine would sit between the PTY and the viewers watching it — and would hand
a program that repaints its title a way to write a row several times a second.

Deliberately not answered here: whether Codex itself uses passthrough for its
title. Its binary carries the wrapped forms of `OSC 9` and `OSC 52` as literals
and a bare `\033]0;{}\007` for the title, so today it probably sets
`pane_title` like everything else — but the panel is not allowed to run it, and
that is exactly the situation where a channel nobody reads should be read. The
`codex_tui::terminal_title` module and a `[tui] terminal_title` list of item
identifiers (`project-name`, `current-dir`, `run-state`, `thread-title`, …) are
also in there, which is worth knowing before blaming the panel for a Codex
session called `codex`: that name is `deriveTitle`'s command fallback, and it
appears when the agent set no title at all.

## Codex gets the same button Claude has

`CodexNotify` had produced a snippet to paste by hand since it existed, and the
settings page said so in as many words: *Codex (paste yourself)*. It installs
now, from a button beside Claude's.

Line editing, not a TOML round-trip. This machine's `~/.codex/config.toml` has a
model provider, thirty-odd `[projects."..."]` tables, aligned assignments and
comments in it; every encoder that reads and rewrites a document loses the
comments and reorders the keys, and handing somebody back a file they did not
write in exchange for one line is the same sin the Claude installer avoids by
merging JSON rather than replacing it. A test asserts the file comes back
byte-identical apart from the inserted line.

**The insertion point is the part with teeth.** TOML keys belong to the table
above them, so `notify` appended to the end of that file defines
`notice.notify` — a key Codex never reads. Nothing reports it: the file still
parses, `codex doctor` is happy, reading the line back finds it, the settings
page says installed, and no Codex session ever reports a state. It goes above
the first table header, and `TestInstallCodexPutsNotifyAboveTheFirstTable` fails
if it does not.

Four more cases, each of which corrupts a file the agent will not start without:
a `notify` written across several lines is replaced whole rather than by its
first line; a `notify` inside a table is a different key and is left alone; our
own older line (an upgrade that moved the data directory) is replaced rather
than duplicated; and somebody else's is commented out in place rather than
deleted, because Codex has exactly one notify slot and a backup beside the file
is not where anybody looks. Uninstall removes only ours.

`hooksAreInstalled` counts either agent now. It read Claude's flag alone, so a
machine wired up through Codex was told to install hooks it already had — and
that notice is the panel admitting it is guessing, which it was not.

`?agent=claude|codex` on `POST` and `DELETE /api/settings/hooks`, defaulting to
Claude, which is what the parameter-less request has always meant. Anything else
is a `400`: the value picks a file in somebody's home directory to edit, so an
unrecognised one is refused rather than resolved to whichever branch is first.

## The three dialogs that were never the panel's

`window.confirm` asked before killing a session, removing a project and
revoking an API token; `window.prompt` asked what to call a passkey. Four
questions, and all four of them were the browser's rather than this product's.

The complaint that started this was about how they look, and that is the least
of it. They arrive in the operating system's chrome, in the operating system's
language — so a panel translated line by line into Chinese asked the one
question that destroys something in English, with an OK button nobody in the
dictionary chose. They are a single string, so the count of sessions about to
be killed, the promise that the directory survives and the token prefix were
concatenated into one paragraph with a blank line in the middle of it. They
cannot mark the destructive answer as destructive: OK and Cancel are the same
button twice, and OK is the one under the cursor. And on a phone installed to
the home screen they are a system sheet with the hostname printed above the
question, which is the shape a phishing prompt has.

They are also invisible to the harness in the direction that hurts. The render
check drove both of them with `page.once('dialog')`, a listener that does not
fail when there is no longer a dialog to hear — it simply never fires, and the
assertions after it read a page that never changed. That has already cost this
project once: the first-run check went on passing against the directory
`window.prompt` for some time after the picker replaced it, and the comment
there says so. Both call sites are the panel's own controls now, and the
project-removal one additionally asserts what the browser dialog could never
have offered: that the dialog marks itself destructive and that the focus does
not start on the button that kills things.

**Two pieces, both module-level stores rather than contexts.** `ask.ts` holds
the question on screen and `toasts.ts` holds the stack; components subscribe
with `useSyncExternalStore`, which is the shape `i18n.ts` already uses and needs
no library. A context would work equally well and would mean every component
between App and a row inside the settings dialog taking a provider it has no
other use for — the passkey list is four levels down and raises both a question
and a toast.

The answer is `string | null` and null is the only thing that means no. An
emptied field is a real answer to a question with a field in it, and a caller
writing `if (!answer) return` would read it as a cancellation and quietly do
nothing; `askConfirm` returns a boolean and `askText` a string or null, so
neither caller can make that mistake. Questions queue rather than replacing one
another, because a dropped one is a promise that never settles and a caller
awaiting it is a click that did nothing.

A toast carries a dictionary key and its parameters, never a finished string.
That is the enforcement rather than a convention: there is nowhere to put an
English sentence. Anything from outside the panel — a filename, a server's
error text — goes in `detail` and is rendered through `safeText`, along with
the composed sentence, because the parameters are the same name-carrying
channel every other funnel exists for.

Three details that are decisions rather than defaults:

- **Enter confirms while the focus starts on cancel**, which reads as a
  contradiction until you write it: Enter on a focused button generates a click
  as its *default action*, so `preventDefault` on the keydown cancels that
  click and leaves the confirm handler as the only thing that runs. Mid-IME
  Enter is excluded — there it is choosing a candidate, not answering.
- **Cancel is the wide button.** The one that destroys something is the
  narrower target and the further one, because the two mistakes do not cost the
  same: cancelling by accident costs a second click.
- **The stack is placed differently on a phone, and not by moving the desktop
  one.** The bottom of a phone screen is the compose box, the key bar, and —
  whenever anybody is typing — the software keyboard, so any fixed offset from
  the bottom of the window is either on top of the controls or underneath the
  keyboard, and which one it is depends on hardware the page is not reliably
  told about. The stack is a zero-height anchor sitting in the layout
  immediately above that chrome instead: the keyboard pushes the chrome, the
  chrome carries the anchor, and nothing has to be measured. Zero-height
  because a toast that took space in the column would resize the terminal
  under it, which means reflowing the grid for every viewer of that session,
  twice, for a sentence about an upload.

The upload note moved into the stack on the way past. It was one string in one
place, so a second upload overwrote the first one's result, and all three of
its sentences were English literals with no route to the dictionary at all.

`no-raw-dialogs.test.ts` fails on `window.confirm(` and on a bare `confirm(`,
which is the same function and the one that is not greppable by eye.

## The keyboard follows the tab

Clicking a session, a terminal tab or a side-panel tab left the focus on the
tab. You then had to click the terminal before you could type, every time,
which is the kind of friction that sends people back to a real terminal.

The rule, and it is one sentence: focus moves only when a person asked for a
terminal by choosing one, and only if — at the instant that terminal is ready
to take it — nothing else is holding the keyboard.

Both halves were arrived at by asking what breaks.

**It is called from click handlers, never from an effect on the selected id.**
An effect fires for every reason the selection changes, and the selection
changes without anybody touching it: `applyState` reselects the first session in
the list whenever the current one stops existing, which happens when a process
exits in another project. Focus jumping out of the notes textarea because a
build finished somewhere else is a worse bug than the one being fixed, and it
would be blamed on the keyboard rather than on the panel.

**The check happens when the focus is applied, not when it was asked for.** The
terminal for a session that was just chosen does not exist yet — the view is
keyed by session id, so choosing another unmounts one xterm and a new one
registers a frame or two later. That gap is long enough to click into the
compose box, and `document.activeElement` at the moment of the click is the tab
that was clicked, not the field somebody was typing in a moment earlier. Asked
at the click, the question has the wrong answer twice over. So the request
waits for the terminal to appear, re-asking each time, and gives up after six
hundred milliseconds rather than holding a claim on the keyboard forever.

Two exceptions with teeth:

- **xterm's hidden textarea is not a text field.** A terminal receives
  keystrokes through a `<textarea>`, so "is the focus in a textarea" answers
  yes for a focused terminal — and this feature is mostly about moving focus
  *between* terminals. Without the exception, clicking a tab in the bottom
  strip while the main terminal has the keyboard would be told somebody is
  typing, forever.
- **A read-only terminal is never focused.** That is the phone: the terminal
  there is a display, input arrives through the compose box, and focusing
  xterm's textarea raises the software keyboard over the thing being read —
  which is the reason `readOnly` is set on the narrow layout in the first
  place.

An open modal counts as holding the keyboard too, which the text-field rule
alone does not cover: the picker's list, the settings dialog and the
confirmation's cancel button are all things the keyboard was handed to on
purpose and none of them is an input. They carry `data-vp-modal` and the rule
looks for it.

The registry of live terminals moved out of `Terminal.tsx` into `terminals.ts`
so that two things can read it — the screen reader the browser checks use, and
this — without focus.ts importing the component that imports it.

## Surviving a reboot

tmux outliving the panel is the premise of this project, and it is written at
the top of `internal/tmux`: the Go server is a client, `systemctl restart
vibepanel` costs nothing, every agent keeps running. All of that is true and
none of it applies to `reboot`. The tmux server is a process on the same
machine, its 20,000 lines of scrollback per pane are in that process's memory,
and both go with the power.

What the panel did about it was mark every row GONE and offer a restart button
that started a login shell. So the state after a reboot was: two dozen rows
saying a session existed, one button per row, and the button quietly did
something other than what the row's name implied.

### The column that looked like it was already there

`sessions.command` is populated, non-empty and named `command`. It is
`#{pane_current_command}`, which the poller overwrites every two seconds with
the name of whatever process is in the pane — `node` for an agent, `bash` for a
shell somebody typed in, `sleep` for the test above. It is a label. Running it
as an argv starts something nobody asked for, and `handleRestartSession` already
knew this and said so in a comment, which is why it ran a login shell instead.

So the first half of restoring is a column that did not exist:
`launch_command`, the argv the session was created with, written once and never
touched again. Migration v9, along with `restore_on_boot` and `restored_at`.

The distinction that took the most thought is `''` versus `'[]'`. An empty argv
is a real, recorded fact — a session created as a login shell, and restoring it
is exact. `''` is what the `ALTER` leaves on every row that already existed, and
means nobody knows. Those have to stay apart, because collapsing them gives
every pre-upgrade agent session a restore that starts a shell under the agent's
name and says nothing. `TestMigrationV9RecordsWhatIsNeededToRebuildASession`
pins the default; changing it to `'[]'` fails on the default *and* on
`launchRecorded` coming back true for a row the panel knows nothing about.

### capture-pane, which nothing had ever called

`internal/tmux` has had `Capture` — `capture-pane -p -e -J -S -` — since the
manager was written, with a comment describing it as the cold path for a
backend restart. Nothing in the product called it. The manager's comment
explains why: attaching makes tmux repaint, the repaint fills the ring, and
priming the ring with history was measured to be invisible because tmux's attach
begins with `ESC[?1049h`.

It is exactly the right thing for this, though, because this is not about the
ring. It is about having a copy of the screen somewhere that is not in the tmux
server's memory.

**The bound is the whole design.** Measured on tmux 3.6, a pane holding a full
20,000-line history of coloured 130-column output:

| | bytes | ms |
|---|---|---|
| `-S -` | 2,971,621 | 69 |
| `-S -8000` | 1,195,852 | 31 |
| `-S -4000` | 601,591 | 19 |
| `-S -2000` | 304,423 | 13 |
| `-S -1000` | 155,836 | 8 |

Linear, so choosing the bound is choosing the cost. Unbounded, across the two
dozen sessions this panel is built for, is 71 MB and 1.7 seconds of tmux per
pass. 2,000 lines is around forty screens, which is well past the point where
what is on it answers a different question than "what was this doing when the
machine went down".

`CaptureLines` asks tmux for the bound rather than trimming afterwards, and
`TestCaptureLinesAsksTmuxForTheBound` is there because a version that asked for
the lines and got the whole history would look identical from the outside — the
archive would still work, still restore, still be correct, and cost ten times
its budget.

A second cap in bytes, 256 KiB, because 2,000 lines is not 2,000 lines worth of
bytes: the 304 KB above is long, densely coloured lines, and the cap is what
keeps the database's size a function of how many sessions exist rather than of
what an agent decided to print. Trimmed from the front, on a line boundary — a
capture sliced at an arbitrary byte can begin inside `ESC [ 3 8 ; 5 ; 2 0 0 m`,
and the tail of that sequence is then printed into the restored pane as literal
text.

The first version of the test for that asserted "it does not look like it starts
with an escape", which a mid-line cut passes vacuously, since arbitrary bytes
usually are not an escape. It asserts the exact thing now: what came back must
be preceded by a newline in what went in.

### What it costs to keep

Measured end to end, six sessions each holding a full 20,000-line history: a
pass that captures all six is 40.7 ms and stores 150 KB each; a pass where none
of them has printed since is 219 µs. The difference is the gate on
`last_output_at`, which the PTY pump already maintains and already debounces to
once a second. Without it, a panel where nothing at all is happening would
rewrite 6 MB of blob every thirty seconds, forever, on a machine that is meant
to stay up for months.

Scaled to two dozen sessions: about 160 ms every thirty seconds at full tilt,
under a millisecond at idle. On disk, 24 sessions all sitting at the byte cap is
6,291,456 bytes of content in a 6,467,584-byte database — 2.8% overhead — written
in 28.6 ms. `TestTheScrollbackArchiveFitsItsBudget` asserts that, because the
obvious "improvement" of keeping a history of captures rather than one row per
session would multiply the largest thing in the database and would pass every
other test in the package.

The blob is in `session_scrollback`, not on the `sessions` row, for a reason
that has nothing to do with tidiness: `ListSessions` runs on every poll tick and
every state broadcast, and a few hundred kilobytes per row would be dragged
through all of it. The row carries `captured_at` through a `LEFT JOIN` instead,
which is what lets the restore dialog say whether there is anything to put back
and how old it is.

And one more capture of everything on the way down, in `ArchiveAll`. An orderly
reboot stops the unit before it stops anything else, so the panel is the last
thing running that can still read those panes — and the thirty seconds the
ticker might be behind by are exactly the seconds somebody wants tomorrow, being
the last thing that was on screen. A power cut still loses up to half a minute;
nothing can fix that, and the API doc says so.

### Putting it back where scrollback lives

The archive has to end up in the pane's own history, not in a viewer beside it.
Scrolling back through a session is tmux's copy-mode, the panel's own capture
reads the same history, and a second read-only "here is what it used to say"
surface would be a second place to look for one thing.

So the pane's first command prints it:

```sh
f=$1; shift
if [ -f "$f" ]; then cat -- "$f"; rm -f -- "$f"; fi
if [ "$#" -gt 0 ]; then exec "$@"; fi
exec "${SHELL:-/bin/sh}" -l
```

Nothing is interpolated into that script. The archive path and the recorded argv
arrive as positional parameters, so a directory with a quote in it, a command
with a space in it, or output containing anything at all cannot change what the
shell runs. It was the alternative — building a command string per session —
that made this worth writing as a fixed constant.

The file deletes itself as it is read, which is what makes the restart button
still work on a restored session: `respawn-pane` reuses a pane's original
command, so without the delete it would replay somebody's scrollback a second
time under a banner that had become a lie. `exec` in both branches, so the
pane's process is the agent rather than a shell holding one — otherwise the
per-session CPU meter reports on the wrong tree and `#{pane_dead}` describes the
wrapper.

### The part that cannot be restored, said out loud

The process. An agent that was halfway through a refactor is gone; its context
lived in a process's memory and in a provider's conversation, and neither
survived. Re-running the command starts a new agent that remembers none of it.

This is the failure mode the whole feature is one step away from: old output on
screen, the old name on the tab, a fresh agent underneath, and nobody told. So
it is marked in two places that fail differently.

In the pane, a bilingual banner between the archive and the new process, with
the timestamp the capture was taken. Hard-coded English and Chinese, which is
the one place in this project allowed to be: `web/src/i18n.ts` is the browser's
dictionary, and this text is printed by a shell into a tmux pane by the server,
where there is no browser and no language preference to read. It opens with
`ESC[0m` — the capture carries SGR, its last line can leave the terminal bold or
coloured, and without the reset the banner and then the agent's first output
inherit it.

In the UI, `restoredAt` and a chip in the header, because banners scroll. The
chip's tooltip says everything above the banner belongs to a process that no
longer exists.

`TestRestoredScrollbackComesBackMarkedAsOld` checks both halves in one test on
purpose, and also checks their order: the banner has to be *below* the old
output, or it marks the wrong half. Removing the archive fails it at the old
marker; removing the banner fails it at "The process below is new".

### Offering rather than acting

An automatic restore of everything on boot would launch two dozen agents, each
of them starting to work, each of them costing money, on a machine somebody just
turned on. That is a worse morning than a list of dead rows.

So: a notice, a dialog that spells out the argv and the directory per session
before anything is pressed, and a per-session `restore_on_boot` for the ones
somebody does want back without being asked. `RestoreFlagged` runs at the end of
`Reconcile` — last, after every row has been compared with tmux, because it
reads the mark the loop above it writes.

`POST /api/sessions/restore` takes explicit ids and has no "all" flag. It
answers 200 with one result per id even when some failed: after a reboot the
ordinary failure is one project directory pruned while the machine was off, and
refusing the batch over it would leave twenty-three sessions dead in order to
report one. The dialog stays open holding the failures rather than closing on a
partial success, which is how somebody would otherwise find out tomorrow that
three of the twenty-four never came back.

`restart` on a vanished session now goes through the same path. Its old comment
reasoned carefully about `command` being a label and concluded that a login
shell was the honest fallback; that conclusion was right, and it stopped being
the best answer available the moment the real argv was recorded.

### Simulating the reboot in a test

Two things go at once, and doing only one produces a state a real reboot never
produces. `simulateReboot` kills the tmux server **on the test's own throwaway
socket** and calls `DetachAll`, because every PTY the manager holds is a child
of the panel and dies with it. Leaving the attachments would have the tests pass
or fail for reasons unrelated to restoring.

It also waits. `kill-server` returns before the server has finished going, and a
command arriving in that window gets `server exited unexpectedly` — which is
neither `ErrNoServer` nor `ErrNoSession`, so `Reconcile` propagates it and a
test fails somewhere that has nothing to do with what it was checking. A real
reboot has no such window: there is nothing left running to ask. That cost one
confusing red run to find.

### Mutations run

Ten, each restored afterwards. Every one made its test red:

| mutation | test |
|---|---|
| `handleCreateSession` does not record the argv | the restored pane is a login shell |
| `Restorable` returns false | nothing is ever offered for restoring |
| the restore payload carries no banner | the pane has old output and no marker |
| `archiveSession` returns immediately | there is nothing to put back |
| `RestoreFlagged` ignores `restore_on_boot` | a session nobody asked for starts on boot |
| an unrecorded `launch_command` reports as recorded | a pre-v9 row claims to know its command |
| the batch restore breaks on the first error | one bad id loses the rest of the batch |
| the archive ignores `last_output_at` | an idle session is captured and rewritten every pass |
| the byte cap keeps the front | the end of the session is thrown away |
| the byte cap cuts mid-line | the archive begins inside an escape sequence |
| migration v9 defaults `launch_command` to `'[]'` | every pre-upgrade row claims to be a login shell |

### Left undone, deliberately

- **Scratch terminals are not offered.** They are tabs under a session holding a
  shell somebody opened for a minute, and two dozen of them in the restore
  dialog would bury the choice that matters. They are still archived and still
  restorable through the API; they are just not in the list.
- **The archive is not compressed.** 6 MB at the worst case is affordable and
  gzip would put a decoder between the database and a shell's `cat`. If the
  bound is ever raised this is the first thing to reach for.
- **No browser check covers the dialog.** The restore path is pinned by the Go
  suite against a real tmux; the notice, the dialog and the chip are not driven
  by `render-check`. That is the gap, and it is the kind of gap this project's
  own notes say is where most of the defects have been.

## One box, and a paste that was listening in the wrong place

**"选择目录 比如说 就可以搜索啊 或者我手动打目录行不行"** — and the picker already
did the second half. The field was at the bottom, under the list and the
create-folder row, which is a good answer to a question nobody could see had
been answered. It is one box at the top now, autofocused, and what you type
decides what it does: a leading `/` or `~` is a path to go to, anything else
filters what is on screen. Arrow keys reach the list without leaving the box,
so typing and choosing are one gesture.

Escape clears the filter before it closes the dialog. An Escape that throws
away both makes a filter feel like something you can be trapped by.

**"无法原生粘贴图片"**, and the extraction was never the problem. The terminal
listened for `paste` on its own host element, and a paste event goes to
whatever holds the keyboard — so with focus on a sidebar row, a panel tab, or
nothing at all, which is where it is after almost every click, the event never
reached the terminal and the image went nowhere. It is on `document` now, in
the capture phase, and only for pastes that carry files: a text paste is
somebody's clipboard going into a text field and is left entirely alone.

**And `make check` started failing on a file this checkout does not contain.**
`gofmt -l .` descends into `.claude/worktrees/`, where the agent worktrees live,
so an unformatted file in one of them failed the gate here. Excluded alongside
`web/`.

## Updating the panel from the panel

The most dangerous thing in the product, and the design says so out loud rather
than reading as routine.

`GET /api/update` asks GitHub what the newest release of one **compiled-in**
repository is. Not configurable, and that is the point: a settable update source
is a way to make a panel install a binary of somebody else's choosing with one
database write. `POST /api/update` downloads that release's archive for this
exact GOOS/GOARCH, checks it against the `SHA256SUMS` published with it, unpacks
only the binary — replacing the unit files or the installer under a running
service is a separate decision nobody asked for by pressing "update" — moves the
running binary aside to `.old`, renames the new one into place, and asks systemd
to restart.

**The version is not a parameter.** A request cannot name what to install; the
panel installs the latest release or refuses with 409. The case this closes is
not a typo, it is somebody with a session cookie who would like this panel to
run something else.

What the checksum buys, stated in `docs/api.md` rather than implied: it detects
a corrupt or truncated download and nothing more, because the sums come from the
same release as the archive. Whoever can publish a release can publish sums to
match. That is the same trust anyone gets from `curl | tar`, and the compiled-in
repository is what keeps it bounded.

Three things that are easy to get wrong and are pinned by mutation:

- **A development build must not talk itself into an update.** `dev` is what a
  build without ldflags reports, and it is what runs on the machine this was
  written on. A comparison that treated an unparseable string as "behind" would
  offer an update on every check and replace a working local build with a
  release nobody asked for. Removing the parse guard fails the table.
- **Rename, not truncate.** A running program's file cannot be rewritten in
  place — the kernel refuses with ETXTBSY — but it can be renamed over, because
  the running process holds the old inode until it exits. The old binary is
  moved aside first, so a panel that will not start again is one `mv` from
  working. Replacing that move with a delete fails the test.
- **The symlink is resolved first.** `~/.local/bin/vibepanel` is often a link to
  somewhere versioned, and renaming over the link would replace the link with a
  file and orphan the real binary.

`KillMode=process` in both units is what makes the button safe to press at all:
systemd stops the panel and leaves the tmux server and every agent under it
alone. Without that line this would be a button that kills everybody's work, and
the confirmation says so before it is pressed rather than after.

## The file panel could only take files out

The right-hand file tab listed a directory and offered a download per row, and
that was all it did. Clicking a filename did nothing at all — not a preview, not
an error, nothing — and there was no way to put a file *in*. Both halves of the
gap had the same shape: the panel knew what was in the directory and would not
act on it.

**Uploading already existed, and the file panel could not reach it.**
`POST /api/projects/{id}/upload?path=` has been there since the terminal learned
to take a dropped screenshot, and the browser side of it was two copies of the
same forty lines — one in `Terminal.tsx` for a paste, one in `App.tsx` for a
drop — that had already drifted. The paste copy read `clipboardData.items`,
which is the only place a *pasted* screenshot appears on Chromium; the drop copy
read `dataTransfer.files`, which is the only place a dropped file appears. Each
was right about its own event and blind to the other, and a third copy in the
file panel would have inherited whichever half got copied.

So `components/upload.ts` now holds both: `filesFrom` reads items and falls back
to files, and `uploadFiles` does the request and the three lines of narration
around it. The narration is the part that turned out to matter — `Uploading
${n} file${n === 1 ? '' : 's'}…` was an English literal in `App.tsx`, invisible
to `i18n.untranslated.test.ts` because a template literal is not a string
literal and not prose between tags, and it had been showing English on a Chinese
page for as long as dropping a file has worked. One and many are separate
dictionary entries, following the `monitor.procs`/`monitor.oneProc` precedent,
because "Uploading 1 files" is the plural rule everybody ships by accident.

The panel takes a drop anywhere in the column (`min-h-full`, so the empty space
under a short listing is a target too), a paste, and a button that opens a file
chooser — which is the only one of the three that works on a phone. The paste
listener is on `window`, because a div cannot receive a paste without focus and
nobody clicks a file list before pressing ctrl-V; it takes the event only when
the target is inside the panel or is `document.body`, so a paste aimed at a
focused terminal still goes to the terminal, which uploads next to the session
and types the path instead.

**The preview is where the thinking went.** A preview reached by tapping a row
is a much lower bar than a download reached by clicking a download button, and
one of those rows is a core dump. Without a ceiling, clicking it is a denial of
service against the person who clicked.

Three bounds, because one number does not cover it:

- **8 MiB** (`previewMaxBytes`) on the transfer. An image or a PDF past it is
  `413` — half a picture draws nothing, so there is nothing to truncate to. The
  number is chosen against what the feature is for: a screenshot off a 5K
  display is two to four megabytes, a scanned page is a few, source is
  kilobytes. Far below `maxUploadBytes`, which is 256 MiB, and the difference is
  the point: an upload is a file you chose, a preview is a file you brushed
  against.
- **256 KiB** on text, which is truncated rather than refused. That is what
  makes a two-gigabyte log worth clicking: what anybody wants from it is the
  top, and the top is the only part ever read.
- **4000 lines**, and this one is not redundant. A quarter of a megabyte of
  `a\n` is a quarter of a million lines, and a wrapped monospace block of those
  in a 280px column is hundreds of megabytes of layout boxes — the byte budget
  bounds the transfer and does nothing at all about the work.

The browser holds the 8 MiB too, so clicking a two-gigabyte file is answered
from the size the listing already carries with no request at all. That copy is
not the enforcement and is not offered as one;
`TestThePreviewBoundIsTheSameOnBothSides` reads `preview.ts` and compares, which
is the same shape as the state enum and its mirrors — a definition with a copy
somewhere no compiler looks. The drift it catches is the quiet one: raise the
server's limit alone and the panel goes on refusing files it would now serve.

**The kind comes from the bytes, never from the name**, and in a directory an
agent writes into that is not fussiness: a log written as `output.dat` is text,
a `notes.txt` holding a truncated tarball is not, and half the files worth
reading — `Makefile`, `Dockerfile`, a shell script — have no extension to
consult. `browse.SniffMagic` matches PNG, JPEG, GIF, WebP, AVIF and PDF;
`browse.IsText` is git's NUL heuristic plus a UTF-8 check. The UTF-8 half costs
something worth naming: a README in CP1252 is refused rather than shown, and
that is deliberate, because the alternative is a screen of U+FFFD that reads as
the panel being broken. "No preview, here is the file" is the smaller lie.

Two formats are deliberately absent. **SVG** is read as text: it is a document
with scripting in it, and the two ways to show one at a useful size — an
`<object>` or an `<iframe>` — run that script on the panel's own origin, against
a file that arrived because an agent was told to do something. **BMP**'s magic
is the two bytes `BM`, which is also how a great many sentences begin, and a
heuristic that turns a README into a broken image is worse than one that shows
it as text.

Nothing is served inline. The preview response is `application/octet-stream`
with `nosniff` and an `attachment` disposition — the same headers as the
download, which is why they are one function now — and the bytes reach the
browser through `fetch`. What they *are* is decided by the `Blob` type the
browser side builds, from the kind rather than echoed from the header:
`blobTypeFor` will not let a response name `text/html` where an image was
promised. That is the one place a file out of a project directory could have
been handed to the browser as something to run.

**The preview is a modal over the whole window, and the panel's width is what
decides that.** It is 280px by default and 200 at its narrowest. A picture at
that width is a thumbnail with a scrollbar and wrapped source is a column of
five words; the panel's job is finding the file, and reading it is a different
job that wants the window. A takeover of the panel was the other candidate and
costs more than it looks: it has to restore the directory, the scroll position
and the row you were on, and it needs a back affordance that sits next to the
"up one level" control already in that header — two controls that look alike and
mean different things, in a column too narrow to label either. The modal leaves
the list untouched behind it, so closing *is* the restore.

Through `createPortal`, and that is not a preference. The panel carries
`vp-blur`, which is `backdrop-filter`, and an element with a backdrop-filter is
a containing block for `position: fixed` descendants — so a `fixed inset-0`
modal rendered inside the panel is clipped to the panel, which is exactly the
280 pixels it exists to escape.

**Two things fixed on the way past.** The rows were `<div onClick>`, so a
keyboard could not reach them at all — not the directories either, which has
been true since the panel was written. The name is a `<button>` now, and it is
the name rather than the row because a row that is a button cannot contain the
download link: nested interactive elements are invalid and the inner one is
unreachable. And `FileTree` still had four English literals in it — "Reading…",
"Showing N of M items", the download title and the "outside" badge, the last of
which had a translated `files.escapes` entry sitting unused in the dictionary
since the day it was added.

Left undone on purpose: syntax highlighting, which would be a dependency for a
thing nobody reads a file this way to get; and a browser check for the modal
itself. The testids are in place for one — `file-open`, `file-upload`,
`file-chooser`, `file-drop-overlay`, `file-note`, `file-preview`,
`preview-text`, `preview-image`, `preview-pdf`, `preview-truncated`,
`preview-unavailable`, `preview-download`, `preview-close`, `preview-backdrop` —
and the drop, the paste and the portal are all things only a real browser can
answer, so they are asserted by nothing today.

## A second door, and the shape it had to be

A read-only link that opens a dashboard on another monitor: machine load,
per-session CPU and memory, every session with its state, grouped by project.
The request was `我希望可以生成只读链接 … 然后在我的其他显示屏上打开 同时能看见且断开链接状态`,
and the last clause of it is the part that shaped the page.

**The authorisation design, and what it is not.** The obvious implementation is
a `scope` column on `api_tokens` and a `readOnly` check in each handler. That is
a hole in whichever handler is written next year, and the handler written next
year is the one nobody re-reads this decision for. So: a separate table, and one
route.

`share_links` stores a SHA-256 and never the token, exactly as `api_tokens`
does, plus a prefix in the clear so a row can be named on the way to being
revoked. `currentUser` does not consult it — which is the property that makes
the whole thing work, because it means a share token presented as a `Bearer`
header or as the session cookie is not a credential at all. It is an unknown
string, and every authenticated route already answers 401 to those.
`TestAShareTokenReachesTheDashboardAndNothingElse` presents it both ways against
nine routes and the WebSocket.

The route lives outside the `RequireAuth` group with its own middleware, and
`registerShareRoutes` mounts exactly one `GET` under it. `POST` to the same path
is a 405 and `/api/share/{token}/state` is a 404, both asserted, because "the
share surface is one GET" is only a fact while nothing has been added to it.

Two things were nearly got wrong and are worth writing down. The allowlist check
is in the share middleware, in the same order `RequireAuth` does it: without it,
creating a link would have been a way around `--allow-from`, which is the
hardening the operator turned on deliberately. And an unresolvable token answers
503 rather than 401 when the *database* is the thing that failed — a 401 there
makes a wall display say "this link was revoked" about a disk hiccup, and there
is nobody standing at a wall display to know better.

**What the link discloses, and the one judgement call.** The redaction is a set
of structs in `internal/httpapi/share.go` rather than an omission at each call
site, so what is absent is reviewable in one place. Absent in every mode: the
project path, the session `cwd`, the command line, the tmux session name, the
hostname, the sampler's disk path, and the panel's own ids. A project path names
a customer and a home directory. A command line carries whatever an agent was
invoked with.

`sysmon.Sample` is not embedded, and that is deliberate rather than tidy: it
carries `DiskPath`, which is the data directory. Restating the numbers means the
next field added to `Sample` is not disclosed by default.

The ids are pseudonyms — an HMAC of the real id under the link's own stored hash
— which costs nothing and buys two things: React gets a stable key, and two
dashboards on two walls cannot be joined into one picture of the panel by
somebody who watches both.

The judgement call is names. A session title is written by an agent and a
project name is typed by the owner, and either can carry a customer or a
repository; both are also what makes a wall useful. So it is a decision per
link: `counts` shows shapes and numbers and no text, `names` adds the two.
`counts` is the default, because the default is what a link made in a hurry gets
and this one is going on a screen chosen because other people can see it. An
unrecognised `detail` is a 400 rather than a default, the same rule
`?agent=claude|codex` follows and for the same reason.

**Bottom terminals are left out**, which is a content decision and not a privacy
one. They are session rows with a parent, so listing them reports two rows for
one job and counts a shell sitting at a prompt as something that finished. What
they cost is still on screen, in the machine meters.

**The page polls; it does not hold a socket.** `/ws` is authorised once at the
handshake and then lives for hours, which is why it needed `StillAuthorized` and
a revalidation tick. A dashboard reading six numbers does not justify a second
copy of that machinery, and revocation-on-next-poll is a property that needs no
machinery at all. It also means the read-only page has no write path to
accidentally leave open: there is nothing to write to.

**Connection state, which is what was actually asked for.** A dashboard that has
silently frozen looks exactly like a quiet system — six sessions all "done" and
a flat CPU line is either a calm afternoon or a page that stopped talking forty
minutes ago, and nothing about the numbers says which.

Four states, not two: `connecting`, `live`, `reconnecting` (a failure within ten
seconds of the last success), `disconnected`, and `gone` for a link that has
been revoked or has expired. `gone` is terminal and stops the polling: it is not
going to start working, saying "reconnecting" about it forever is a lie somebody
eventually acts on, and continuing to ask would be an unauthenticated request in
a loop against an endpoint that records rejections.

Each carries a shape as well as a hue, per red line 4 — a dot inside a ring, a
ring with a gap, a ring struck through, a broken chain — with the word beside it
at the largest size on the page. The header always shows the time of the reading
and how long ago that was, counted by a one-second timer that is deliberately
independent of the polling: if the two shared a clock, the "as of" line would
freeze at exactly the moment the numbers did. Anything other than `live` puts a
band across the top and dims the content to 55%. Dimmed and not hidden — the
last true reading is still the most useful thing on the screen, it just must not
be presented as this moment's.

**Three new steps on the type scale.** The five that exist top out at 15px,
which is a caption at three metres, and the alternative is an arbitrary
`text-[42px]` in one component — the tenth font size arriving by exactly the
route `scale.test.ts` exists to close. `--text-vp-xl|2xl|3xl` are in the
`@theme` block with a comment saying they are the dashboard's and nothing
else's. They are `clamp()` against `vw` rather than fixed, which is the
difference between "legible at 1080p" and "legible at 1080p and at 4K": a 4K
panel at 100% reports 3840 CSS pixels, so a fixed 56px headline has a third of
the angular size it has on a 1920 screen at the same distance — and the distance
is the reason the display was chosen.

**The dashboard is a separate root, not the panel with pieces hidden.**
`main.tsx` reads `/share/<token>` off the path and builds either `<Dashboard>`
or `<AuthGate><App>`. `AuthGate` is what asks who you are and then hands over
the whole console; a read-only page must not be one `if` away from it. The token
is read from `location.pathname` and used in one fetch — never stored, never
sent anywhere else, never rendered. `Referrer-Policy: no-referrer` was already
set, which is what stops a URL-borne capability leaking to whatever a link on
the page points at.

## Counting tokens, and finding out the count was wrong twice before it was right

**"我希望右边 tab 可以统计 token"** — a fifth panel tab for token spend, per session,
per day, in total, filtered by project, tool and time, with a monthly view and
a GitHub-style year grid.

The chart was the easy half. The question that took the day was where a number
that is not a guess comes from at all.

### It comes out of the agents' own files, or it does not come

There is exactly one honest source: the transcripts the agents write for
themselves. Claude Code keeps `~/.claude/projects/**/*.jsonl`, one record per
line, and every assistant record carries a `usage` object with the real input,
output and cache counts the API billed. Codex keeps
`~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl` and emits an
`event_msg`/`token_count` payload carrying both `last_token_usage` and a running
`total_token_usage`.

Everything else that was considered is an estimator, and there is no estimator
in this. Counting characters and dividing by four produces a number that looks
like a measurement, is not one, and is never checked again because it already
looks right. Where a source cannot be read the answer is "unknown" and the
reason travels with it, all the way to the screen.

The cost of that honesty is a sentence the UI has to keep saying: **these are
the agents' numbers, not the panel's.** A `claude` run in a terminal this panel
never started is counted. A session the panel *did* start that wrote no
transcript is not. Neither agent publishes the id of the transcript it is
writing, so there is no mapping from a transcript back to a `sessions` row —
`launch_command` and `cwd` are what the panel has, and a cwd is shared by every
session in a project. So the unit reported is the agent's own session, said in
as many words on the panel, in the full view and in `docs/api.md`.

`cwd` *is* enough to place work in a project, and that is done at query time
rather than at ingest: projects are created and renamed long after the
transcripts were written, and baking the answer in would mean a project added
today never sees the history that belongs to it. Directories that match no
project keep a row of their own — dropping them would make the project table
disagree with the total above it by an amount nothing explains.

### Two counting bugs that both flatter

**Claude Code writes one line per content block, and every line carries the same
`usage` object.** One response with a thinking block and a text block is two
lines, both saying 242 output tokens. Measured on one real 89 MB transcript:
13,869 usage-bearing lines for 6,563 actual requests, and 14,118,636 "output
tokens" against a true 5,954,333. An over-count of 2.37x, in the direction
nobody questions. Across all 430 Claude transcripts on this machine, 125,102
lines for 67,339 requests.

So records are deduplicated on `(message.id, requestId)`. The interesting part
is *how far apart* the duplicates sit, because that decides the whole storage
design. Measured across every transcript: 57,296 duplicates are adjacent, and
466 sit **exactly 1,787 usage-lines apart**, all in one file. That second shape
is a resumed session — Claude Code replays the entire history back into the same
transcript. A sliding window of any affordable size passes the adjacent case
and silently double-counts the replayed prefix, which is the failure that looks
correct.

**Codex re-emits `token_count` with an unchanged `last_token_usage` when only
the rate limits moved.** Summing `last` over this machine's largest rollout
gives 53,309,297 tokens where Codex's own final `total_token_usage` says
52,519,697 — 1.5% high. Differencing the running total reproduces that figure
*to the token*, which is the check that settled it: what the panel reports for
a whole thread is exactly what Codex last wrote down about it. A duplicate event
contributes a delta of zero and needs no dedupe table at all.

**And the two agents do not mean the same thing by `input_tokens`.** Claude's
excludes what came from cache; Codex's includes it. Added into one column, this
machine's largest Codex thread reads as 52.4M tokens of fresh input where 50.7M
of them were cache reads. Everything is normalised to Claude's split, once, in
the reader.

### Why the cursor is a whole file and not a byte offset

The obvious incremental design is `(path, offset)`: read only the bytes that are
new. It is wrong here, and the 1,787-line replay is why. Deduplicating across a
resume boundary needs the keys from *before* the offset, and there are 67,339 of
them — more state to carry than re-reading costs.

So `usage_files` stores `(size, modified_at)` and a changed file is re-read
whole, its previous contribution deleted and replaced inside one transaction.
That is what makes reading a file twice harmless, and it is a property with its
own test, because the failure without it is that every *active* session's
numbers creep upward on every pass — slowly, plausibly, and only for the
sessions somebody is using.

Measured on this machine, 568 transcripts totalling 2.16 GB:

| pass | files read | time |
|---|---|---|
| first | 568 of 568 | 3.09 s |
| nothing changed since | 0 of 568 | 35 ms |
| one 395 MB transcript grew | 1 of 568 | 539 ms |

618–810 MB/s, which is what the `bytes.Contains` prefilter buys: most lines in a
transcript are user turns and tool results with no usage object anywhere in
them, and `json.Unmarshal` on those is where a pass would otherwise spend nearly
all of its time. `TestIngestPassCost` and `TestFullPassCost` are how those
numbers were taken and are skipped unless `VIBEPANEL_USAGE_BENCH` is set — a
test whose result depends on the developer's own history is not a gate, but the
stopwatch needs somewhere to live.

The facts are rolled up to `(day, agent session, model)` at read time, in
`usage_daily`. A row per API request is the obvious shape and would be roughly
650,000 rows and 40 MB a year on this machine — larger than everything else in
the database together — to answer questions that are all per-day anyway.

A day is a **local** day. Transcripts stamp UTC, and at UTC+8 bucketing by the
raw stamp moves a whole working morning onto the previous date: the total stays
right and only the bars move, which is the shape of error nobody notices.

### Where it went on screen

The panel is 280 pixels. A 53-week year grid is about 580 before a day-of-week
gutter, and the per-session table has six columns. So the tab holds the glance —
today, the range, a thirty-day sparkline, and the reason any figure might be
missing — and a button opens a full-width overlay with the grid, the filters and
the four tables. The alternative was three months of grid instead of a year, or
squares too small to aim at.

The tokens tab is the only one that renders **before** the no-project guard. The
other four are about a project; token spend is a fact about the machine, and an
agent run in a directory the panel has never been told about is exactly the case
that hiding the tab would hide.

Colour is not doing any work alone (red line 4). Every square carries its exact
figure on hover, on focus and in `aria-label`; every square is tab-reachable;
the legend says what the shades mean in words. A day the range never covered is
drawn as a dashed outline rather than a fainter fill — a different *shape* from
the legend's leftmost square, which is a day that was covered and empty. And the
shades are `--vp-accent` at five opacities rather than a fixed palette, so the
grid follows the theme instead of becoming the white-on-white failure with an
extra step.

The quantiles for those five shades come from the non-empty days only. Including
the empty ones, on a year that is mostly empty, puts every cut at zero and
paints every working day the darkest shade.

Nothing from inside a transcript is ever served. The counts leave the machine's
own process; the words do not, and `TestNoTranscriptContentIsEverServed` writes
a passphrase into a transcript and fails if it appears anywhere in the response.
Reading outside the data directory is a new capability for this panel, so the
walk resolves its root through `EvalSymlinks`, refuses anything that is not a
regular file, and checks every path against the resolved root anyway.

### The mutation run, and the two things it caught

Twenty-eight mutations, each restored afterwards. Twenty-six went red
immediately. Two did not, and both were the test's fault rather than the code's:

**`eachLine` had a real hole.** The mutation "an overlong record ends the file"
passed, which should have been impossible. The cap on a record's length was
checked only while the buffer was *filling*, never on the final chunk — so a
record that filled the 256 KiB buffer an exact number of times and then grew
past 16 MiB on its last piece sailed through. The test had been passing for an
unrelated reason (the giant line had no timestamp, so it was skipped as
unparseable). Fixed, and the fix is what the mutation now proves.

**The symlink test was pinning the standard library, not this package.** It
built a symlinked *directory* pointing outside the root — which `filepath.WalkDir`
declines to descend into on its own, so removing this package's file-type check
changed nothing. It now also builds a symlinked *file* named `linked.jsonl`,
where the only thing between the walk and a file outside the root is the
`IsRegular` check. Both halves are asserted, because they are stopped by
different things and only one of them is stopped by code in this repository.

| mutation | test |
|---|---|
| the Claude dedupe is removed | one response counts as three |
| a sliding window replaces the whole-file seen-set | a replayed prefix counts twice |
| Codex input keeps its cached part | 50.7M cache reads become fresh input |
| Codex sums `last` instead of differencing `total` | a repeated event becomes a request |
| a decreasing Codex total is not treated as a reset | a negative token count |
| days are bucketed in UTC | a working morning moves to the previous date |
| an overlong record ends the file | everything after it is lost, silently |
| a missing directory reports as read | Codex says zero instead of "not found" |
| the file-type check goes | a symlink leads out of the root |
| the ingest cursor is ignored | every pass reads 2.16 GB |
| a re-read is added to the old rows rather than replacing them | active sessions creep upward |
| a deleted transcript keeps its numbers | totals nothing on disk supports |
| the project filter is a string prefix | `/home/me/api-v2` folds into `/home/me/api` |
| the project filter is a LIKE pattern | a `%` in a path matches its siblings |
| months are cut to years | the monthly chart is an annual one |
| a tool with no rows is left out of the file stats | "read and empty" looks like "never read" |
| the capped session list does not say it is capped | a partial list implies it is complete |
| an unknown project id is accepted | an empty chart looks like a quiet week |
| the heatmap follows the range control | a 53-week grid holding seven days |
| work outside every project is dropped | the project table stops adding up |
| a tool with no spend is left off the screen | an agent's absence has no explanation |
| the response stops saying which day is today | a phone abroad highlights the wrong square |
| transcript text reaches the response | the panel serves conversations |
| a field leaves `wire.ts` | it is `undefined` at runtime and nothing says so |
| a route leaves `docs/api.md` | nobody can find the endpoint |
| the heatmap quantiles count the empty days | every working day is the darkest shade |
| `today` is parsed as UTC | the whole grid shifts by one square |
| tomorrow is drawn as an empty day | a claim about the future |
| a day outside the range becomes a zero | "not read" and "nothing spent" look alike |

### Left undone

- **No browser check covers any of it.** The Go and vitest suites pin the
  arithmetic, the containment, the API and the grid layout; nothing drives the
  tab, the overlay, the filters or the heatmap in a real browser. That is the
  gap, and it is the kind this project's own notes say is where most of the
  defects have been. `render-check` would want `panel-tab-tokens`,
  `token-panel`, `token-spark`, `token-open`, `token-refresh`, `token-view`,
  `token-view-close`, `token-filters`, `token-filter-project`,
  `token-filter-tool`, `token-range-7|30|90|365`, `token-totals`,
  `token-heatmap` (whose squares carry `data-level`), `token-session-row`.
- **Only two agents are read.** Anything else reports nothing, and says so only
  by not appearing — there is no row for "an agent this panel has never heard
  of". Adding one means adding a reader, which is the right shape.
- **No cost in money.** Prices differ per model, per tier and over time, and a
  currency figure derived from a stale table is exactly the confident-looking
  wrong number this whole entry is about. The token columns are what the
  transcripts actually say.
- **The ingester reads the home directory of whoever the panel runs as.** Under
  the system unit that is a different account with no transcripts, and the panel
  reports "not found" rather than zero — correct, but the runbook does not yet
  say that is what an operator seeing it should expect.

## The check that hid the defect it was written to find

`render-check` failed on the phone drag after the token-usage merge, and the
failure had nothing to do with tokens. The touch layer measures a row's height
by reading `.xterm-rows` — the DOM renderer's grid of spans — and divides by it.
Under the renderer that actually ships that element exists and has **no height
at all**, so the drag scrolled zero rows and the selection could not find a
cell. On a real phone the terminal had stopped scrolling and stopped being
selectable the moment the WebGL renderer was loaded.

It did not fail then, because of a decision made in the same commit. Two checks
needed DOM geometry, so their contexts were pinned to the DOM renderer — which
meant the phone paths were only ever exercised against a renderer nobody runs.
The pin was reasonable and the consequence was not: a check pinned to the thing
it is not testing will keep passing about the thing it is.

Both are gone now. `.xterm-screen` is the element both renderers draw into and
is the same box, so the touch layer measures that. The marker's position in
render-check is computed from the buffer index and the screen's rect instead of
looked up on an element, so that context runs the shipped renderer too.
Restoring the `.xterm-rows` read fails two assertions — "pressing and holding
selected nothing; there is no way to copy from a phone" and the drag — which is
the pair that should have failed the first time.

## Three checks that read the terminal through the DOM, found one at a time

The renderer change kept surfacing, and each time in a check that had not been
looked at yet.

**tls-check** read `.xterm-screen`'s `innerText` and got `""` for a terminal
that was working perfectly, then reported it as *"nothing typed over TLS reached
the session; the WebSocket did not work"* and *"an open session stopped working
when the certificate was replaced"* — two alarming sentences about a feature
that was fine. It goes through the buffer reader now, like the other four.

**scale-check** waited for "any terminal has content", which is answered by the
terminal that was already on screen, so the wait either returned instantly or
sat out its full fifteen-second timeout — and reported *that* as the session
switch taking fifteen seconds. Now it asks about the session it clicked, by id,
which meant giving a session row its id in the DOM.

And then it still failed, for a second reason worth separating: **"has content"
is the wrong question**. A seeded session that has printed nothing never
satisfies it. The reader answers `null` for a session with no terminal and an
array — possibly of empty rows — for one that has it, so the probe waits for the
mount, which is what "switching took N ms" means.

**head-check** found the committed frontend bundle is not what the sources
build. `internal/webui/dist` is tracked and embedded, so `go build` from a fresh
clone was shipping an older frontend than the repository's own source. Rebuilt
and committed.

## The picker was five decisions in a box, taken on five different days

**"选择目录那个控件我认为仍然得完全重构"**, and it was right for a reason a
repaint would not have touched: every piece of that dialog was correct on its
own and none of them had been decided together. A list. A filter box added over
it the day somebody asked whether they could search. A create-folder row that
was a mode toggle at the bottom. Two buttons under that. A path at the top that
was a *title* — the one thing in the dialog that says where you are, and it was
the only thing you could not click.

Five decisions stacked vertically read as five products. The content — the
directories, the only reason the dialog exists — was fourth in line for the
eye.

**It is one list with one field over it now, and the field is one control doing
what three used to.** The crumbs live inside it, to the left of the caret:
click a segment to go there, type to filter what is on screen, or type
something starting with `/` or `~` and the crumbs step aside, because you have
stopped searching a place and started addressing one. A breadcrumb you can
click and a path you can edit are the same fact — every file dialog worth using
treats them as one control, and this one had them as a title and a text box at
opposite ends of the dialog.

The title bar is gone. A dialog whose subject is a list should open with the
list nearest the eye, and a heading reading "Choose a directory" over a control
that is visibly a directory chooser spends a row of the only thing anybody came
to read. It is an `aria-label` now, which is worth something to a screen reader
and nothing to the layout.

**The mode is on screen, in words, before you commit to it.** The old confirm
button said "use this directory" and silently took the *typed* path when the
box held one — the visible directory and the taken directory could differ with
nothing saying so. The button now says which: `进入` for a path under home that
the picker can list, `用这个路径` for one outside it that it cannot. Outside is
not an error and does not read as one: a project under `/srv` or `/opt` is
ordinary, the server roots the listing at home for noise rather than for
safety, and "there is nothing to show you" is a reason to accept the path
rather than to refuse it.

**Motion says which direction you went.** Going into a directory and coming
back out redrew identically — one frame of a list, then one frame of a
different list — so the only way to find out which had happened was to re-read
the path. Deeper arrives from the right, up arrives from the left, on the same
curve as everything else; a typed path gets neither, because it is not a step
in the hierarchy. `prefers-reduced-motion` removes all three, which is why the
crumb bar states where you are at all times rather than only while moving.

**Refusals moved to whatever was refused.** One strip above the buttons used to
hold every one of them: a path that does not exist, a folder name already
taken, a directory the server would not list. Now the refusal carries where it
came from, and lands under the field, under the new-folder row, or above the
confirm button.

**Loading and empty are drawn rather than fallen into.** Four skeleton rows the
shape of the rows that are coming, so the list does not jump when they arrive,
plus the word for what is happening — the shimmer is gone under reduced motion
and grey bars alone could be an empty directory rendered badly. An empty
directory and a filter that matched nothing are different sentences and each
offers the folder you would make next, named after what you typed.

**The keyboard has one rule now, and it is a sentence.** One box and one list
share one focus, so every key belongs to exactly one of them: *the box owns the
keys that edit text as soon as there is text to edit.* Empty box, and Backspace
goes up a level the way it does in a browser; text in it, and Backspace deletes
a character. Home and End are the deliberate exception while filtering — a
filter is five characters and Home there moves a caret across three of them,
while in a directory of two hundred entries it moves you two hundred rows. In
path mode they go back to being text keys, because a path is long and worth
editing.

**All of that is in `web/src/components/dirpicker.ts`, and that is the actual
change.** What the text means, where a path sits relative to the root, which
rows survive a filter and in what order, what every key does: they were spread
through a component that could only be checked by opening a browser and
looking, which is how `Home` came to move a caret in one mode and a selection
in the other with nothing saying which. Thirty-seven tests, and nineteen
mutations run against them — removing the `~` expansion, the `..` collapse, the
prefix-first ordering, the clamp at the ends of the list, the difference
between `go` and `use` — each one fails at least one test.

Two of those mutations are about the ends nothing else covers. The picker is
driven from `web/scripts` by `data-testid`, and those scripts start a browser:
a renamed testid is a failure twenty minutes into a run somebody started for
another reason, reading as the feature being broken rather than as the handle
having moved. So the test scans the scripts for every `dir-` testid they reach
for and checks the component still has it, and checks that every class the
picker's motion is in has a rule in `styles.css` — two files that have to agree
with nothing between them, because house rule keeps component motion out of
components.

**One thing was found and left alone.** `browse.Dirs` skips entries beginning
with a dot, so `~/.config/x` cannot be reached by clicking — but it can be
reached by typing, which is exactly what the field is for. A "show hidden"
toggle is a server change and another control in a dialog this entry exists to
take controls out of.
