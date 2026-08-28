#!/usr/bin/env python3
"""Mutation testing for the two-tab side panel and the repository link.

Each entry removes one guard and asserts that a test goes red. A guard whose
removal leaves the suite green is decoration, and the run prints it as a
survivor.

Three commands, because the guards live in three places. WEB is `vitest run`,
a second or two. GO is the httpapi suite, which is where the disclosure
decision is. PANES is `npm run check:panes`, which drives the real panel in a
real browser through panes-harness.html — about forty seconds each, and the
only thing that can see a block that opens beside the stack instead of
replacing it.

Do not edit the sources while this is running. Every entry restores the file it
mutated from a copy taken before the run, so an edit that lands in between is
overwritten — and a run killed part-way leaves the file it was holding mutated.
"""
import subprocess
import sys

WEB = ('npx', 'vitest', 'run')
PANES = ('npm', 'run', 'check:panes')
GO = ('go', 'test', './internal/httpapi/', '-count=1', '-run',
      'TestARepositoryIsNamed|TestTheDashboardNeverCarries|TestTypeScriptRows|'
      'TestTheAPIDocCovers|TestABoardCanOnlyNarrow')

# (id, file, needle, replacement, command, cwd)
MUTATIONS = [
    # ── the tab list, and what a stored layout does with the two that went ──
    ('N1 the strip keeps a tab that was retired',
     'web/src/components/chrome.ts',
     "export const PANEL_TABS = ['files', 'notes'] as const",
     "export const PANEL_TABS = ['files', 'notes', 'tokens'] as const",
     WEB, 'web'),
    ('N2 the tab order flips',
     'web/src/components/chrome.ts',
     "export const PANEL_TABS = ['files', 'notes'] as const",
     "export const PANEL_TABS = ['notes', 'files'] as const",
     WEB, 'web'),
    ('N3 the retired list forgets the two tabs that just went',
     'web/src/components/chrome.ts',
     "export const RETIRED_TABS = ['git', 'todos', 'vnc', 'monitor', 'tokens'] as const",
     "export const RETIRED_TABS = ['git', 'todos', 'vnc'] as const",
     WEB, 'web'),
    ('N4 a pane emptied by a retired tab is kept as an empty strip',
     'web/src/components/panes.ts',
     '    if (tabs.length === 0) continue',
     '    if (tabs.length === 0) tabs.push(PANEL_TABS[0])',
     WEB, 'web'),
    ('N5 the pane cap counts panes that were dropped',
     'web/src/components/panes.ts',
     '    if (groups.length >= MAX_PANES) break',
     '    if (raw.groups.indexOf(g) >= MAX_PANES) break',
     WEB, 'web'),
    ('N6 a layout whose every pane was retired comes back empty',
     'web/src/components/panes.ts',
     '  if (groups.length === 0) return defaultLayout()',
     '  if (groups.length === 0) return { version: PANE_LAYOUT_VERSION, groups: [] }',
     WEB, 'web'),

    # ── the six figures ─────────────────────────────────────────────────────
    ('N7 the week window is open at the far end',
     'web/src/components/panels/spend.ts',
     '    if (d.day >= from && d.day <= today) sum += totalOf(d)',
     '    if (d.day <= today) sum += totalOf(d)',
     WEB, 'web'),
    ('N8 the week window counts a day the server calls tomorrow',
     'web/src/components/panels/spend.ts',
     '    if (d.day >= from && d.day <= today) sum += totalOf(d)',
     '    if (d.day >= from) sum += totalOf(d)',
     WEB, 'web'),
    ('N9 an unparseable date is a window starting in 1970',
     'web/src/components/panels/spend.ts',
     "  if (!Number.isFinite(y) || !Number.isFinite(m) || !Number.isFinite(d)) return ''",
     '  if (false) return ""',
     WEB, 'web'),
    ('N10 no project selected reads as a project total of zero',
     'web/src/components/panels/spend.ts',
     '  if (!projectId) return null',
     '  if (!projectId) return 0',
     WEB, 'web'),
    ('N11 a project the range never saw reads as zero',
     'web/src/components/panels/spend.ts',
     '  return row ? totalOf(row) : null',
     '  return row ? totalOf(row) : 0',
     WEB, 'web'),
    ('N12 a tool that spent nothing gets a segment nobody can aim at',
     'web/src/components/panels/spend.ts',
     '    .filter((t) => t.total > 0)',
     '    .filter(() => true)',
     WEB, 'web'),
    ('N13 the breakdown is not sorted, so the bar and the legend disagree',
     'web/src/components/panels/spend.ts',
     '    .sort((a, b) => b.total - a.total)',
     '    .sort(() => 0)',
     WEB, 'web'),
    ('N14 字数 counts every token rather than what was produced',
     'web/src/components/panels/spend.ts',
     '    if (d.day >= from && d.day <= today) sum += d.output',
     '    if (d.day >= from && d.day <= today) sum += totalOf(d)',
     WEB, 'web'),
    ('N15 the hero is the same size as its context',
     'web/src/components/panels/TokenBlock.tsx',
     "        className={`tabular truncate ${hero ? 'text-vp-lg text-ink' : 'text-vp-md text-ink'}`}",
     '        className="tabular truncate text-vp-md text-ink"',
     PANES, 'web'),

    # ── three states, and each replaces the one below it ───────────────────
    ('N16 an opened block is drawn beside the stack rather than instead of it',
     'web/src/components/RightPanel.tsx',
     '        {detail !== null && !detail.full ? (',
     '        {false ? (',
     PANES, 'web'),
    ('N17 the dock header is not the only press target in the block',
     'web/src/components/panels/PanelDock.tsx',
     '          <DockHeader block={block} onOpen={() => onOpen(block)} />',
     '          <DockHeader block={block} onOpen={() => onOpen(block)} />\n          <button type="button" onClick={() => onOpen(block)}>x</button>',
     PANES, 'web'),
    ('N18 Escape leaves a full-screen block closed rather than one level back',
     'web/src/components/RightPanel.tsx',
     '          onBack={() => setDetail({ block: detail.block, full: false })}',
     '          onBack={() => setDetail(null)}',
     PANES, 'web'),
    ('N19 Escape does nothing at all',
     'web/src/components/PanelDetail.tsx',
     "      if (e.key !== 'Escape') return",
     '      if (true) return',
     PANES, 'web'),
    ('N20 the tokens block cannot be opened',
     'web/src/components/panels/dock.ts',
     "  tokens: { icon: Coins, key: 'panel.tokens' },\n",
     '',
     WEB, 'web'),

    # ── the repository, in the panel and on a wall ─────────────────────────
    ('N21 the link is built from the remote string',
     'web/src/components/repo.ts',
     '  return `https://github.com/${remote.owner}/${remote.name}`',
     '  return remote.url',
     WEB, 'web'),
    ('N22 a host that is not github.com is linked anyway',
     'web/src/components/repo.ts',
     "  if (remote.host !== 'github.com') return null",
     '',
     WEB, 'web'),
    ('N23 a segment that walks up a level is allowed',
     'web/src/components/repo.ts',
     "  if (v === '.' || v === '..') return false",
     '',
     WEB, 'web'),
    ('N24 a segment may contain anything',
     'web/src/components/repo.ts',
     '  return /^[A-Za-z0-9._-]+$/.test(v)',
     '  return v !== ""',
     WEB, 'web'),
    ('N25 a counts-mode board names the repository',
     'internal/httpapi/share.go',
     '\t\tout.ScopeRepoOwner, out.ScopeRepoName = s.shareRepoFor(ctx, scope)',
     '\t}\n\t{\n\t\tout.ScopeRepoOwner, out.ScopeRepoName = s.shareRepoFor(ctx, scope)',
     GO, '.'),
    ('N26 a session-scoped board names the project it belongs to',
     'internal/httpapi/share.go',
     '\tif scope.kind != store.ShareProject || scope.missing || scope.cwd == "" {',
     '\tif scope.cwd == "" {',
     GO, '.'),
    ('N27 a remote this panel will not link to is disclosed anyway',
     'internal/httpapi/share.go',
     '\tif err != nil || !snap.HasRemote || !snap.Remote.GitHub() {',
     '\tif err != nil || !snap.HasRemote {',
     GO, '.'),
    ('N28 the dashboard sends the remote URL as well',
     'internal/httpapi/share.go',
     '\tScopeRepoName  string `json:"scopeRepoName"`',
     '\tScopeRepoName  string `json:"scopeRepoName"`\n\tScopeRepoURL   string `json:"scopeRepoUrl"`',
     GO, '.'),

    # ── the arithmetic suite is still running against four tabs ────────────
    ('N29 the arithmetic suite quietly runs against the real two tabs',
     'web/src/components/panes.arithmetic.test.ts',
     "  return { ...real, PANEL_TABS: ['a', 'b', 'c', 'd'] as const }",
     '  return { ...real }',
     WEB, 'web'),
]


def run(cmd, cwd):
    return subprocess.run(cmd, cwd=cwd, capture_output=True, text=True).returncode


def main():
    only = sys.argv[1:]
    survivors = []
    ran = 0
    for name, path, needle, repl, cmd, cwd in MUTATIONS:
        if only and name.split(' ', 1)[0] not in only:
            continue
        ran += 1
        original = open(path).read()
        if needle not in original:
            print(f'!! {name}: the code it mutates is not there any more', flush=True)
            survivors.append(name + ' (needle missing)')
            continue
        open(path, 'w').write(original.replace(needle, repl, 1))
        try:
            code = run(cmd, cwd)
        finally:
            open(path, 'w').write(original)
        if code == 0:
            print(f'SURVIVED  {name}', flush=True)
            survivors.append(name)
        else:
            print(f'killed    {name}', flush=True)
    print(f'\n{ran} run, {len(survivors)} survived')
    for s in survivors:
        print('  survivor:', s)
    return 1 if survivors else 0


if __name__ == '__main__':
    sys.exit(main())
