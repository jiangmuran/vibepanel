package pages

import _ "embed"

// SDK is vibepanel.js, served at ./vibepanel.js inside every page.
//
// From the binary rather than from the page's files, so a page always runs the
// SDK that matches the panel serving it: a page published a year ago against
// v1 keeps working because this build's v1 is what it loads, not the copy that
// was sitting in its directory then.
//
//go:embed sdk/vibepanel.js
var SDK []byte

// Types is vibepanel.d.ts, written into a scaffolded directory for editors.
//
//go:embed sdk/vibepanel.d.ts
var Types []byte
