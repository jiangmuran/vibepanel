<div align="center">

<img src="docs/images/icon.png" width="76" height="76" alt="">

# vibepanel

**A web console for running many parallel coding-agent sessions.**

tmux keeps the processes alive. The browser owns everything else: how sessions are
grouped into projects, what they are called, which one needs you right now, and
what order they appear in.

[![check](https://github.com/jiangmuran/vibepanel/actions/workflows/check.yml/badge.svg)](https://github.com/jiangmuran/vibepanel/actions/workflows/check.yml)
[![Go 1.26](https://img.shields.io/badge/go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![runtime deps: tmux only](https://img.shields.io/badge/runtime%20deps-tmux%20only-3fb950)](#requirements)
[![license: MIT](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

**English** · [简体中文](README.zh-CN.md)

</div>

![The panel](docs/images/panel-dark.png)

## The problem

Run a dozen coding agents at once and a terminal multiplexer gives you a flat strip
of tabs called `bash`. You cannot tell which agent is waiting on a confirmation and
which is still working without clicking into each one. Tabs belonging to one project
are scattered among tabs from five others. None of it is usable from a phone.

That is a task-management problem wearing a terminal costume. vibepanel splits the
two apart: **tmux does process persistence, the web UI does organisation.**

## What you get

- **Sessions that outlive the panel.** Every session is a tmux session on a
  dedicated socket. Restart, upgrade or crash the panel and nothing notices —
  the processes are children of the tmux server, not of this program.
- **A status you can read across the room.** *working*, *waiting for you*, *done*.
  Waiting sorts first and is the loudest thing on screen. States are told to the
  panel by the agent where a hook is installed, and read from the output stream
  where it is not.
- **Projects, names that stick, and ordering that means something.** Rename a
  session and it stays renamed; the automatic title from the pane never overwrites
  a name you chose.
- **The same session in several places at once.** One authoritative grid per
  session, owned by whoever last typed. Other viewers scale it rather than reflow
  it — an agent's TUI is not confetti on your phone.
- **A phone layout that is not a shrunken desktop.** A command composer that gets
  along with an IME, a soft key row with `esc` `tab` `ctrl` and the `y`/`n`/`1`/`2`
  answers agents actually ask for, and touch selection with drag handles.
- **Files without a transfer protocol.** Click to download. Drop onto the terminal
  to upload — it lands next to the session and types its absolute path at the
  prompt, ready to press enter on. Paste a screenshot straight into the terminal
  and the same thing happens.
- **Notes, todos, a file tree and system load** per project, in the side panel.
- **English and 简体中文**, chosen automatically from the browser and switchable in
  settings.
- **Install as an app.** A PWA with notifications, so a session that starts waiting
  reaches your phone with the panel in the background.
- **An HTTP API for agents**, with tokens that are separate from your password —
  see [docs/api.md](docs/api.md).
- **Passkeys, passwords, TLS of its own**, including automatic certificates over
  DNS-01. It is designed to face the public internet.

<div align="center">
<img src="docs/images/panel-light.png" width="49%" alt="Light theme, in Simplified Chinese">
<img src="docs/images/phone.png" width="20%" alt="The phone layout">
</div>

## Requirements

- **tmux 3.3 or newer** (`apt install tmux`).

  3.3 rather than 3.2 because the embedded config sets `allow-passthrough`, which
  arrived in 3.3. An older tmux does not refuse to start: it reports an unknown
  option, carries on with defaults, and the sequences agent TUIs use for progress
  and notifications are quietly swallowed from then on.
- **Nothing else.** The release binary is static and self-contained — the frontend,
  the database driver and the TLS client are all inside it.

## Install

From a release archive, on any machine with tmux:

```sh
tar -xzf vibepanel_<version>_linux_amd64.tar.gz
cd vibepanel_<version>_linux_amd64
./deploy/install.sh --enable          # everything it touches is under $HOME
journalctl --user -u vibepanel -n 30  # the one-time setup token
```

Nothing needs root: it is a systemd *user* service, because the panel runs your
agents as you, with your keys and your dotfiles. The installer also enables
lingering for you, which is not optional — a user service stops when your last
login session ends, and a panel that dies when you log out is a panel that only
appears to work.

Open `http://<host>:8443`, paste the setup token, choose a password. That is the
whole first run.

<details>
<summary><b>Running it as a system service instead</b></summary>

Use `deploy/vibepanel-system.service` if the machine runs close to its memory, or
if you want the panel up before anyone logs in:

```sh
sudo cp deploy/vibepanel-system.service /etc/systemd/system/vibepanel.service
sudo sed -i "s/__USER__/$USER/g; s#__HOME__#$HOME#g" /etc/systemd/system/vibepanel.service
sudo systemctl daemon-reload && sudo systemctl enable --now vibepanel
```

The difference is measured, not theoretical: a *user* unit asking for
`OOMScoreAdjust=-500` gets `100`, because lowering it needs `CAP_SYS_RESOURCE` and
a user manager does not have it. `systemd-analyze verify` accepts the directive
either way. A system unit with `User=` sets it before dropping privileges and the
process really reads `-500` — the panel and the tmux server holding every session
are then the last things the kernel reaches for.

</details>

<details>
<summary><b>From source</b></summary>

```sh
cd web && npm ci && npm run build && cd ..
make build            # CGO_ENABLED=0 go build -o vibepanel ./cmd/vibepanel
./vibepanel doctor    # checks tmux, the database, disk, and isolation
./vibepanel serve
```

</details>

<details>
<summary><b>Docker</b></summary>

```sh
docker compose -f deploy/docker-compose.yml up -d
```

**In a container, restarting the panel kills every session.** Everywhere else the
tmux server outlives the Go process, which is what makes `systemctl restart` and an
upgrade harmless — the whole premise of the project. Inside a container tmux is a
child of the entrypoint and the container is the boundary, so `docker restart`, a
rebuild, and anything that recreates the container take the agents with them.
Nothing can be done about that from inside the image.

Agents also run with the container's tools, keys and repositories, which is a
smaller world than most people expect. Run it this way only if the sessions are
cheap to lose.

</details>

## Why the sessions survive

This is the one property everything else is arranged around, so it is worth being
explicit about how it is kept:

- The panel never owns a PTY that a session's process is a child of. It runs
  `tmux attach` as a client, exactly like you would.
- The systemd unit sets `KillMode=process`, so stopping the service kills the panel
  and leaves the tmux server and every agent under it alone.
- The tmux socket is `-L vibepanel` with its own config file, never the default
  one. You can run this beside an existing tmux or zellij setup with weeks-old
  sessions in it, and `vibepanel doctor` asserts that its socket contains nothing
  but its own sessions.

```sh
systemctl --user restart vibepanel   # the panel goes away and comes back
tmux -L vibepanel ls                 # every session still there, still running
```

The browser notices the new build on reconnect and offers to reload, so an upgrade
mid-session is a banner rather than a puzzle.

## Telling the panel what an agent is doing

Without help, the panel reads the output stream: bytes recently means *working*,
a terminal bell means *waiting*, and a pane back at a shell prompt means *done*.
That is honest but coarse.

Settings → **state reporting** installs a small hook into Claude Code's or Codex's
own configuration, showing you the exact JSON first and backing up the file it
merges into. The hook reads two environment variables the panel injects into each
session and posts the state directly:

```json
{"sessionId": "…", "state": "waiting"}
```

Sessions started outside the panel do not have those variables, so the same global
hook config is a no-op for them. Removing it takes only the entries vibepanel
tagged.

## Configuration

Every flag has a `VIBEPANEL_<UPPER_SNAKE>` environment equivalent. Flags win. Any
`VIBEPANEL_*` variable that is not recognised is *reported* at startup and by
`doctor` rather than ignored — a misspelled `VIBEPANEL_TLS` used to mean a panel
serving plaintext on a public port while its operator believed otherwise.

| Flag | Default | Notes |
|---|---|---|
| `--data-dir` | `~/.local/share/vibepanel` | database, tmux config, ACME state |
| `--addr` | `:8443` | listen address; the default is every interface |
| `--domain` | — | public hostname; also the WebAuthn Relying Party ID |
| `--tls` | `off` | `off`, `files`, or `acme`. `off` on anything but loopback is warned about at startup: the terminal, the password and the session cookie all cross the network in the clear |
| `--tls-cert` / `--tls-key` | — | for `--tls files`; reloaded when the files change |
| `--acme-dns` | — | DNS-01 provider for `--tls acme` (currently `cloudflare`) |
| `--acme-email` | — | contact address for the CA |
| `--acme-directory` | Let's Encrypt | point at a staging endpoint while testing |
| `--allow-from` | — | comma-separated CIDRs allowed to reach the panel |
| `--trusted-proxies` | — | CIDRs whose `X-Forwarded-For` may be believed |
| `--tmux-socket` | `vibepanel` | keep it dedicated to stay isolated |
| `--static-dir` | — | serve the frontend from disk instead of the embedded build |

### Signing in

First run prints a one-time setup token to the console. That is the handover:
whoever can read the server's output is entitled to claim the panel, and merely
reaching it over the network is not. The setup endpoint closes permanently once an
account exists.

Everything except the health probe and the agent-hook endpoint needs a credential,
the WebSocket included — it *is* the terminal.

Failed logins are throttled per source address with exponential backoff, and
`--allow-from` narrows who may reach the panel at all. Both judge the address
`--trusted-proxies` says to believe: with no trusted proxy configured, that is the
peer on the socket and `X-Forwarded-For` is ignored entirely. A header that can
rename the caller turns both controls off.

### Passkeys

WebAuthn needs a secure context and a Relying Party ID that is a registrable domain
name. **An IP address is never a valid RP ID**, so `https://192.168.1.10:8443`
cannot register a passkey however the TLS is arranged. Use a hostname.

Password login always works and is set up on first run. Passkeys are an addition on
top of it, never the only way in. Both `vibepanel doctor` and the sign-in screen
report whether the current configuration supports them, and say why not if it does
not.

### Certificates

```sh
# your own certificate, reloaded when it changes
vibepanel --domain panel.example.com --tls files \
          --tls-cert /etc/ssl/panel.pem --tls-key /etc/ssl/panel.key

# or issued and renewed automatically
CLOUDFLARE_API_TOKEN=… vibepanel --domain panel.example.com \
          --tls acme --acme-dns cloudflare --acme-email you@example.com
```

HTTP-01 validation needs port 80, which this panel does not expect to have, so
automatic certificates use DNS-01.

## Driving it from a program

```sh
TOKEN=…   # Settings → API tokens

curl -sX POST https://panel.example.com:8443/api/sessions \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"projectId":"…","title":"billing","command":["claude"]}'
```

Everything the panel's own frontend does is available over the same API.
[docs/api.md](docs/api.md) is the whole surface, and it is checked against the
router in both directions — an endpoint that is not documented fails the build,
and so does a paragraph describing a route that no longer exists.

## Design notes

**The page is a view, not the state.** Close it, open it in three places, reload it
mid-command — the sessions never notice. All state lives in the backend and is
broadcast to every connected client.

**One authoritative grid per session.** A desktop at 200×50 and a phone at 45×20
cannot both size the same terminal. Rather than reflowing, the panel keeps one grid
owned by whoever last interacted and other viewers scale it to fit. Everyone sees
the same bytes in the same grid.

***Done* means the process exited, not that a session went quiet.** An agent
thinking, waiting on a slow tool call, or writing somewhere other than the screen
produces no output for as long as it likes, and reporting that as finished is the
panel giving a confident wrong answer to the only question it exists to answer.
Without a hook, a silent running agent is reported as *working* — true whether it
is thinking or asking — and the two signals that mean a person is genuinely needed,
the terminal bell and a hook report, outrank it.

**Colour is never the only carrier of meaning.** Each state has a shape as well as
a hue: circle, triangle, check. People read this panel at 2am on a phone in a dark
room.

**Files move by HTTP, not through the terminal.** In-band transfer protocols fight
with full-screen TUIs, and the reason to put a screenshot on the server is to hand
it to the agent — so the path being ready to press enter on is the feature, not a
detail.

## Development

```sh
make check         # vet, gofmt, eslint, Go tests, frontend units — the fast gate
make verify        # everything, including the browser checks (~20 min)
make head-check    # build and test a clean worktree at HEAD, not your tree
```

`make check` never starts a browser, and most of this project's bugs were found by
the ones that do:

| | |
|---|---|
| `make first-run-check` | the setup wizard and the first project |
| `make render-check` | layout, states, arbitration, panels, mobile, clipboard, passkeys |
| `make stress-check` | wide characters, full-screen programs, scrollback, floods, dropped sockets |
| `make restart-check` | kill the backend; the sessions and the login must outlive it |
| `make scale-check` | two dozen sessions: snapshot size, sidebar reachability, poller |
| `make tls-check` | its own TLS: wss, the Secure cookie, swapping a certificate |
| `make release-check` | build the archives and run one from a throwaway HOME |

The tmux wrapper is tested against a real tmux on a throwaway socket rather than a
mock — the bugs worth catching there are tmux's, and a mock reproduces none of them.

`AGENTS.md` has the conventions and the red lines. `docs/build-log.md` is the
chronological record of what was built and what went wrong, including a tmux 3.6
crash that shaped one of the core design details. `docs/runbook.md` is where to
look when a running deployment misbehaves.

## License

[MIT](LICENSE)
