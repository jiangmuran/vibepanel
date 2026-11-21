import type { SettingsSection } from './groups'

/**
 * One block inside a group, and one label/value line inside a block.
 *
 * `id` is not decoration. It is the name a caller deep-links by (see
 * groups.ts), and it is written into the DOM so a check driving a browser can
 * assert that asking for a section actually put it on screen — which is the
 * one thing the rail can get wrong in a way nobody notices: the dialog opens,
 * something is shown, and it is not what the notice promised.
 */
export function Section({
  id,
  title,
  children,
}: {
  id: SettingsSection
  title: string
  children: React.ReactNode
}) {
  return (
    <section data-section={id} className="@container mb-6 last:mb-0">
      <h3 className="mb-2 text-vp-base font-semibold tracking-tight text-ink">{title}</h3>
      {children}
    </section>
  )
}

export function Row({
  label,
  value,
  tone = 'normal',
}: {
  label: string
  value: string
  tone?: 'normal' | 'warn' | 'bad'
}) {
  return (
    <div className="flex items-baseline gap-3 border-b border-hairline py-1.5 last:border-0">
      <span className="shrink-0 text-vp-sm text-ink-2" style={{ width: 'clamp(6.5rem, 24cqi, 9.5rem)' }}>{label}</span>
      <span
        className={`tabular min-w-0 flex-1 truncate text-vp-base ${
          tone === 'bad' ? 'text-danger-ink' : tone === 'warn' ? 'text-warn-ink' : 'text-ink'
        }`}
        title={value}
      >
        {value}
      </span>
    </div>
  )
}
