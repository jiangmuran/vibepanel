import type { LucideIcon } from 'lucide-react'

/**
 * The small shapes the sharing list is built from.
 *
 * What it looked like before: every fact about a link — its scope, what it
 * discloses, when it expires, which version it draws — was a word in grey,
 * separated from the next one by three spaces, on a line under the name. Six
 * facts read as one sentence nobody could parse, and a row of five ghost icons
 * at the other end of the same line read as nothing at all.
 *
 * A fact is a chip: it has an edge, so a reader counts them instead of reading
 * them. A state that matters — expiring, interactive, not published — is a
 * chip with a tone, and a tone is never what says it (red line 4): the chip
 * that changes colour changes its word too, and `actions.test.ts` is what
 * keeps that true.
 */

export type Tone = 'plain' | 'accent' | 'warn'

/**
 * A tone's ground: the state's own colour, thinned until text sits on it.
 *
 * Built rather than written out, because the untranslated-string check reads
 * `background: 'text with spaces'` as prose somebody forgot to translate.
 */
const wash = (token: string, pct: number) => `color-mix(in srgb, var(${token}) ${pct}%, transparent)`

/** The ground and ink of each tone, in tokens so both themes follow. */
const TONE: Record<Tone, { background: string; color: string }> = {
  plain: { background: 'var(--vp-surface-2)', color: 'var(--vp-ink-2)' },
  accent: { background: wash('--vp-accent', 14), color: 'var(--vp-accent)' },
  warn: { background: wash('--vp-state-waiting', 16), color: 'var(--vp-state-waiting)' },
}

export function Chip({
  children,
  icon: Icon,
  tone = 'plain',
  title,
  testid,
  ...rest
}: {
  children: React.ReactNode
  icon?: LucideIcon
  tone?: Tone
  title?: string
  testid?: string
} & Record<`data-${string}`, string | number | boolean | undefined>) {
  return (
    <span
      // `rest` first, so the named props win: `testid` and a hand-written
      // `data-testid` both type-check, and the one spelt like every other
      // component in this repo has to be the one that lands.
      {...rest}
      title={title}
      data-testid={testid}
      // `whitespace-nowrap`, because a chip that wraps inside its own border is
      // two half chips: the row wraps between chips instead.
      className="inline-flex max-w-full shrink-0 items-center gap-1 rounded-md px-1.5 py-0.5 text-vp-xs whitespace-nowrap"
      style={TONE[tone]}
    >
      {Icon && <Icon size={11} className="shrink-0" />}
      <span className="min-w-0 truncate">{children}</span>
    </span>
  )
}

/**
 * The square a page or a link is marked with.
 *
 * A glyph on a ground, at the left of a card, is what makes a list of cards
 * scannable without reading any of them — and it is where the eye lands, so
 * the name beside it does not have to be the biggest thing on the page.
 *
 * Not `Mark`: `AuthGate` exports one of those, it is the product's own logo,
 * and the sharing page draws both at once.
 */
export function IconTile({ icon: Icon, size = 'md' }: { icon: LucideIcon; size?: 'sm' | 'md' }) {
  const box = size === 'sm' ? 'h-6 w-6 rounded-md' : 'h-8 w-8 rounded-vp'
  return (
    <span
      className={`flex shrink-0 items-center justify-center bg-surface-2 text-ink-2 ${box}`}
      aria-hidden="true"
    >
      <Icon size={size === 'sm' ? 12 : 15} />
    </span>
  )
}

/**
 * A heading for a block inside a card: the word, then what it counts.
 *
 * Small, upper-case and quiet on purpose. It is a label on a box rather than a
 * title on a page, and the panel already had this exact shape in the settings
 * dialog's sections.
 *
 * `h3`, under the card's `h2`: this page's outline is the title, then a page,
 * then the blocks inside it, and a level skipped is a level a screen reader
 * reports as missing.
 */
export function BlockTitle({ children, count }: { children: React.ReactNode; count?: number }) {
  return (
    <h3 className="flex items-center gap-1.5 text-vp-xs font-semibold tracking-wide text-ink-3 uppercase">
      {children}
      {count !== undefined && count > 0 && (
        <span className="tabular rounded-md bg-surface-2 px-1 py-0.5 text-ink-2 normal-case">{count}</span>
      )}
    </h3>
  )
}

/**
 * Nothing here yet, said in a box rather than as a line of grey text.
 *
 * A bordered empty state is the difference between "there is nothing" and
 * "this did not load": a sentence on its own on a page reads as a failure.
 */
export function Empty({
  icon: Icon,
  testid,
  children,
}: {
  icon: LucideIcon
  testid?: string
  children: React.ReactNode
}) {
  return (
    <div
      data-testid={testid}
      className="flex flex-col items-center gap-2 rounded-vp border border-dashed border-hairline-strong px-4 py-8 text-center"
    >
      <span className="flex h-9 w-9 items-center justify-center rounded-vp bg-surface-2 text-ink-3">
        <Icon size={16} />
      </span>
      <p className="text-vp-base text-ink-2">{children}</p>
    </div>
  )
}

/** Every field in this page's forms, so they are one width and one shape. */
export const INPUT =
  'w-full min-w-0 rounded-vp border border-hairline bg-surface-2 px-2 py-1.5 text-vp-md text-ink outline-none focus:border-accent'

/** A control with its name above it, filling one cell of the grid. */
export function Field({
  label,
  htmlFor,
  children,
}: {
  label: string
  htmlFor: string
  children: React.ReactNode
}) {
  return (
    <div className="min-w-0">
      <label htmlFor={htmlFor} className="mb-1 block text-vp-sm text-ink-3">
        {label}
      </label>
      {children}
    </div>
  )
}

/**
 * One question a form asks, under its own heading.
 *
 * The form was eight controls and a paragraph in one grid, and the grid put
 * what the link is called, who can see what, and the page's own colours in one
 * run of boxes with a hole in the middle of it. The headings are the
 * decisions, and the disclosure one is the one worth reading.
 */
export function Group({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="min-w-0 border-t border-hairline pt-3 first:border-t-0 first:pt-0">
      <div className="mb-2">
        <BlockTitle>{title}</BlockTitle>
      </div>
      {children}
    </div>
  )
}
