# Runbook

What to check when a running deployment misbehaves.

## First, always

```sh
vibepanel doctor
```

It verifies the tmux binary, the data directory, the database schema, the tmux
server, isolation (no foreign sessions on our socket) and whether passkeys can
work with the current configuration.

## The panel is up but a session shows GONE

The database has a row whose tmux session no longer exists. Usually the tmux
server was restarted or `kill-server` was run by hand.

```sh
tmux -L vibepanel ls                    # what actually exists
vibepanel session ls                    # what the panel believes
```

Sessions marked GONE can be removed with `vibepanel session kill --id <id>`,
which is a no-op against tmux and just clears the row.

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

If the table is far past 50,000 rows, the panel has not been restarted since
before trimming existed. Restarting it trims.

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
exactly this and fails past three.

For the box as a whole:

```sh
systemd-cgls --user-unit vibepanel.service
systemctl --user show vibepanel -p MemoryCurrent
```

Note that this only accounts for what is inside the unit's cgroup. A tmux
server started outside the unit keeps running there, with none of the unit's
memory limits applying to it.
