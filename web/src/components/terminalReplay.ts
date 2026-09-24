/** What TerminalReplay needs from xterm, so the ordering can be tested in node. */
export interface ReplayTerminal {
  write(data: Uint8Array, callback: () => void): void
  reset(): void
}

interface Job {
  data: Uint8Array
  replay: boolean
  /** Which snapshot this belongs to. See restart. */
  generation: number
}

type Schedule = (callback: () => void) => number
type Cancel = (handle: number) => void

/**
 * Keeps replay parsing ordered while yielding between chunks.
 *
 * xterm's write callback means that parsing finished, not that the browser had
 * a chance to paint. A mobile browser can therefore receive every 64 KiB
 * frame and process them in one long task, leaving the terminal blank until
 * the tail arrives. A frame boundary between replay writes lets the first
 * rows paint without changing the byte order or interleaving live output.
 */
export class TerminalReplay {
  private queue: Job[] = []
  private active = false
  private activeReplay = false
  private yieldHandle: number | null = null
  private resetBeforeReplay = false
  private disposed = false
  private generation = 0

  constructor(
    private readonly term: ReplayTerminal,
    private readonly schedule: Schedule = (callback) => requestAnimationFrame(callback),
    private readonly cancel: Cancel = (handle) => cancelAnimationFrame(handle),
    private readonly onReplayDone?: () => void,
    /**
     * Bytes of snapshot xterm has finished parsing, reported per chunk.
     *
     * This is what the load bar measures. Counting bytes as they *arrived*
     * put 68% on the bar the moment the network was done, and then held it
     * there for the part that actually took the time: on a fast link the
     * parse is most of the wait, and it had four percent of the bar.
     */
    private readonly onReplayParsed?: (bytes: number) => void,
  ) {}

  /**
   * Whether the snapshot is still being put on screen.
   *
   * Queued chunks count as well as the one being parsed. In the frame between
   * two of them nothing is parsing, but the terminal is still showing a screen
   * from minutes ago, and input sent then answers something that is not there.
   */
  get replaying(): boolean {
    return this.activeReplay || this.queue.some((job) => job.replay)
  }

  enqueue(data: Uint8Array, replay: boolean): void {
    this.queue.push({ data, replay, generation: this.generation })
    this.drain()
  }

  /** Drop queued output after a reconnect and reset before the next snapshot. */
  restart(): void {
    // The chunk xterm is parsing right now still finishes, and still calls
    // back. It belongs to the snapshot being thrown away, so its bytes must
    // not be counted toward the one that replaces it.
    this.generation++
    this.queue = []
    this.resetBeforeReplay = true
    // A yield still pending would hold the new snapshot back a frame, behind
    // bytes that were just thrown away.
    if (this.yieldHandle !== null) {
      this.cancel(this.yieldHandle)
      this.yieldHandle = null
    }
  }

  dispose(): void {
    this.disposed = true
    if (this.yieldHandle !== null) this.cancel(this.yieldHandle)
  }

  private drain(): void {
    if (this.disposed || this.active || this.yieldHandle !== null) return
    const job = this.queue.shift()
    if (!job) return

    this.active = true
    this.activeReplay = job.replay
    if (job.replay && this.resetBeforeReplay) {
      this.resetBeforeReplay = false
      this.term.reset()
    }
    this.term.write(job.data, () => {
      // The callback can arrive after dispose(), and nothing may be scheduled
      // against a terminal that no longer exists.
      if (this.disposed) return
      this.active = false
      this.activeReplay = false
      if (job.replay && job.generation === this.generation) this.onReplayParsed?.(job.data.byteLength)
      if (job.replay && !this.queue.some((queued) => queued.replay)) {
        this.onReplayDone?.()
      }
      if (this.queue.length === 0) return

      // Only replay chunks need a paint boundary. Live output should stay
      // responsive once the snapshot has been consumed.
      if (job.replay && this.queue[0].replay) {
        this.yieldHandle = this.schedule(() => {
          this.yieldHandle = null
          this.drain()
        })
      } else {
        this.drain()
      }
    })
  }
}
