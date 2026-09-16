import type { UpdateJob, UpdateStatus } from '../protocol/wire'
import type { Key } from '../i18n'
import { formatBytes } from './bytes'

/**
 * What the update section says, decided away from the JSX so it can be
 * tested without a DOM.
 *
 * Every sentence here is a key and its parameters; the component calls t().
 * Two things are deliberately not sentences: the server's own text (`byHand`,
 * `unreachable`, a job's `error`) is shown as detail under the panel's
 * sentence, never instead of it, because it is English and may contain a path.
 */

export interface Line {
  key: Key
  params?: Record<string, string | number>
  /** Text the panel did not write, shown after the sentence. */
  detail?: string
}

const VERSION = /^v?\d+\.\d+\.\d+/

/** Whether a job is still going, as the server defines it. */
export function jobActive(job: UpdateJob | undefined): boolean {
  return job !== undefined && (job.stage === 'downloading' || job.stage === 'installing' || job.stage === 'restarting')
}

/**
 * The one line under the buttons, about the release.
 *
 * Null while nothing has been asked and nothing is being asked: the section
 * then shows the version and the check button and says nothing else, which is
 * the honest amount for a panel that has never looked.
 */
export function statusLine(st: UpdateStatus): Line | null {
  if (st.unreachable) {
    const key: Key =
      st.unreachableKind === 'offline'
        ? 'upd.offline'
        : st.unreachableKind === 'timeout'
          ? 'upd.timeout'
          : st.unreachableKind === 'rateLimited'
            ? 'upd.rateLimited'
            : 'upd.unreachable'
    return { key, detail: st.unreachable }
  }
  if (st.checkedAt === undefined || st.checkedAt === 0) {
    return st.checking ? { key: 'upd.checking' } : null
  }
  if (!st.version) return { key: 'upd.noRelease' }
  if (!st.newer) {
    // A development build cannot be compared, and saying "up to date" would
    // be a claim nobody can support.
    return VERSION.test(st.current) ? { key: 'upd.upToDate' } : { key: 'upd.devBuild' }
  }
  if (!st.asset) return { key: 'upd.noAsset', params: { v: st.version, platform: st.platform } }
  return { key: 'upd.available', params: { v: st.version } }
}

/**
 * The job, as a sentence and, while downloading, a fraction for the bar.
 *
 * `progress` is undefined outside the download: the install is milliseconds
 * and the restart is the supervisor's, and a bar that sits at 100% over
 * "restarting" reads as stuck.
 */
export function jobLine(job: UpdateJob): Line & { progress?: number } {
  switch (job.stage) {
    case 'downloading': {
      if (job.total > 0) {
        return {
          key: 'upd.downloading',
          params: { done: formatBytes(job.done), total: formatBytes(job.total) },
          progress: Math.min(1, job.done / job.total),
        }
      }
      return { key: 'upd.downloadingUnsized', params: { done: formatBytes(job.done) } }
    }
    case 'installing':
      return { key: 'upd.installing' }
    case 'restarting':
      return job.elevated ? { key: 'upd.elevated' } : { key: 'upd.restarting', params: { v: job.version } }
    case 'installed':
      return { key: 'upd.installedNoRestart', params: { v: job.version }, detail: job.restartWhy }
    case 'failed':
      return { key: failureKey(job.reason), detail: job.error }
  }
}

function failureKey(reason: UpdateJob['reason']): Key {
  switch (reason) {
    case 'checksum':
      return 'upd.failedChecksum'
    case 'verify':
      return 'upd.failedVerify'
    case 'network':
      return 'upd.failedNetwork'
    case 'install':
      return 'upd.failedInstall'
    default:
      return 'upd.failed'
  }
}

/** Whether the apply button may be offered, in this process. */
export function canApply(st: UpdateStatus): boolean {
  return st.newer && Boolean(st.asset) && !st.byHand && !jobActive(st.job)
}

/**
 * Whether the app should say, outside the settings dialog, that a release is
 * waiting. Not for a version the person has said they are skipping, and not
 * while it is being installed -- the section says that.
 */
export function shouldNotice(st: UpdateStatus | null, skipped: string | null): boolean {
  if (!st || !st.newer || !st.version) return false
  if (skipped === st.version) return false
  return !jobActive(st.job)
}

/** The key the app stores a skipped version under. Per browser, on purpose. */
export const SKIPPED_KEY = 'vp.update.skipped'

export function readSkipped(): string | null {
  try {
    return localStorage.getItem(SKIPPED_KEY)
  } catch {
    return null
  }
}

export function writeSkipped(version: string | null): void {
  try {
    if (version === null) localStorage.removeItem(SKIPPED_KEY)
    else localStorage.setItem(SKIPPED_KEY, version)
  } catch {
    // A browser that refuses storage shows the notice again next time,
    // which is the safe direction.
  }
}

/**
 * The release's date, for the reader's locale, or '' when GitHub did not say.
 * Date only: the hour a release was cut is not something anybody decides on.
 */
export function releaseDate(publishedAt: string | undefined, lang: 'zh' | 'en'): string {
  if (!publishedAt) return ''
  const d = new Date(publishedAt)
  if (Number.isNaN(d.getTime())) return ''
  return d.toLocaleDateString(lang === 'zh' ? 'zh-CN' : 'en-GB', { year: 'numeric', month: 'short', day: 'numeric' })
}
