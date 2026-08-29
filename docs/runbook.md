# Runbook

What to check when a running deployment misbehaves.

## First, always

```sh
vibepanel doctor
```

Fifteen lines, and it prints all of them rather than stopping at the first
failure. A machine with three problems used to take three runs to find them.

| line | what a failure means |
|---|---|
| `tmux binary` | missing is fatal; older than 3.3 is `--`, and says which sequences are lost |
| `data dir` | the directory cannot be created or written |
| `running panel` | whether something already holds the data directory, and its pid |
| `hook endpoint` | whether anything answers where this configuration says the panel is — the address every session's hooks post to. Skipped when no panel is running, because nothing answering is then the expected state |
| `database` | the schema version, or a refusal to open one a newer binary wrote |
| `database writes` | a real write, in a transaction it rolls back — opening a database says nothing about writing to one |
| `disk` | under 512 MiB is `--`, under 64 MiB is a failure; a full disk is the panel's quietest one |
| `tmux server` | it says so when the check started the server itself |
| `tmux config` | the running server started with a different config than this binary carries. tmux reads `-f` once, at `start-server`, and the panel never restarts its server — so a changed config takes effect at the next reboot or not at all |
| `isolation` | any session on our socket that is not ours, which is the promise that lets this run beside your existing tmux |
| `agents` | what tmux reports each session is running, and whether any of it is recognised as an agent. Never a failure: a panel full of shells is not a problem |
| `hook url` | sessions still posting to an address the panel no longer serves, because a session's environment is fixed when it is created |
| `hook token` | sessions holding a token the panel no longer accepts. The token is created once and never rotated, so this means the row holding it went away — a restored database, or the setting cleared |
| `passkeys` | `--` and the reason when the configuration cannot support them; password login is unaffected |
| `environment` | `VIBEPANEL_*` variables that are set and never read — a misspelled `VIBEPANEL_TLS` once meant plaintext on a public port |

`--` is "works, but not here", never a failure. It is used where saying FAIL
would train you to skip the output.

## The panel is up but a session shows GONE

The database has a row whose tmux session no longer exists. Usually the tmux
server was restarted or `kill-server` was run by hand.

```sh
tmux -L vibepanel ls                    # what actually exists
vibepanel session ls                    # what the panel believes
```

Sessions marked GONE can be removed with `vibepanel session kill --id <id>`,
which is a no-op against tmux and just clears the row.

They can also be brought back: the panel shows a notice offering to rebuild
them, with the command and directory each one will use. See the next section
for what that does and does not recover.

## Everything shows GONE after a reboot

Expected. tmux outlives the *panel*, which is what `KillMode=process` below is
about; it does not outlive the *machine*. The tmux server is an ordinary process
and its scrollback is in that process's memory.

The panel offers to rebuild them. What comes back:

- the session, under the same id, in the same project, with the same name,
  working directory, pinned/sorted position, notes and todos;
- the command it was created with, re-executed;
- the last 2,000 lines of scrollback, printed into the new pane's history above
  a banner saying when it was captured.

What does not come back, at all: **the process**. An agent that was mid-task is
gone, and re-running its command starts a new one that remembers none of it. The
banner in the pane and the `restored` chip in the header both say so; do not
read the output above the banner as current.

The scrollback is captured every 30 seconds for sessions that have printed
something, and once more for every session when the panel shuts down. An orderly
`reboot` or `systemctl stop` therefore loses nothing. A power cut or a hard
reset loses up to half a minute of output, because there was no shutdown for the
archive to ride along with.

Two things to check when a restore does *not* offer what you expect:

```sh
# Was the command ever recorded? Rows created before this version have none, and
# the dialog says "will start a login shell" for those.
sqlite3 ~/.local/share/vibepanel/vibepanel.db \
  "SELECT id, title, launch_command FROM sessions"

# Is there an archive, and how big is it?
sqlite3 ~/.local/share/vibepanel/vibepanel.db \
  "SELECT session_id, captured_at, LENGTH(content) FROM session_scrollback"
```

An empty `launch_command` string means the row predates the column; `[]` means
the session really was created as a login shell. They are different answers and
the dialog words them differently.

To have particular sessions come back on their own, tick "restore automatically"
on them in the restore dialog, or:

```sh
curl -sX PATCH .../api/sessions/<id> -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"restoreOnBoot":true}'
```

It is off by default on purpose: a boot that starts two dozen agents at once,
each of them beginning to work, is worse than a list of dead rows.

## A restored session replayed its scrollback twice, or the banner is wrong

The archive is handed to the pane as a file under `<data-dir>/restore/`, and the
pane's first command deletes it as it reads it. A file left there is a restore
whose pane never started: the tmux session could not be created, or the panel
died between writing the file and creating it.

```sh
ls -la ~/.local/share/vibepanel/restore/
```

Nothing reads a leftover except the next restore of that same session, which is
usually what you want. They are 0600 in a 0700 directory and hold a verbatim
copy of a terminal, so treat them as you would the database. Deleting one costs
that session's archived scrollback and nothing else.

If a *restart* (not a restore) replays scrollback, the delete did not happen:
`respawn-pane` reuses the pane's original command, which after a restore is the
wrapper script, and the guard against a second replay is that the file is gone.
Check the file above and check the pane's command with
`tmux -L vibepanel display-message -p -t '=<name>' '#{pane_start_command}'`.

## The log says there are tmux sessions with no database row

```
tmux sessions on our socket with no database row count=2 socket=vibepanel
```

Printed by `Reconcile` at startup. The mirror image of GONE above: there the
row outlived the process, here the process outlived the row. Nothing in the UI
can reach these — they have no row to render — so they keep running, holding a
pane and whatever the agent was doing, until the machine restarts.

The panel reports rather than adopts, deliberately: taking one over would mean
guessing which project it belongs to.

`vibepanel session kill` used to make them: it killed one tmux session, and
the scratch terminals under it kept running while their rows cascaded away
with the parent's. Fixed — both paths now kill the children first — so on a
current binary this warning points at something else: a row deleted by hand, a
database restored from a backup taken before those sessions existed, or a
`--data-dir` pointed somewhere new while the tmux server kept running.

```sh
tmux -L vibepanel ls          # everything on the panel's socket
vibepanel session ls          # what the panel has rows for
```

The difference is the orphans. Kill one with an exact target:

```sh
tmux -L vibepanel kill-session -t '=vp_3f9a1c4e2b7d8506'
```

The leading `=` is not decoration. tmux matches targets by prefix, so
`-t vp_3f9a` would also match a longer name that starts the same way, and on a
socket full of generated names that is a real way to kill the wrong thing.

## Sessions died when I restarted the panel

Check the unit for `KillMode=process` first. Without it, this happens every
time, to everyone, and none of the other explanations apply.

```sh
systemctl --user show vibepanel -p KillMode      # want: KillMode=process
```

tmux's server is started by the panel. It daemonises and re-parents, but cgroup
membership does not change on re-parenting, so it stays in the panel's unit,
and systemd's default `KillMode=control-group` SIGTERMs everything in that
cgroup on stop. Nothing in the panel's code ties a session to the panel's
lifetime; the unit file did it instead. Measured: two sessions before the stop,
zero after, on the default; two after with `KillMode=process`.

`KillMode=mixed` does not work either, which matters because it reads like the
cautious choice. After the main process exits, the SIGKILL phase still goes to
the whole cgroup.

The unit shipped in `deploy/vibepanel.service` has the right line. If the panel
was installed before that was fixed, copy it again and `systemctl --user
daemon-reload`.

If `KillMode` is already `process` and sessions still vanish, then look at the
socket: a panel started with a different `--tmux-socket` is talking to an
empty server rather than to a dead one:

```sh
ls /tmp/tmux-$(id -u)/                  # every socket on the box
vibepanel doctor                        # which one this config uses
```

## Codex sessions never report their state

Claude and Codex are configured by different mechanisms and fail separately, so
check which one is quiet before assuming the panel is at fault.

Codex is wired through `notify` in `~/.codex/config.toml`, which is one command
for one event, so a Codex session can only ever report `waiting`, never
`working` or `done`. A Codex session that shows a guessed state most of the
time is behaving as designed, not misconfigured.

Settings → state reporting has a button for it, the same as Claude's. If the
line is missing after pressing it, check *where* it landed: it belongs above the
first `[table]` in the file, and a `notify` under `[notice]` or
`[tui.model_availability_nux]` is a different key that Codex never reads. That
is the one failure the installer is written to avoid and the one to look for if
a hand-edited file goes quiet.

```sh
codex doctor | grep -A2 'config.toml'   # does Codex still accept the setting?
grep notify ~/.codex/config.toml        # is it still there?
```

`notify` lives in `hooks/src/legacy_notify.rs` inside codex-cli 0.147, which
also ships a full hooks system. If a future version drops `notify`, Codex
sessions go quiet with no error anywhere: the reporter script suppresses its
own failures on purpose, because a hook that makes an agent wait is worse than
a missed state update. `codex doctor` reporting a parse error or a deprecation
on that line is the signal.

## A session is named after its directory, and renaming it from inside does nothing

The automatic name comes from the title the program in the pane set, and there
are two ways for it to get here:

- A plain `OSC 0/2`, which tmux records in `#{pane_title}`. The poller reads
  that every couple of seconds.
- The same sequence wrapped in tmux's passthrough DCS, which is what a program
  that has noticed `$TMUX` sends: the title is meant for the terminal a human
  is looking at, not for tmux. tmux hands those bytes to its client without
  looking inside, so `pane_title` never moves and the panel's own PTY is the
  only place the title exists.

The panel reads both. Check which one you are looking at before changing
anything:

```sh
tmux -L vibepanel display -p -t '=vp_abc123:' '#{pane_title}'
tmux -L vibepanel show -wg allow-set-title      # must be on where it exists
tmux -L vibepanel show -wg allow-passthrough    # must be on
```

`allow-set-title` does not exist below tmux 3.6 and defaults to `on` where it
does, so a missing option there is normal. `allow-passthrough` off is not: it
takes the second route away entirely, and the sessions that use it are exactly
the ones running a tmux-aware agent.

A name you typed yourself is never overwritten by either route — that is what
`title_source` is for — so a tab that ignores the program's title may simply
have been renamed by hand. Renaming it back to nothing is not possible on
purpose; create a session or rename it to what you want.

## Hooks say they are installed and no state ever arrives

Not Codex-specific: this takes Claude down with it, and the settings page still
reports the hooks as installed because it reads the agent's configuration file,
not whether anything ever reached the panel.

Ask `doctor` first. Two of its lines are about exactly this, and between them
they cover both ways it happens:

    [ok  ] hook endpoint      http://127.0.0.1:18443/api/health answers
    [ok  ] hook url           4 of 4 session(s) post to http://127.0.0.1:18443

**`hook endpoint` failing** means nothing is listening where this configuration
says the panel is. Either it is not running there, or `doctor` is not being run
with the environment the service runs with -- in which case every other line of
its output is describing a differently-configured panel than the one holding the
lock, so check the unit's environment before anything else.

**`hook token` failing** means the same thing about a different variable. The
token is created once and never rotated, so it only changes when the row holding
it goes away: a database restored from a backup taken before it existed — which
the "database will not open" section below tells you to do — or the setting
cleared. A new one is generated, the sessions keep presenting the old one, and
every report from them is refused for as long as they live.

**`hook url` failing** is the one that catches people, and it has nothing to do
with the address being wrong *now*:

    [FAIL] hook url           3 of 5 session(s) still post to http://10.0.0.4:18443,
                              not http://127.0.0.1:18443

`VIBEPANEL_URL` is injected into a session's environment when the session is
created, and tmux's `set-environment` reaches only panes started after it. A
session's environment cannot be updated in place. So changing `--addr` and
restarting the panel leaves every session made before the change posting to the
old address forever, while new ones work -- which is why the symptom is usually
"some sessions report their state and some do not" rather than none of them.

Restart those sessions from the panel. The processes in them do not survive that,
which is the cost; nothing else gives a live session a new environment.

**Three things change that URL**, and two of them are in the documented setup
sequence. `LoopbackURL()` builds the scheme from the TLS mode and the port from
the configured one, so:

- turning TLS on — install ships with `--tls off`, and the README says to change
  it before exposing the panel — moves every new session to `https://`, while
  the sessions you already have keep posting plaintext at a listener that now
  speaks only TLS;
- changing the port does the same;
- so does changing the bind address, though only to the extent that the new
  address is not reachable.

Restart the sessions after any of those. The processes in them do not survive
that, which is the cost, and there is no other way to give a live session a new
environment.

A note on what this is *not*. Binding one interface -- `--addr 192.168.8.20:18443`
-- does not break hooks on its own. `LoopbackURL()` follows the bound address, so
the sessions are told `192.168.8.20` too and reach it perfectly well. This
runbook said otherwise until it was measured.

If both lines are green and states are still guessed, the reports are arriving
and being rejected. Check the state names: `internal/hooks` writes them as bare
literals, and the server refuses anything that is not `waiting`, `working` or
`done`.

```sh
ss -tlnp | grep vibepanel                       # what it is actually bound to
tmux -L vibepanel show-environment -t =vp_x: VIBEPANEL_URL   # what one session holds
curl -sk "$(tmux -L vibepanel show-environment -t =vp_x: VIBEPANEL_URL | cut -d= -f2-)/api/health"
```

## Passkeys will not register

WebAuthn needs a secure context and a Relying Party ID that is a registrable
domain. An IP address is never valid, whatever the TLS setup. `doctor` prints
the reason:

```
[--  ] passkeys           disabled; password login only
       needs --domain with a hostname plus TLS, or localhost
```

Password login always works; passkeys are an addition, never the only door.

## Certificate renewal failed

With `--tls acme`, certificates and account keys live in `<data-dir>/acme`.
HTTP-01 cannot be used because the panel listens on a non-standard port, so the
challenge is DNS-01 and the provider credential has to be in the environment.
Cloudflare is the only provider wired up, and it reads:

```sh
CLOUDFLARE_API_TOKEN=...     # or CF_API_TOKEN, whichever is set first
```

Missing, and startup fails with `tlsmgr: no DNS API token; set
CLOUDFLARE_API_TOKEN`. Anything other than `cloudflare` in `--acme-dns` fails
with the list of what is supported, which is that one name.

Point `--acme-directory` at the CA's staging endpoint while debugging; the
production endpoint has rate limits that a retry loop will exhaust.

## The certificate on disk changed and the panel did not notice

`--tls files` polls once a minute and compares modification times. A renewal
that preserves them — `cp -p`, a restore from backup, some sync tools — is
invisible, and the panel keeps serving the old certificate until it expires and
then keeps serving it expired, in silence. `touch` the pair to force the
reload:

```sh
touch /path/to/cert.pem /path/to/key.pem
```

A corrupt or half-written pair is handled: the load fails, the previous
certificate keeps being served, and the failure is logged. It is only the
timestamps that can lie.

## It will not start: "data directory is in use"

```
vibepanel: config: data directory is in use: pid 4711 is already running with
/home/you/.local/share/vibepanel
```

One panel per data directory, on purpose. Two start happily otherwise, and the
second one voids the premise the design rests on: the panel is meant to be the
only tmux client, so that there is one authoritative grid and one place that
decides its size. Each also keeps its own state detector in memory, so a bell
one of them saw is invisible to the other, and the "waiting" it set is
overwritten by the other's "working" on the next tick.

Usually it is the systemd unit, and you are about to start a second one by
hand. `systemctl --user status vibepanel` says so, and so does:

```
vibepanel doctor
[ok  ] running panel      pid 4711 holds /home/you/.local/share/vibepanel
```

The lock is `flock` on `vibepanel.lock` in the data directory, so the kernel
releases it however the holder exits, a SIGKILL included, which is the case a
pid file gets wrong. If nothing is running and it still refuses, something else
is holding that file open; `fuser` on it will say what.

## The database will not open

```
store: database is version N but this build only knows M
```

An older binary is being pointed at a database written by a newer one. Either
upgrade the binary or restore a copy of the database from before the upgrade.
The panel refuses rather than opening it and ignoring unknown columns, because
that silently drops whatever the newer version wrote.

## Everything looks fine and nothing is being saved

Renames revert, a session's state never changes, the sidebar order stops
moving. The terminals are unaffected — they belong to tmux — so nothing else
looks wrong.

```
vibepanel doctor
```

```
[FAIL] database writes    store: write check: attempt to write a readonly database (8)
[--  ] disk               412 MiB free of 626.4 GiB (0.1%) on /home — getting tight
```

`doctor` used to report `[ok] database` here and exit 0: opening a database and
reading from it says nothing about writing to one. It attempts a real write now,
inside a transaction it rolls back, so it leaves nothing behind.

The panel says so too, once the failures have lasted three ticks: `/api/health`
answers `"ok": false` with the reason, and every open page shows a banner. If
viewers are being told to sign in instead, that is a different fault: a
database that cannot be *read*. The panel answers 503 for that and says which
it is.

Usually the disk is full. `du -sh ~/.local/share/vibepanel` and the audit-log
entry below are the two places it accumulates.

## The database is growing

Two candidates now. `session_scrollback` is the larger per row and the smaller
in total, because it is bounded by how many sessions exist rather than by how
long the panel has been up: one row per session, at most 256 KiB each, replaced
in place rather than accumulated. Two dozen sessions all at the cap is about
6 MB, and the row goes when the session does.

```
sqlite3 ~/.local/share/vibepanel/vibepanel.db \
  'SELECT COUNT(*), SUM(LENGTH(content)) FROM session_scrollback'
```

More than one row per session there, or a total far past `sessions × 256 KiB`,
means something is keeping a history of captures instead of one; that is a bug,
not a configuration.

A third arrived with the boards: `session_events`, one row per session state
transition. It is the only table here that grows with *time* rather than with
what exists.

```
sqlite3 ~/.local/share/vibepanel/vibepanel.db \
  'SELECT COUNT(*), MIN(date(at, "unixepoch", "localtime")) FROM session_events'
```

It is kept for 31 days and swept hourly plus once at startup, so the oldest day
should be about a month back and the count should be roughly sessions ×
transitions a day × 31, a few tens of thousands on a busy panel. An oldest day
much further back than a month means the sweep is not running, and the sweep
runs on the same goroutine that drains the log: if that has stopped, so has the
poller, and the sidebar would have gone stale long before this table became the
problem.

The opposite reading — a count that is *low* while a share board's trends are
empty — is not a database problem. Transitions are queued to one writer through
a bounded channel and **dropped when it is full** rather than made to wait,
because the alternative is the poller stalling and the whole panel losing track
of what is running. That only happens when writes are already failing, which the
stale banner says in words.

Otherwise it is `audit_log`. Check before assuming:

```
sqlite3 ~/.local/share/vibepanel/vibepanel.db \
  'SELECT event, COUNT(*) FROM audit_log GROUP BY event ORDER BY 2 DESC'
```

`login.failed` in the thousands is a panel on a public port doing its job. The
login throttle bounds how fast those arrive, and the table is trimmed to the
newest 50,000 rows at every startup.

`blocked` in the thousands means somebody is hitting a panel with
`--allow-from` set, from an address that is not on the list. That refusal
happens before authentication and outside the throttle, so its database write
is gated to one row per source per minute. Every one of them is still in the
journal, which is where to look for the real rate and what a fail2ban rule
should read:

```
journalctl --user -u vibepanel | grep event=blocked | tail -50
```

If the table is far past 50,000 rows on a current binary, something is writing
faster than the trim runs, which is every five minutes while the panel is up.
It used to trim only at startup, so on an older build the answer is to restart
it once: a bound that applies only at startup is no bound at all on a panel
meant to run for months.

## Memory

Look in both places, and know which is which.

**tmux** holds the authoritative scrollback: 20,000 lines per session, by the
`history-limit` in the embedded config. This is the larger number and it grows
with what agents print.

**The panel** is not flat with session count, whatever an earlier version of
this page claimed. It attaches to *every* session rather than only the one being
watched, because state detection reads the byte stream, and each attachment
costs a replay buffer of up to 2 MiB (`session.DefaultRingSize`), a PTY, and a
goroutine. Twenty-four sessions is therefore tens of megabytes of panel before
anything has gone wrong. Buffers fill as output arrives rather than being
allocated up front, so an idle panel sits well below that ceiling.

If the panel's own memory is far above roughly 2 MiB per live session,
something is retaining more than it should; `scripts/scale-check.mjs` measures
exactly this and fails past 3 MiB per session.

It also refuses to pass when it cannot measure. `rssMiB` returns NaN where
there is no `/proc`, and every comparison against NaN is false, so the check
tests for that before comparing rather than letting a run it could not measure
count as a good one.

For the box as a whole:

```sh
systemd-cgls --user-unit vibepanel.service
systemctl --user show vibepanel -p MemoryCurrent
```

Note that this only accounts for what is inside the unit's cgroup. A tmux
server started outside the unit keeps running there, with none of the unit's
memory limits applying to it.


## The states look right but nothing says they are guesses

The panel shows a notice when an agent is running and no hook has reported
anything, so that a state which is only inferred is not read as a fact. If your
agents run without hooks and the notice never appears, the panel has not
recognised them as agents.

It matches `#{pane_current_command}` against `claude` and `codex`, and that
string is a fact about how the program was packaged rather than about this
project. A native binary reports its own name. Anything shipped as a script
with a `#!` line reports the interpreter, because that is what the kernel
executed -- Claude Code installed through npm reports `node`.

`doctor` prints what tmux actually reports, which is the whole diagnosis:

    $ vibepanel doctor
    [--  ] agents             none recognised; tmux reports: node
           1 session(s) running something that is not a shell. If one of
           those is an agent, the panel cannot tell, and the notice saying
           its states are guessed will not appear.

The same list read directly, if the panel is not to hand:

    tmux -L vibepanel list-panes -a -F '#{pane_current_command}'

There is nothing to configure. The list is deliberately two names rather than
"any non-shell process", because htop and a build are not agents and a notice
that fires on them is one people stop reading. If your agent reports something
else, the states are still inferred correctly from the output heuristic -- what
is missing is only the panel saying so. Installing the hooks from the settings
page removes the question entirely: with a hook reporting, the state is not a
guess and the notice is not wanted.


## An upgrade that did not take

Two things survive an upgrade that look installed and are not, and they compound:
the new binary may not be running, and the new tmux config is certainly not
loaded.

**The binary.** `install.sh` used to end in `systemctl --user enable --now
vibepanel`, which is a no-op on a unit that is already enabled and active --
exactly what an upgrade finds. The new binary went to disk and the old one kept
serving, while the script printed "started. the one-time setup token is in:
journalctl …", a start that did not happen and a token consumed at first
install. It restarts when the unit is already active now, and says which of the
two happened. Restarting is free here by design: the sessions belong to tmux, not
to the panel process.

If you upgraded with an older installer, the fix is the restart it did not do:

```sh
systemctl --user restart vibepanel
vibepanel version                       # what is on disk
systemctl --user show vibepanel -p ExecMainStartTimestamp
```

**The tmux config.** tmux reads `-f` once, at `start-server`. The panel rewrites
the file on every start and never kills the server -- that is the premise of the
project -- so a changed config takes effect at the next reboot or not at all.
`doctor` says so:

    [--  ] tmux config        the running server started with a different config

It is a `--`, not a failure, because the remedy costs every session on the
socket:

```sh
tmux -L vibepanel kill-server     # every session on it dies
```

Nothing is broken in the meantime; what is missing is whatever the config
changed. That has included `allow-passthrough`, which is the reason tmux 3.3 is
the floor, and the terminal overrides that keep the alternate screen out of the
way. If your sessions are cheap to lose, restart the server; if they are not,
the change lands at the next reboot.

A server started before this check existed reports `the running server predates
this check`: the stamp is written at `start-server`, so there is nothing to
compare against until the next one.

## Two panels: a user unit and a system unit at once

**Symptom.** Nothing fails. Notes and todos come back stale or empty, a project
added in the morning is gone by the afternoon, the session list disagrees with
`tmux -L vibepanel ls` in one direction and then the other, and every restart
seems to fix it for a while. `doctor` is clean, because from inside either panel
everything is exactly as it should be.

**Cause.** Both units are installed and enabled, so two processes are serving the
same data directory on the same tmux socket. SQLite does not let them corrupt the
file; they take turns, and each one's in-memory view drifts from the other's. The
`running panel` line in `doctor` reports one pid — the one it found — and says
nothing about the second.

`install.sh` refuses to create the second unit now, and asks to migrate instead.
This is for a machine where both were installed before that, or by hand.

```sh
systemctl --user is-enabled vibepanel   # the user unit
systemctl is-enabled vibepanel          # the system unit
ls ~/.config/systemd/user/vibepanel.service /etc/systemd/system/vibepanel.service
```

Two answers that are not "No such file or directory" is the diagnosis. Keep one —
the system unit if you installed it for `OOMScoreAdjust`, the user unit
otherwise — and remove the other:

```sh
# keeping the system unit
systemctl --user disable --now vibepanel
rm ~/.config/systemd/user/vibepanel.service
systemctl --user daemon-reload

# or keeping the user unit
sudo systemctl disable --now vibepanel
sudo rm /etc/systemd/system/vibepanel.service
sudo systemctl daemon-reload
```

Sessions survive both of those: `KillMode=process` is in both units, so stopping
either leaves the tmux server and everything under it alone. Start the survivor
and the panel's view is whatever the last writer wrote. Check the project list
and the notes on anything you edited that day, because a write from the panel
you just stopped may have been the one that lost.

## A killed agent shows as "exited 0" (tmux 3.4 and older)

**Symptom.** A session whose process was killed — by you, by the OOM killer,
by anything — appears in the panel as a clean exit rather than as `exit 137`.

**Why.** tmux marks a pane dead the moment its pty closes, which can be before
the server has reaped the child and collected its wait status. When that
happens, `pane_dead_status` and `pane_dead_signal` are both `0` and they stay
that way: the number never existed to be read later. Measured on tmux 3.4,
roughly one kill in ten; not observed on 3.6.

The killed process is still visible as a zombie in `/proc` at that moment,
which is how this was pinned down.

**What the panel does.** It stores what tmux reports and nothing else. There is
no wait status to recover, and inventing one — treating "dead with status 0"
as a kill — would misreport every agent that genuinely finished cleanly, which
is the far more common case.

**What to do.** Upgrade tmux if the distinction matters to you. Otherwise read
the session's last screen: a killed agent leaves its output where it stopped,
and a finished one usually says so.

## The one-liner refused with "checksum mismatch"

The archive that arrived is not the archive `SHA256SUMS` describes, and the
download has already been deleted. Nearly always a truncated transfer or a
caching proxy; retry once.

If it happens twice, do not install it. Download both files by hand and look:

```sh
curl -fsSLO https://github.com/jiangmuran/vibepanel/releases/download/<tag>/SHA256SUMS
curl -fsSLO https://github.com/jiangmuran/vibepanel/releases/download/<tag>/vibepanel_<tag>_linux_amd64.tar.gz
sha256sum -c SHA256SUMS --ignore-missing
```

"SHA256SUMS does not mention ..." is a different fault and says so: the
checksum file belongs to another release. That is a broken release, not a
broken download.

## "vibepanel: command not found" right after installing it

`~/.local/bin` is on `PATH` by default on some distributions and on none of
the others. The service does not care — the unit uses the full path — but you
will. The installer prints the exact line for your shell; it is:

```sh
export PATH="$HOME/.local/bin:$PATH"     # ~/.bashrc or ~/.zshrc
fish_add_path ~/.local/bin               # fish
```

## The installer said the binary will not run on this machine

It installs the binary and then runs `vibepanel version` once. Three causes
share two messages between them, and nothing distinguishes them until you run
it by hand:

```sh
~/.local/bin/vibepanel version
```

- `Permission denied` → the filesystem holding `$HOME` is mounted `noexec`
  (`findmnt -no OPTIONS --target ~`), or SELinux/AppArmor refuses that label
  (`ausearch -m avc -ts recent`).
- `Exec format error` → the archive is for another architecture. `uname -m`,
  and re-run the one-liner, which picks by `uname` and would not have got this
  wrong on its own.

No service is installed when this happens; there would be nothing for it to
start.

## The installer said there is no service manager here

Containers, WSL1, and machines with an init that is not systemd. The binary and
the env file are installed and nothing else, which is correct: the panel runs
perfectly well from a shell.

```sh
~/.local/bin/vibepanel serve
```

Put it behind whatever supervises that machine. `vibepanel service` will only
answer `upgrade` there; the rest of it needs a service to talk to.

## The unit is installed and `systemctl --user` will not talk to it

`XDG_RUNTIME_DIR` is not set in this shell, so there is no session bus to
reach. This is the state of a bare non-login ssh command and of every cron job;
it is not a broken install. From a real login session:

```sh
ssh -t you@host 'systemctl --user enable --now vibepanel'
```

Under the *system* unit this does not arise, which is one more reason it is the
recommended default where root is available.

## tmux is too old and the package manager will not fix it

The floor is 3.3, for `allow-passthrough`. The installer offers an upgrade and
then re-reads the version, because on a distribution shipping 3.2 the package
*is* the old version and the upgrade changes nothing while reporting success.

The panel works. What is lost is every progress and notification sequence an
agent TUI emits, and every symptom of that is something not appearing. Building
from source is the only way up:
<https://github.com/tmux/tmux/wiki/Installing>.

`vibepanel doctor` says the same thing, and marks it `--` rather than `FAIL`.

## macOS: the panel stops when I log out

Expected, and it is the one real gap against the Linux user unit. A LaunchAgent
runs in your login session; macOS has no `loginctl enable-linger`. On a Mac you
stay logged into, this is the same as lingering. On one you log out of, it is
not, and there is no plist key that changes it.

## The installer refused to touch a unit file

There is already a file at that path with no vibepanel `Documentation=` line in
it: a hand-written unit, a distribution package, or an older layout.
Overwriting it loses whatever was configured in it and there is no copy
anywhere. Move it aside and run the installer again:

```sh
mv ~/.config/systemd/user/vibepanel.service{,.bak}
```

## `vibepanel service` says no service is installed

It looks for the files the installer writes, not for what systemd or launchd
believes: `systemctl --user is-active` answers for the logged-in user's manager
whatever `$HOME` says, which on a machine with two accounts is somebody else's
panel.

```
~/.config/systemd/user/vibepanel.service     the user unit
/etc/systemd/system/vibepanel.service        the system unit
~/Library/LaunchAgents/io.github.jiangmuran.vibepanel.plist   macOS
```

If one of those exists and this still says nothing is installed, you are
running the command as a different user than the one it was installed for.

## `vibepanel service token` finds nothing

Three situations, and the message says all three because guessing between them
is worse:

- Somebody has already claimed this panel. A setup token exists only while
  `CountUsers()` is zero. Log in normally.
- The account was created by the installer (`--username`), so no token was ever
  printed. That is the line the installer ends with.
- It scrolled out of the window. `vibepanel service logs -n 2000`.

If the panel never started, `vibepanel service status` says so first.

## I want a fresh setup token

There is no way to reissue one, deliberately: it would be a second door into a
claimed panel. If the account is lost, the recovery is to remove the users row
from the database with the panel stopped, at which point the next start prints a
new token:

```sh
vibepanel service stop
sqlite3 ~/.local/share/vibepanel/vibepanel.db 'DELETE FROM users;'
vibepanel service start && vibepanel service token
```

Everything else — projects, sessions, notes — survives that. Passkeys do not:
they are registered against the account that is being deleted.
