# A vibepanel share page

This directory is a page that vibepanel shows on a read-only share link: a wall
display, a phone glance, a screen for a customer. It draws live data about the
panel's coding sessions through `vibepanel.js`.

Read this file before changing anything. Then read `.vibepanel/HISTORY.md` if
it exists: it is what was published before, and why.

## What is here

| file | what it is |
|---|---|
| `index.html` | the page. Add CSS, JS, images and fonts beside it as you like |
| `vibepanel.json` | the manifest: which data the page receives, its parameters |
| `vibepanel.js` | the SDK. The panel serves its own copy; this one is for reading |
| `vibepanel.d.ts` | every field of a snapshot, with what it means |
| `fixtures/*.json` | made-up snapshots for testing: `busy`, `empty`, `counts`, `hostile`, `stale`, `revoked` |

## The contract

```js
const vp = VibePanel.connect()
vp.on('snapshot', (s) => draw(s))      // every ~2 seconds
vp.on('params', (p) => applyParams(p)) // when the owner changes a setting
vp.badge(document.querySelector('#status'))
```

- **Data comes only from the SDK.** A page cannot reach any other address:
  no CDN fonts, no APIs, no images from the internet. Put every file the page
  needs in this directory and load it by relative path. The two exceptions are
  scripts from `cdnjs.cloudflare.com` and `cdn.jsdelivr.net`, and only if they
  are listed in `scriptHosts` in `vibepanel.json`.
- **A section you did not ask for is `null` or empty.** Add it to `sections` in
  `vibepanel.json`. Day series need `spend.days` / `repo.days`.
- **Names may be empty.** A link made in `counts` mode sends no session or
  project names at all, and it is the default. Draw something useful without
  them: `vp.name(row, 'session')`. Test with `?fixture=counts`.
- **Every string is untrusted.** A session title is text somebody else chose.
  Use `vp.text(el, value)` or `textContent`; never `innerHTML` with data. The
  `hostile` fixture puts markup in every name to show you where you did.
- **No browser storage.** The page runs in a sandbox with no origin, and
  `localStorage`, `sessionStorage`, `indexedDB` and `document.cookie` throw.
  Use `vp.storage`.
- **No dialogs, forms, popups or links that leave.** Nobody is standing at a
  wall to close an alert.
- **Unknown is not zero.** `spend.readable`, `repo.readable`, `session.measured`
  and `machine.cpuPercent === null` each mean "not counted yet". Show that
  differently from 0.
- **Say when the data is not live.** Keep `vp.badge()` somewhere visible, or
  draw `vp.status` yourself with a shape as well as a colour. A frozen
  dashboard looks exactly like a quiet one.
- **Times are the panel's.** Use `vp.now()` and `vp.since(unix)`, not
  `Date.now()`: the screen's clock may be wrong.

## Parameters

Settings the owner changes per screen without touching code — a title, a
colour, a threshold — are declared in `vibepanel.json`:

```json
"params": [
  { "key": "title",  "type": "text",   "label": "Title", "max": 40, "default": "Lobby" },
  { "key": "accent", "type": "color",  "default": "#4f7cff" },
  { "key": "warnAt", "type": "number", "min": 1, "max": 100, "default": 5 },
  { "key": "unit",   "type": "enum",   "values": ["tokens", "requests"] },
  { "key": "compact","type": "bool",   "default": false }
]
```

They arrive as `snapshot.params` and through `vp.on('params', …)`, with no
reload. Prefer a parameter to a hard-coded value the owner might want to change.

## Checking your work

```sh
vibepanel page check      # problems, with file, line and fix
vibepanel page shot       # screenshots into .vibepanel/shots/, and what broke
vibepanel page shot --viewport phone,tv-1080 --fixture busy,counts,hostile
```

Look at the screenshots. They are PNGs you can read. `page shot` also prints
errors the page threw, requests the policy refused, text that rendered as
`NaN`/`undefined`/`null`, and whether the page overflows the screen.

If the owner has the Preview pane open, errors it saw are in
`.vibepanel/errors.json`.

Screens this page is likely to be on: a 1920×1080 television read from across a
room, a 390×844 phone, a laptop. Big type for the television; nothing that
needs a mouse.

## Publishing

**Do not publish.** The owner reviews the page in the Preview pane and presses
Publish. `vibepanel page publish` exists for people and scripts.
