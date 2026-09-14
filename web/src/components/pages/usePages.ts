import { useEffect, useState } from 'react'

import { api } from '../../protocol/api'
import type {
  Project,
  SharePageDetail,
  SharePageManifest,
  SharePageParam,
  SharePageRow,
  ShareParamValue,
} from '../../protocol/wire'

/** How often the list of pages is re-read while a project is on screen. */
const PAGE_FOR_MS = 30_000

/** Paths compared the way a person typed them, give or take a trailing slash. */
function samePath(a: string, b: string): boolean {
  const trim = (p: string) => (p.length > 1 ? p.replace(/\/+$/, '') : p)
  return a !== '' && trim(a) === trim(b)
}

/**
 * The share page whose draft is this project's directory, or null.
 *
 * A page is a project like any other -- its directory shows in the sidebar,
 * its agent runs in a session -- and what makes it a page is a row in the
 * panel's list whose source directory is this one. Asked for rather than
 * pushed, because it changes when somebody makes a page, which is rare, and a
 * field on every project in the socket snapshot would be paid for by all of
 * them.
 */
export function usePageFor(project: Project | null): SharePageRow | null {
  const [pages, setPages] = useState<SharePageRow[]>([])
  const path = project?.path ?? ''
  useEffect(() => {
    if (path === '') return
    let cancelled = false
    const read = () => {
      if (document.hidden) return
      api.listPages().then(
        (list) => {
          if (!cancelled) setPages(list)
        },
        () => {},
      )
    }
    read()
    const timer = window.setInterval(read, PAGE_FOR_MS)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [path])
  if (path === '') return null
  return pages.find((p) => samePath(p.sourceDir, path)) ?? null
}

/** The manifest a link on this page, at this pin, is drawing. */
export function manifestFor(detail: SharePageDetail | null, pin: number): SharePageManifest | null {
  if (!detail) return null
  const version = pin > 0 ? pin : detail.page.publishedVersion
  return detail.versions.find((v) => v.version === version)?.manifest ?? null
}

/**
 * Unix seconds, ticking.
 *
 * A trial ends at a moment on the clock, and whether one is running is a
 * question the render asks. Reading Date.now() inside the render gives a
 * different answer on every re-render and never re-renders on its own when
 * the moment passes; this does both properly.
 */
export function useNow(everyMs = 5000): number {
  const [now, setNow] = useState(() => Math.floor(Date.now() / 1000))
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Math.floor(Date.now() / 1000)), everyMs)
    return () => clearInterval(timer)
  }, [everyMs])
  return now
}

/** What a parameter is when the link has not set it: the page's default, or the type's zero. */
export function paramValue(spec: SharePageParam, values: Record<string, ShareParamValue>): ShareParamValue {
  const v = values[spec.key]
  if (v !== undefined) return v
  if (spec.default !== undefined) return spec.default
  switch (spec.type) {
    case 'text':
      return ''
    case 'color':
      return '#000000'
    case 'number':
      return spec.min ?? 0
    case 'enum':
      return spec.values?.[0] ?? ''
    default:
      return false
  }
}
