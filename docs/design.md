# Why it is built this way

The decisions in this document are the ones that would look arbitrary from the
outside, and each of them cost something to learn. `docs/build-log.md` has the
chronological version with the failures attached; this is the shorter argument,
ordered by how much of the product depends on it.

`AGENTS.md` states the same ground as red lines — rules for anyone changing the
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
panel, `kill -9` the panel, replace its binary, reboot its container — the tmux
server and every process under it carry on, because nothing about their
lifetime ran through the program you just stopped.

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
have, and `systemd-analyze verify` accepts the directive either way — a setting
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
as finished is the panel giving a confident wrong answer to the only question it
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

Each state has a shape as well as a hue — circle, triangle, check — and so does
the share dashboard's connection state. People read this panel at 2am on a phone
in a dark room, and some of them cannot tell the hues apart at any hour.

The same rule is why the share dashboard says *live* / *reconnecting* /
*disconnected* in words, and always carries the time of the last reading and how
long ago that was. A dashboard that has silently frozen otherwise looks exactly
like a quiet machine.

## Files move over HTTP, not through the terminal

In-band transfer protocols fight with full-screen TUIs, and the reason to put a
screenshot on the server is almost always to hand it to the agent. So an upload
lands next to the session and types its absolute path at the prompt, ready to
press enter on. The path being ready is the feature; the transfer is the detail.

Preview sniffs the file's magic bytes rather than trusting its name, and refuses
SVG on purpose — an SVG is a document that can run scripts, and rendering one on
the panel's own origin would run it there.

## A read-only share token is narrowed by its route, never by a flag

Share links live in their own table, and `currentUser` does not consult it. That
is the entire security design: a share token presented as a session cookie or a
`Bearer` header is an unrecognised string, and every authenticated route already
answers 401 to those. Exactly one `GET` is mounted below the share middleware.

The alternative — a `scope` or `readOnly` column on the existing token table —
makes every handler in the panel one that has to remember to check a flag, and
the handler that forgets is the one somebody writes next year.

Redaction is the same shape. The share response restates the fields it discloses
instead of embedding `sysmon.Sample` or `store.Session`, so a field added to
either is not disclosed by default. Row ids on the dashboard are
`HMAC(token hash, real id)`: stable within one link, different between links, so
two screens cannot be correlated and neither carries the panel's real ids.

## Token counts come from what the agents wrote down, or not at all

The usage panel reads the transcripts Claude Code and Codex write for
themselves. There is no estimator anywhere in the package: characters divided by
four is a thing that looks like a measurement and is not one.

Three format facts had to be verified rather than assumed, and each changes the
answer. Claude writes one line per content block and every line carries the same
`usage` object: one real 89 MB transcript holds 13,869 usage-bearing lines for
6,563 actual requests, and summing them reports 14.1M output tokens where the
truth is 5.95M — an over-count of 2.37×, in the direction that flatters.
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
keeps a `restored` mark afterwards — because the banner scrolls away and the fact
does not. The API documentation says the same thing in the same words.

Restore is offered, never automatic, unless you asked for it on a particular
session. A boot that starts two dozen agents at once is a worse failure than a
list to click through.

## Small decisions that keep being questioned

**Per-session CPU is a share of the whole machine, not top's.** top means "one
core saturated" by 100%, which is more informative in isolation — but the machine
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
