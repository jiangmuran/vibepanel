/**
 * Which Prism grammar a file is, and how to load it.
 *
 * The table docs/file-preview.md, "Types", names: forty languages, each loaded
 * the first time a file in it is opened. The loaders are written out one by
 * one rather than built from a template string, because a bundler can only
 * split what it can see: `import(\`prismjs/components/prism-${x}.js\`)` would
 * either fail to resolve or pull every grammar Prism ships into one chunk.
 *
 * A grammar extends the ones it names in `requires` and reads them when it
 * loads, so they load first, in the order given. Prism's core already holds
 * markup, css, clike and javascript.
 *
 * A file nobody listed is plain text with line numbers: no grammar is better
 * than a guessed one, which is what the keyword list this replaces was.
 */

type Loader = () => Promise<unknown>

const L: Record<string, Loader> = {
  'markup-templating': () => import('prismjs/components/prism-markup-templating.js'),
  typescript: () => import('prismjs/components/prism-typescript.js'),
  jsx: () => import('prismjs/components/prism-jsx.js'),
  tsx: () => import('prismjs/components/prism-tsx.js'),
  json: () => import('prismjs/components/prism-json.js'),
  json5: () => import('prismjs/components/prism-json5.js'),
  yaml: () => import('prismjs/components/prism-yaml.js'),
  toml: () => import('prismjs/components/prism-toml.js'),
  ini: () => import('prismjs/components/prism-ini.js'),
  bash: () => import('prismjs/components/prism-bash.js'),
  powershell: () => import('prismjs/components/prism-powershell.js'),
  python: () => import('prismjs/components/prism-python.js'),
  go: () => import('prismjs/components/prism-go.js'),
  rust: () => import('prismjs/components/prism-rust.js'),
  c: () => import('prismjs/components/prism-c.js'),
  cpp: () => import('prismjs/components/prism-cpp.js'),
  csharp: () => import('prismjs/components/prism-csharp.js'),
  java: () => import('prismjs/components/prism-java.js'),
  kotlin: () => import('prismjs/components/prism-kotlin.js'),
  scala: () => import('prismjs/components/prism-scala.js'),
  swift: () => import('prismjs/components/prism-swift.js'),
  dart: () => import('prismjs/components/prism-dart.js'),
  ruby: () => import('prismjs/components/prism-ruby.js'),
  php: () => import('prismjs/components/prism-php.js'),
  perl: () => import('prismjs/components/prism-perl.js'),
  lua: () => import('prismjs/components/prism-lua.js'),
  r: () => import('prismjs/components/prism-r.js'),
  elixir: () => import('prismjs/components/prism-elixir.js'),
  haskell: () => import('prismjs/components/prism-haskell.js'),
  ocaml: () => import('prismjs/components/prism-ocaml.js'),
  zig: () => import('prismjs/components/prism-zig.js'),
  sql: () => import('prismjs/components/prism-sql.js'),
  graphql: () => import('prismjs/components/prism-graphql.js'),
  protobuf: () => import('prismjs/components/prism-protobuf.js'),
  scss: () => import('prismjs/components/prism-scss.js'),
  less: () => import('prismjs/components/prism-less.js'),
  hcl: () => import('prismjs/components/prism-hcl.js'),
  docker: () => import('prismjs/components/prism-docker.js'),
  makefile: () => import('prismjs/components/prism-makefile.js'),
  nginx: () => import('prismjs/components/prism-nginx.js'),
  diff: () => import('prismjs/components/prism-diff.js'),
  markdown: () => import('prismjs/components/prism-markdown.js'),
}

/** What each grammar extends, from Prism's components.json. */
const REQUIRES: Record<string, string[]> = {
  typescript: [],
  jsx: [],
  tsx: ['jsx', 'typescript'],
  c: [],
  cpp: ['c'],
  php: ['markup-templating'],
  scss: [],
  less: [],
  scala: ['java'],
  markdown: [],
  json5: ['json'],
}

/** In Prism's core: present without loading anything. */
const CORE = new Set(['markup', 'css', 'clike', 'javascript'])

/** The grammars a language needs, its own last, without repeats. */
export function loadOrder(lang: string): string[] {
  const out: string[] = []
  const visit = (l: string) => {
    if (CORE.has(l) || out.includes(l)) return
    for (const r of REQUIRES[l] ?? []) visit(r)
    out.push(l)
  }
  visit(lang)
  return out
}

/** Every language the table can draw, core included. */
export const LANGUAGES: readonly string[] = [...CORE, ...Object.keys(L).filter((l) => l !== 'markup-templating')]

const BY_EXTENSION: Record<string, string> = {
  ts: 'typescript', mts: 'typescript', cts: 'typescript',
  tsx: 'tsx',
  js: 'javascript', mjs: 'javascript', cjs: 'javascript',
  jsx: 'jsx',
  json: 'json', jsonl: 'json', ndjson: 'json', webmanifest: 'json', jsonc: 'json5', json5: 'json5',
  yaml: 'yaml', yml: 'yaml',
  toml: 'toml',
  ini: 'ini', cfg: 'ini', conf: 'ini', editorconfig: 'ini', properties: 'ini',
  sh: 'bash', bash: 'bash', zsh: 'bash', fish: 'bash', envrc: 'bash', env: 'bash',
  ps1: 'powershell', psm1: 'powershell',
  py: 'python', pyi: 'python', pyw: 'python',
  go: 'go',
  rs: 'rust',
  c: 'c', h: 'c',
  cc: 'cpp', cpp: 'cpp', cxx: 'cpp', hpp: 'cpp', hh: 'cpp', hxx: 'cpp',
  cs: 'csharp',
  java: 'java',
  kt: 'kotlin', kts: 'kotlin',
  scala: 'scala', sc: 'scala',
  swift: 'swift',
  dart: 'dart',
  rb: 'ruby', rake: 'ruby', gemspec: 'ruby',
  php: 'php',
  pl: 'perl', pm: 'perl',
  lua: 'lua',
  r: 'r',
  ex: 'elixir', exs: 'elixir',
  hs: 'haskell',
  ml: 'ocaml', mli: 'ocaml',
  zig: 'zig',
  sql: 'sql',
  graphql: 'graphql', gql: 'graphql',
  proto: 'protobuf',
  css: 'css',
  scss: 'scss',
  less: 'less',
  tf: 'hcl', tfvars: 'hcl', hcl: 'hcl',
  html: 'markup', htm: 'markup', xml: 'markup', svg: 'markup', xhtml: 'markup', plist: 'markup',
  diff: 'diff', patch: 'diff',
  md: 'markdown', markdown: 'markdown',
  mk: 'makefile',
}

const BY_NAME: Record<string, string> = {
  makefile: 'makefile', gnumakefile: 'makefile',
  dockerfile: 'docker', containerfile: 'docker',
  '.bashrc': 'bash', '.zshrc': 'bash', '.profile': 'bash', '.bash_profile': 'bash',
  '.gitconfig': 'ini', '.npmrc': 'ini',
  gemfile: 'ruby', rakefile: 'ruby',
  'nginx.conf': 'nginx',
}

/**
 * The grammar for a file name, or null for plain text.
 *
 * By whole name first (a Makefile, a Dockerfile, `Dockerfile.dev`), then by
 * extension. A dotfile is a name, not an extension: `.env` is `env`.
 */
export function grammarFor(name: string): string | null {
  const base = (name.split('/').pop() ?? name).toLowerCase()
  if (BY_NAME[base]) return BY_NAME[base]
  if (/^(docker|container)file\./.test(base)) return 'docker'
  const dot = base.lastIndexOf('.')
  const ext = dot < 0 ? '' : base.slice(dot + 1)
  return BY_EXTENSION[ext] ?? null
}

/** Loads a language and what it extends. Resolves when it can be used. */
export async function loadGrammar(lang: string): Promise<void> {
  for (const l of loadOrder(lang)) {
    const load = L[l]
    if (!load) throw new Error(`no loader for the grammar ${l}`)
    await load()
  }
}

/** For the test that every loader resolves to a grammar Prism registers. */
export const loaders: Readonly<Record<string, Loader>> = L
