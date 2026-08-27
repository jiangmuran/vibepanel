import { useState } from 'react'
import { createRoot } from 'react-dom/client'

import './styles.css'
import { RightPanel } from './components/RightPanel'
import { defaultLayout, type PaneLayout } from './components/panes'
import type { PanelSocket } from './protocol/socket'
import type { Project } from './protocol/wire'

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
 * `project` is null by default and deliberately: the project panels then
 * render their "no project selected" line instead of fetching, and what is
 * left on screen is exactly the chrome this check is about. The console will
 * carry 502s from the token panel, which reaches for /api with no server
 * behind it.
 *
 * `?project=1` turns that off, and the check needs it for one thing: the
 * divider *inside* the files and notes tabs only exists when there is a
 * project, because a stacked tab with nothing to show would otherwise say "no
 * project selected" twice with a line between them. The panels inside it then
 * fetch and fail — which is fine, and is the point: the stack is the tab's
 * structure and does not depend on either half having anything to say.
 */

/** A project shaped like a project, for the stacked tabs. Nothing reads it. */
const FAKE: Project = {
  id: 'harness',
  name: 'harness',
  path: '/tmp/harness',
  sortIndex: null,
  pinned: false,
  lastActiveAt: 0,
  createdAt: 0,
}

export function PanesHarness() {
  const [layout, setLayout] = useState<PaneLayout>(defaultLayout())
  // Settable from the query string: the panel lays its figures out in two
  // columns above 380px, and a check about the width threshold has to be able
  // to sit on either side of it. It was here first for the label fold, which
  // moved the marker and is gone.
  const params = new URLSearchParams(location.search)
  const [width, setWidth] = useState(Number(params.get('w')) || 320)
  return (
    <div style={{ display: 'flex', height: '100vh' }}>
      <div style={{ flex: 1 }} />
      <RightPanel
        project={params.get('project') ? FAKE : null}
        sessions={[]}
        // The notes and todo panels subscribe through this, and that one
        // method is the whole surface they touch. It used to be stubbed as
        // `onNote`/`onTodo`, which are not methods PanelSocket has — harmless
        // while `project` was always null and those panels never mounted, and
        // an immediate crash into the error boundary the moment ?project made
        // them render. A stub is only a stub of what is actually called.
        socket={{ onPanelChange: () => () => {} } as unknown as PanelSocket}
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
