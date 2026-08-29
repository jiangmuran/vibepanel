# Installing vibepanel

The README covers the two commands most people need. This file is the rest: the
service kinds, every installer flag, the mirror, and the paths that are not the
one-liner.

## The two installers

`install.sh` at the repository root is the network bootstrap the one-liner pipes
into `sh`. It works out the platform, resolves a release, downloads the archive
and its `SHA256SUMS`, verifies one against the other, unpacks it and hands over.
It is POSIX `sh` and it knows nothing about services.

`deploy/install.sh` ships inside the archive and does the install: tmux, the
service unit, the first account. It is bash, and bash 3.2, because that is what
macOS ships.

The bootstrap forwards any flag it does not recognise to the second one, so
`... | sh -s -- --yes --system` reaches the installer that understands those.

Bootstrap flags:

| Flag | Meaning |
|---|---|
| `--version <tag>` | install a specific release instead of the latest |
| `--repo <owner/name>` | fetch from a different repository |
| `--mirror[=<url>]` | route GitHub fetches through a mirror; bare means `github.muran.tech` |
| `--lang zh` / `--lang en` | the language of both halves |
| `--keep` | leave the unpacked archive on disk |
| `--help` | print usage without downloading anything |

## Ways to run it

| | Use it when | Sessions survive | Needs root | Starts at boot |
|---|---|---|---|---|
| **System service** | Root is available; the machine runs close to its memory; the panel must be up before anyone logs in | a panel restart, a crash, a logout | once, to install | yes |
| **User service** | No root, or a shared machine | a panel restart, a crash, a logout | no | yes, via lingering |
| **LaunchAgent** | macOS | a panel restart, a crash | no | at login |
| **Just run it** | Trying it out, or under a supervisor you already have | a panel restart | no | no |
| **Docker** | Isolation matters more than the sessions | nothing | no | container policy |

The installer offers the first three; it picks the LaunchAgent on macOS and asks
between the other two on Linux. "Just run it" is `./vibepanel serve`. Docker is
`deploy/docker-compose.yml`.

## Choosing between the user unit and the system unit

Same panel, same database, same account. The system unit runs as
`User=<you>` and adds `OOMScoreAdjust=-500`, which a user unit cannot set: with
it, the panel and the tmux server holding the sessions are the last things the
kernel kills. Choose it if the machine runs close to its memory, or if the panel
has to be up before anyone logs in.

The user unit sets `CPUWeight`, `IOWeight` and `ManagedOOMPreference` instead,
and the installer enables lingering for it. Without lingering the unit stops
when your last login session ends.

Both units carry `MemoryAccounting=yes`, `MemoryHigh=20G` and `MemoryMax=26G`,
sized for a 32 GB machine running a dozen agents. Lower them on a small VPS.

**Install one, never both.** Two units are two panels on one tmux socket and one
database, and the symptom is a panel that forgets things. The installer refuses
unless `--migrate` is passed, which removes the other unit first.

```sh
./deploy/install.sh --system          # add --migrate to replace a user unit
./deploy/install.sh --user
```

## Installer flags

```sh
./deploy/install.sh --yes --enable    # no questions, user service, start it
./deploy/install.sh --yes --system    # no questions, system service, needs root
./deploy/install.sh --help
```

| Flag | Meaning |
|---|---|
| `--yes`, `-y`, `--non-interactive` | take every default and ask nothing |
| `--enable` / `--no-enable` | start the service, or do not |
| `--user` / `--system` | choose the unit kind |
| `--migrate` | allow replacing one unit kind with the other |
| `--lang zh` / `--lang en` | the language of the questions, the plan and the errors |
| `--username <name>` | create the first account during the install |
| `--password-file <path>` | read that account's password from a file |
| `--password-stdin` | read it from standard input |
| `--password-env <VAR>` | read it from an environment variable |
| `--tune-claude` / `--no-tune-claude` | force the Claude Code question on or off |

Without root the installer says so and installs the user service.

**There is no `--password <value>`, and there will not be.** A password on a
command line is in the shell history and in `ps` while the installer runs. The
flag is refused with its own exit status and a message saying which of the three
alternatives to use.

## Claude Code's own settings

If there is a `claude` on `PATH` and somebody to ask, the installer offers to
adjust `~/.claude/settings.json` as well. Seven keys, all of them about what
leaves the machine or what the agent writes into your git history: session
mirroring to claude.ai, whether Remote Control connects on its own, the commit
and pull-request attribution, the `Claude-Session` link, the `Co-Authored-By`
byline, and the billing header that carries the CLI's version.

Each key is printed with its current value before the question, and the file is
copied beside itself before anything is written. Nothing else in it is touched,
including the hooks the panel installs there. Running it again changes nothing.

It is never done in a run with nobody watching: `--yes` skips it, and
`--tune-claude` is how a script asks for it anyway. Under `sudo` it is skipped
with a line saying so, because `~/.claude` then means root's.

The same thing outside the installer, at any time:

```sh
vibepanel tune claude            # print what it would change
vibepanel tune claude --apply    # change it
```

## Language

Both installers speak English and 简体中文. `--lang zh` or `--lang en` decides.
Without it, `LC_ALL`, `LC_MESSAGES` and `LANG` decide, in that order: the first
of the three that is set wins, and only a value starting `zh` or `en` counts.
An unrecognised `LC_ALL` leaves English rather than falling through to `LANG`.

When nothing has said and somebody is at a terminal, the first question is which
language, before anything else is asked. A pipeline never stops to ask: the
flag, then the environment, then English.

Translated is what a person reads while deciding something: every question, the
plan printed before anything is touched, the summary, the errors that say what
to do next, and `--help`. The `verb + path` trace lines during the install stay
English.

## Where GitHub is not reachable

`--mirror` routes every fetch — the release archive, its `SHA256SUMS` and the
latest-tag lookup — through a GitHub mirror. It defaults to `github.muran.tech`.
Only GitHub's own hosts are rerouted: `github.com`,
`raw.githubusercontent.com`, `api.github.com` and `objects.githubusercontent.com`.

`github.muran.tech` authorises by IP address. The first request from an
unrecognised address answers with a link to open in a browser; the installer
prints that link and waits, retrying up to five times.

```sh
curl -fsSL https://github.muran.tech/https://raw.githubusercontent.com/jiangmuran/vibepanel/main/install.sh -o vibepanel-install.sh \
  || curl -sSL https://github.muran.tech/https://raw.githubusercontent.com/jiangmuran/vibepanel/main/install.sh
sh vibepanel-install.sh --mirror
```

Two commands rather than one, and the `||` is the reason: `curl -f` throws away
the body on an HTTP error, so `curl -f ... | sh` against a mirror that has not
authorised the address yet fails with nothing on screen, and the discarded body
was the link. The second `curl` runs only in that case and prints it.

Through a pipe the installer cannot wait, so it prints the link and exits `3`,
its own status, so a wrapper can tell "go and click a link" from "the download
failed". Run it again once the address is authorised.

**A mirror is never chosen automatically.** Under `--mirror` the archive and the
checksums it is verified against both come from the mirror, so the check no
longer says anything about the mirror itself. That is a change in who is being
trusted, and not one to make because GitHub timed out. `--mirror=https://your.mirror`
points it elsewhere.

`--mirror` belongs to the bootstrap only. `deploy/install.sh` does not download
anything and rejects the flag.

## The first account

The panel prints a one-time setup token on first start. Open
`http://<host>:8443`, paste it, choose a password. The endpoint closes for good
once an account exists.

`vibepanel service token` finds the token again if the console output is gone.

Or create the account during the install:

```sh
./deploy/install.sh --username you --password-file /path/to/pw
```

## From source

```sh
cd web && npm ci && npm run build && cd ..
make build            # CGO_ENABLED=0 go build -o vibepanel ./cmd/vibepanel
./vibepanel doctor
./vibepanel serve
```

## Docker

```sh
docker compose -f deploy/docker-compose.yml up -d
```

Restarting the panel in a container kills every session, and nothing in the
image can change that: tmux is a child of the entrypoint and the container is
the boundary, so `docker restart` and any rebuild take the agents with them.
Agents also see only the container's tools, keys and repositories.

## Afterwards

```sh
vibepanel service status | start | stop | restart | logs | token | upgrade | uninstall
```

One command whichever way the panel runs. `logs` takes `-n <lines>` and `-f`;
`upgrade` and `uninstall` take `--yes` and `--dry-run`.

`service uninstall` stops the panel, removes the unit and removes the binary.
It leaves the data and every running session alone, because those are the two
things you usually want back.

## Removing all of it

`deploy/uninstall.sh` is the other end: the service, the sessions, the hooks
written into Claude Code, Codex and opencode, the data directory and the
binary.

```sh
./deploy/uninstall.sh          # list what would go; change nothing
./deploy/uninstall.sh --yes    # do it, copying the database out first
```

It prints every session by name before killing it, and it names the things it
is not touching — your own tmux, zellij, ttyd — because sitting beside those
without disturbing them is the point of the socket this project runs on.

`--purge` skips the copy and removes the older backups too, but keeps the
newest data archive: that one takes `--purge-archives`, because it is usually
the last copy of the database and `--purge` gets typed while thinking about
something else. `--keep-data`
leaves the data directory. `--dev-leftovers` also clears the sockets and
servers this repository's own tests leave behind, which a normal install never
has.

Whether the hooks are gone is checked by reading the three files afterwards,
not by the exit status: a binary older than `vibepanel hook remove` treats
`remove` as a stray word and exits 0 having done nothing. If any are left the
script says so and keeps the data directory, so the reporter those hooks call
is still there.

## Upgrading

**Settings → Updates** fetches the newest release from GitHub, verifies it
against the published `SHA256SUMS`, swaps the binary and restarts the service,
keeping the old binary as `.old`. It runs only when the button is pressed: no
scheduled check, no heartbeat, no telemetry.

`vibepanel service upgrade` does the same from a terminal.

Or unpack the new archive and run `./deploy/install.sh` again. It keeps the unit
already installed and restarts it. Either way the sessions keep running; see
[runbook.md](runbook.md) for what to check when one does not.

## Flags

Every one has a `VIBEPANEL_<UPPER_SNAKE>` environment equivalent, and the flag
wins. A `VIBEPANEL_*` variable nothing reads is reported at startup and by
`vibepanel doctor` rather than ignored, so a renamed setting is loud instead of
silently doing nothing.

| Flag | Default | Notes |
|---|---|---|
| `--data-dir` | `~/.local/share/vibepanel` | database, tmux config, ACME state |
| `--addr` | `:8443` | listen address |
| `--domain` | — | public hostname; also the WebAuthn Relying Party ID |
| `--tls` | `off` | `off`, `files` or `acme` |
| `--tls-cert` / `--tls-key` | — | for `--tls files`; reloaded when the files change |
| `--acme-dns` | — | DNS-01 provider for `--tls acme` (`cloudflare`) |
| `--acme-email` | — | contact address for the CA |
| `--acme-directory` | Let's Encrypt | point at a staging endpoint while testing |
| `--allow-from` | — | CIDRs allowed to reach the panel; empty means all |
| `--trusted-proxies` | — | CIDRs whose `X-Forwarded-For` may be believed |
| `--tmux-socket` | `vibepanel` | keep it dedicated to stay isolated |
| `--static-dir` | — | serve the frontend from disk instead of the embedded build |

`vibepanel doctor` runs fifteen checks and does not stop at the first failure:
tmux and its version, the data directory, a running panel, the hook endpoint,
the database and a real write to it, disk, the tmux server and its config,
socket isolation, installed agent hooks, hook URLs and tokens that live sessions
still hold, passkeys, and unread `VIBEPANEL_*` variables.
