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

<sup>The sidebar is every session, grouped by project, with whatever is blocked
on a human at the top. Triangle: stopped to ask a question. Circle: still
working. Check: finished. Cross: exited non-zero. The strip along the bottom is
a scratch terminal attached to the selected session. On the right, the project's
files, and under them the machine's CPU, memory and disk.</sup>

## What it is

vibepanel is a single Go binary that serves one web page. Every session it
creates is a real tmux session holding a shell in a project's directory, and the
command is typed in: `claude`, `codex`, a test loop, a `tail -f`.

tmux keeps the processes alive. The panel owns what tmux has no opinion about.
Sessions belong to projects. Names stick. State is readable at a glance and the
sessions that need a human sort to the top. Each project carries a file tree and
notes. The whole thing works on a phone.

The panel never owns a session's terminal, so restarting it, upgrading it or
killing it leaves the agents running under tmux.

It is not a wrapper around an agent: it does not touch prompts and does not
proxy anyone's API. It is not a team tool: there is one account. It is not a
replacement for tmux.

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
- **Real terminals.** xterm.js over a WebSocket, with WebGL rendering,
  full-screen TUIs, wide characters and scrollback. Not a log view.
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
- **English and 简体中文**, in the interface and in the installer.
- **One binary and one dependency.** The release binary is static, with the
  frontend, the database and the TLS client inside it. tmux is the only thing
  that has to be installed.

## Install

tmux 3.3 or newer is the only requirement. The installer offers to install it.

```sh
curl -fsSL https://raw.githubusercontent.com/jiangmuran/vibepanel/main/install.sh | sh
```

That works out the platform, fetches the matching release, verifies it against
the published `SHA256SUMS`, installs tmux if it is missing or too old, and
installs a service — a systemd unit on Linux, a launchd LaunchAgent on macOS. It
prints the plan and waits for agreement before touching anything. It asks only
when stdin and stdout are both terminals, so a pipeline takes the unattended
path.

Then open `http://<host>:8443`, paste the setup token it printed, and choose a
password.

**Where GitHub is not reachable**, `--mirror` routes every fetch through a
GitHub mirror, defaulting to `github.muran.tech`. That mirror authorises by IP
address: the first request answers with a link to open in a browser, which the
installer prints and waits for.

```sh
curl -fsSL https://github.muran.tech/https://raw.githubusercontent.com/jiangmuran/vibepanel/main/install.sh -o vibepanel-install.sh \
  || curl -sSL https://github.muran.tech/https://raw.githubusercontent.com/jiangmuran/vibepanel/main/install.sh
sh vibepanel-install.sh --mirror
```

Two commands rather than one: `curl -f` discards the body on an HTTP error, and
on a mirror that has not authorised the address yet that body is the link. The
second `curl` runs only in that case and prints it.

Both installers speak English and 简体中文. `--lang zh` or `--lang en` decides;
otherwise `LC_ALL`, `LC_MESSAGES` and `LANG` do, and at a terminal with none of
them set the first question asked is which language.

Afterwards there is one command, whichever way the panel runs:

```sh
vibepanel service status | start | stop | restart | logs | token | upgrade | uninstall
```

[docs/install.md](docs/install.md) is the rest: the user unit against the system
unit, every flag, unattended installs, creating the first account from the
command line, Docker, and building from source.

## Worth knowing

- **Linux, amd64 or arm64.** The machine monitor reads `/proc` and the installer
  writes systemd units. A `darwin/arm64` binary is built and the panel runs, but
  the monitor is blank and supervision is manual.
- **One account.** No sharing, no roles. A second screen gets a
  [read-only link](#screens-for-other-people).
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

### Sessions

A project is a name and a directory. A new session opens with a chosen way to
start it — a shell in that directory is the first entry, and the rest are launch
profiles. Sessions can be renamed, and a name chosen by hand stops being
overwritten by the pane title. Closing the browser tab does not reach the
session.

Sessions sort by urgency inside their project and can be pinned in place. Each
one can carry scratch terminals, the strip along the bottom of the screenshot;
they open in the same directory, for a `git status` that does not interrupt the
agent above.

<div align="center">
<img src="docs/images/panel-light.png" width="49%" alt="Light theme">
<img src="docs/images/phone.png" width="20%" alt="The phone layout">
</div>

### States

Shape carries the meaning as much as colour does.

| | | |
|---|---|---|
| ▲ | **waiting** | the agent stopped and wants a human; sorts first |
| ● | **working** | producing output, or thinking |
| ✓ | **done** | finished, or a shell at its prompt |

A process that is gone gets its own shape: a cross for a non-zero exit with the
status in the label, a hollow square for a clean one, a dashed square when the
tmux session itself has vanished. A live session can be marked *waiting* or
*done* by hand, and stays that way until it does something new.

### State reporting

Left alone, the panel reads the output stream: recent bytes mean *working*, a
terminal bell means *waiting*, a pane back at a shell prompt means *done*. A
silent session is never called finished.

**Settings → State reporting** has a button for Claude Code, one for Codex and
one for opencode. It installs a hook into the agent's own configuration, showing
what it will write and backing up the file first. The hook reads two environment
variables the panel injects into each session and posts the state:

```json
{"sessionId": "…", "state": "waiting"}
```

Sessions started outside the panel do not have those variables, so the same
global configuration does nothing for them. Uninstalling removes only the
entries vibepanel added. The hook is a `/bin/sh` script that calls `curl`;
without curl the panel falls back to the heuristic.

Claude Code exposes four events and reports *working*, *waiting* and *done*.
Codex has a single `notify` command, so a Codex session reports *waiting* and
the heuristic covers the rest. opencode takes a standalone plugin rather than a
merge into its configuration.

Any other agent can report by posting to `/api/hook/state` itself; the shape is
in [docs/api.md](docs/api.md).

### The side panel

Two tabs per project — **Files** and **Notes** — over a dock that is the same on
both.

- **Files** browses and downloads. Dragging onto the tree or onto the terminal
  uploads; the file lands next to the session and its absolute path is typed at
  the prompt. Pasting a screenshot into the terminal does the same. Preview
  reads a file's magic bytes rather than its name and handles text, PNG, JPEG,
  GIF, WebP, AVIF and PDF up to 8 MiB, truncating long text at 256 KiB or 4,000
  lines.

  An `.html` or `.svg` file is *drawn*, in a frame with an opaque origin, no
  scripts and a policy that permits it no network at all, so a page in a project
  cannot reach the panel's cookie and cannot fetch anything. **Page** and
  **Source** are a two-segment control in the header, and scripts are a per-file
  switch that starts off every time.

  Above the tree, one line of repository: the branch, how far it is from its
  upstream, how much is uncommitted, and a word when there is a conflict.
  Pressing it opens the whole thing — the changed files, the last fifteen
  commits, and a row per session sitting in a different worktree with its branch
  on it, all read off the disk with no credential. One button there asks GitHub
  about open pull requests and joins them to those branches by name; it needs
  `GITHUB_TOKEN` or `GH_TOKEN` in the panel's environment and runs only when
  pressed.

- **Notes** is a markdown pad per project, saved as typing stops.

- **The dock** is the bottom half of both tabs: token usage over the monitor,
  in the same place whichever tab is in front. Pressing either opens it into the
  side panel, and again into the whole window.

  Token usage is what the agents recorded spending — today, this week, this
  project, and a split by tool over thirty days. The numbers come from the
  transcripts Claude Code and Codex write for themselves. Nothing is estimated
  and nothing is priced; where there is no transcript to read the panel shows a
  dash rather than a zero.

  The monitor is the machine's CPU, memory and disk, and per-session CPU and
  memory summed over each session's whole process tree. The percentage is a
  share of the machine, not of one core.

### On a phone

A layout of its own: a command composer that gets along with an IME, a soft key
row with `esc` `tab` `ctrl` and the `y`/`n`/`1`/`2` answers agents ask for, and
touch selection with drag handles.

Added to the home screen it is a PWA. Its service worker deliberately caches
nothing: a cached bundle would pin the panel to an old build, and being
restartable underneath a browser is the point of the whole thing.

To hear about a session with the panel closed there is **Settings → Push
notifications**, which POSTs to a URL when a session starts waiting. Bark, ntfy
and ServerChan have presets; anything else is a custom body with `{session}`,
`{state}`, `{project}`, `{url}` and `{time}` in it. Up to twenty destinations,
each of which can be told which states to fire on.

### Launch profiles

**Settings → Launch profiles** names a way to start a session: an argv, and the
environment to start it in. The endpoint is why it exists: the same agent
pointed at Anthropic, at a company proxy and at a self-hosted gateway is three
configurations differing in one variable.

Four ship with the panel: a shell, `claude`, `codex` and `opencode`. Those
cannot be edited; a duplicate arrives with the variable names that agent reads
already filled in. A variable left empty is not set at all, so a half-filled
profile runs the agent exactly as a bare terminal would.

There is no "API host" field on purpose. Which variable carries the endpoint is
each agent's decision, and opencode has none. It chooses per provider in its
own configuration.

A variable can be marked a **secret**, and then its value is never sent back to
a browser: the settings page shows the name and says something is stored. It
reaches the process through tmux rather than a command line, so it is not in
`ps` and not in the audit log. It is **not encrypted**: it is plaintext in the
panel's database file, like everything else there.

A session remembers which profile started it, so a restore after a reboot brings
back the endpoint as well as the command.

### Screens for other people

**Settings → Read-only share links** makes a URL for a second screen:
`https://<panel>/share/<token>`. It opens a dashboard and nothing else: no
terminal, no write path, no file browser, and no way to make a second link from
the first.

The dashboard draws a **board**, chosen when the link is made.
Thirty starting points, grouped by the screen they were composed for:

| | |
|---|---|
| a phone | one column, three things, four seconds standing up |
| a laptop | overview · does anything need me · the waiting queue · everything at once · only the machine · what has gone wrong · cost against output · where it went · per project · which model is working · what today cost |
| a screen on a wall | four numbers and a clock · one number filling the screen · how busy it is · three pages that cycle · **what it is spending, live** · every session as a tile · **for a client** · **what got built** · **sitting in front of it** · the year as a grid of days |
| a 4K wall | **the room screen** · **for leadership** · a corridor screen |

Three of those are named for the room. **The room screen** is the 4K one: what
got built today at headline size, the number of agents waiting for a person
beside it, what it cost against what came out on one time axis under both, and a
feed. Five things, because a screen read across a room carries five to nine
before it is noise. **Sitting in front of it** is the same board at the highest
density, for the same screen read from the desk it sits on. **For a client** scopes itself to
one project and turns names off, because the failure there is a customer reading
another customer's project name off the screen they are sat in front of.

A template is a starting point, not a mode, and it is **arranged by dragging
it**. The preview of the wall *is* the editor: pick a tile up and drop it where
it goes, drag its right edge for width and its bottom for height, and pick the
next one out of a library of small pictures beside it. Templates are chosen the
same way — a gallery of thumbnails of the actual arrangement, not a list of
names. Every landing place is drawn for the whole drag with the one under the
pointer filled, and arrow keys do the same thing without a pointer.

There are **forty-four widget kinds**: single figures, gauges, sparklines, a
filled machine line over the last fifteen minutes, stacked token bars, ranked
breakdowns, session tiles, a dwell timeline, the year as a grid of days, what
just happened as a feed, how the day went hour by hour, what was committed, and
the furniture a composed screen needs — a spacer, a rule, a section heading, and
the screen's own name.

The grid is **twelve columns wide and four rows tall**, so a widget can be a
third of a wall or three times the height of the tiles beside it. That ratio is
where hierarchy comes from: a screen where every tile is the same size is a
dashboard, not a display. A board can also be set to **fill** the screen rather
than flow down it — nobody is going to scroll a television.

Two more settings, and they are two different questions:

- **How large** everything is drawn follows the screen and needs no setting. A
  4K television shows the same composition *bigger*, not more columns of smaller
  type; a phone gets the same board collapsed to one column.
- **How much** is on screen is the board's **density**, in three steps. The same
  wall can be a headline legible from the door and a working dashboard read
  from the chair in front of it, without rebuilding it.

**Changing a wall does not mean walking to it.** The board is edited from the
settings page on a laptop and the screen follows within two seconds, with nobody
touching it: every poll re-reads the link. The canvas being arranged is
drawn at the shape of the screen that is actually showing the link, from that
screen's own live data, and each row says how many screens have it open right
now. **Lock** a link and its board cannot be changed until it is unlocked, which
is what stops the one a customer is watching being rearranged from an editor left
open on the wrong row.

Give a link a **remark** — "the screen in meeting room three", "for the
customer" — and it appears on the screen as well as in the settings row. It is
shown in both detail levels, because it is the owner's sentence to whoever is standing
in front of the screen rather than one of the panel's own words.

The numbers are what the panel knows and what the repositories say.

**What it cost**: what the agents recorded spending, by day, by agent, by model,
by project. Tokens, never money — prices differ by model and tier and change,
and a figure from a stale table is a confident wrong number on a wall.

**What came out**: commits, lines added and removed, files touched — today, over
a window, and as a series. Pull requests open, checks green or red, and what was
merged today, where the panel has been given a GitHub token. These are counted
by reading the working trees, so they are things that exist now and did not this
morning rather than things somebody remembered to tick off. Lines are always two
numbers and never a net one, and they are labelled as *change*: +1200/−800 is a
different day from +400/−0 and the net figure is the same in both. Work an agent
has not committed yet is invisible to all of it.

**How the day went**: what started, what went quiet waiting for a person, what
finished, and how long things sat before somebody got to them — hour by hour, or
day by day. And a feed of what just happened, which is the thing on a wall that
moves.

The two halves on one time axis — what it cost beside what came out — is the
board worth pointing a television at, and it is a template of its own.

A link is scoped to the whole panel, one project or one session, and the server
enforces that from the link's own row. Detail is either **counts and states**,
which shows shapes and numbers and no text at all, or **names as well**, which
adds session titles and project names. Neither ever sends a path, a working
directory, a command line, a hostname or the panel's own ids. The board, the
remark and the lock can
be changed later; the detail level and the scope cannot, because by then the URL
is in somebody's email and widening what it discloses is a change nobody holding
it would see.

The link is a credential: anyone holding it can watch. The panel stores only a
hash, so creation is the only time it can be read. Links are revoked
individually, can be given an expiry, and their creation and revocation are in
the audit log.

### Restarts, reboots and upgrades

```sh
systemctl --user restart vibepanel   # the panel goes away and comes back
tmux -L vibepanel ls                 # every session still there, still running
```

The panel attaches to tmux as a client and never owns a session's terminal, and
it runs on its own socket (`-L vibepanel`) with its own config, so it sits
beside an existing tmux or zellij setup. Browsers with the panel open notice the
new build on reconnect and offer to reload.

A reboot is different. The tmux server is an ordinary process and its scrollback
lives in that process's memory, so the machine going down takes both.

The panel keeps what it needs to rebuild each session: the command it was
created with, its directory, its name and place, and the last 2,000 lines or 256
KiB of its scrollback, captured every thirty seconds and again at shutdown. An
orderly reboot loses no output; a power cut loses up to half a minute. On the
next start the panel offers to restore them, one or all, showing the command
each will run and where. A per-session switch restores without asking; it is off
by default.

The **processes** do not come back. An agent's context lived in its process and
in a conversation with a provider, and re-running the command starts a new agent
that remembers none of it. The restored pane says so in a banner, and the
session keeps a `restored` mark.

**Settings → Updates** fetches the newest release from GitHub, verifies it
against the published `SHA256SUMS`, swaps the binary and restarts the service,
keeping the old one as `.old`. It runs only when the button is pressed: no
scheduled check, no heartbeat, no telemetry.

It talks to `api.github.com` and has no mirror setting, so a machine that cannot
reach GitHub takes the other route: unpack a new archive and run
`./deploy/install.sh` again, which keeps the existing unit and restarts it.
Either way the sessions keep running.

Two behaviours worth knowing: a changed tmux config takes effect at the next
`start-server`, and the panel never kills its server, so the settings page and
`vibepanel doctor` report when the file on disk and the settings in memory have
diverged; and an older binary refuses a database a newer one has migrated,
naming both versions rather than opening it and dropping columns.

[docs/runbook.md](docs/runbook.md) is organised by symptom for everything else.

### On a network

The panel listens on `:8443`, on every interface, and is built to face the
public internet.

Everything needs a credential, including the WebSocket, which is the terminal.
The exceptions are the health probe, the hook endpoint (which takes a token
injected into every session) and the share dashboard (which takes a share token,
on one `GET`, and is rejected everywhere else).

First run prints a one-time setup token to the console: whoever can read the
server's output claims the panel, and the endpoint closes once an account
exists. Failed logins back off exponentially per source address, and
`--allow-from` limits who can reach the panel at all. Both judge the address
that `--trusted-proxies` says to trust; with none configured that is the peer on
the socket, and `X-Forwarded-For` is ignored.

**Passkeys** sit on top of the password and never replace it. WebAuthn needs a
registrable domain name, so an IP address can never register one however the TLS
is arranged. `vibepanel doctor` and the sign-in screen both report whether the
current configuration supports them.

**API tokens** are independent of the password: changing the password signs out
browsers and leaves tokens alone, and the reverse. A token is readable once,
when it is created.

**TLS** is the panel's own, either supplied or issued:

```sh
# your own certificate, reloaded when the files change
vibepanel --domain panel.example.com --tls files \
          --tls-cert /etc/ssl/panel.pem --tls-key /etc/ssl/panel.key

# or issued and renewed automatically
CLOUDFLARE_API_TOKEN=… vibepanel --domain panel.example.com \
          --tls acme --acme-dns cloudflare --acme-email you@example.com
```

Automatic certificates use DNS-01, since HTTP-01 needs port 80. Cloudflare is
the provider that is wired up. Leaving TLS off anywhere but loopback is warned
about at startup.

## Configuration

Every flag has a `VIBEPANEL_<UPPER_SNAKE>` environment equivalent, and flags
win. A `VIBEPANEL_*` variable nothing reads is reported at startup and by
`doctor` rather than ignored.

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
| `--vnc` | off | turn on the built-in VNC proxy; off means its routes do not exist |
| `--vnc-allow` | — | CIDRs the VNC proxy may connect out to; empty means loopback only |

The binary is also the admin CLI: `serve`, `project`, `session`, `hook`,
`service`, `account`, `doctor` and `version`. `doctor` prints fifteen checks and
never stops at the first failure — tmux and its version, the data directory, a
running panel, the hook endpoint, the database and a real write to it, disk, the
tmux server and its config, socket isolation, installed agent hooks, hook URLs
and tokens that live sessions still hold, passkeys, and unread `VIBEPANEL_*`
variables.

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

The frontend depends on [noVNC](https://github.com/novnc/noVNC), unmodified,
under the [MPL-2.0](https://github.com/novnc/noVNC/blob/master/LICENSE.txt).
That licence is per-file, so it covers noVNC's own files and nothing around
them; its source is at the link above.
