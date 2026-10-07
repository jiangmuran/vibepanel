package plugins

import _ "embed"

// SDK is vibepanel-plugin.js, served beside every frame's entry. From the
// binary rather than from the plugin's files, so a plugin always runs the SDK
// that matches the panel serving it.
//
//go:embed sdk/vibepanel-plugin.js
var SDK []byte

// Types is vibepanel-plugin.d.ts, written into a scaffolded directory for
// editors. Never served.
//
//go:embed sdk/vibepanel-plugin.d.ts
var Types []byte

// UI is vibepanel-ui.css: the panel's tokens as variables and a short list of
// classes with stable names, so a frame can look like the panel without
// seeing its stylesheet. docs/plugins.md §5a.
//
//go:embed sdk/vibepanel-ui.css
var UI []byte
