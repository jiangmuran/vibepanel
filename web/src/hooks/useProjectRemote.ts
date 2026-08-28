import { useEffect, useState } from 'react'

import { api } from '../protocol/api'
import type { GitRemote } from '../protocol/wire'

/**
 * The current project's remote, if it has one this panel will link to.
 *
 * Read once per project rather than polled. A remote is a line in a config
 * file: it changes when somebody runs `git remote set-url`, which is not a
 * thing that happens while you watch a panel, and a poller for it would be a
 * repository read every few seconds to keep a link up to date that has not
 * moved since the clone.
 *
 * Through the repository endpoint rather than a new route of its own. That
 * handler is already server-cached and already parses the remote — `internal/
 * git` is the only thing in this product allowed to decide what a remote
 * string means, and a second parser here would be a second answer to "is this
 * GitHub". The extra work the handler does is paid once per project switch and
 * lands in the same cache the repository line then reads.
 *
 * `github` is the server's own answer to "is this a host we can talk to", so a
 * remote on a GitHub Enterprise install or a self-hosted forge resolves to null
 * and the caller renders the project's name alone. That is the
 * deliberate-looking absence, not a broken link.
 */
export function useProjectRemote(projectId: string | null): GitRemote | null {
  // The answer *and* what it is an answer about, so the return can be derived
  // rather than reset. Storing the remote alone would need a synchronous
  // `setRemote(null)` in the effect when the project changes — which renders
  // the previous project's repository under the new project's name for one
  // frame, and is what react-hooks/set-state-in-effect is pointing at.
  const [got, setGot] = useState<{ id: string; remote: GitRemote | null } | null>(null)

  useEffect(() => {
    if (projectId === null) return
    let ignore = false
    api.git(projectId).then(
      (info) => {
        if (!ignore) setGot({ id: projectId, remote: info.github ? info.remote : null })
      },
      () => {
        // Not a repository, or the read failed. Both are recorded as "asked,
        // no link" rather than left unanswered, so a directory that is not a
        // checkout settles instead of retrying on every render.
        if (!ignore) setGot({ id: projectId, remote: null })
      },
    )
    return () => {
      ignore = true
    }
  }, [projectId])

  return got !== null && got.id === projectId ? got.remote : null
}
