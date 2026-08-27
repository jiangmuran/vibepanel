#!/usr/bin/env python3
"""Mutation testing for the side panel's restructure into four tabs.

Each entry removes one guard and asserts that a test goes red. A guard whose
removal leaves the suite green is decoration, and the run prints it as a
survivor.

Two commands, because several of the guards are not reachable from node. WEB is
`vitest run`, a second or two. PANES is `npm run check:panes`, which drives the
real panel in a real browser through panes-harness.html — about forty seconds
each, and the only thing that can see a divider that does not drag or a tab
that draws a word.

Both run from `web/`. The paths in the table are repository-relative and are
opened by this script rather than by the command, so only the working directory
moves — and running `npx vitest` from the root leaves a `node_modules/.vite`
cache there, in a tree whose .gitignore has no reason to mention one.

Do not edit the sources while this is running. Every entry restores the file it
mutated from a copy taken before the run, so an edit that lands in between is
overwritten — and a run killed part-way leaves the file it was holding mutated.
"""
import subprocess
import sys

WEB = ('npx', 'vitest', 'run')
PANES = ('npm', 'run', 'check:panes')

# (id, file, needle, replacement, command, cwd)
MUTATIONS = [
    # ── the divider inside a tab ───────────────────────────────────────────
    ('M1 a half can be dragged to nothing',
     'web/src/components/stack.ts',
     '  return Math.max(STACK_MIN_RATIO, Math.min(1 - STACK_MIN_RATIO, ratio))',
     '  return ratio',
     WEB, 'web'),
    ('M2 only the top half has a floor',
     'web/src/components/stack.ts',
     '  return Math.max(STACK_MIN_RATIO, Math.min(1 - STACK_MIN_RATIO, ratio))',
     '  return Math.max(STACK_MIN_RATIO, ratio)',
     WEB, 'web'),
    ('M3 a ratio that is not a number is clamped to a bound',
     'web/src/components/stack.ts',
     '  if (!Number.isFinite(ratio)) return STACK_DEFAULT_RATIO\n',
     '',
     WEB, 'web'),
    ('M4 an empty key is read as zero',
     'web/src/components/stack.ts',
     "  if (raw === null || raw.trim() === '') return STACK_DEFAULT_RATIO\n",
     '  if (raw === null) return STACK_DEFAULT_RATIO\n',
     WEB, 'web'),
    ('M5 a stored position is trusted rather than clamped',
     'web/src/components/stack.ts',
     '  return clampStackRatio(Number(raw))',
     '  return Number(raw)',
     WEB, 'web'),
    ('M6 an unmeasured box is divided by',
     'web/src/components/stack.ts',
     '  if (!Number.isFinite(height) || height <= 0) return null\n',
     '',
     WEB, 'web'),
    ('M7 both stacked tabs share one divider position',
     'web/src/components/stack.ts',
     '  return `vibepanel.stack.${id}`',
     '  return `vibepanel.stack.${id ? "one" : "one"}`',
     WEB, 'web'),

    # ── which tabs there are, and which divide their own height ────────────
    ('M8 the tab order changes without anybody noticing',
     'web/src/components/chrome.ts',
     "export const PANEL_TABS = ['files', 'monitor', 'tokens', 'notes'] as const",
     "export const PANEL_TABS = ['files', 'notes', 'monitor', 'tokens'] as const",
     WEB, 'web'),
    ('M9 a retired tab comes back to the strip',
     'web/src/components/chrome.ts',
     "export const PANEL_TABS = ['files', 'monitor', 'tokens', 'notes'] as const",
     "export const PANEL_TABS = ['files', 'monitor', 'tokens', 'notes', 'todos'] as const",
     WEB, 'web'),
    ('M10 no tab owns its own height',
     'web/src/components/chrome.ts',
     '  return (STACKED_TABS as readonly string[]).includes(tab)',
     '  return false',
     WEB, 'web'),
    ('M11 every tab owns its own height',
     'web/src/components/chrome.ts',
     '  return (STACKED_TABS as readonly string[]).includes(tab)',
     '  return true',
     WEB, 'web'),
    ('M12 the panel is two columns at every width',
     'web/src/components/chrome.ts',
     "  return width >= PANEL_DENSE_WIDTH ? 'wide' : 'narrow'",
     "  return 'wide'",
     WEB, 'web'),
    ('M13 the removed split preset comes back as an inert control',
     'web/src/components/chrome.ts',
     "  return [menu, { id: 'collapse', testid: 'panel-collapse' }]",
     "  return [menu, { id: 'split', testid: 'panel-split' },\n    { id: 'collapse', testid: 'panel-collapse' }]",
     WEB, 'web'),

    # ── a stored layout naming tabs that are gone ──────────────────────────
    ('M14 a pane emptied by a retired tab is kept as an empty strip',
     'web/src/components/panes.ts',
     '    if (tabs.length === 0) continue',
     '    if (tabs.length === 0) tabs.push(PANEL_TABS[0])',
     WEB, 'web'),
    ('M15 the pane cap counts panes that were dropped',
     'web/src/components/panes.ts',
     '    if (groups.length >= MAX_PANES) break',
     '    if (raw.groups.indexOf(g) >= MAX_PANES) break',
     WEB, 'web'),
    ('M16 a layout whose every pane was retired comes back empty',
     'web/src/components/panes.ts',
     '  if (groups.length === 0) return defaultLayout()',
     '  if (groups.length === 0) return { version: PANE_LAYOUT_VERSION, groups: [] }',
     WEB, 'web'),
    ('M17 a retired tab name is accepted as a tab',
     'web/src/components/panes.ts',
     "  return typeof v === 'string' && (PANEL_TABS as readonly string[]).includes(v)",
     "  return typeof v === 'string'",
     WEB, 'web'),
    ('M18 a pane keeps an active tab it no longer holds',
     'web/src/components/panes.ts',
     '    const active = isTab(g.active) && tabs.includes(g.active) ? g.active : tabs[0]',
     '    const active = (g.active ?? tabs[0]) as PanelTab',
     WEB, 'web'),
    ('M19 a tab this build has and the layout does not is left homeless',
     'web/src/components/panes.ts',
     '  if (missing.length > 0) groups[groups.length - 1].tabs.push(...missing)',
     '',
     WEB, 'web'),

    # ── the strip is icons, and the name still reaches a person ────────────
    ('M20 the tab has no accessible name, only a tooltip',
     'web/src/components/RightPanel.tsx',
     '                aria-label={label}\n',
     '',
     PANES, 'web'),
    ('M21 the tab has no tooltip, only an accessible name',
     'web/src/components/RightPanel.tsx',
     '                title={label}\n',
     '',
     PANES, 'web'),
    ('M22 the strip goes back to drawing the name in words',
     'web/src/components/RightPanel.tsx',
     '                <Icon size={14} className="vp-tab-icon shrink-0" />',
     '                <Icon size={14} className="vp-tab-icon shrink-0" />\n                <span>{label}</span>',
     PANES, 'web'),
    ('M23 a stacked tab is given no height and collapses',
     'web/src/components/RightPanel.tsx',
     "            className={`vp-swap ${tabOwnsHeight(tab) ? 'h-full' : 'min-h-full'}`}",
     '            className="vp-swap min-h-full"',
     PANES, 'web'),

    # ── the movement, which is the whole of 要有动画 ───────────────────────
    ('M24 the marker teleports between tabs',
     'web/src/styles.css',
     '  transition: transform 260ms var(--vp-ease), width 260ms var(--vp-ease);',
     '',
     PANES, 'web'),
    ('M25 the selected glyph is not lifted',
     'web/src/styles.css',
     '  transform: scale(1.12);',
     '  transform: none;',
     PANES, 'web'),
    ('M26 the divider eases while it is being dragged',
     'web/src/components/StackedTab.tsx',
     "        className={`min-h-0 overflow-y-auto ${dragging ? '' : 'vp-stack-half'}`}",
     '        className="min-h-0 overflow-y-auto vp-stack-half"',
     PANES, 'web'),
    ('M27 the halves never ease at all',
     'web/src/styles.css',
     '.vp-stack-half {\n  transition: flex-grow 180ms var(--vp-ease);\n}',
     '.vp-stack-half {\n  color: inherit;\n}',
     PANES, 'web'),
    ('M28 a staggered row stays invisible under prefers-reduced-motion',
     'web/src/styles.css',
     '    animation-delay: 0ms !important;\n',
     '',
     WEB, 'web'),

    # ── the divider is remembered ──────────────────────────────────────────
    ('M29 the divider position is never written',
     'web/src/components/StackedTab.tsx',
     '        localStorage.setItem(key, String(next))',
     '        void next',
     PANES, 'web'),
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
