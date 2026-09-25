import type { LoadTiming } from '../protocol/wire'

/** How far a terminal's snapshot has got, in bytes. */
export interface LoadCounts {
  /** The snapshot size the server announced. */
  total: number
  /** Bytes that have come off the socket. */
  received: number
  /** Bytes xterm has finished parsing, which is the part a person waits for. */
  parsed: number
}

/**
 * The bar, as two percentages: what is on screen, and what has arrived.
 *
 * The bar is the parsed figure. It used to be the received one, squeezed into
 * 8-68%, with the parse and a fixed eight-frame "scroll" animation sharing the
 * rest -- measured on a desktop, the network part took a quarter of the wait
 * and the parse more than half, so the bar sat at 68% for most of the load and
 * the screen was already full by the time it moved again. The received figure
 * is still worth showing, as the lighter track behind it, because on a slow
 * link it is the one that explains the wait.
 *
 * Capped at 99 until the load is actually finished: the last step, scrolling
 * to the bottom once the terminal has its size, is not a byte count, and a bar
 * reading 100 with the overlay still up is the one thing it must never say.
 */
export function loadPercents(c: LoadCounts): { parsed: number; received: number } {
  if (c.total <= 0) return { parsed: 99, received: 100 }
  const pct = (n: number) => Math.min(99, Math.max(0, Math.floor((n / c.total) * 100)))
  return { parsed: pct(c.parsed), received: Math.min(100, Math.max(0, Math.floor((c.received / c.total) * 100))) }
}

/** "0.9 / 2.0 MiB", or KiB below a megabyte, where MiB would read as 0.0. */
export function formatLoadBytes(done: number, total: number): string {
  const mib = 1 << 20
  if (total >= mib) return `${(done / mib).toFixed(1)} / ${(total / mib).toFixed(1)} MiB`
  return `${Math.round(done / 1024)} / ${Math.round(total / 1024)} KiB`
}

/**
 * Where the time went in one terminal load.
 *
 * Four marks after the start, each set once: the server's confirmation, the
 * first snapshot byte, the last one, and the terminal being ready. The gaps
 * between them are the three candidates for a slow switch -- the server
 * (attaching, and a machine that is out of memory answers slowly), the network,
 * and the parse -- and without them "it takes 20 seconds" could not be pinned
 * on any one of them.
 */
export class LoadTimer {
  private start = 0
  private subscribed = -1
  private firstByte = -1
  private received = -1
  private reconnect = false
  private resumed = false

  constructor(private readonly now: () => number = () => performance.now()) {}

  begin(reconnect: boolean, resumed = false): void {
    this.start = this.now()
    this.subscribed = this.firstByte = this.received = -1
    this.reconnect = reconnect
    this.resumed = resumed
  }

  markSubscribed(): void {
    if (this.subscribed < 0) this.subscribed = this.since()
  }

  markByte(receivedAll: boolean): void {
    if (this.firstByte < 0) this.firstByte = this.since()
    if (receivedAll && this.received < 0) this.received = this.since()
  }

  /** The finished record, with -1 for a mark that never happened. */
  finish(bytes: number, hidden: boolean): LoadTiming {
    return {
      bytes,
      subscribedMs: this.subscribed,
      firstByteMs: this.firstByte,
      receivedMs: this.received,
      readyMs: this.since(),
      reconnect: this.reconnect,
      resumed: this.resumed,
      hidden,
    }
  }

  private since(): number {
    return Math.round(this.now() - this.start)
  }
}
