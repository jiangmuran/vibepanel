/**
 * Two things Prism has to be told before it loads, so this module is imported
 * first by the one that imports Prism.
 *
 * `manual`: without it, Prism looks for `language-*` elements on the whole
 * page when it loads and highlights them itself, writing HTML into the
 * panel. The viewer only ever asks it for tokens.
 *
 * `disableWorkerMessageHandler`: in a worker it would answer any message
 * with highlighted HTML. There is no worker today; the switch costs nothing.
 */
const g = globalThis as unknown as { Prism?: { manual?: boolean; disableWorkerMessageHandler?: boolean } }
g.Prism = { ...(g.Prism ?? {}), manual: true, disableWorkerMessageHandler: true }

export {}
