/** A tiny ordered writer for terminal output received over WebSocket. */
export interface ReplayTerminal {
  write(data: Uint8Array, callback: () => void): void
  reset(): void
}

interface Job {
  data: Uint8Array
  replay: boolean
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

  constructor(
    private readonly term: ReplayTerminal,
    private readonly schedule: Schedule = (callback) => requestAnimationFrame(callback),
    private readonly cancel: Cancel = (handle) => cancelAnimationFrame(handle),
  ) {}

  get replaying(): boolean {
    return this.activeReplay || this.queue.some((job) => job.replay)
  }

  enqueue(data: Uint8Array, replay: boolean): void {
    if (this.disposed) return
    this.queue.push({ data, replay })
    this.drain()
  }

  /** Drop queued output after a reconnect and reset before the next snapshot. */
  restart(): void {
    if (this.disposed) return
    this.queue = []
    this.resetBeforeReplay = true
    if (!this.active && this.yieldHandle !== null) {
      this.cancel(this.yieldHandle)
      this.yieldHandle = null
    }
  }

  dispose(): void {
    this.disposed = true
    this.queue = []
    if (this.yieldHandle !== null) this.cancel(this.yieldHandle)
    this.yieldHandle = null
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
      if (this.disposed) return
      this.active = false
      this.activeReplay = false
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
