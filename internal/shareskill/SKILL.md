---
name: vibepanel-share
description: >-
  Create, iterate on, validate, publish, and share a vibepanel share page
  (design preview, interactive prototype, report, discussion page, or live panel
  dashboard) from any project and return a clickable share URL. Use whenever the
  user asks to create or publish a vibepanel share page, spin up a page to
  preview or discuss, share an HTML prototype/report via vibepanel, or says
  "起一个share页面", "发个预览链接", "发布到分享页", or invokes /vibepanel-share.
user-invocable: true
---

# vibepanel Share Pages

Use this workflow when the user asks you to put a page, prototype, report,
proposal, or live dashboard onto vibepanel's share surface so they can open a
link, review it, and discuss revisions with you.

## 1. Choose a new page or reuse an existing one

First check whether a page for this topic already exists:

```sh
vibepanel page list
```

- **Updating an existing page:** edit the files in its `DRAFT` directory from
  `vibepanel page list`.
- **Creating a new page:** scaffold and register one under the panel's pages
  directory (do not put share pages inside the current project repository unless
  the user explicitly asks):

```sh
vibepanel page init --name <short-slug>
```

`vibepanel page init` prints the page directory (for example
`<pages-root>/page-<short-slug>`) and creates `index.html`, `vibepanel.json`,
`vibepanel.js`, `vibepanel.d.ts`, `ARCHITECTURE.md`, `AGENTS.md`, and
`fixtures/`.

## 2. Build the page inside its sandbox

Pick the right shape for `vibepanel.json`:

- **Preview, prototype, report, or discussion page (most common):** set
  `"sections": []` in `vibepanel.json`. You do not need to load `vibepanel.js`
  unless you want `vp.storage`, theme helpers, or visitor actions.
- **Live panel dashboard (sessions, token spend, activity, git stats):** list
  only the sections you need in `vibepanel.json` (for example
  `["sessions", "spend", "repo"]`), load `./vibepanel.js`, and check
  `vibepanel.d.ts` and `ARCHITECTURE.md` in the page directory.

Hard limits enforced by vibepanel's publish check and CSP sandbox:

- **Self-contained files:** put every stylesheet, script, font, and image in
  the page directory and reference it by relative path. External CDN fonts,
  remote images, and direct `fetch()` calls to third-party APIs are blocked by
  CSP. Scripts from `cdnjs.cloudflare.com` or `cdn.jsdelivr.net` work only when
  explicitly listed in `scriptHosts` in `vibepanel.json`.
- **Allowed file types and budget:** HTML, CSS, JS/MJS, JSON, SVG, TXT, images,
  and WOFF/WOFF2 fonts. At most 64 files, 2 MiB per file, and 5 MiB total.
- **Opaque origin:** share links run with `sandbox allow-scripts` (no
  `allow-same-origin`). Do not rely on `localStorage`, `sessionStorage`, or
  `document.cookie` (they throw); keep ephemeral state in memory or use
  `vp.storage` if using `vibepanel.js`.
- **Safe rendering:** escape any untrusted or dynamic text (`textContent`, not
  `innerHTML`). Make sure the layout works cleanly on both desktop and mobile
  viewports.

## 3. Validate before publishing

Run the panel's own linter on the page directory:

```sh
vibepanel page check <page-dir>
```

Fix every error until `vibepanel page check` exits 0. For pages using
`vibepanel.js`, you can also capture rendered screenshots if a browser is
installed:

```sh
vibepanel page shot --viewport laptop,phone <page-dir>
```

## 4. Publish and return the live share URL

Publish the draft as a new immutable version and print a clickable share URL in
one command:

```sh
vibepanel page publish --link --note "<what changed>" <page-dir>
```

How `--link` behaves:

- If this page already has an active copyable share link, it reuses that URL
  (the link automatically serves the newly published version).
- If the page has no link yet, it mints one on the panel's configured share
  origin (or panel URL) with `detail=counts` (privacy-safe default) and prints
  the URL.

When you need more control over links (for example creating a second link,
setting an expiration, or exposing session/project names on a live dashboard):

```sh
vibepanel page link [--new] [--name "<label>"] [--detail counts|names] [--expires 7d] [--json] <page-dir>
```

Keep `--detail counts` unless the user explicitly asks to show session or
project names on the link.

## 5. Reply and iterate

In your response to the user:

1. Give the clickable share URL first.
2. Briefly summarize what is on the page so they know what to look for.
3. For follow-up edits, update files in the same `<page-dir>`, re-run
   `vibepanel page check <page-dir>`, and run
   `vibepanel page publish --link --note "<change>" <page-dir>` — the existing
   URL refreshes to the new version immediately.
