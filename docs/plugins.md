# Plugins: a design, and the argument against most of it

Nothing in this document is implemented. It exists because "正在设计的插件和 api
系统 可以让一个 harness 接入管理所有 session" was asked for, the API half was
built, and the plugin half turned out to be four different features wearing one
word.

The conclusion, up front. For a harness that manages every session, the API
already does it and a plugin system would do it worse. What does not exist is a
way to package such a harness so it is a file you drop in rather than a daemon
you supervise. That is a distribution problem and not a runtime one, and the two
should not be solved with the same mechanism.

## Four things called a plugin

They have nothing in common except the word.

### 1. "React to what sessions do"

This is what the sentence asks for, and it is done. A harness holds an API
token, opens `/ws`, and is pushed a full state snapshot within 60 ms of
anything changing. That is the coalesce window in `internal/ws/hub.go`, not a
poll interval. From there it can create sessions, restart them, rename them,
mark state, read and write notes and todos, read the machine, and type into any
terminal. `docs/api.md` is the whole surface and
`TestTheAPIDocCoversEveryRoute` keeps it honest.

A plugin doing this in-process would be the same logic with worse properties:
it could not be restarted without restarting the panel, it could not be written
in whatever language its author already uses, and its bugs would be the panel's
bugs. Every argument for it reduces to "I do not want to run a second process",
which is section 4.

### 2. "Send an event somewhere when something happens"

Also done, and by a feature nobody has connected to this question:
`PUT /api/settings/webhooks` configures an outbound HTTP request made on a
state transition, with `{state}`, `{session}`, `{project}`, `{url}` and `{time}`
substituted, to any method, URL, headers and body. Point one at a small
program of your own and you have an event-driven plugin whose runtime is
`http.Server`.

A webhook cannot read anything back; it is fire and forget. But its target
holds no credential and needs none, because a program that wants to *do*
something in response holds an API token and calls back. Both halves were
already there. What was missing is this paragraph.

### 3. "Add something to the interface"

This is the one that sounds most like a plugin. It is also the one to refuse
hardest.

The panel's origin holds a session cookie, a WebSocket carrying a writable
terminal, and a file browser rooted at the user's projects. Third-party
JavaScript on that origin is not an extension point; it is the same access the
person signed in has, granted to a file. There are two shapes and no middle
between them. Same origin, and the plugin has the terminal; calling that a
plugin API is a way of not saying "arbitrary code as you, in the page". An
iframe on another origin, and the plugin has nothing, so to get anything it
needs an API token, which is section 1 again with a rectangle around it. A
`postMessage` bridge is the first shape with a list of allowed messages, and the
list is a thing somebody extends on a Tuesday.

If a panel tab is wanted, it is a pull request, not a plugin.

### 4. "Ship it as a file, not a daemon"

The real gap is here, and it is not about capability.

Everything in sections 1 and 2 is available to a program that somebody has to
start, keep running, restart when it dies, and read the logs of. On a machine
where the panel is a systemd unit, that means a second unit, and writing a
systemd unit is more work than the twenty-line script it supervises. That is
the whole friction, and it is real.

The fix is a supervisor, not a runtime: a declared subprocess, an API token
minted for it and passed in its environment, restart with backoff, output
captured where the panel can show it. Nothing about the panel's data model
changes. No new capability exists that an API token did not already grant.

This is the only part worth building, and it is worth building last.

## What a plugin would be allowed to reach

If section 4 is ever built, the capability model is already decided by the rest
of the panel and should not be reinvented:

- A plugin reaches the HTTP API and nothing else. Not the database, not the
  tmux socket, not `internal/*`. It is a process with a token, which means every
  scope question is the API-token question and gets answered once.
- It gets its own token, minted at install and revoked at uninstall, so
  `settings/tokens` shows it by name and revoking it is the off switch. Not the
  user's token, which would make "which of these things did that" unanswerable
  in the audit log.
- The default is a token that can read and nothing else. Today API tokens are
  not scoped at all, one token does everything, so this is work that would have
  to happen first. It is the honest reason section 4 is not a small change after
  all.
- It never gets the tmux socket. Red line 1 is that the panel touches one socket
  and nothing else touches it. A plugin holding it is a plugin that can
  `kill-server`.

## Installing one

A plugin under section 4 runs arbitrary code as the user who runs the panel.
That is not a flaw to be mitigated; it is what "run my script when a session
finishes" means. What would be wrong is being quiet about it.

So, if it ships:

- Installing one is a file the person writes or a path they type, never a
  registry the panel fetches from. The panel does not phone home for the
  frontend, the database driver or the TLS client, and it is not going to start
  for this.
- The install screen says what it is, in the words above, and shows the exact
  command line that will be run. The confirm button says *Run this as you*, not
  *Install*.
- Installation, removal and every crash are audit events, sharing the `plugin.`
  prefix — `plugin.installed` / `plugin.removed` / `plugin.crashed` — for the
  reason `password.changed` and `password.change_refused` were renamed.
- There is no way to install one from a share link, an API token, or the hook
  endpoint. It is a settings action by the signed-in account, like changing the
  password.

## When a plugin misbehaves

**Nothing a plugin does may make a session's state stop updating.** The poller
is the loop that keeps every session's state current, and it already has a
scar: `fireWebhooks` runs in a goroutine and is never waited for, because a
destination taking eight seconds to answer would otherwise stall the panel's
own idea of what is happening.

That scar is why the subprocess shape is the right one:

- A separate process cannot block the poller. There is no call to wait on. The
  strongest coupling available is a webhook, which is already fire and forget
  with the goroutine to prove it.
- A crash is a restart with backoff, capped, and after the cap the plugin is
  stopped and the settings page says so. A plugin that crashes on every event is
  a plugin that would otherwise be restarted a hundred times a second.
- A loop is the operator's problem, bounded. The process gets a memory and CPU
  ceiling from the same unit the panel does; it is not the panel's job to
  schedule somebody's script.
- Its output is bounded and dropped oldest-first, like a session's ring buffer.
  A plugin printing in a loop must not fill the disk the projects live on.

The one shape that would break the rule is the one people ask for next: a
**veto** — "ask my plugin before deleting a session". That is a call the panel
makes and waits on, and every safe answer to "what if it never returns" is a
deadline after which the panel does it anyway. A veto that is ignored when the
plugin is slow is a suggestion, and a suggestion is not worth an in-process
runtime. If a synchronous hook is ever built it must be fail-open with a hard
deadline, and that property must be in the name of the thing.

## Versioning

The panel will change under a plugin, and there are only two honest ways to say
what a plugin was written against:

- The API version it speaks, because the API is the whole surface. A plugin
  declares a minimum panel version; the panel refuses to start one that asks for
  more than it is, by name and by number, the way an older binary refuses a
  database a newer one has migrated rather than opening it and dropping columns.
- Nothing else, because nothing else is exposed. This is the main argument for
  the subprocess shape over an in-process one: a WASM ABI or a Go plugin
  interface is a second surface with its own compatibility story, and the panel
  would then have two. `docs/api.md` is already checked against the router in
  both directions; a second contract would not be.

## What was considered and rejected for the runtime

- **Go's `plugin` package.** Requires cgo, and `CGO_ENABLED=0` must keep
  working, which is a stated convention rather than a preference. It also
  requires the identical toolchain and dependency graph, which for a project
  distributed as release binaries means it works for nobody.
- **WASM (wazero, which is cgo-free and would work).** A real option, and the
  cost is a host-function ABI: every capability a plugin has becomes a function
  the panel writes, documents and versions forever, and the first one anybody
  wants is "make an HTTP request", at which point the sandbox holds a program
  that can call the API, which is section 4 with several megabytes of runtime
  in between.
- **Embedded JavaScript (goja).** Same as WASM, plus a language runtime to keep
  patched, and no isolation from a loop except an interrupt.
- **Lua.** Same again, and the honest version of "it is only for small scripts"
  is that the small scripts grow.

Every one of them is a way to avoid starting a process, and starting a process
is not the hard part of this.

## What would have to be proved before any of it ships

1. **API tokens are scoped.** Today one token does everything. Until a token
   can be read-only, or limited to one project, "a plugin gets its own token"
   is a phrase that means nothing.
2. **Somebody has written the harness first.** The composition in sections 1
   and 2 — a token, `/ws`, a webhook — should have been used in anger by at
   least one real thing before it is packaged. A packaging format for something
   nobody has built yet is a guess about what it will need.
3. **The supervisor has a failing test for every one of "slow, throws, loops".**
   Specifically: a plugin that never exits is stopped at shutdown; a plugin that
   crashes in a loop is stopped and reported; a plugin producing output faster
   than it is read does not grow the panel's memory. The panel's history is
   that a periodic job nobody can drive from a test is one that ships never
   having run.

## The recommendation

Do not build a plugin runtime.

Write down the composition — an API token, the `/ws` push, a webhook — as the
supported way to attach a harness, in `docs/api.md` where somebody looking for
it will be. That composition is the feature; it was only ever missing a name.

Revisit section 4 if and when somebody has a harness that works and is annoyed
about supervising it. At that point the design is a supervisor and a scoped
token, both of which are ordinary things to build, and neither of which is a
plugin system.
