# The command line

`vibepanel` is the panel's binary, and with a subcommand it is the panel's
command line: everything the sidebar and the settings page can do, from a
shell, for a panel in a container where there is no browser at hand.
`vibepanel --help` lists the commands; each has its own `--help`.

## How it reaches the panel

A command that changes what the panel shows -- a rename, a restart, text
typed into a session -- goes through the running panel's own API, so it is
the same handler the browser uses: audited, pushed to every open tab, tmux
asked by the process that owns it. The address is the one the panel wrote
into `<data dir>/panel.url` when it started (and removes when it stops),
so the CLI reaches the panel that owns its data directory and never another
one on the default port; `VIBEPANEL_CLI_URL` names one across a wire.

The credential is an API token named `cli`. The CLI mints it itself on
first use, into the panel's token table -- it runs as the panel's user with
the database in reach, so this is a default and not a boundary -- and
keeps it in `<data dir>/cli.token`, mode 0600. It is on the settings page's
token list like any other; revoking it there (or `vibepanel token revoke
cli`) locks the shell out until the next command mints a new one, which the
activity log records. `VIBEPANEL_CLI_TOKEN` supplies a token instead, for a
panel across a wire -- over https; plain http off loopback is refused
unless `VIBEPANEL_CLI_INSECURE=1`, because the token would cross the wire
in the clear. `panel.url` is trusted only while the panel holds the data
directory's lock: a file a crash left behind points nowhere, and no token
is sent to whatever bound that port since.

`--data-dir` and `--tmux-socket` may go anywhere on the command line; they
become `VIBEPANEL_DATA_DIR` and `VIBEPANEL_TMUX_SOCKET` for the command.
Every command answers `--help`.

Without a running panel -- or before the first account exists, when there
is nothing to mint a token for -- the subcommands that existed before the
panel could be asked (`project ls|add|archive|restore|rm`, `session
ls|new|kill`) work on the database and tmux directly and say so on stderr.
The rest say the panel is not running.

## Naming things

A project is named by its id, its exact name, an unambiguous start of its
name, or its directory. A session by its id, its exact title, or an
unambiguous start of its title. A name that fits two things is an error
that lists both rather than a guess. Flags may come after the thing they
qualify: `project add ~/work/api --name API`.

`--json` on the listing commands prints the panel's answer as it came, for
scripts.

## The commands

| | |
|---|---|
| `status` | the projects and sessions as the sidebar shows them: state (● working ▲ waiting ✓ done ○ none), title, live, frozen, pinned |
| `project ls` | projects, with their session counts, then the archived ones |
| `project add <dir> [--name N]` | register a directory (an archived project at that directory comes back instead) |
| `project rename <p> <name>` | |
| `project pin\|unpin <p>` | |
| `project order <p> <p> … \| --auto` | a manual order for the ones named, the rest after them; `--auto` is by activity again |
| `project archive\|restore\|rm <p>` | `rm` kills the project's sessions first |
| `session ls` | every session with its state, grouped by project |
| `session new --project <p> [--title T] [--cols N --rows N] [-- cmd…]` | a session; no command runs the project's default |
| `session rename <s> <title>` | the title is then manual: the next poll does not overwrite it |
| `session pin\|unpin <s>` | |
| `session state <s> working\|waiting\|done` | set the state by hand |
| `session restart <s>` | the same command again, in place: the session keeps its id and its pane |
| `session kill <s>` | |
| `session send <s> [text] [--enter]` | type into it; with no text, stdin; `--enter` presses Enter after |
| `session screen <s>` | the pane as text |
| `session info <s> summary <text> \| progress <0-100> \| labels a,b \| clear` | what the session says about itself, set from outside |
| `session say <s> <text>` | a line in the session's messages: the chat card shows it, the routes push it by their rules, every open page gets a quiet toast |
| `session notify <s> <text> [--level info\|warn\|ask] [--choices a,b]` | ring every open page and every paired phone whatever the state, with the choices as buttons; a pressed choice is typed into the session |
| `session attach <s> <file> [--caption "…"]` | a file into the session's attachments and to the phone: an image as a photo, anything else as a card with the download link |
| `session attachments <s>` | its attachments and where to download them |
| `session restore` | rebuild the sessions a reboot took |
| `note get\|set <p> [text]` | the project's note; `set` with no text reads stdin |
| `todo ls\|add\|done\|undo\|rm <p> [text\|id]` | the project's todos, which plugins and the chat assistant read; the panel's own pages no longer draw them |
| `token ls\|create <name>\|revoke <id\|name>` | API tokens; `create` prints the token once |
| `audit [-n 50]` | the activity log, oldest first |
| `settings archive-idle <days>` | archive a project idle that long; 0 never |
| `settings timezone <zone>` | |
| `settings share-origin <origin\|clear\|check <origin>\|setup <origin>>` | dedicated `scheme://host[:port]` origin for share links, `clear` for the panel's address, `check` live DNS/TLS reachability (an `https://` domain name only), or `setup` for one-click HTTPS, which saves the origin only once the check passes. Setting one prints the command that undoes it; the panel refuses a name it knows as its own |
| `page publish [--link] [--new] [--detail counts\|names] [--expires 7d]` | publish the draft as the next immutable version; `--link` reuses the page's active copyable link or creates one on the configured share origin and prints the URL |
| `page link [--new] [--name N] [--detail counts\|names] [--expires 7d] [--json]` | print or create a clickable handed-out share URL for a published page |
| `page skill [status\|install\|remove] [--agent claude,codex,agy,dsh\|detected\|all]` | inspect, install or remove the optional `vibepanel-share` agent skill/plugin bundle |
| `page …`, `plugin …`, `account …`, `service …`, `hook …`, `tune …`, `doctor` | as before: share pages, plugins, the first account, the service, the agent hooks, another tool's settings, a health check |

## From inside a session

`vibepanel me` is the agent's own voice. It runs inside a session the
panel started and needs nothing: the environment holds the session's id,
its own token and the panel's address. The token names that one session
and reaches `/api/me/v1` and nothing else, so an agent can say what it is
up to and cannot touch another session, a project or the owner's settings
(`TestASessionTokenIsItsSessionsAlone`).

| | |
|---|---|
| `me` | who am I: title, state, summary, progress, labels |
| `me state working\|waiting\|done [--why "…"]` | mark the state; the why is kept as a notice the chat card shows |
| `me title <title>` | |
| `me info set summary <text>` | one line, 120 characters, under the title in the sidebar |
| `me info set progress <0-100>` | a bar with the figure beside it |
| `me info set labels a,b` | up to five chips |
| `me info clear [summary\|progress\|labels]` | |
| `me say <text>` | a milestone: a line on the phone by the chat rules, a quiet toast on the panel |
| `me notify <text> [--level info\|warn\|ask] [--choices a,b]` | a person has to decide now: every page and phone, buttons for the choices, the pressed one typed back into the session. Six a minute |
| `me send <file> [--caption "…"]` | a result that is a picture or a file: 16 MB, the newest twenty kept, a photo on the phone or a link the owner opens signed in |
| `me guide [--write [dir]]` | the best practice, one page; `--write` appends it to the project's `AGENTS.md` once, with a `CLAUDE.md` pointing at it when there is none |

The practice, in short: mark `waiting` with a why **before** asking a
person anything; one summary per milestone, written for somebody reading
a phone at 2am; say a thing once. `vibepanel me guide` prints the rest,
and `vibepanel me guide --write` puts it into the project's `AGENTS.md`
for the agent to find.

## In a container

The panel's image runs `vibepanel serve` as its one process; `docker exec
<container> vibepanel status` is the panel from the outside, and every
command above works the same way, because the CLI finds the panel at its
own loopback address inside the container and the token file under the
data volume survives a restart of the container.
