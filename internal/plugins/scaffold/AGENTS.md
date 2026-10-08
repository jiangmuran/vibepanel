# Writing a vibepanel plugin

This directory is a plugin the panel is running in **dev mode**: the files
here are what the panel serves, and a change to any of them reloads the
plugin's frames within a second. The panel you are inside is the preview.
`plugin.json` is the manifest; `docs/plugins.md` in the vibepanel repository
is the design, and `vibepanel-plugin.d.ts` beside this file declares the SDK.

## The rules the sandbox enforces (you cannot change these)

- A **frame** (`panels[]`) is HTML in a sandboxed iframe: no cookie, no
  storage, an opaque origin, and a `connect-src` that names this plugin's own
  API and nothing else. Load the SDK with `<script src="vibepanel-plugin.js">`
  and the panel's look with `<link rel="stylesheet" href="vibepanel-ui.css">`;
  both come from the panel, not from this directory.
- Every id you see is a **handle**, never the panel's own id. A path, a
  command line or a working directory arrives only if the owner ticked
  `read:paths`.
- What you may do is the `capabilities` list in `plugin.json`, as the owner
  granted it: `vp.caps` in a frame, `ctx.caps` in `server.js`. A capability
  not granted is a 403 in a frame and an absent member in `ctx`. Degrade;
  do not ask. The install screen shows every capability as a sentence, and
  a plugin that asks for less is a plugin more people install.
- `server.js` runs inside the panel with a budget (500 ms per hook, 64 KiB
  per result), no modules, no files, no timers, and a fresh runtime per
  call. `ctx.fetch` reaches only hosts listed as `net:<host>` and granted.
- A **process** runs as the panel's owner with a cleared environment; its
  credential is `VIBEPANEL_PLUGIN_URL`; it is ended when the panel stops.
  With `process.http` in the manifest it is also served on the panel's port
  at `VIBEPANEL_PLUGIN_MOUNT`: listen on the unix socket in
  `VIBEPANEL_PLUGIN_SOCKET` and nowhere else, and **refuse any request whose
  `X-Vibepanel-Proxy` header is not `VIBEPANEL_PLUGIN_PROXY_SECRET`** --
  every process of this user can reach the socket, and only the panel knows
  the secret. The panel has already checked who is calling; the name is in
  `X-Vibepanel-Caller`. Never set cookies; answer JSON, an event stream,
  text, images, audio or bytes -- anything else is served as a download.
- Do not **install, grant or enable** anything yourself. `vibepanel plugin
  install --grant …` works from here because you run as the same user; it is
  the owner's decision, made on the install screen. Leave it to them.

## What is yours

Everything else: the HTML, the styling, what the pane shows, what the
service does. The template is a starting point, not a house style.

## Checking your work

```
vibepanel plugin check            # the manifest, every entry file, the theme lint
vibepanel plugin describe         # the install screen the owner will read
```

Errors a frame throws are logged by the panel in dev mode; `server.js` logs
to `.vibepanel/server.log` here and to the card's *Service* block.
