# A vibepanel share page

This directory is a page that vibepanel shows on a share link: a wall display,
a phone glance, a screen for a customer, or something nobody has thought of
yet. It gets live data about the panel's coding sessions through
`vibepanel.js`. What it looks like and how it is built is up to you and the
person you are working with.

Read `.vibepanel/HISTORY.md` if it exists: it is what was published before.

Docs: [share pages](https://github.com/jiangmuran/vibepanel/blob/main/docs/share-pages.md) ·
[the snapshot's fields](vibepanel.d.ts) ·
[how the owner uses this](https://github.com/jiangmuran/vibepanel/blob/main/docs/features.md#screens-for-other-people)

## What is here

| file | what it is |
|---|---|
| `index.html` | where the page opens. Everything else is yours to add, rename or delete |
| `vibepanel.json` | the manifest: which data the page receives, its parameters |
| `vibepanel.js` | the SDK. The panel serves its own copy; this one is for reading |
| `vibepanel.d.ts` | every field of a snapshot, with what it means |
| `fixtures/*.json` | made-up snapshots: `busy`, `empty`, `counts`, `hostile`, `stale`, `revoked` |

If the page was made from a template, the template is a starting point, not a
style guide. Replace it wholesale if the owner wants something else.

## The limits

These are enforced by the sandbox the page runs in, or by the panel when it is
published. Working against them does not work.

- **Data comes only from the SDK.** The page cannot reach any other address:
  no CDN fonts, no APIs, no remote images. Put every file it needs in this
  directory and load it by relative path. Scripts from `cdnjs.cloudflare.com`
  and `cdn.jsdelivr.net` are allowed when listed in `scriptHosts` in
  `vibepanel.json`.
- **Sections you did not ask for are `null` or empty.** Add them to `sections`
  in `vibepanel.json`. Day series need `spend.days` / `repo.days`.
- **No browser storage, cookies, dialogs, popups or navigation away.** The page
  has no origin; `localStorage` and friends throw. `vp.storage` is the
  replacement.
- **Files a page can serve:** html, css, js/mjs, json, svg, txt, images and
  woff/woff2 fonts. At most 64 files, 2 MiB each, 5 MiB in total. A build step
  is fine as long as what it outputs is here as plain files.
- **Do not publish.** The owner reviews the page in the Preview pane and
  presses Publish.

## The SDK, in brief

```js
const vp = VibePanel.connect()
vp.on('snapshot', (s) => draw(s))      // every ~2 seconds
vp.on('params', (p) => applyParams(p)) // when the owner changes a setting
```

Everything else — `vp.badge`, `vp.text`, `vp.name`, `vp.since`, `vp.now`,
`vp.storage` — is described in `vibepanel.d.ts`. Use what helps.

## Worth knowing

Not rules, and not a design. Things that have bitten pages before:

- A link in `counts` mode (the default) sends no session or project names.
  `?fixture=counts` shows what the page looks like then.
- Session titles are text somebody else chose. Put them in the page with
  `textContent` or `vp.text`, not `innerHTML`; the `hostile` fixture shows where
  that went wrong.
- `spend.readable`, `repo.readable`, `session.measured` and a null
  `machine.cpuPercent` mean "not counted yet", which is different from 0.
- A page that stopped updating looks exactly like a quiet one. Showing the
  connection state somewhere (`vp.badge`, or `vp.status` drawn your way) avoids
  that.
- The screen's clock may be wrong; `vp.now()` is the panel's.

## Parameters

Settings the owner can change per screen without touching code are declared in
`vibepanel.json`, if the page wants any:

```json
"params": [
  { "key": "title",  "type": "text",   "label": "Title", "max": 40, "default": "Lobby" },
  { "key": "accent", "type": "color",  "default": "#4f7cff" },
  { "key": "warnAt", "type": "number", "min": 1, "max": 100, "default": 5 },
  { "key": "unit",   "type": "enum",   "values": ["tokens", "requests"] },
  { "key": "compact","type": "bool",   "default": false }
]
```

They arrive as `snapshot.params` and through `vp.on('params', …)`.

## Checking your work

```sh
vibepanel page check      # problems, with file, line and fix
vibepanel page shot       # screenshots into .vibepanel/shots/, and what broke
vibepanel page shot --viewport phone,tv-1080 --fixture busy,counts,hostile
```

The screenshots are PNGs you can read. If the owner has the Preview pane open,
errors it saw are in `.vibepanel/errors.json`.
