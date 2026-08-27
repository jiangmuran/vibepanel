/**
 * noVNC ships no types, so the surface this panel uses is declared here.
 *
 * Deliberately partial. Every property below is one this code sets or reads;
 * copying the whole API in would be a second, unversioned copy of somebody
 * else's documentation, and the compiler would go on believing it after the
 * dependency moved.
 *
 * Two are absent on purpose. `credentials` is never set — the panel
 * authenticates to the display itself and presents security type None to the
 * browser, so there is nothing for noVNC to send. `viewOnly` is not set
 * either: view-only is enforced at the proxy, and a property on the client
 * would read as though this were where it is decided.
 */
declare module '@novnc/novnc' {
  export interface RFBOptions {
    shared?: boolean
    repeaterID?: string
    wsProtocols?: string[]
  }

  export default class RFB extends EventTarget {
    constructor(target: HTMLElement, urlOrChannel: string | WebSocket, options?: RFBOptions)

    /** Scale the remote framebuffer to fit its container. */
    scaleViewport: boolean
    /** Ask the display to resize itself to the container. */
    resizeSession: boolean
    clipViewport: boolean
    background: string
    /** 0-9; JPEG quality where the encoding supports it. */
    qualityLevel: number
    /** 0-9; how hard the server should work to make the stream small. */
    compressionLevel: number

    disconnect(): void
    focus(): void
    blur(): void
  }
}
