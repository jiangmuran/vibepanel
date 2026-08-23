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

They should not. If they did, something is running the processes as children of
the Go process rather than of the tmux server. Check that the panel is not
being started with a `--tmux-socket` that differs from the one the sessions were
created on:

```sh
ls /tmp/tmux-$(id -u)/                  # every socket on the box
vibepanel doctor                        # which one this config uses
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
HTTP-01 cannot be used because the panel listens on a non-standard port, so a
DNS-01 provider credential must be present in the environment. Point
`--acme-directory` at the CA's staging endpoint while debugging; the production
endpoint has rate limits that a retry loop will exhaust.

## The database will not open

```
store: database is version N but this build only knows M
```

An older binary is being pointed at a database written by a newer one. Either
upgrade the binary or restore a copy of the database from before the upgrade.
The panel refuses rather than opening it and ignoring unknown columns, because
that silently drops whatever the newer version wrote.

## Memory

Every session's scrollback lives in the tmux server, not in the panel, so the
panel's own memory stays roughly flat regardless of session count. If the box
is under pressure, the tmux server is where the memory is:

```sh
systemd-cgls --user-unit vibepanel.service
systemctl --user show vibepanel -p MemoryCurrent
```

Note that this only accounts for what is inside the unit's cgroup. A tmux
server started outside the unit keeps running there, with none of the unit's
memory limits applying to it.
