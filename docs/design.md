# Why it is built this way

The decisions in this document are the ones that would look arbitrary from the
outside, and each of them cost something to learn. `docs/build-log.md` has the
chronological version with the failures attached; this is the shorter argument,
ordered by how much of the product depends on it.

`AGENTS.md` states the same ground as red lines: rules for anyone changing the
code. This page is the reasoning behind them, for anyone deciding whether to
trust the thing.

---

## The split: tmux does persistence, the browser does organisation

Run a dozen coding agents in a terminal multiplexer and you get a flat strip of
tabs called `bash`. You cannot tell which agent is blocked on a confirmation and
which is still working without visiting each one. Tabs belonging to one project
sit between tabs from five others. None of it is usable from a phone.

That is a task-management problem wearing a terminal costume, and the two halves
want different technology. Process persistence is solved, and tmux solved it:
detached sessions, a server that outlives its clients, scrollback, resize. What
tmux has no opinion about is which of your twelve sessions needs a human right
now.

So vibepanel adds nothing to the persistence half and everything to the other
one. Sessions are grouped into projects, named by you and kept named, sorted by
urgency, and given a status you can read across a room. All of that lives in
SQLite next to the panel; none of it lives in tmux, which is why losing the
panel loses none of it.

## The panel never owns a session's PTY

This is the property everything else is arranged around.

The panel runs `tmux attach` as a client, exactly the way you would. It never
forks an agent itself, so no agent is ever a child of the Go process. Stop the
panel, `kill -9` the panel, replace its binary, reboot its container; the tmux
server and every process under it carry on, because nothing about their lifetime
ran through the program you just stopped.

Systemd nearly took it away through the deployment rather than the code. tmux's
server is started by the panel and daemonises, but cgroup membership does not
change on re-parenting, so the server sits in the unit's cgroup and the default
`KillMode=control-group` SIGTERMs everything in it. Measured on a throwaway unit
and socket with two sessions and a `systemctl --user stop`:

```
KillMode default (control-group)  ->  2 sessions before, 0 after
KillMode=mixed                    ->  2 sessions before, 0 after
KillMode=process                  ->  2 sessions before, 2 after
```

`mixed` is the trap: it reads like the careful middle option and kills them too,
because the SIGKILL phase still goes to the whole cgroup after the main process
exits. Both shipped units set `KillMode=process`, and that one line is what makes
`systemctl restart vibepanel` a non-event.

The same measuring exercise produced the second systemd unit. A memory squeeze
should reach the panel and its tmux server last, and the directive for that does
not work where the default install puts it:

```
a user unit with OOMScoreAdjust=-500   ->  the process reads 100
a system unit with User= and the same  ->  the process reads -500
```

Lowering `oom_score_adj` needs `CAP_SYS_RESOURCE`, which a user manager does not
have, and `systemd-analyze verify` accepts the directive either way: a setting
that looks applied, passes its own check and does nothing. So the shipped user
unit omits it entirely and uses the knobs that do work unprivileged
(`CPUWeight`, `IOWeight`, `ManagedOOMPreference`), and
`deploy/vibepanel-system.service` exists for machines that need the real thing.
What it protects is the tmux server, which inherits the score and holds every
session. The agents inherit it too, which is the cost; `MemoryMax` is what bounds
that, since the cgroup's own OOM killer picks the largest process inside.

The other half of the promise is the socket. The panel runs with
`-L <socket>` and its own `-f` config file, never the default socket, so it can
sit beside a tmux or zellij setup with weeks-old sessions in it and touch none of
them. `vibepanel doctor` asserts that: every session on the socket must be one of
ours, and a foreign one is a failure rather than a note.

## One authoritative grid per session

A desktop at 200×50 and a phone at 45×20 cannot both be the size of the same
terminal. The alternatives are reflow — which turns an agent's full-screen TUI
into confetti — or one grid that everybody shares.

The panel keeps one grid per session, owned by whoever last interacted with it;
other viewers scale that grid to fit rather than resizing it. Everyone sees the
same bytes in the same cells.

The obvious tmux setting for this, `window-size manual` plus an explicit
`resize-window`, kills the tmux 3.6 server outright:

```
$ tmux -L probe -f <(echo 'setw -g window-size manual') new-session -d /bin/sleep 5
server exited unexpectedly
```

`window-size latest` reaches the same place by another route: the panel attaches
exactly one client per session, so "whichever client was last active" is always
the panel, and no second viewer can shrink the grid because no second viewer is a
tmux client at all.

## *Done* means the process exited, not that a session went quiet

An agent that is thinking, waiting on a slow tool call, or writing somewhere
other than the screen produces no output for as long as it likes. Reporting that
as finished is the panel giving a confident wrong answer to the question it
exists to answer.

So silence is never promoted to *done*. Without a hook installed the heuristic
reads the output stream — recent bytes mean *working*, a terminal bell means
*waiting*, a pane back at a shell prompt means *done* — and a silent running
agent stays *working*, which is true whether it is thinking or asking. The two
signals that actually mean a person is needed, the bell and a hook report,
outrank the heuristic regardless of which fired more recently.

`internal/session/state.go` is the only definition of the enum. Three things
mirror it and none of them share a type system with it, which is why each has a
test pinning it: the TypeScript constants, the SQL sort order, and the state
strings `internal/hooks` writes into files the panel does not own.

## Colour is never the only carrier of meaning

Each state has a shape as well as a hue — circle, triangle, check. People read
this panel at 2am on a phone in a dark room, and some of them cannot tell the
hues apart at any hour.

The same rule is why the share SDK hands a page its connection state as a word —
*live*, *reconnecting*, *disconnected*, *revoked* — along with the time of the
last reading, for a page to print. A screen that has silently
frozen otherwise looks exactly like a quiet machine.

## Files move over HTTP, not through the terminal

In-band transfer protocols fight with full-screen TUIs, and the reason to put a
screenshot on the server is almost always to hand it to the agent. So an upload
lands next to the session and types its absolute path at the prompt, ready to
press enter on. The path being ready is the feature; the transfer is the detail.

Preview sniffs the file's magic bytes rather than trusting its name, and the
image list refuses SVG on purpose: an SVG is a document that can run scripts,
and rendering one on the panel's own origin would run it there. Drawing HTML and
SVG came later and is a separate route, which serves them into a frame with an
opaque origin, no scripts and a policy that allows it no network.

## A read-only share token is narrowed by its route, never by a flag

Share links live in their own table, and `currentUser` does not consult it. That
is the entire security design: a share token presented as a session cookie or a
`Bearer` header is an unrecognised string, and every authenticated route already
answers 401 to those. The routes a share token reaches are `GET`s, three of them,
and a test holds the list: the v1 snapshot, and a share page's files at
`/share/{token}` and below it. It was one data route before share pages and is
one data route again now that boards are gone; the page files are the only
addition, and the next section is why.

Pages and links are made and edited on a page of their own, `/sharing`, behind
the same `AuthGate` and the same cookie as the panel. That is an authenticated
surface in an authenticated container and changes nothing above: the page
reaches the settings routes, and a share token still reaches its five. The one
thing that page cannot do is *open* a page — a project, a terminal, the Preview
— so it hands the page's id to the panel on the panel's own address
(`web/src/routes.ts`), and the panel opens it after its first snapshot.

The alternative — a `scope` or `readOnly` column on the existing token table —
makes every handler in the panel one that has to remember to check a flag, and
the handler that forgets is the one somebody writes next year.

Redaction is the same shape. The share response restates the fields it discloses
instead of embedding `sysmon.Sample` or `store.Session`, so a field added to
either is not disclosed by default. Row ids in the snapshot are
`HMAC(token hash, real id)`: stable within one link, different between links, so
two screens cannot be correlated and neither carries the panel's real ids.

The surface reads working trees now, and the same argument was applied twice
more rather than relaxed. What it asks git for is `--shortstat` and a commit
timestamp, so filenames, subjects, authors and shas cannot leak, because they
are never read, and the *argument list* is what a test pins rather than the
parser. The one outbound request a wall can cause needs four independent
decisions by a signed-in owner and is bounded by a cache that stops the moment
nobody is looking; a ticker is the thing that may not be added to it.

Neither is read on the request goroutine. A wall polls every two seconds
forever, so a poll that runs `git log` is a fork per project per poll: the poll
takes what a background refresh already produced and says how old it is, and
"not counted yet" stays a different answer from zero.

## A share page draws the snapshot; it cannot ask for more of it

The board's limit was its vocabulary, not its data. Every new way of drawing a
number had been a Go widget kind, a React component, a preset and a check
budget, while what a link may disclose was already one fixed, redacted struct.
So the struct is published as `GET /api/share/{token}/v1/snapshot`, a small SDK
polls it, and a page is HTML the owner writes — in practice, HTML an agent in
one of the panel's own sessions writes while a Preview beside it reloads.

Three things keep that from being a way in, and each is a layer a check removes
to watch the page become the owner:

- **The page is sandboxed by its response header**, not by an iframe: `sandbox
  allow-scripts` on every file, so a wall that opens the page top-level gets
  the same opaque origin a frame would. No cookie, no storage, and every request
  to the panel is cross-origin without credentials. Take it away and
  `pages-check` reads the cookie, writes storage and opens a window.
- **`connect-src` names that token's snapshot and nothing else.** Take it away
  as well and the same probe reads `/api/state`, reads the audit log and opens
  the terminal socket. Each layer alone held in that experiment; neither is
  decoration.
- **A page can only subtract.** Its manifest chooses among the snapshot's fixed
  sections, and every option is a switch or a bounded day count (`pages.Needs`),
  so the snapshot builder is the one redaction there is and a page has no
  vocabulary for anything it does not already compute. Which sections are
  computed is a cost decision; it would become a permission one the day a
  manifest field carried a parameter into a query, which is the edit to refuse.
  Parameters are only echoed: a test changes them and asserts every other key of
  the snapshot is identical.

What CSP does not do is stop a determined page sending what it can see
somewhere else. That residue is written down rather than argued away: what a
page can see is the snapshot, which whoever holds the URL can already fetch.

The workflow decisions have reasons too. A preview is a real share link that
lives fifteen minutes, because a signed-in preview route cannot work — the
sandbox that protects the cookie is the same thing that stops the page sending
it — and because a second path to the same bytes would be a preview that could
show what a wall does not. Publishing is a person's action and the scaffolded
instructions tell the agent not to; the agent runs as the same user, so that is
a default and is called one. A trial on a screen ends by itself, decided on
read like expiry, because whoever pressed the button is by definition not
standing at the wall.

## Boards were removed, not kept beside pages

For one release a link could draw either a board or a page, and the settings
section showed both: a board editor with thirty presets under a list of pages,
and a "shows" select on every link choosing between them. Two ways to make the
same screen is two vocabularies to keep redacting, two sets of browser checks,
and a settings page nobody could read. The board was always going to lose — its
limit was the vocabulary — so it went entirely: the editor, the presets, the
widget registry, the dashboard route and the SPA that drew it.

The addresses already on walls did not go with it. At startup, before the
listener, every link that still draws no page is pointed at a page built from
the template closest to what its board showed, one page per owner and template,
published, at the same address with the same detail and scope. The detail and
scope are the disclosure; the drawing was never part of it, so changing the
drawing under a handed-out URL discloses nothing new.

A page lives in `page-<name>` under the pages directory — `<data dir>/pages`
unless the owner chose another, beside the other things the panel writes for its
own use, never the home directory by default — and its project is called
`page-<name>` so the sidebar says what it is. The default is not stored as a
setting, and a directory that cannot be written falls back rather than failing
the page; see share-pages.md. The directory is
a working copy, not the page: the page is its published versions in the
database. So a directory or project that has gone is recovered by **Open**,
which writes the published version back and makes the project again, rather
than by a command somebody has to know.

Two things the list needed once it was the only way in. The panel keeps only a
hash of a link's token, so it can never show an owner the address again; **View**
mints a fifteen-minute unlisted copy of the link — same page, pin, trial,
parameters, detail and scope — which draws the same screen through the same
route. And `/share/<token>` for a dead token used to fall through to the SPA,
which meant a stranger holding a revoked address was handed the panel's bundle
and its sign-in page. It now answers with a static page that says the link no
longer works and carries nothing of the panel's.

## What a screen can show is what the panel wrote down at the time

The read-only screens were empty for a reason that no way of drawing them could
fix: the panel kept *state* and no history. `sessions` carries one
`state_changed_at` and nothing about what came before, so every chart with a
time axis had a single current number to draw. The fix is an append-only row per
state transition, and the shape of that row decides what can honestly be asked
of it.

It is a **flow** log: a row says a session left one state for another, having
been in the first for so many seconds. It does not say how many sessions were
waiting at two o'clock. Reconstructing a stock from a flow needs a starting
census and every event since, and one dropped write makes the reconstruction
wrong in a way nothing can detect, on a screen with nobody standing at it. So
the queue is reported as a duration ("how long did things sit") rather than as a
depth, because that is a flow and is true.

Nothing hung off a state change may run on the poller's goroutine. That loop is
what keeps the panel's idea of every session current, and it is the thing every
other feature is built on; the log is written through a bounded channel whose
producer side is a non-blocking send, so a full queue loses a row in a chart
rather than stopping the panel from knowing what is running.

## What was produced is counted, not reported

"Sessions finished today" and "checklist items ticked today" were the headline
output figures, and both read zero on a real wall. Neither was unlucky: both are
self-reported. A todo is ticked because somebody remembered; a session reaches
`done` because a hook said so, and one left running all day never says it. They
measure whether the panel was *told* something.

Commits, changed lines and merged pull requests are things that exist now and
did not this morning, and anybody can check them against the repository. Lines
are two numbers and never a net one — +1200/−800 is a different day from +400/−0
and the net figure is identical — and they are labelled as change rather than as
output. Work an agent has not committed is invisible to all of it, which is
worth knowing before quoting the number at anybody.

## Token counts come from what the agents wrote down, or not at all

The usage panel reads the transcripts Claude Code and Codex write for
themselves. There is no estimator anywhere in the package: characters divided by
four is a thing that looks like a measurement and is not one.

Three format facts had to be verified rather than assumed, and each changes the
answer. Claude writes one line per content block and every line carries the same
`usage` object: one real 89 MB transcript holds 13,869 usage-bearing lines for
6,563 actual requests, and summing them reports 14.1M output tokens where the
truth is 5.95M: an over-count of 2.37×, in the direction that flatters.
Duplicates come in two shapes, adjacent and exactly 1,787 usage-lines apart (a
session restore replays its history into the same file), so the ingest cursor is
per-file rather than a byte offset; a sliding window catches the first shape and
silently double-counts the second. Codex's `input_tokens` includes the cached
part and Claude's does not,
and without normalising it the largest Codex thread on the development machine
reported 52.4M "new input" of which 50.7M was cache reads.

Nothing here is money. The panel reports tokens and does not price them.

Where a transcript directory is missing, the answer is *unknown*, rendered as an
em-dash. Zero is a real reading and must not be used for "did not look".

## What a restore cannot restore

tmux outlives the panel; it does not outlive the machine. The tmux server is an
ordinary process and its scrollback is in that process's memory, so a reboot
takes both.

What the panel records is enough to rebuild the *session*: the argv it was
created with, its directory, its name and place, and a bounded copy of its
scrollback. What it cannot rebuild is the *process*. An agent's context lived in
that process and in a conversation with a provider, and neither survived the
power going off; re-running the command starts a new agent that remembers none of
it.

The product is not allowed to blur that. The restored pane carries a banner
between the archived scrollback and the new process saying so, and the session
keeps a `restored` mark afterwards, because the banner scrolls away and the fact
does not. The API documentation says the same thing in the same words.

Restore is offered, never automatic, unless you asked for it on a particular
session. A boot that starts two dozen agents at once is a worse failure than a
list to click through.

## A chat app is a two-way notification, and every reply is addressed

The webhook told a phone that a session was waiting. What it could not do was
take the answer back. The chat bridge (`internal/chat`) is the reverse
direction, and three decisions shape it.

**It is three layers, and the seam is a declared capability list.** An adapter
knows one IM's protocol and nothing about sessions; the bridge knows sessions,
handles, rules and the write path into a pane, and nothing about any IM. Between
them is `Adapter` plus `Capabilities`: can it edit a sent message, does it have
buttons, does a quoted reply carry the quoted message's id or only its text,
can it speak first. The bridge never asks which IM it is talking to; it asks
those questions and picks a strategy. Telegram has everything; 飞书 has cards
and edits and calls the panel back; 微信's iLink has none of it and cannot say
a word until the person says one first, so the bridge holds the person's last
context token and counts the pushes it had to drop. A fourth IM is a package
that registers a factory and declares what it can do.

**Everything is private chat.** No groups, threads or topics exist in the
types, so an adapter for an IM that has them has nowhere to put them. One
person, one window, many sessions — which is why every card begins with its
handle, `[3]`, in every rendering: on the IM that quotes by text rather than by
id, that is the address a reply is read back from, and at 2am the first four
characters are all anyone reads.

**Where a reply goes is decided by a rule, in order, and refused when the rule
does not apply.** A quoted card first (a card, not any message with a number
in it: the list the bridge sends when it refuses also has numbers, and a
quote of it must not pick the first); a handle in the text second (`3: …`,
`#3`, `[3]`); then, for words, the focus the person chose and after it the
one session that is waiting, and for a bare yes or no the other way round,
because a yes means the thing that asked. With six sessions something is
nearly always waiting, and while waiting outranked the focus for everything,
every push took over the next sentence. Two waiting sessions and a bare "y" is
refused with the list, even when one of them is focused, because the cost of
asking again is one message and the cost of guessing is a keystroke in the
wrong shell. A quote that names no one session (a reply about nothing, a list)
is never answered for whoever happens to be waiting. A session at a
permission prompt takes no words at all until the prompt is answered: its
dialog reads keys, and Enter after a paste is "allow". What "allow" is —
Enter for Claude Code, `y` for Codex — is a per-tool key profile, editable,
and a tool without one is refused rather than guessed at. Every delivery comes
back as a receipt naming the handle.

**An answer is bound to the request the person saw.** Addressing says which
session; it does not say which request, and a session asks, is answered, and
asks again with the same keys. So every card, list line or reply that shows a
request is recorded against that session message (`chat_outbound.message_id`),
a button carries the message id, and a quoted card names the request it
showed — by record where the IM quotes by id, by the request's words where
微信 quotes by text. An answer that names an older request is refused with the
current one shown; an answer that names none (a bare "y", `3: y`) goes through
only if this person has been shown the current request, and otherwise shows
it. The person is never one keystroke from allowing a command they have not
read. A quote by text is read for the request it shows: of the session's
requests whose whole words are in the quote, the longest. Claude Code's
commands start with `cd <project> &&`, so a prefix comparison read a card for
clearing a cache as the card for deleting `src`; and a containment check read
a card for `rm -rf ~/app/tmp` as one for `rm -rf ~`, because narrow-then-broad
is how an agent escalates a delete. A bare yes goes to the session asking for
permission, not to one waiting on a question, and never to the focus when
nothing asks. A person a rule does not send a request to is not shown it by an
edit of a card they already have either. Once a request ends — answered in a chat, at the laptop, or
replaced by the next — every message that still offers its buttons is edited
to say so. Pushes never move the focus: the focus is what a person chose, and
a push that moved it put the next sentence into whichever session had spoken
last.

**Nothing an agent printed can reach the write path through the advanced
mode.** The headless agent that reads a sentence like "tell the docs one to add
a changelog entry" runs with no tools and sees only the sentence and a table of
handles, titles and states; what it returns is an intent that goes through the
same executor a typed command does, with a confirmation before anything is
sent. The agent that answers questions runs with read-only MCP tools and no way
to reach a pane at all. Its tools are six `GET`s under `/api/chat/tools`,
reachable with a token that exists only in the running process and is refused
on every other route — the same shape as the share token, narrowed by its route
list and pinned by a test — and the session views restate their fields, so
paths, commands and ids are not disclosed by default. The agent's working
directory is the panel's own, with the hook variables stripped, so it is never
mistaken for a session.

The hook script forwards the agent's own document now (the last message, the
prompt it is waiting on, the transcript path) as the request body, with the
state in the query string, so a document the panel cannot read costs the
message and never the state. What arrives is bounded, decoded, stripped of
escape sequences and never interpreted (red line 6), and the panel keeps two
hundred of them per session.

## Sessions run in a scope of their own, and the panel budgets it

Until this existed the tmux server and every session lived in the panel's own
unit, under one `MemoryMax`. On 2026-09-16 a typst compile in one session grew
to 17 GiB, was OOM-killed, was re-run by its agent, and did it again. The unit's
cgroup sat at its limit for minutes: `memory.events max` 6,922,896,
`workingset_refault_file` 464,893,737, `io.pressure full avg300` 49%, 37 OOM
kills. The kernel does not OOM-kill while reclaim makes progress, and evicting
file pages always looks like progress -- including the panel's own binary and
its SQLite pages. So the panel stalled with the session, the database calls
timed out, and the page said "store: get session: context canceled". Raising
the limit from 70% to 95% moves the moment; it does not change what happens at
it.

**Placement.** Sessions are moved out of the panel's unit into a transient
scope -- `vibepanel-sessions.scope` under a user manager,
`vibepanel-sessions-<uid>.scope` under the system one, where every account's
units share a namespace, and a socket suffix for any socket but the default --
with this inside it:

    vibepanel-sessions.scope/   MemoryMax backstop (95%), MemorySwapMax=0
    └── pool/                   memory.max = the budget the panel sets
        ├── tmux/               the server; memory.min 256 MiB, cpu.weight 1000
        ├── other/              what cannot be traced to a session
        └── s-<tmux name>/      one per session: memory, pressure, freezer

Measured before building it: a stand-in panel probing its file pages kept 1470
of ~1480 probes while the sessions beside it thrashed (33-41 million refaults),
against 458-529 when they shared a cgroup.

**Why a scope and not a delegated service.** The first design was
`Delegate=yes` and `DelegateSubgroup=panel` on the panel's own unit. It cannot
be restarted: systemd spawns a service's main process into the unit's cgroup and
only then moves it into the subgroup (`src/core/execute.c`), and cgroup v2
refuses a process in a cgroup whose subtree has controllers enabled. With a
session alive, `systemctl restart vibepanel` failed with "Failed to spawn
executor: Device or resource busy" on systemd 259. A scope has no main process
and is never spawned into, so the panel's unit stays an ordinary, restartable
one, and the sessions are not inside it at all.

**Why a pool inside the scope.** Delegation hands over the inside of a cgroup,
not its own limits: the scope's `memory.max` stays root's (or systemd's) to
write. The budget the person sets goes one level down, where the panel may
change it without root, and the scope's `MemoryMax` is the backstop for when the
panel is not running.

**Who moves the processes.** cgroup v2 checks write access on the common
ancestor of the two cgroups a process moves between.

- A **user unit** does it itself: both cgroups are under `user@<uid>.service`,
  which is the user's. The scope is created by running `vibepanel service
  anchor` in it through `systemd-run --user --scope`, not with `busctl --user`:
  a server without `dbus-user-session` (Ubuntu 22.04's minimal image) answers
  `systemctl --user` and has no user bus. The anchor also keeps the scope
  alive while no tmux server is.
- A **system unit** cannot: the two cgroups meet at `system.slice`. So the unit
  runs `ExecStartPre=-+vibepanel service prepare --as <user>` -- as root, and
  never able to stop the panel starting. It starts tmux as the account if there
  is none, creates the scope with `busctl` (with `User=`, or by chowning the
  delegation files itself on systemd 249, which refuses `User=` on a scope), and
  moves everything left in the unit's cgroup into it. That last step is the
  upgrade: a machine whose sessions were in the panel's unit has them moved at
  the first restart after the new unit is written, without any of them
  restarting.

  It is root, and the unit it runs in reads the account's own env file, so it
  trusts nothing the account controls. The security review found two ways the
  first version did: a `PATH` line in that file made root run the account's
  own "systemctl", and root moved whatever pid the tmux query answered -- which
  the account decides -- into a cgroup the account can then `cgroup.kill`. Now
  the environment is read for two values and cleared, `PATH` is fixed, the
  account comes from `--as` in the unit file, every process is checked to be
  wholly the account's (all four uids) before and after it is moved, the scope
  is handed over only if everything in it is, and the cgroup path systemd
  reports is checked to be under `system.slice` before anything is chowned.
- A panel that is not a service -- started by hand, or from inside one of its
  own sessions -- moves nothing. That is read from its own cgroup path, not
  from `INVOCATION_ID`, which a shell inherits.

`scripts/isolation-check.sh` runs all of this against real systemd 249, 252
and 259 as PID 1.

**tmux moves panes too.** tmux built with systemd support puts each new pane in
a `tmux-spawn-<uuid>.scope` of the user manager, where it can reach the user
bus -- under a user unit, every pane escaped the sessions' scope. The panel pulls
a pane back from a scope of exactly that name beside its own, and nothing else.

**Sorting.** A process belongs to the session whose pane process it descends
from; one that has left the tree (nohup, setsid, a double fork) still carries
`TMUX_PANE`, checked against `TMUX`'s server pid so a pane of the person's own
tmux is never taken. Each sorted process gets its `oom_score_adj` raised back to
0: the system unit's -500 was inherited by every agent, which left a machine-wide
OOM with no preference between the panel and the thing eating the machine.
Raising a score needs no privilege.

**What is measured, and why "held".** `memory.current` counts page cache, and a
pool whose sessions have read files sits at its limit indefinitely with nothing
wrong: right after the process that caused a stall was ended, the pool still read
89% full, all of it cache, and the panel asked again about nothing. The
thresholds use anon plus shmem -- what only leaves when a process does.

**When the panel asks, and when it acts.**

- *Warn* when held memory reaches the mode's percentage of the budget, when the
  machine is below its reserve (5% of RAM, floor 768 MiB), or when the pool's
  memory stall (`memory.pressure full avg10`) reaches 10%.
- *Critical* at 20% stall, or half the reserve. The pool's I/O stall counts as
  memory stall only while the pool is at 95% of its limit *and* pages are coming
  back from disk after reclaim (`workingset_refault_file`, 2000 a second): that
  is a thrash; the same I/O stall without refaults is an `npm install` on a slow
  disk, which a review showed reading as critical. Deliberate thrash tests read
  20-60; an ordinary busy build reads 0-2.
- The host stalling or short while the sessions are not is its own reason,
  *machine*, and is judged against the machine rather than the pool.
- A question is a bar across the console and a notification when the page is not
  focused. It names a session only if it holds a quarter of what the pool holds
  (a twentieth of the machine without a pool): naming a 200 MiB shell beside two
  sessions holding gigabytes sent somebody to end the wrong thing. A paused
  session is never named.
- The same session and process is the same question: its level changing updates
  it and keeps its countdown. A stall hovering around 20 -- the incident's
  pattern -- restarted the countdown on every dip in the first version, so the
  panel never acted and pushed forty questions. A countdown is forgotten after
  30 seconds of warning, and the question is taken down after two readings of OK.
- The panel ends a process on its own only when all of these hold: critical;
  the mode allows it (Performance never does); the grace period has passed with
  no answer; the session holds half of the pool (a twentieth of the machine for
  the machine reason); and the process is not the session's own. The agent is
  never ended unasked -- the page offers that, with a confirmation.
- A process the kernel refuses to let the panel signal (a `sudo` in a session)
  is not tried again, and is not offered again.
- Ending, pausing or loosening -- from the bar, the page or the panel itself --
  takes the question down and keeps the next one back for twenty seconds while
  the ten-second average falls.
- "Don't ask for 15 min" silences a warning and is not offered for a stall or a
  countdown: a bar that went away while its countdown ran was a process ended
  with nothing on screen.
- What it names is the largest process in the session that grew most in the
  last minute, passing over the pane's own process for a child at least half its
  size.
- A kill carries the pid and the start time the page was shown. A pid is reused;
  the two together are not.
- A paused session is marked in the sidebar as well as on the page: its terminal
  just stops, and one that looks like every other session reads as hung.

**A system unit restarts itself only for a server started after it.** The
unit's root step runs before the panel, so a server older than the panel is one
that step failed on, and a restart fails the same way. The first version
restarted regardless, and a unit whose account the helper refuses restarted
every minute, dropping every terminal each time. A server newer than the panel
-- the old one died and the panel started another -- is fixed by a restart, at
most once in ten minutes.

**Turning it off lets go.** `--isolation=off` after a panel that managed the
scope sets the pool's limit back to unlimited and thaws every session: the scope
outlives the panel, and a budget nobody adjusts, or a pause nobody can resume,
is worse than either.

**The budget moves.** In Conservative, Balanced and Custom the pool may have
what it holds plus whatever the host has above its reserve, and no more, so a
container or a desktop elsewhere on the machine squeezes the sessions -- which
the panel can see and ask about -- rather than the whole machine. "What it holds"
is held memory, not `memory.current`: MemAvailable already counts the pool's
cache, and adding it twice gave a cache-full pool a budget that could never
shrink. The floor is held memory plus 256 MiB, so the squeeze lands on cache. Performance does not follow the host; somebody who chose it said
the sessions come first.

**CPU.** The session somebody typed into in the last two minutes, or that is
waiting on them, gets `cpu.weight` 400 against the others' 100. A session that
has taken 40% of every core for a minute while the machine is short of CPU
(`/proc/pressure/cpu some avg10` at 25% or more) is moved down to 25, and back
once either stops being true; one somebody is using is never moved down. Weights
only matter under contention, which is when they should, and nothing here ends
or pauses anything for CPU: a slow build is not an emergency.

**What it never does.** It never freezes a session on its own: a frozen agent
mid-request is a failure somebody has to explain. It never ends the tmux server
or the panel. It never asks tmux anything on the request path or on its own
tick, beyond the server's pid when it has changed -- the moment the page matters
is the moment tmux is slowest to answer.

## Small decisions that keep being questioned

**Per-session CPU is a share of the whole machine, not top's.** top means "one
core saturated" by 100%, which is more informative in isolation, but the machine
meter is an inch above this list on the same panel, and a session reading 310%
beside a machine reading 31% invites exactly one wrong conclusion. `cores` is in
the payload for anyone who wants to convert.

**A session whose pane has gone is absent, not zero.** Zero is what a shell
sitting at a prompt reads, and a dead session drawn at 0.0% looks like an idle
one.

**The service worker deliberately caches nothing.** A caching service worker pins
the panel to an old bundle, and the entire premise of the project is that the
backend can be restarted and upgraded underneath you.

**Notifications are not Web Push.** Push would need a subscription endpoint and a
server that keeps it; what is here fires while the page is alive, including a
background tab and an installed PWA, and does not pretend to reach a phone whose
browser is closed.

**There is one account.** The schema carries a users table so a second is a
migration rather than a rewrite, but the panel is a single-user tool today and
says so rather than shipping an authorisation model nobody enforces.

---

Further reading: `docs/build-log.md` for what each of these cost,
`docs/api.md` for the interface they add up to, `docs/runbook.md` for what to do
when one of them is misbehaving, and `AGENTS.md` for the same ground stated as
rules.
