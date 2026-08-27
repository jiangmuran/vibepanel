import { useState } from 'react'
import { createRoot } from 'react-dom/client'

import './styles.css'
import { RightPanel } from './components/RightPanel'
import { defaultLayout, type PaneLayout } from './components/panes'
import type { PanelSocket } from './protocol/socket'

/**
 * The side panel on its own, for scripts/panes-check.mjs.
 *
 * The pane layout is the one part of this interface whose behaviour is a
 * gesture — press, move, watch where it would land, release — and none of that
 * can be asserted in vitest, which runs in node with no layout at all. The
 * other browser harnesses drive the whole product against a real server and a
 * real tmux, which takes minutes; this needs neither, because the layout
 * engine is pure and the panel is the only thing under test.
 *
 * Rendered through `panes-harness.html`, which is not in the build's inputs —
 * `vite build` takes `index.html` and nothing else, so this ships nowhere.
 *
 * `project` is null deliberately: the four project panels then render their
 * "no project selected" line instead of fetching, and what is left on screen
 * is exactly the chrome this check is about. The console will carry 502s from
 * the token panel, which reaches for /api with no server behind it.
 */
export function PanesHarness() {
  const [layout, setLayout] = useState<PaneLayout>(defaultLayout())
  // Settable from the query string, because whether the selected tab is
  // labelled is a function of width and the label folding open moves the
  // marker — so a check about the marker has to be able to turn it off.
  const [width, setWidth] = useState(
    Number(new URLSearchParams(location.search).get('w')) || 320,
  )
  return (
    <div style={{ display: 'flex', height: '100vh' }}>
      <div style={{ flex: 1 }} />
      <RightPanel
        project={null}
        sessions={[]}
        // The notes and todo panels subscribe through this and never render
        // here, so the two listeners they would register are the whole surface.
        socket={{ onNote: () => () => {}, onTodo: () => () => {} } as unknown as PanelSocket}
        layout={layout}
        onLayout={setLayout}
        onRefocus={() => {}}
        width={width}
        onWidthChange={setWidth}
        onCollapse={() => {}}
        onOpenTokens={() => {}}
      />
    </div>
  )
}

createRoot(document.getElementById('root')!).render(<PanesHarness />)
