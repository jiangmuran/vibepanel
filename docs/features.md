# Features

What each part of the panel does, at the length that needs. The README has the
short version; this is where it was moved when that page became a manual.

Links here are relative to `docs/`.

## Sessions

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

## States

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

## State reporting

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
in [docs/api.md](api.md).

## The side panel

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

## On a phone

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

## Launch profiles

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

## Screens for other people

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

## Restarts, reboots and upgrades

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

[docs/runbook.md](runbook.md) is organised by symptom for everything else.

## On a network

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

