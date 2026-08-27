# Writable share links: a proposal, not a feature

Nothing in this document is implemented. It exists because "read only 或者允许
对方编辑" was asked for, and because building it on a judgement call would have
undone the one property that makes share links defensible.

## What is there now

A share link reaches exactly one `GET`. That is not a policy, it is the shape of
the router:

```go
r.Route("/share/{token}", func(r chi.Router) {
    r.Use(s.requireShareToken)
    r.Get("/dashboard", s.handleShareDashboard)
})
```

`requireShareToken` resolves the token against `share_links` and nothing else,
and `currentUser` never consults `share_links`. So a share token presented as a
cookie or as a `Bearer` header is not a credential — it is an unknown string,
and every authenticated route in the panel already answers `401` to it. Red line
8 in `AGENTS.md` is that sentence.

The alternative shape — a `readOnly` flag on the session that each handler
consults — is a hole in whichever handler is written next. That is the whole of
the argument, and it does not get weaker because the feature would be useful.

## What "edit" would mean, from least to most dangerous

1. **Notes and todos on one project.** Writes to two tables the panel owns.
   No process is started, no bytes reach a PTY, and the worst case is somebody
   writing nonsense into a checklist the owner can see and undo.
2. **Session state.** Marking something done or waiting. Same shape as (1) —
   a column on a row — and it is what a collaborator watching a queue would
   most naturally want to do.
3. **Restarting or killing one session.** Starts and stops processes. The
   command is already recorded, so this cannot run something new; it can still
   end work in progress.
4. **Creating a session.** Runs a command in a directory. This is the line: the
   panel would be executing something on behalf of a URL.
5. **Terminal input.** A URL that is a shell on somebody's machine.

(4) and (5) are refusals at any setting. Not "off by default" — there should be
no setting that turns them on, because a bearer token in a URL is a credential
that ends up in chat logs, screenshots, browser history, a television's
"recently visited", and the referer header of anything the page links to. A
password can be rotated after a leak nobody noticed; a URL somebody pasted into
a group chat two months ago cannot be un-noticed.

(3) is arguable and is not worth arguing for first. (1) and (2) are the useful
half and are the ones a collaborator actually asks for.

## The shape it would have to take

**A different table, not a column.** `share_links` must stay the table that
grants exactly one `GET`, because that is what makes every other route's `401`
free. A writable link is a second table — say `collab_links` — resolved by its
own middleware, mounted under its own prefix, and consulted by nothing else.
Two tables means "can this credential write" is answered by which lookup
succeeded rather than by a field somebody has to remember to read.

**Its own routes, and few of them.** Not "the panel's routes with a check".
Something like:

```
PUT    /api/collab/{token}/notes
POST   /api/collab/{token}/todos
PATCH  /api/collab/{token}/todos/{id}
DELETE /api/collab/{token}/todos/{id}
PATCH  /api/collab/{token}/sessions/{id}/state
```

Every one of them scoped by the link's own row, the way the dashboard already
is: the `{id}` in the path is a per-link pseudonym, resolved server-side against
the scope, so a request naming another project's todo resolves to nothing rather
than to that todo. That is the same mechanism `shareID` already provides, run
backwards, and it is the part that needs a test before it needs an
implementation.

**Scope is mandatory.** A writable link with no scope is a writable link to the
whole panel, which nobody would ask for on purpose. `scope` should be
`NOT NULL` and non-empty on that table.

**Expiry is mandatory, and short.** The read-only link offers "never" because a
monitor on a desk is a real case. A writable link has no such case: it is for a
piece of work that ends. Thirty days is a defensible cap; the default should be
days, not months.

**Revocation has to be visible.** The settings page should show, per link, when
it was last used and what it last wrote — a writable credential nobody can audit
is one nobody will revoke, because they will not know it is still in use.

**Everything it does is audited**, with the link's own name in the row:
`collab.note`, `collab.todo`, `collab.state`. Every one of them on the explicit
list in `TestEveryAuditEventIsAccountedFor`.

**A separate front-end root**, the way `/share/<token>` already is. Not the
panel with buttons enabled: the panel's shell fetches state, opens the socket
and offers to start a session, and "the panel with the dangerous parts turned
off" is a list of things somebody has to keep turning off.

## What would have to be proved before it ships

Each of these is a test that must fail when its code is removed:

- A collab token answers `401` on every `/api/share/...` route and every
  authenticated route, presented as a path segment, a cookie and a header.
- A share token answers `401` on every `/api/collab/...` route, the same three
  ways. The two tables are not interchangeable in either direction.
- A collab token scoped to project A cannot read or write anything in project B,
  including by naming B's real ids, B's pseudonyms under a different link, or an
  id from before A was created.
- A collab token cannot open `/ws`.
- A collab token cannot create a session, restart one, or reach `/api/browse`.
- A revoked collab link stops working on its next request.
- A collab link whose scope has been deleted writes nothing, rather than writing
  to whatever the empty filter matches.

## The recommendation

Build (1) and (2) — notes, todos, session state — on their own table and their
own prefix, or build nothing. Do not add a `writable` column to `share_links`.
The column is one line, it will look obviously correct in review, and it ends
the property that makes every other `401` in the panel free.
