# Runbook

What to check when a running deployment misbehaves.

## First, always

```sh
vibepanel doctor
```

Fourteen lines, and it prints all of them rather than stopping at the first
failure — a machine with three problems used to take three runs to find them.

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
membership does not change on re-parenting, so it stays in the panel's unit —
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
socket — a panel started with a different `--tmux-socket` is talking to an
empty server rather than to a dead one:

```sh
ls /tmp/tmux-$(id -u)/                  # every socket on the box
vibepanel doctor                        # which one this config uses
```

## Codex sessions never report their state

Claude and Codex are configured by different mechanisms and fail separately, so
check which one is quiet before assuming the panel is at fault.

Codex is wired through `notify` in `~/.codex/config.toml`, which is one command
for one event, so a Codex session can only ever report `waiting` — never
`working` or `done`. A Codex session that shows a guessed state most of the
time is behaving as designed, not misconfigured.

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

## Hooks say they are installed and no state ever arrives

Not Codex-specific: this takes Claude down with it, and the settings page still
reports the hooks as installed because it reads the agent's configuration file,
not whether anything ever reached the panel.

Ask `doctor` first. Two of its lines are about exactly this, and between them
they cover both ways it happens:

    [ok  ] hook endpoint      http://127.0.0.1:8443/api/health answers
    [ok  ] hook url           4 of 4 session(s) post to http://127.0.0.1:8443

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

    [FAIL] hook url           3 of 5 session(s) still post to http://10.0.0.4:8443,
                              not http://127.0.0.1:8443

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

A note on what this is *not*. Binding one interface -- `--addr 192.168.8.20:8443`
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
releases it however the holder exits — a SIGKILL included, which is the case a
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
viewers are being told to sign in instead, that is a different fault — a
database that cannot be *read*; the panel answers 503 for that and says which
it is.

Usually the disk is full. `du -sh ~/.local/share/vibepanel` and the audit-log
entry below are the two places it accumulates.

## The database is growing

Almost always `audit_log`. Check before assuming:

```
sqlite3 ~/.local/share/vibepanel/vibepanel.db \
  'SELECT event, COUNT(*) FROM audit_log GROUP BY event ORDER BY 2 DESC'
```

`login.failed` in the thousands is a panel on a public port doing its job —
the login throttle bounds how fast those arrive, and the table is trimmed to
the newest 50,000 rows at every startup.

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
it once — and the reason that was wrong is that a panel is meant to run for
months, so the bound arrived exactly when nobody needed it.

## Memory

Look in both places, and know which is which.

**tmux** holds the authoritative scrollback — 20,000 lines per session, by the
`history-limit` in the embedded config. This is the larger number and it grows
with what agents print.

**The panel** is not flat with session count, whatever an earlier version of
this page claimed. It attaches to *every* session, not only the one being
watched, because state detection reads the byte stream — and each attachment
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
