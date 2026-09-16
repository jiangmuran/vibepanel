# Features

What each part of the panel does, at whatever length that takes. The README has
the short version; this is where it was moved when that page became a manual.

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

Which means **done is a state the guess cannot reach**. A finished agent has
not exited -- it is sitting at its own prompt waiting for you -- so what the
panel sees is a running process, and the session stays blue until you look. The
first-run tour opens on this for a reason.

**Settings → State reporting** has a button per agent: Claude Code, Codex, Kimi
Code, zcode and opencode. It installs a hook into the agent's own configuration,
showing what it will write and backing up the file first. Each is a different
mechanism in a different file and they fail separately, which is why there is a
row each rather than one "turn reporting on".

The panel knows five and offers three -- Claude Code, Codex and opencode. Tick
the rest below the rows; an agent whose hooks are installed is shown whatever
the ticks say, so nothing you have turned on can be hidden from you. Two of them
have a wrinkle worth knowing: Codex runs a hook only after `/hooks` has trusted
it, and zcode ignores every hook until `hooks.enabled` is `true`, which the
install turns on and only turns back off if it was the one that turned it on. The hook reads two environment
variables the panel injects into each session and posts the state:

```json
{"sessionId": "…", "state": "waiting"}
```

Sessions started outside the panel do not have those variables, so the same
global configuration does nothing for them. Uninstalling removes only the
entries vibepanel added. The hook is a `/bin/sh` script that calls `curl`;
without curl the panel falls back to the heuristic.

Claude Code exposes four events and reports *working*, *waiting* and *done*.
Codex gets the same three through its own hooks, in `~/.codex/hooks.json`
(under `$CODEX_HOME` if that is set): a turn starting or a tool running is
*working*, an approval prompt is *waiting*, a turn ending or being interrupted is
*done*. **Codex runs hooks only once you trust them: after installing, run
`/hooks` in Codex once.** The settings row says whether Codex has recorded that,
and how many running Codex sessions have actually reported. An older install
that used Codex's `notify` line is switched over the next time you press
install.

A Codex session with no hook reporting -- hooks not installed or not yet
trusted, or a Codex too old for them -- is not left to the guess on Linux: the
panel reads the session's own rollout file, which it finds through the files
the pane's `codex` process has open, and takes the turn's start, end and
approval requests from it.

opencode takes a standalone plugin rather than a merge into its configuration.

Any other agent can report by posting to `/api/hook/state` itself; the shape is
in [docs/api.md](api.md).

## The side panel

Two tabs per project — **Files** and **Notes** — over a dock that is the same on
both. Pressing **Notes** while you are already on it switches to a note that
belongs to no project, and pressing it again goes back. The global one opens
with no project selected, which is where you write down what you are about to
go and do.

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

A **share link** is a URL for a second screen: `https://<panel>/share/<token>/`.
It opens one **share page** and nothing else: no terminal, no write path, no file
browser, and no way to make a second link from the first.

A share page is HTML, usually written by an agent in one of the panel's own
sessions, reading the panel's data through a small SDK. Everything to do with
it is on the **Sharing** page — its own address, `/sharing`, on the same
sign-in as the panel, and where Settings → Sharing leads — which is one list:
each page, and under it the links that show it.

### Making one, publishing it, handing it out

1. **New page.** Give it a name and pick a starting point — a session wall,
   token spend, what got built, a phone glance, or blank — blank really is
   blank, and a template is a starting point the agent is free to throw away.
   It goes in the pages directory as `page-<name>` unless you give it a
   directory, and it becomes a project called `page-lobby`. The pages directory
   is `~/.local/share/vibepanel/pages` (the data directory, beside pasted
   screenshots) until you change it on the line under the page list; nothing is
   stored until you do. With
   *start an agent* ticked, an agent opens in it with a first line already typed
   at its prompt for you to finish.
2. **Write it.** Above the project's file list, **Share page** opens the Preview
   beside the terminal:
   - it redraws each time the agent finishes writing a file, on the real data or
     on a made-up one — forty sessions, nothing at all, no names, names full of
     markup;
   - errors the page throws are listed under it, and written to a file the agent
     reads;
   - **pick** an element and a line pointing at it is typed at the agent's
     prompt, for you to finish with what should change;
   - side by side on a phone, a laptop and a television, in the window;
   - click the preview to open it large over the whole window, switch screens
     there, and press Escape to put it away.

   From a shell, `vibepanel page check` says what is wrong with a page, file and
   line, and `vibepanel page shot` screenshots it on the screens it is for and
   reports what broke — which is how an agent checks its own work.
3. **Publish.** From the Preview, from the page's row in settings, or with
   `vibepanel page publish` in its directory. Publishing stores the directory as
   it is now as the next version; nothing a link shows changes until you do.
4. **New link**, under the page (it needs a published version). Name it, give
   the screen a label, choose what it may say and for how long, and set the
   page's own settings for this screen. The URL is shown **once** — copy it, or
   open it — because the panel keeps only a hash.
5. **Open it on the screen.** That is the whole install.

Afterwards, from the same list:

- **View** (the eye on a link's row) opens what that link is showing right now,
  in a new tab, through a fifteen-minute copy of the link. The real address
  cannot be shown again, so this is how you look at it.
- **Publish** again and every link following the published version picks it up
  on its next poll, without anybody touching the screen. A link can instead be
  **pinned** to one version.
- **Try it on a screen** from the Preview puts the draft on one link for ten
  minutes; the screen goes back by itself if you do nothing.
- **Versions** lists every published version, and any of them can be rolled
  back to.
- **Open** takes you back to the page's project with the Preview beside it. If
  its directory has gone — deleted, a new machine, a wiped data directory — the
  button says **Restore and open** and writes the published version back first;
  if the `page-…` project was removed, it is made again.

**Export** (the download on a page's row) saves the published version as a zip,
or the directory as it is for a page never published; **Import** above the list
makes a new, unpublished page from one. `vibepanel page export` and `vibepanel
page import` do the same from a shell.

If the pages directory cannot be written — a disk that is gone, a data directory
under a system unit's `/var/lib` — new pages go to the next place that works:
`~/.local/share/vibepanel/pages`, then a directory in `/tmp`, and the line under
the list says which and why.

A page can declare settings — a title, a colour, a threshold — that are changed
per link, from the link's row, and reach the screen without a reload. Each row
also says how many screens have the link open right now. **Lock** a link and its
page, version, settings and trials cannot be changed until it is unlocked, which
is what stops the one a customer is watching being changed from a row left open.

Give a link a **remark** — "the screen in meeting room three", "for the
customer" — and it is shown in the settings row and handed to the page. It is
shown in both detail levels, because it is the owner's sentence to whoever is
standing in front of the screen rather than one of the panel's own words.

### What a page can show

The numbers are what the panel knows and what the repositories say, and a page
names which of these it wants in its `vibepanel.json`; nothing else is sent to it.

**What it cost**: what the agents recorded spending, by day, by agent, by model,
by project. Tokens, never money — prices differ by model and tier and change,
and a figure from a stale table is a confident wrong number on a wall.

**What came out**: commits, lines added and removed, files touched — today, over
a window, and as a series. Pull requests open, checks green or red, and what was
merged today, where the panel has been given a GitHub token. These are counted
by reading the working trees, so they are things that exist now and did not this
morning rather than things somebody remembered to tick off. Lines are always two
numbers and never a net one: +1200/−800 is a different day from +400/−0 and the
net figure is the same in both. Work an agent has not committed yet is invisible
to all of it.

**How the day went**: what started, what went quiet waiting for a person, what
finished, and how long things sat before somebody got to them — hour by hour, or
day by day. And a feed of what just happened, which is the thing on a wall that
moves.

Plus the sessions and their states, checklist progress, and the machine's load.

### What a link may say

A link is scoped to the whole panel, one project or one session, and the server
enforces that from the link's own row. Delete the project a link was scoped to
and the link shows nothing, rather than falling back to the whole panel. Detail
is either **counts and states**, the default, which carries shapes and numbers
and no text at all, or **names as well**, which adds session titles and project
names. Neither ever sends a path, a working directory, a command line, a
hostname or the panel's own ids. The name, remark, lock, version and settings
can be changed later; the detail level and the scope cannot, because by then
the URL is in somebody's email and widening what it discloses is a change nobody
holding it would see.

The link is a credential: anyone holding it can watch. Links are revoked
individually, can be given an expiry, and their creation and revocation are in
the audit log. A revoked or expired address answers with a plain page saying the
link no longer works, and nothing of the panel's.

A page runs sandboxed: it cannot read the panel's cookies or storage, call the
panel's API or open its terminal, and cannot reach any other address. The SDK
says *live*, *reconnecting*, *disconnected* or *revoked*, so a page that has
quietly frozen does not look like a quiet machine. [share-pages.md](share-pages.md)
has how.

### Links from before pages

Earlier releases drew **boards** — arrangements of widgets — on share links.
Boards are gone. On the first start after upgrading, every existing link is
pointed at a page made from the template closest to what its board showed (what
got built, token spend, a phone glance, or a session wall), published, in the
same `page-…` directories. The addresses on walls keep working, with the same
detail level, scope, remark and expiry.

## The first run

A panel with no account prints a one-time token; you paste it, choose a
password, and the tour opens.

Five steps, and two of them do something. It installs state reporting for the
agents the panel is set up for -- separate mechanisms in separate files, a
button and an answer each -- and it offers the rest of Claude Code's settings: session mirroring,
Remote Control, the commit and pull-request attribution, the billing header.
Every key is printed with the value on disk beside the value that would replace
it, and the file is copied before anything is written.

It is put away for good on the server rather than in the browser, so reading it
on a laptop settles it for the phone as well.

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

## Settings that live in a file

**Settings → This panel** edits the service's environment file: the address,
the domain, TLS and its certificates, ACME, and who is allowed to reach the
panel at all. It writes the same file the installer wrote, keeping every
comment and commented-out example where it was, and copies it first.

These take effect on the next start, so the restart button is the next block
down. It stops the panel and lets the service manager bring a new one back --
which costs the connection and nothing else, because tmux owns the sessions. On
a machine where nothing would restart it, the button says so instead of
stopping.

Two settings are shown and not editable. `CLOUDFLARE_API_TOKEN` is a
credential, so the page never receives it. `VIBEPANEL_TMUX_SOCKET` is the one
that keeps the panel away from your own tmux: a panel pointed somewhere else
cannot see its own sessions, and the ones it was managing keep running with
nothing attached to them.

## On a network

The panel listens on `:18443`, on every interface, and is built to face the
public internet.

Everything needs a credential, including the WebSocket, which is the terminal.
The exceptions are the health probe, the hook endpoint (which takes a token
injected into every session) and the share link (which takes a share token, on
its page's files and its snapshot — `GET`s only — and is rejected
everywhere else).

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

