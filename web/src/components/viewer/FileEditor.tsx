import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { Loader2 } from 'lucide-react'
import { Compartment, EditorState, type Extension, type Text } from '@codemirror/state'
import { drawSelection, EditorView, highlightActiveLine, highlightActiveLineGutter, keymap, lineNumbers } from '@codemirror/view'
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands'
import { bracketMatching, HighlightStyle, indentOnInput, syntaxHighlighting } from '@codemirror/language'
import { highlightSelectionMatches, search, searchKeymap } from '@codemirror/search'
import { tags } from '@lezer/highlight'

import { getLang, t } from '../../i18n'
import { api, FileChangedError } from '../../protocol/api'
import { formatBytes } from '../panels/preview'
import { editLanguage, type EditLang } from './editlangs'

/** What the viewer asks of the editor: Ctrl/⌘ S from outside it. */
export interface EditorHandle {
  save: () => void
}

interface Props {
  projectId: string
  path: string
  name: string
  text: string
  bom: boolean
  /** The version the text is; a save sends it as If-Match. */
  etag: string
  separator: '\n' | '\r\n' | '\r'
  handleRef: React.RefObject<EditorHandle | null>
  onDirty: (dirty: boolean) => void
  /** A save landed: the viewer shows this text, at this version, from now on. */
  onSaved: (text: string, etag: string) => void
  /** The file changed on disk: read it again, and drop what is here. */
  onReload: () => void
}

type Status =
  | { kind: 'idle' }
  | { kind: 'saving' }
  | { kind: 'saved'; size: number }
  | { kind: 'changed' }
  | { kind: 'error'; message: string }

/**
 * A text file, editable: CodeMirror 6, in a chunk of its own and fetched the
 * first time anything is edited (bundle.test.ts), with a grammar for the ten
 * languages in editlangs.ts, each its own chunk again.
 *
 * Colours are the viewer's token colours, by variable, so the editor and the
 * view of the same file agree in both themes without a theme block here (red
 * line 5).
 *
 * The save is against the version the file was read at. A 412 means somebody
 * -- an agent in a session, most likely -- wrote the file since, and the
 * answer is to read it again rather than to write over it: there is no
 * "save anyway".
 */
export default function FileEditor({ projectId, path, name, text, bom, etag, separator, handleRef, onDirty, onSaved, onReload }: Props) {
  const hostRef = useRef<HTMLDivElement | null>(null)
  const viewRef = useRef<EditorView | null>(null)
  const [status, setStatus] = useState<Status>({ kind: 'idle' })
  // What is on disk, as far as this editor knows: the text and version it
  // opened at, then each save's.
  const base = useRef<{ doc: Text | null; etag: string }>({ doc: null, etag })
  const saving = useRef(false)
  const props = useRef({ onDirty, onSaved })
  useLayoutEffect(() => {
    props.current = { onDirty, onSaved }
  })

  const save = async () => {
    const view = viewRef.current
    if (!view || saving.current) return
    saving.current = true
    setStatus({ kind: 'saving' })
    const doc = view.state.doc
    // Joined with the file's own line ending (the lineSeparator facet), and
    // the byte order mark the read dropped put back in front.
    // The separator is given, because sliceString joins with "\n" by default
    // whatever the state's lineSeparator is: a CRLF file saved through the
    // default came back LF, every line ending rewritten.
    const body = (bom ? '﻿' : '') + doc.sliceString(0, doc.length, separator)
    try {
      const r = await api.saveFile(projectId, path, body, base.current.etag)
      base.current = { doc, etag: r.etag }
      setStatus({ kind: 'saved', size: r.size })
      props.current.onDirty(!view.state.doc.eq(doc))
      props.current.onSaved(doc.sliceString(0, doc.length, separator), r.etag)
    } catch (e) {
      setStatus(e instanceof FileChangedError ? { kind: 'changed' } : { kind: 'error', message: e instanceof Error ? e.message : String(e) })
    } finally {
      saving.current = false
    }
  }
  const saveRef = useRef(save)
  useLayoutEffect(() => {
    saveRef.current = save
    handleRef.current = { save: () => void saveRef.current() }
  })
  useLayoutEffect(
    () => () => {
      handleRef.current = null
    },
    [handleRef],
  )

  // The editor is made once per text it opens with; the viewer keys this
  // component by the read, so a reload is a new editor.
  const language = useRef(new Compartment())
  useEffect(() => {
    const host = hostRef.current
    if (!host) return
    const state = EditorState.create({
      doc: text,
      extensions: [
        EditorState.lineSeparator.of(separator),
        getLang() === 'zh' ? EditorState.phrases.of(ZH) : [],
        lineNumbers(),
        highlightActiveLineGutter(),
        history(),
        drawSelection(),
        indentOnInput(),
        bracketMatching(),
        highlightActiveLine(),
        highlightSelectionMatches(),
        search({ top: true }),
        syntaxHighlighting(TOKENS),
        language.current.of([]),
        keymap.of([
          { key: 'Mod-s', run: () => (void saveRef.current(), true), preventDefault: true },
          ...searchKeymap,
          ...historyKeymap,
          indentWithTab,
          ...defaultKeymap,
        ]),
        EditorView.lineWrapping,
        THEME,
        EditorView.contentAttributes.of({ 'aria-label': t('edit.label', { name }), 'data-testid': 'editor-content' }),
        EditorView.updateListener.of((u) => {
          if (!u.docChanged) return
          const doc = base.current.doc
          props.current.onDirty(doc === null || !u.state.doc.eq(doc))
          setStatus((s) => (s.kind === 'saved' ? { kind: 'idle' } : s))
        }),
      ],
    })
    const view = new EditorView({ state, parent: host })
    base.current = { doc: state.doc, etag: base.current.etag }
    viewRef.current = view
    view.focus()
    return () => {
      view.destroy()
      viewRef.current = null
    }
    // Made once: the text and its separator are the read this editor is for.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // The grammar, when it arrives; plain text until then, and if it never does.
  useEffect(() => {
    const lang = editLanguage(name)
    if (!lang) return
    let live = true
    loadLanguage(lang).then(
      (ext) => {
        if (live) viewRef.current?.dispatch({ effects: language.current.reconfigure(ext) })
      },
      () => {
        /* no colour; the text is still the text */
      },
    )
    return () => {
      live = false
    }
  }, [name])

  return (
    <div className="flex min-h-0 flex-1 flex-col" data-testid="viewer-editor">
      <div ref={hostRef} className="min-h-0 flex-1 overflow-hidden" />
      <footer
        data-testid="edit-status"
        data-status={status.kind}
        className="flex shrink-0 flex-wrap items-center gap-x-2 border-t border-hairline px-3 py-1.5 text-vp-xs text-ink-2"
        aria-live="polite"
      >
        {status.kind === 'idle' && <span>{t('edit.hint')}</span>}
        {status.kind === 'saving' && (
          <span className="flex items-center gap-1.5">
            <Loader2 size={11} className="animate-spin" />
            {t('edit.saving')}
          </span>
        )}
        {status.kind === 'saved' && <span className="text-ink">{t('edit.saved', { size: formatBytes(status.size) })}</span>}
        {status.kind === 'changed' && (
          <>
            <span className="text-ink">{t('edit.changed')}</span>
            <button type="button" onClick={onReload} data-testid="edit-reload" className="vp-control text-vp-xs">
              {t('edit.reload')}
            </button>
            <span>{t('edit.reloadDrops')}</span>
          </>
        )}
        {status.kind === 'error' && <span className="text-ink">{status.message}</span>}
      </footer>
    </div>
  )
}

async function loadLanguage(lang: EditLang): Promise<Extension> {
  switch (lang) {
    case 'javascript':
    case 'jsx':
    case 'typescript':
    case 'tsx': {
      const { javascript } = await import('./cm/lang-javascript')
      return javascript({ jsx: lang === 'jsx' || lang === 'tsx', typescript: lang === 'typescript' || lang === 'tsx' })
    }
    case 'python':
      return (await import('./cm/lang-python')).python()
    case 'go':
      return (await import('./cm/lang-go')).go()
    case 'rust':
      return (await import('./cm/lang-rust')).rust()
    case 'json':
      return (await import('./cm/lang-json')).json()
    case 'markdown':
      return (await import('./cm/lang-markdown')).markdown()
    case 'yaml':
      return (await import('./cm/lang-yaml')).yaml()
    case 'html':
      return (await import('./cm/lang-html')).html()
    case 'css':
      return (await import('./cm/lang-css')).css()
    case 'cpp':
      return (await import('./cm/lang-cpp')).cpp()
  }
}

/**
 * CodeMirror's own words -- its search panel's -- in Chinese, for a panel in
 * Chinese. The English ones are its defaults.
 */
const ZH: Record<string, string> = {
  Find: '查找',
  Replace: '替换',
  next: '下一个',
  previous: '上一个',
  all: '全部',
  'match case': '区分大小写',
  regexp: '正则',
  'by word': '整词',
  replace: '替换',
  'replace all': '全部替换',
  close: '关闭',
  'current match': '当前匹配',
  'on line': '在行',
  'replaced match on line $': '替换了第 $ 行的匹配',
  'replaced $ matches': '替换了 $ 处',
  'Go to line': '跳到行',
  go: '跳转',
}

/** The viewer's token colours (styles.css, --vp-tok-*), by Lezer's tags. */
const TOKENS = HighlightStyle.define([
  { tag: [tags.comment, tags.lineComment, tags.blockComment, tags.docComment], color: 'var(--vp-tok-comment)' },
  { tag: [tags.keyword, tags.controlKeyword, tags.operatorKeyword, tags.definitionKeyword, tags.moduleKeyword, tags.modifier, tags.self, tags.null, tags.bool], color: 'var(--vp-tok-keyword)' },
  { tag: [tags.string, tags.special(tags.string), tags.character, tags.docString], color: 'var(--vp-tok-string)' },
  { tag: [tags.number, tags.integer, tags.float], color: 'var(--vp-tok-number)' },
  { tag: [tags.function(tags.variableName), tags.function(tags.propertyName), tags.macroName], color: 'var(--vp-tok-function)' },
  { tag: [tags.typeName, tags.className, tags.namespace, tags.definition(tags.typeName)], color: 'var(--vp-tok-type)' },
  { tag: [tags.propertyName, tags.attributeName, tags.labelName], color: 'var(--vp-tok-property)' },
  { tag: [tags.tagName, tags.heading], color: 'var(--vp-tok-tag)' },
  { tag: [tags.regexp, tags.escape], color: 'var(--vp-tok-regex)' },
  { tag: [tags.variableName, tags.special(tags.variableName)], color: 'var(--vp-tok-variable)' },
  { tag: [tags.punctuation, tags.bracket, tags.separator, tags.operator], color: 'var(--vp-tok-punct)' },
  { tag: tags.inserted, color: 'var(--vp-tok-inserted)' },
  { tag: tags.deleted, color: 'var(--vp-tok-deleted)' },
  { tag: tags.strong, fontWeight: '600' },
  { tag: tags.emphasis, fontStyle: 'italic' },
  { tag: tags.link, textDecoration: 'underline' },
])

/**
 * The editor in the viewer's furniture: its surface, ink, hairline and
 * accent, by variable, so a theme switch reaches it like everything else.
 */
const accent = (pct: number) => `color-mix(in srgb, var(--vp-accent) ${pct}%, transparent)`
const THEME = EditorView.theme({
  '&': { height: '100%', backgroundColor: 'var(--vp-surface-2)', color: 'var(--vp-ink)', fontSize: 'var(--text-vp-sm)' },
  '&.cm-focused': { outline: 'none' },
  '.cm-scroller': { fontFamily: 'var(--font-mono)', lineHeight: '20px' },
  '.cm-content': { caretColor: 'var(--vp-ink)', padding: '8px 0' },
  '.cm-cursor, .cm-dropCursor': { borderLeftColor: 'var(--vp-ink)' },
  '.cm-gutters': { backgroundColor: 'var(--vp-surface-2)', color: 'var(--vp-ink-3)', border: 'none' },
  '.cm-activeLine, .cm-activeLineGutter': { backgroundColor: `color-mix(in srgb, var(--vp-ink) 5%, transparent)` },
  '&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection': {
    backgroundColor: accent(28),
  },
  '.cm-selectionMatch': { backgroundColor: accent(14) },
  '.cm-matchingBracket': { outline: '1px solid var(--vp-hairline)' },
  '.cm-panels': { backgroundColor: 'var(--vp-surface)', color: 'var(--vp-ink)' },
  '.cm-panels.cm-panels-top': { borderBottom: '1px solid var(--vp-hairline)' },
  '.cm-searchMatch': { backgroundColor: accent(22) },
  '.cm-searchMatch-selected': { backgroundColor: accent(45) },
  '.cm-textfield': {
    backgroundColor: 'var(--vp-surface-2)',
    color: 'var(--vp-ink)',
    border: '1px solid var(--vp-hairline)',
    borderRadius: '4px',
  },
  '.cm-button': { backgroundImage: 'none', backgroundColor: 'var(--vp-surface-2)', color: 'var(--vp-ink)', border: '1px solid var(--vp-hairline)' },
})
