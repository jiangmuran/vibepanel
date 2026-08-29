<div align="center">

<img src="docs/images/icon.png" width="76" height="76" alt="">

# vibepanel

**A web console for a dozen coding agents at once, and for seeing which one is waiting.**

[![check](https://github.com/jiangmuran/vibepanel/actions/workflows/check.yml/badge.svg)](https://github.com/jiangmuran/vibepanel/actions/workflows/check.yml)
[![Go 1.26](https://img.shields.io/badge/go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![runtime deps: tmux only](https://img.shields.io/badge/runtime%20deps-tmux%20only-3fb950)](#install)
[![license: MIT](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

**English** · [简体中文](README.zh-CN.md)

</div>

![The panel](docs/images/panel-dark.png)

## What it is

vibepanel is a single Go binary that serves one web page. Every session it
creates is a real tmux session holding a shell in a project's directory, and the
command is typed in: `claude`, `codex`, a test loop, a `tail -f`.

tmux keeps the processes alive. What the panel owns is everything tmux has no
opinion about: sessions belong to projects, a name you chose stays chosen, and
the ones that have stopped to ask you something sort to the top. Each project
carries a file tree and notes. It all works on a phone.

The panel never owns a session's terminal, so restarting it, upgrading it or
killing it leaves the agents running under tmux.

It does not wrap an agent. Prompts are untouched and nobody's API is proxied.
There is one account, so it is not a team tool, and it does not replace tmux.

## Who it's for

The situation it is built for: several agents running at once, across more than
one repository, on a machine that stays up. A workstation also reached from a
laptop, or a VPS checked from a phone.

One agent at a time, in a terminal that is already open, needs none of this.

## What it does

- **Sessions outlive the panel.** The processes belong to the tmux server, on a
  socket of the panel's own. `systemctl restart vibepanel` costs nothing.
- **The one that is waiting is at the top.** Sessions sort by urgency inside
  their project. Claude Code, Codex and opencode report their state exactly
  through a hook the panel installs; without one, the panel reads the output
  stream.
- **Real terminals**, not a log view: xterm.js over a WebSocket, rendered on
  the GPU, with full-screen TUIs, wide characters and scrollback.
- **A phone layout, not a shrunken desktop.** A command composer that gets along
  with an IME, a soft key row, touch selection, and a push to Bark, ntfy or
  ServerChan when a session starts waiting.
- **It comes back after a reboot.** The command, the directory, the name and the
  last of the scrollback are kept, and the panel offers to rebuild each session.
- **Screens for other people.** A read-only link opens a dashboard and nothing
  else, composed from widgets, editable from a laptop while a wall displays it.
- **Launch profiles.** A named argv and environment, so the same agent pointed
  at three different endpoints is three entries rather than three retyped
  variables.
- English and 简体中文, in the interface and in the installer.
- One binary, one dependency. The release build is static, with the frontend,
  the database and the TLS client inside it; tmux is the only thing that has to
  be installed separately.

## Install

tmux 3.3 or newer is the only requirement, and the installer offers to install
it.

```sh
curl -fsSL https://raw.githubusercontent.com/jiangmuran/vibepanel/main/install.sh | sh
```

It works out the platform, fetches the matching release, checks it against the
published `SHA256SUMS`, and installs a service: a systemd unit on Linux, a
launchd LaunchAgent on macOS. It prints the plan and waits for agreement first,
and only asks when stdin and stdout are both terminals, so a pipeline takes the
unattended path. Then open `http://<host>:8443` and paste the setup token it
printed.

**Where GitHub is not reachable**, `--mirror` routes every fetch through a
mirror, by default `github.muran.tech`. That one authorises by IP address, and
the first request answers with a link to open in a browser:

```sh
curl -fsSL https://github.muran.tech/https://raw.githubusercontent.com/jiangmuran/vibepanel/main/install.sh -o vibepanel-install.sh \
  || curl -sSL https://github.muran.tech/https://raw.githubusercontent.com/jiangmuran/vibepanel/main/install.sh
sh vibepanel-install.sh --mirror
```

Two commands, because `curl -f` throws away the body on an HTTP error and on an
unauthorised mirror that body is the link. The second `curl` runs only then.

Both installers speak English and 简体中文, and at a terminal the first question
is which one.

Afterwards there is one command, whichever way the panel runs:

```sh
vibepanel service status | start | stop | restart | logs | token | upgrade | uninstall
```

The rest is in [docs/install.md](docs/install.md): user unit against system
unit, every flag, unattended installs, Docker, and building from source.

## Worth knowing

- **Linux, amd64 or arm64.** The machine monitor reads `/proc` and the installer
  writes systemd units. A `darwin/arm64` binary is built and the panel runs, but
  the monitor is blank and supervision is manual.
- **One account.** No sharing, no roles. A second screen gets a
  [read-only link](docs/features.md#screens-for-other-people).
- **Agents run as the user who runs the panel**, with that user's keys,
  dotfiles and repositories. Anyone who gets into the panel has a shell.
- **Docker loses every session on restart.** In a container tmux is a child of
  the entrypoint, so `docker restart` and any rebuild take the agents with them.
  Nothing in the image can change that.
- **A reboot loses the processes.** The panel can rebuild a session's command
  and scrollback; it cannot bring back an agent's context.
- **tmux older than 3.3 works, badly.** The 3.3 floor is `allow-passthrough`;
  below it, agent TUIs lose the escape sequences they use for progress bars and
  notifications. `vibepanel doctor` reports it.

## Features

[docs/features.md](docs/features.md) covers each part at length: sessions and
how their state is worked out, the side panel, the phone layout, launch
profiles, the read-only boards, what survives a restart, and running it on a
network.

## Configuration

Every flag has a `VIBEPANEL_<UPPER_SNAKE>` environment equivalent, and flags
win. A `VIBEPANEL_*` variable nothing reads is reported at startup and by
`doctor` rather than ignored. The full table is in
[docs/install.md](docs/install.md).

The binary is also the admin CLI: `serve`, `project`, `session`, `hook`,
`service`, `account`, `doctor` and `version`.

## Driving it from a program

```sh
TOKEN=…   # Settings → API tokens

curl -sX POST https://panel.example.com:8443/api/sessions \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"projectId":"…","title":"billing","command":["claude"]}'
```

`command` is an argv; omitting it opens a shell, which is what the panel's own
interface sends. Everything the frontend does is available over the same API.
[docs/api.md](docs/api.md) is the whole surface, and it is checked against the
router in both directions.

## Design notes

Three that change what a user does:

- **The page is a view, not the state.** Close it, open it in three places,
  reload it mid-command; the sessions never notice.
- ***Done* means the process exited**, not that a session went quiet.
- **Colour is never the only carrier of meaning.**

[docs/design.md](docs/design.md) has the reasoning behind the decisions that
would otherwise look arbitrary. [docs/build-log.md](docs/build-log.md) is the
chronological record of what was built and what fought back.
[docs/plugins.md](docs/plugins.md) and [docs/writable-links.md](docs/writable-links.md)
are two designs that were written down and not shipped, with the arguments
against them.

## Development

```sh
make check         # vet, gofmt, eslint, Go tests, frontend units — the fast gate
make verify        # everything, including the browser checks (~20 min)
make head-check    # build and test a clean worktree at HEAD, not the working tree
```

`make check` never starts a browser. Most of this project's bugs were found by
the ones that do:

| | |
|---|---|
| `make panes-check` | the side panel's pane layout: drag, drop, merge, restore |
| `make first-run-check` | the setup wizard and the first project |
| `make render-check` | layout, states, arbitration, panels, mobile, clipboard, passkeys |
| `make stress-check` | wide characters, full-screen programs, scrollback, floods, dropped sockets |
| `make restart-check` | kill the backend; the sessions and the login must outlive it |
| `make scale-check` | two dozen sessions: snapshot size, sidebar reachability, poller |
| `make tls-check` | its own TLS: wss, the Secure cookie, swapping a certificate |
| `make install-check` | both installers down every branch, in both languages |
| `make release-check` | build the archives and run one from a throwaway HOME |

The tmux wrapper is tested against a real tmux on a throwaway socket rather than
a mock; `TEST_TMUX_BIN=/path/to/tmux go test ./...` points it at another build.
`web/scripts/shots.mjs` takes the screenshots in this file by booting the real
binary and photographing it.

`AGENTS.md` has the conventions and the red lines.

## License

[MIT](LICENSE).
