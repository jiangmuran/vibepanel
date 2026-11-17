# The file viewer

What the Files tab opens when you click a file, rebuilt. This is the design;
`docs/build-log.md` records what was built against it and what went wrong.

## Why

The preview shipped as a modal with seven states (`FilePreview.tsx`): text,
image, PDF, too big, none, loading, error. Text is one of three drawings: a
hand-written markdown parser with thirteen node types and no images, task
lists or nested lists; a keyword-list highlighter that colours every language
with the same forty words and two comment markers; or `<pre>`. Nothing moves
between files, nothing finds text in one, a 300 KB log shows its first 256 KB
and 4,000 lines and stops without a way on, a 20 MB JPEG is "too big", an SVG
goes to a sandboxed iframe, a CSV is a wall of commas, a JSON is a wall of
braces, a binary is a sentence, a video is nothing. Escape closes it and that
is the whole keyboard. It was built to prove the panel could show a file. The
owner's verdict after a month of using it: a demo with too many bugs.

The right panel is 280 px wide, so reading a file still wants the window
(the reasoning at the top of `FilePreview.tsx` stands). What changes is
everything inside that window.

## What it is

One viewer, full-window, opened from the Files tab, closed with Escape, with
a header that is the same for every file:

```
┌ project / src / components / Terminal.tsx     12.4K  tsx  ┐
│ [‹] [›]   [Rendered | Source]   ⌕ find   ↩ wrap   # lines   ⤓   ×  │
├─────────────────────────────────────────────────────────────┤
│ body                                                        │
├─────────────────────────────────────────────────────────────┤
│ 412 lines · UTF-8 · LF        [first 2 MB of 9.1 MB]        │
└─────────────────────────────────────────────────────────────┘
```

- The path is a breadcrumb, each segment a button that closes the viewer and
  moves the tree there. The root segment is the project's name, never a bare
  `/` (an audit finding on the tree, fixed in the same stroke).
- `‹` `›` move to the previous and next file in the directory the tree is
  showing, in the tree's order, skipping directories. The viewer is keyed by
  path, so every file starts in the safe state (source off, scripts off), as
  today.
- The segmented `Rendered | Source` appears only when there are two views.
- Find is a bar under the header, not the browser's: the browser's cannot see
  what is virtualised. Matches are counted, highlighted and stepped with
  Enter and Shift+Enter; Escape leaves the bar before it closes the viewer.
- Wrap and line numbers are per-browser preferences (`localStorage`, the
  renderer-switch pattern), default on and on.
- The footer says what the body is made of: lines, encoding, line endings,
  and when the file was cut, how much of it is on screen and how much is not.

The viewer takes the keyboard the way the settings dialog does
(`useDialogFocus`): focus moves in, Tab stays in, Escape closes the topmost
thing, focus goes back to the row in the tree. Clicking the backdrop closes
it, as today.

## Types

The server classifies; the browser draws. `browse.SniffMagic` reads the
first 512 bytes and the server answers with `X-Preview-Kind`; the name decides
the rest in the browser. The table is the contract between the two halves and
`TestThePreviewKindsMatchTheClient` pins it against `wire.ts`.

| kind | how it is found | drawn as |
|---|---|---|
| `text` | not any of the below, and `browse.IsText` | by extension, below |
| `image` | PNG, JPEG, GIF, WebP, AVIF, BMP, ICO magic; SVG by `<svg` or `<?xml` + `.svg` | `<img>` with zoom and pan |
| `pdf` | `%PDF-` | `<object>` with the fallback |
| `video` | MP4/MOV (`ftyp`), WebM/MKV (EBML), Ogg | `<video controls>` from the inline route |
| `audio` | MP3 (ID3 or frame sync), WAV, FLAC, Ogg with Vorbis/Opus, M4A | `<audio controls>` from the inline route |
| `archive` | ZIP (`PK`), gzip, tar (ustar at 257), 7z, RAR | the entry list, bounded; later |
| `binary` | everything else | hex: offset, 16 bytes, ASCII, first 64 KB |

Text, by extension in the browser:

| | |
|---|---|
| code | Prism's tokeniser, one grammar per language loaded on first use, drawn as React elements from the token tree. Never Prism's HTML output and never `innerHTML`: the line markdown.ts draws for documents holds for code. Forty grammars, in `highlight.ts`'s table, which is what `langOf` consults; a file nobody listed is plain text with line numbers. |
| diff | `.diff`, `.patch`, and a body that starts with `diff --git` or `---`/`+++`: added and removed lines are tinted, and the `+` or `-` each line already carries is the shape beside the colour (red line 4). No second sign in a gutter: copy has to be the file, and a gutter copy would read `+ +foo` |
| markdown | mdast (`mdast-util-from-markdown` + `mdast-util-gfm`) to React. GFM: tables, task lists, strikethrough, autolinks, footnotes. Nested lists. Images whose path stays inside the project load through the download route, same origin, cookie and all; a remote image is a placeholder with its address, never a request. Raw HTML in a document is shown as text. Headings get ids so `#anchors` inside the document work. |
| json / jsonl | a tree: collapsible objects and arrays, keys and values typed and coloured by token, strings unescaped, long arrays folded past 200 items, the whole thing searchable; past 2 MB or when it does not parse, code |
| csv / tsv | a table with a sticky header row and row numbers, quoted fields handled, the delimiter sniffed from the first lines; past 10,000 rows the first 10,000 and a footer line saying so |
| notebook | `.ipynb`: cells in order, markdown cells as markdown, code cells as code, text outputs as text, image outputs as images; later |
| plain | line numbers and wrap, which is also what every type falls back to under `Source` |

### Size

Text is served up to 2 MB and 50,000 lines (`previewTextBytes`,
`previewTextLines`, up from 256 KB and 4,000). The body is not windowed:
every line is in the DOM, in blocks of 256 lines, each block
`content-visibility: auto` with a `contain-intrinsic-size` that is corrected
once the block has been laid out, so layout and paint skip what is off
screen while selection, copy and wrap stay the browser's own. Windowing was
the first idea and loses on two lines of this design at once: wrapped lines
have no fixed height, so a window has to measure every row and still jumps
imprecisely; and a drag or Ctrl+A across lines that are not in the DOM
copies a hole. Highlighting is per block and lazy -- a block is tokenised
when it first comes into view and is plain spans until then -- so a 50,000
line file does not start with half a million elements. Go to line scrolls
the block into view and then the line. Find searches the text, not the DOM.
`preview-check` opens a 50,000 line file and pins the time to first paint
and the scroll frame rate. Past the cap the footer says so and offers the
download; a `?from=` byte offset on the preview route is the way to read on,
and the viewer offers it as "next 2 MB" rather than loading it by itself.

Images, PDF, video and audio have no preview cap: the browser streams them.
The inline route (`GET /api/projects/{id}/inline?path=`) is the download
route with `Content-Disposition: inline`, `Accept-Ranges`, and a content type
from the table above only — anything not in the table is served as
`application/octet-stream` with `attachment`, so this route cannot be made to
render HTML on the panel's origin. `TestTheInlineRouteNamesOnlyMediaTypes`
walks the table.

## Display

- Images: fit to the window on open, with the real size in the footer
  (`1920 × 1080 · 2.4M`). `+` `-` `0` and Ctrl+wheel zoom about the cursor;
  drag pans; pinch zooms on a touch screen. A transparent image sits on a
  checkerboard. An image that does not fit starts at its top-left, which is
  the bug 「图片预览有bug 没法看到开头」 already fixed once, kept by a test.
- Code colours are theme tokens (`--vp-tok-keyword` and friends), redefined
  under `[data-theme]` and the media query like every other token and nowhere
  else (red line 5), and every token colour meets 4.5:1 on `--vp-surface-2`
  in both themes (`contrast.test.ts` extends to them).
- Long lines wrap by default; with wrap off the body scrolls sideways and the
  line numbers stay put.
- The whole body is selectable and copies as plain text without line numbers
  (the gutter is `user-select: none`, as now).
- On a phone the viewer is the screen: the header wraps to two rows, the
  breadcrumb keeps its last two segments, find has its own row, images pinch.
  `render-check` measures it at 320, 768 and 1280.
- Everything that is a word is translated; nothing explains itself.
- **Nothing that holds the focus is removed before the focus has somewhere
  to go.** The rebuild hit this three times -- the dialog keyed by path lost
  the keyboard on `]`, go to line replaced the block the caret was in, and
  unfolding a JSON array replaced its own button -- and each time the keys
  stopped working with nothing on screen to say why. A component that
  replaces an element moves the focus first (to itself, to the row, to the
  dialog), and `preview-check` presses a key after every such change.

## Keys

In the viewer, when the focus is not in the find box:

| key | does |
|---|---|
| Esc | close find, then close the viewer |
| `[` `]` | previous, next file in the directory |
| Ctrl/⌘ F | find in this file |
| Ctrl/⌘ G | go to line |
| Enter, Shift+Enter | next, previous match |
| `w` | wrap on and off |
| `n` | line numbers on and off |
| `s` | Source and Rendered |
| `+` `-` `0` | zoom an image |
| `d` | download |
| `c` | copy the path |

They are listed in the ⌥/ dialog under a *Viewer* heading, and nothing here
takes a key from a text field or from the terminal: the viewer is a dialog
and the terminal is behind it.

In the Files tab, which this touches because the viewer opens from it:

| key | does |
|---|---|
| ↑ ↓ | move between rows |
| Enter | open: a directory moves in, a file opens the viewer |
| Backspace, ← | up one level |
| typing | filters the listing by name, Escape clears |

Rows are focusable, the current one is `aria-current`, and icon sizes in the
tab are one size (an audit finding).

## What stays

- The download route, the directory listing and the upload path are
  unchanged.
- `safeBody` on every string a file contributes, and `safeText` on every
  name.
- The render-in-a-sandbox path for a project's own HTML (`preview_render.go`,
  `sandboxFor`) is unchanged and still the only place a file's HTML runs. SVG
  moves from that frame to `<img>`, which runs nothing.
- `PREVIEW_MAX_BYTES` and the server's bound stay pinned to each other.

## Dependencies

Three libraries, all parsers that return trees and none a component or
state library: `prismjs` (core and grammars, loaded per language on first
use), and `mdast-util-from-markdown` with `mdast-util-gfm` and
`micromark-extension-gfm` -- the extension parses GitHub's syntax, the util
builds its nodes, and one without the other is half a table. The viewer is
its own chunk, imported when a file is first opened, and the document
renderer another, imported when a document is, so the panel's first paint
pays for none of them; `bundle.test.ts` pins the main chunk's size, the
viewer's and the renderer's.

## Batches

Each is one branch, reviewed and merged on its own, with `make check`, the
covering browser checks and a mutation per guard, in this order because each
later one draws inside what the earlier one built:

1. **The shell**: header, breadcrumb, previous/next, find, go to line, wrap
   and line numbers, the text body in blocks, the raised text cap, the
   footer, focus and keys, the phone layout, the ⌥/ entries. The old
   drawings -- markdown, the keyword highlighter, the HTML frame, image,
   PDF -- stay inside the new shell until the batch that replaces each, with
   find working on text only. `make preview-check` is born here: fixtures
   for every type the table names, driven at three widths in both themes,
   with the keys.
2. **Code and diff**: Prism tokens to React, the grammar table, the token
   colours as theme tokens, diff.
3. **Markdown**: mdast with GFM, images from the project, anchors.
4. **Structured text**: JSON tree, CSV table, hex for binary.
5. **Media**: image zoom and pan, SVG as an image, the inline route with
   ranges, video and audio, the kinds table pinned against `wire.ts`.
6. **The Files tab**: keyboard, filter, breadcrumb root, icon sizes.
7. **More kinds**, below, in four parts: 7a PDF, 7b notebooks, fonts and
   encodings, 7c archives and SQLite, 7d office files.
8. Later, each its own decision: "next 2 MB", and a relative link in a
   document (`[x](other.md)`) opening that file in the viewer -- today it
   shows where it points and does nothing, because followed as a URL it
   would take the whole panel to a path it does not serve.

## More kinds

The six batches gave every file a bounded answer. What is left is the files
whose answer is "download it and open it somewhere else": a PDF on a phone,
a notebook, a spreadsheet, a zip. Each of these has a drawing that is worth
having, and each is its own batch because each brings its own parser.

| kind | how it is found | drawn as |
|---|---|---|
| `pdf` | `%PDF-` | pdf.js in the browser: pages drawn to canvases, one at a time as they come into view, with a page strip, zoom, and find over the text layer. The `<object>` is gone: on a phone it was most of the time a grey box, and on a desktop it was somebody else's viewer with its own keys. |
| notebook | `.ipynb` that parses as one | cells in order: markdown cells through the document renderer, code cells through the code body with the kernel's language, and each cell's outputs under it -- text as text, images as pictures, HTML as text and never rendered, errors with their traceback. Execution counts in the gutter. |
| font | TTF, OTF, WOFF, WOFF2 by magic | a specimen: the name from the file, the alphabet in both cases, digits, a pangram, and a line of the file's own name, at three sizes, loaded through `FontFace` from the bytes and unloaded with the viewer |
| text in another encoding | `IsText` fails, but the bytes decode as UTF-16 with a byte-order mark, or as Latin-1 with the control-character rule of `Readable` | text, with the encoding named in the footer (`UTF-16 LE`, `Latin-1`) and decoded on the server so the browser sees UTF-8 like any other text |
| `archive` | ZIP (`PK`), tar (ustar at 257), gzip, tgz | its table of contents from the server: path, size, modified, with directories folded, bounded at 10,000 entries and the footer saying so. Entries are not opened in this batch. |
| `sqlite` | `SQLite format 3\0` | its tables from the server, opened read-only and immutable: each table's name, row count and columns, and the first 200 rows of the one chosen, drawn by the table body; the schema under Source |
| spreadsheet | XLSX, XLS, ODS by magic and name | each sheet as a table, the sheet names as a strip, cells as their formatted text; the table body from batch 4 |
| document | DOCX by magic and name | the text with its headings, lists, tables and pictures, through a conversion in the browser into a sandboxed frame with no scripts and no network: an opaque origin, the same line the project's own HTML is drawn on. Word's styling is not kept; the words and the pictures are. |
| slides | PPTX | later; not in these four |

What a server reads for these is bounded like everything else it reads: an
archive's central directory and tar headers, never an entry's bytes; a
database with `?mode=ro&immutable=1` and a statement timeout, never a write;
and both on the request goroutine only because each is a few milliseconds on
a file the panel already opened, measured in the batch's test.

### Keys, added

| key | does |
|---|---|
| PgUp PgDn, `g` `G` | previous and next page, first and last, in a PDF |
| ← → | previous and next sheet in a spreadsheet, previous and next table in a database |

### Dependencies, added

`pdfjs-dist` (the worker as its own chunk), `xlsx` (SheetJS, the community
build), `mammoth` for DOCX. Each is a lazy chunk fetched by the first file of
its kind, with its own line in `bundle.test.ts`. Fonts, notebooks, archives,
SQLite and encodings need nothing new: `FontFace`, the renderers already
built, and Go's standard library (`archive/zip`, `archive/tar`,
`compress/gzip`) plus the SQLite driver the store already uses.

### The four parts

- **7a PDF**: pdf.js, pages, find, zoom, the keys; `preview-check` reads a
  three-page fixture, finds a word on page two, and measures first paint.
- **7b notebooks, fonts, encodings**: client side except the server's
  decoding of UTF-16 and Latin-1, which `X-Preview-Encoding` names.
- **7c archives and SQLite**: the server's two listings
  (`GET /api/projects/{id}/archive?path=` and `/sqlite?path=&table=`) with
  their bounds pinned by tests against crafted files -- a zip whose central
  directory claims a gigabyte, a tar with a path that climbs, a database
  with a trigger -- and the viewer's two tables.
- **7d office**: spreadsheets and documents.

The old `FilePreview.tsx`, `Rendered.tsx`, `highlight.ts` and `markdown.ts`
go when the batch that replaces each lands; nothing is kept beside its
replacement.
