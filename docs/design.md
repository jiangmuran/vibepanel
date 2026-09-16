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
`#3`, `[3]`); then the one session that is waiting, if exactly one is; then
the focus. Two waiting sessions and a bare "y" is refused with the list, even
when one of them is focused, because the cost of asking again is one message
and the cost of guessing is a keystroke in the wrong shell. A session at a
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
read. Once answered, the request comes off every card that showed it. Pushes
never move the focus: the focus is what a person chose, and a push that moved
it put the next sentence into whichever session had spoken last.

**Nothing an agent printed can reach the write path through the advanced
mode.** The headless agent that reads a sentence like "tell the docs one to add
a changelog entry" runs with no tools and sees only the sentence and a table of
handles, titles and states; what it returns is an intent that goes through the
same executor a typed command does, with a confirmation before anything is
sent. The agent that answers questions runs with read-only MCP tools and no way
to reach a pane at all. Its tools are five `GET`s under `/api/chat/tools`,
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
