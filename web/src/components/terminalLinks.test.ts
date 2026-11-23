import { describe, expect, it } from 'vitest'
import { Terminal } from '@xterm/xterm'

import {
  classifyOscUri,
  findTerminalPathHits,
  linkActionAtCell,
  provideTerminalFileLinks,
} from './terminalLinks'

describe('classifyOscUri', () => {
  it('routes file and editor URIs to the panel viewer and keeps web links external', () => {
    expect(classifyOscUri('file:///root/.gemini/plan.md#L12')).toEqual({
      kind: 'path',
      target: 'file:///root/.gemini/plan.md#L12',
    })
    expect(classifyOscUri('vscode://file/niuniu869_dev/vibepanel/README.md:10:2')).toEqual({
      kind: 'path',
      target: 'vscode://file/niuniu869_dev/vibepanel/README.md:10:2',
    })
    expect(classifyOscUri('~/.gemini/antigravity-cli/brain/id/walkthrough.md')).toEqual({
      kind: 'path',
      target: '~/.gemini/antigravity-cli/brain/id/walkthrough.md',
    })
    expect(classifyOscUri('https://example.com/docs')).toEqual({
      kind: 'external',
      url: 'https://example.com/docs',
    })
    expect(classifyOscUri('javascript:alert(1)')).toBeNull()
    expect(classifyOscUri('data:text/html,<script>alert(1)</script>')).toBeNull()
  })
})

describe('findTerminalPathHits', () => {
  it('extracts Codex and Antigravity Markdown links, file:// URIs and ~/.gemini paths', () => {
    const line =
      '请查看 [panels.go](/niuniu869_dev/vibepanel/internal/httpapi/panels.go#L482) 与 file:///root/.gemini/brain/plan.md#L10-L20。'
    const hits = findTerminalPathHits(line)
    expect(hits.map((h) => h.target)).toEqual([
      '/niuniu869_dev/vibepanel/internal/httpapi/panels.go#L482',
      'file:///root/.gemini/brain/plan.md#L10-L20',
    ])
  })

  it('separates inline ASCII paths surrounded by Chinese prose without spaces', () => {
    const line =
      '在desktop_client/data/units.json与desktop_client/sim/combat.py中同步更新了记忆~/.claude/projects/demo/MEMORY.md。'
    const hits = findTerminalPathHits(line)
    expect(hits.map((h) => h.target)).toEqual([
      'desktop_client/data/units.json',
      'desktop_client/sim/combat.py',
      '~/.claude/projects/demo/MEMORY.md',
    ])
  })

  it('supports quoted and backticked paths containing spaces or CJK characters, plus folder trails', () => {
    const line = '已生成 `/root/.gemini/测试 报告.md`、目录 `src/engine/` 和 "docs/设计说明.md#L5"'
    const hits = findTerminalPathHits(line)
    expect(hits.map((h) => h.target)).toEqual([
      '/root/.gemini/测试 报告.md',
      'src/engine/',
      'docs/设计说明.md#L5',
    ])
  })

  it('ignores http(s) URLs, MIME types, git branch names, ratios and /dev/null', () => {
    const line =
      'GET https://example.com/a/b.png returned text/html and application/json on feat/guests (2/2) 2>/dev/null'
    expect(findTerminalPathHits(line)).toEqual([])
  })

  it('keeps balanced route brackets in Next.js paths while trimming outer parens', () => {
    const line = 'Edited (web/src/app/(dashboard)/[slug]/page.tsx:24): done.'
    const hits = findTerminalPathHits(line)
    expect(hits.map((h) => h.target)).toEqual(['web/src/app/(dashboard)/[slug]/page.tsx:24'])
  })
})

describe('provideTerminalFileLinks and linkActionAtCell', () => {
  it('maps columns accurately after wide CJK characters across both soft-wrap and hard-wrapped TUI margins', async () => {
    // 1. Hard-wrapped TUI margin: 4 cols (`记忆`) + 37 cols = 41 cols on a 42-col grid.
    const term = new Terminal({ cols: 42, rows: 6, allowProposedApi: true })
    await new Promise<void>((resolve) => {
      term.write(
        '记忆~/.gemini/antigravity-cli/brain/12345\r\n' +
          '  67890/walkthrough.md\r\n',
        resolve,
      )
    })

    const opened: Array<{ target: string; fallback?: string }> = []
    const row1Links = provideTerminalFileLinks(term, 1, (target, fallback) => {
      opened.push({ target, fallback })
    })
    expect(row1Links).toHaveLength(1)
    // `记忆` occupies 4 columns (2 wide chars), so `~` starts at column 5 (1-based) and ends at 41.
    expect(row1Links[0].range.start).toEqual({ x: 5, y: 1 })
    expect(row1Links[0].range.end).toEqual({ x: 41, y: 1 })
    expect(row1Links[0].text).toBe('~/.gemini/antigravity-cli/brain/1234567890/walkthrough.md')

    // Tapping the continuation on row 2 (0-based row 1, col 4) resolves the same joined path.
    const act = linkActionAtCell(
      term,
      4,
      1,
      (target, fallback) => opened.push({ target, fallback }),
      () => {},
    )
    expect(act).not.toBeNull()
    act?.()
    expect(opened).toEqual([
      {
        target: '~/.gemini/antigravity-cli/brain/1234567890/walkthrough.md',
        fallback: '~/.gemini/antigravity-cli/brain/12345',
      },
    ])
    term.dispose()

    // 2. Terminal soft-wrap (`isWrapped`): 4 + 37 = 41 cols on a 40-col grid wraps col 41 onto row 2 col 1.
    const soft = new Terminal({ cols: 40, rows: 6, allowProposedApi: true })
    await new Promise<void>((resolve) => {
      soft.write('记忆~/.gemini/antigravity-cli/brain/12345\r\n', resolve)
    })
    const softLinks = provideTerminalFileLinks(soft, 1, () => {})
    expect(softLinks).toHaveLength(1)
    expect(softLinks[0].range).toEqual({
      start: { x: 5, y: 1 },
      end: { x: 1, y: 2 },
    })
    soft.dispose()
  })

  it('recognizes agy bullet lines, Codex #L..C.. anchors, tsc (line,col), and parenthesized paths', () => {
    const agyLines = [
      '  • ~/.claude/projects/-niuniu869-dev-vibepanel/memory/MEMORY.md（~目录下的任意服务器文件预览）',
      '  • ~/.claude/projects/-niuniu869-dev-vibepanel/memory/deploy-lineage.md（Markdown/file://文件链接预览）',
      '  • /etc/systemd/system/vibepanel.service:35（服务器任意位置绝对路径+行号跳转）',
      '  • internal/httpapi/（当前工作目录内文件夹，点击后自动展开右侧文件栏并切换到该目录）',
      '  • ~/.gemini/（工作目录外文件夹，点击会提示拦截，右侧文件栏不会跳出当前工作目录）',
    ]
    expect(agyLines.map((l) => findTerminalPathHits(l)[0]?.target)).toEqual([
      '~/.claude/projects/-niuniu869-dev-vibepanel/memory/MEMORY.md',
      '~/.claude/projects/-niuniu869-dev-vibepanel/memory/deploy-lineage.md',
      '/etc/systemd/system/vibepanel.service:35',
      'internal/httpapi/',
      '~/.gemini/',
    ])

    expect(
      findTerminalPathHits(
        'Check (internal/httpapi/fsfile.go#L42C5) and web/src/App.tsx(115,3) and app/(dashboard)/page.tsx',
      ).map((h) => h.target),
    ).toEqual(['internal/httpapi/fsfile.go#L42C5', 'web/src/App.tsx(115,3)', 'app/(dashboard)/page.tsx'])
  })
})
