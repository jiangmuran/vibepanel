#!/usr/bin/env python3
"""The file viewer's fixtures: one file for every kind docs/file-preview.md
names, and the awkward cases inside each.

Run from anywhere; writes into ./files next to this script. The output is
committed so preview-check does not need ffmpeg or PIL, and this is kept so
that changing a fixture is changing a line here rather than hand-editing a
binary. The two large files (a 50,000 line log, a 2 MB+ one) are made by
preview-check at run time instead: committing megabytes of generated text
buys nothing.
"""
import gzip
import io
import os
import random
import shutil
import struct
import subprocess
import tarfile
import wave
import zipfile
import zlib

from PIL import Image, ImageDraw

HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.join(HERE, 'files')
rng = random.Random(19)


def put(name, data):
    path = os.path.join(OUT, name)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    mode = 'wb' if isinstance(data, bytes) else 'w'
    kw = {} if isinstance(data, bytes) else {'encoding': 'utf-8', 'newline': ''}
    with open(path, mode, **kw) as f:
        f.write(data)


def text():
    put('README.md', """# Fixture project

A paragraph with **bold**, *emphasis*, ~~struck~~, `code`, a [link](#tables)
and an autolink https://example.com.

## Tables

| left | centre | right |
|:-----|:------:|------:|
| a    |   b    |     c |
| 中文 |  ✓     |   1.5 |

## Lists

- one
  - nested
    - deeper
- two

1. first
2. second

- [x] done
- [ ] not yet

## Code

```ts
export function add(a: number, b: number): number {
  return a + b // a comment
}
```

## Images

![a pixel from the project](img/alpha.png)

![a remote image](https://example.com/remote.png)

## Raw HTML

<div onclick="alert(1)">raw html is shown as text</div>
<script>alert('no')</script>

## Footnotes

A claim.[^1]

[^1]: Its source.

> A quote, with a line
> that continues.
""")
    put('src/app.ts', """import { readFileSync } from 'node:fs'

/** A doc comment. */
export interface Thing {
  id: string
  count: number
}

const PATTERN = /^[a-z]+\\d*$/i
const template = `value: ${1 + 2}`

export class Store<T extends Thing> {
  private items = new Map<string, T>()

  add(item: T): void {
    if (!PATTERN.test(item.id)) throw new Error(`bad id ${item.id}`)
    this.items.set(item.id, item)
  }
}

export default readFileSync
""")
    put('src/main.go', """package main

import (
\t"fmt"
\t"os"
)

// main prints its arguments.
func main() {
\tfor i, a := range os.Args[1:] {
\t\tfmt.Printf("%d: %q\\n", i, a)
\t}
}
""")
    put('src/tool.py', """#!/usr/bin/env python3
\"\"\"A docstring.\"\"\"
import sys


def main(argv: list[str]) -> int:
    for a in argv:
        print(f"arg: {a!r}")  # a comment
    return 0


if __name__ == '__main__':
    sys.exit(main(sys.argv[1:]))
""")
    put('src/style.css', """:root { --accent: #3b82f6; }
.card > h2::before { content: "§"; color: var(--accent); }
@media (prefers-color-scheme: dark) { :root { --accent: #60a5fa; } }
""")
    put('scripts/run.sh', """#!/bin/sh
set -eu
for f in "$@"; do
  echo "file: $f"  # quoted
done
""")
    put('Makefile', "all:\n\t@echo built\n\n.PHONY: all\n")
    put('Dockerfile', "FROM alpine:3.20\nRUN apk add --no-cache tmux\nCMD [\"tmux\"]\n")
    put('notes.unknownext', "A file with an extension nobody listed.\nIt is plain text.\n")
    put('changes.diff', """diff --git a/src/app.ts b/src/app.ts
index 1111111..2222222 100644
--- a/src/app.ts
+++ b/src/app.ts
@@ -1,5 +1,6 @@
 import { readFileSync } from 'node:fs'
-const OLD = 1
+const NEW = 2
+const ALSO = 3

 /** A doc comment. */
 export interface Thing {
""")
    put('data/config.json', """{
  "name": "fixture",
  "version": 3,
  "enabled": true,
  "ratio": 0.75,
  "nothing": null,
  "escaped": "tab\\there \\"quoted\\" \\u4e2d",
  "nested": { "list": [1, 2, 3], "deep": { "deeper": { "deepest": "yes" } } },
  "long": [""" + ", ".join(str(i) for i in range(500)) + """]
}
""")
    put('data/events.jsonl', "".join(
        '{"t":%d,"event":"%s","ok":%s}\n' % (i, rng.choice(['start', 'stop', 'tick']), 'true' if i % 3 else 'false')
        for i in range(40)))
    put('data/broken.json', '{"name": "fixture", "unclosed": [1, 2,\n')
    put('data/table.csv', 'id,name,note,amount\n'
        '1,Alice,"has, a comma",10.5\n'
        '2,Bob,"has ""quotes""",7\n'
        '3,"Carol","spans\ntwo lines",0\n'
        '4,中文名,ünïcödé,-3\n'
        + ''.join(f'{i},row{i},,{i * 1.25}\n' for i in range(5, 60)))
    put('data/table.tsv', 'id\tname\tvalue\n' + ''.join(f'{i}\tname {i}\t{i * i}\n' for i in range(30)))
    put('data/semicolon.csv', 'a;b;c\n1;2;3\n4;5;6\n')
    put('page.html', """<!doctype html>
<html><head><title>A page</title><style>body{font-family:serif;color:#333}</style></head>
<body><h1>A project's own page</h1><p id="p">static text</p>
<script>document.getElementById('p').textContent = 'scripts ran'</script>
</body></html>
""")
    put('img/icon.svg', """<svg xmlns="http://www.w3.org/2000/svg" width="64" height="64" viewBox="0 0 64 64">
  <circle cx="32" cy="32" r="28" fill="#3b82f6"/>
  <script>alert('svg script')</script>
  <text x="32" y="38" font-size="18" text-anchor="middle" fill="#fff">SVG</text>
</svg>
""")
    # Line endings, terminators and encodings.
    put('endings/crlf.txt', 'one\r\ntwo\r\nthree\r\n')
    put('endings/cr.txt', 'one\rtwo\rthree\r')
    put('endings/mixed.txt', 'one\r\ntwo\nthree\r\n')
    put('endings/no-final-newline.txt', 'one\ntwo\nthree')
    put('endings/empty.txt', '')
    put('endings/blank-lines.txt', '\n\n\n')
    put('encodings/latin1.txt', 'caf\xe9 na\xefve\n'.encode('latin-1'))
    put('encodings/utf16.txt', '﻿UTF-16 text\n'.encode('utf-16-le'))
    put('encodings/bom.txt', '﻿UTF-8 with a BOM\n'.encode('utf-8'))
    put('encodings/controls.txt', 'bell\x07 escape\x1b[31m red\x1b[0m nul-free\n')
    put('long/minified.js', 'var a=' + ';var '.join(f'v{i}=function(x){{return x*{i}}}' for i in range(4000)) + ';\n')
    put('long/wide-lines.txt', ''.join(('word ' * rng.randint(40, 400)).strip() + '\n' for _ in range(50)))
    # Names.
    put('names/中文 文件名.md', '# 中文标题\n\n一段中文。\n')
    put('names/emoji 🎉 name.txt', 'a file whose name has an emoji\n')
    put('names/with spaces and (parens).txt', 'spaces\n')
    put('names/.hidden', 'a dotfile\n')
    put('names/no-extension', 'no extension at all\n')
    put('names/' + 'very-long-name-' * 12 + '.txt', 'a long name\n')
    put('names/rtl ‮evil.txt', 'a name with a right-to-left override\n')


def images():
    os.makedirs(os.path.join(OUT, 'img'), exist_ok=True)
    im = Image.new('RGBA', (96, 64), (0, 0, 0, 0))
    d = ImageDraw.Draw(im)
    d.ellipse((8, 8, 56, 56), fill=(59, 130, 246, 255))
    d.rectangle((48, 20, 88, 44), fill=(234, 88, 12, 160))
    im.save(os.path.join(OUT, 'img/alpha.png'))
    photo = Image.new('RGB', (320, 200))
    px = photo.load()
    for y in range(200):
        for x in range(320):
            px[x, y] = (x * 255 // 320, y * 255 // 200, 128)
    photo.save(os.path.join(OUT, 'img/photo.jpg'), quality=80)
    photo.save(os.path.join(OUT, 'img/photo.webp'), quality=70)
    frames = [Image.new('P', (40, 40), i * 40) for i in range(4)]
    frames[0].save(os.path.join(OUT, 'img/anim.gif'), save_all=True, append_images=frames[1:], duration=200, loop=0)
    # Taller and wider than any window: its top-left must be reachable
    # (「图片预览有bug 没法看到开头」).
    big = Image.new('RGB', (3000, 2400), (240, 240, 240))
    d = ImageDraw.Draw(big)
    d.rectangle((0, 0, 200, 60), fill=(220, 38, 38))
    d.text((10, 20), 'TOP-LEFT', fill=(255, 255, 255))
    d.rectangle((2800, 2340, 3000, 2400), fill=(22, 163, 74))
    big.save(os.path.join(OUT, 'img/huge.png'), optimize=True)
    Image.new('RGB', (1, 1), (0, 0, 0)).save(os.path.join(OUT, 'img/one-pixel.bmp'))
    Image.new('RGBA', (16, 16), (59, 130, 246, 255)).save(os.path.join(OUT, 'img/favicon.ico'))


def pdf():
    # Three pages, hand-written, so there is no generator dependency: text on
    # every page, a word that is only on page two for find to land on
    # ("lighthouse"), and a picture on page three, drawn from an image
    # XObject, so a page that is not only text is drawn too.
    objs = []

    def obj(body):
        objs.append(body)
        return len(objs)

    def stream(data, extra=b''):
        return obj(b'<< /Length %d %s>>\nstream\n%s\nendstream' % (len(data), extra, data))

    font = obj(b'<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>')
    w, h = 64, 48
    pixels = bytes(v for y in range(h) for x in range(w) for v in (x * 4, y * 5, 160))
    image = stream(zlib.compress(pixels),
                   b'/Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB '
                   b'/BitsPerComponent 8 /Filter /FlateDecode ' % (w, h))
    lines = {
        1: [b'Fixture PDF, page 1', b'The first of three pages.'],
        2: [b'Fixture PDF, page 2', b'The word to find is lighthouse, and it is only here.'],
        3: [b'Fixture PDF, page 3', b'A picture, drawn below.'],
    }
    contents = []
    for n in (1, 2, 3):
        ops = b'BT /F1 24 Tf 72 720 Td (%s) Tj /F1 14 Tf 0 -32 Td (%s) Tj ET' % tuple(lines[n])
        if n == 3:
            ops += b' q 256 0 0 192 72 400 cm /Im1 Do Q'
        contents.append(stream(ops))
    pages_id = len(objs) + 4
    page_ids = [obj(b'<< /Type /Page /Parent %d 0 R /MediaBox [0 0 612 792] /Contents %d 0 R '
                    b'/Resources << /Font << /F1 %d 0 R >> /XObject << /Im1 %d 0 R >> >> >>'
                    % (pages_id, c, font, image)) for c in contents]
    assert obj(b'<< /Type /Pages /Kids [%s] /Count 3 >>' % b' '.join(b'%d 0 R' % p for p in page_ids)) == pages_id
    root = obj(b'<< /Type /Catalog /Pages %d 0 R >>' % pages_id)
    out = io.BytesIO()
    out.write(b'%PDF-1.4\n')
    offsets = []
    for i, body in enumerate(objs, 1):
        offsets.append(out.tell())
        out.write(b'%d 0 obj\n%s\nendobj\n' % (i, body))
    xref = out.tell()
    out.write(b'xref\n0 %d\n0000000000 65535 f \n' % (len(objs) + 1))
    for o in offsets:
        out.write(b'%010d 00000 n \n' % o)
    out.write(b'trailer\n<< /Size %d /Root %d 0 R >>\nstartxref\n%d\n%%%%EOF\n' % (len(objs) + 1, root, xref))
    put('docs/manual.pdf', out.getvalue())

    # The same document behind a password, which pdf.js cannot open without
    # one: the viewer says so and offers the download. RC4 so pypdf needs no
    # crypto library to write it.
    from pypdf import PdfReader, PdfWriter
    w = PdfWriter(clone_from=PdfReader(io.BytesIO(out.getvalue())))
    w.encrypt(user_password='fixture', algorithm='RC4-128')
    locked = io.BytesIO()
    w.write(locked)
    put('docs/locked.pdf', locked.getvalue())
    # The magic and nothing a PDF is made of: found as a PDF by its first
    # bytes, and unreadable to pdf.js.
    # Its own generator, so the shared one draws the same bytes for every
    # fixture after this one as it did before this one existed.
    own = random.Random(7)
    put('docs/broken.pdf', b'%PDF-1.7\n' + bytes(own.getrandbits(8) for _ in range(2048)))


def media():
    os.makedirs(os.path.join(OUT, 'media'), exist_ok=True)
    rate = 8000
    with wave.open(os.path.join(OUT, 'media/tone.wav'), 'wb') as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(rate)
        import math
        w.writeframes(b''.join(struct.pack('<h', int(8000 * math.sin(2 * math.pi * 440 * i / rate))) for i in range(rate // 2)))
    ff = ['ffmpeg', '-v', 'error', '-y']
    subprocess.run(ff + ['-i', os.path.join(OUT, 'media/tone.wav'), '-c:a', 'libmp3lame', '-b:a', '32k',
                         os.path.join(OUT, 'media/tone.mp3')], check=True)
    subprocess.run(ff + ['-i', os.path.join(OUT, 'media/tone.wav'), '-c:a', 'libopus', '-b:a', '16k',
                         os.path.join(OUT, 'media/tone.ogg')], check=True)
    src = ['-f', 'lavfi', '-i', 'testsrc=duration=1:size=160x120:rate=10']
    subprocess.run(ff + src + ['-pix_fmt', 'yuv420p', '-c:v', 'libx264', '-crf', '40',
                               os.path.join(OUT, 'media/clip.mp4')], check=True)
    subprocess.run(ff + src + ['-c:v', 'libvpx-vp9', '-crf', '50', '-b:v', '0',
                               os.path.join(OUT, 'media/clip.webm')], check=True)


def archives():
    os.makedirs(os.path.join(OUT, 'archives'), exist_ok=True)
    with zipfile.ZipFile(os.path.join(OUT, 'archives/bundle.zip'), 'w', zipfile.ZIP_DEFLATED) as z:
        z.writestr('a.txt', 'a\n')
        z.writestr('dir/b.txt', 'b\n')
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode='w') as t:
        data = b'inside a tarball\n'
        info = tarfile.TarInfo('inside.txt')
        info.size = len(data)
        info.mtime = 0
        t.addfile(info, io.BytesIO(data))
    put('archives/bundle.tar', buf.getvalue())
    put('archives/bundle.tar.gz', gzip.compress(buf.getvalue(), mtime=0))
    put('archives/log.gz', gzip.compress(b'a compressed log\n' * 50, mtime=0))


def binary():
    put('bin/random.bin', bytes(rng.getrandbits(8) for _ in range(4096)))
    # An ELF header followed by zeros: a binary that starts like an executable.
    put('bin/program', b'\x7fELF\x02\x01\x01' + bytes(1017))
    put('bin/sqlite.db', b'SQLite format 3\x00' + bytes(2032))
    put('bin/zlib.dat', zlib.compress(b'x' * 10000))


if __name__ == '__main__':
    if os.path.isdir(OUT):
        shutil.rmtree(OUT)
    os.makedirs(OUT)
    text()
    images()
    pdf()
    media()
    archives()
    binary()
