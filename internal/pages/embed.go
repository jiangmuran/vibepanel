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

// ArchitectureFile is the name Architecture is written under in a page
// directory.
const ArchitectureFile = "ARCHITECTURE.md"

// Architecture is docs/page-backend.md, written into every page directory so
// the agent building a page reads how data, admin pages, sources, server.js
// and actions work, and what stops each, from the build it is building for.
// A test keeps it byte-identical to the document; `vibepanel page docs`
// prints it.
//
//go:embed scaffold/ARCHITECTURE.md
var Architecture []byte
