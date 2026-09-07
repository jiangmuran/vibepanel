import { useRef, useState } from 'react'
import { ChevronDown, ChevronUp, ImagePlus, Keyboard } from 'lucide-react'

import { MobileKeyBar } from './MobileKeyBar'
import { t, useLang } from '../../i18n'

/**
 * The keys and the attach button, for a touchscreen that is not a phone.
 *
 * A tablet is 820 css pixels, so it is not `narrow`, so it gets the desktop
 * layout — and the desktop layout has no key bar and no file chooser, because a
 * desktop has a keyboard and a drag-and-drop. A tablet has neither.
 * 「iPad 端没法摁 ESC，没法上传图片」. There was no Escape anywhere on the
 * screen, which for an agent that asks yes-or-no questions is most of the
 * product.
 *
 * Collapsed by default and opened with one button, which is what was asked for
 * — 「可以折叠或者藏在二级菜单里」 — and is also the right default for the one
 * case that makes a tablet different from a phone: a tablet often has a real
 * keyboard attached, and eighteen soft keys permanently across the bottom of a
 * screen that does not need them is worse than the missing Escape was.
 *
 * A phone does not use this. There the bar is always up, because a phone never
 * has the keys and the screen is too small to spend a tap on reaching them.
 */
export function TouchBar({
  onSend,
  onPaste,
  onFiles,
}: {
  onSend: (bytes: string) => void
  onPaste: (text: string, submit: boolean) => void
  onFiles: (files: File[]) => void
}) {
  useLang()
  const [open, setOpen] = useState(false)
  const chooser = useRef<HTMLInputElement | null>(null)

  return (
    <div className="shrink-0 border-t border-hairline vp-blur" data-testid="touchbar">
      <div className="flex items-center gap-1 px-2 py-1">
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          aria-expanded={open}
          data-testid="touchbar-toggle"
          title={open ? t('touch.hideKeys') : t('touch.showKeys')}
          className="vp-control vp-tap shrink-0 gap-1 text-ink-2"
        >
          <Keyboard size={14} />
          {open ? <ChevronDown size={12} /> : <ChevronUp size={12} />}
        </button>

        {/* Attaching is one press whether the keys are open or not: it is the
            other half of what was missing, and burying it behind the toggle
            would trade one hidden thing for another. */}
        <input
          ref={chooser}
          type="file"
          multiple
          accept="image/*,application/pdf,text/*"
          data-testid="touchbar-file"
          className="hidden"
          onChange={(e) => {
            onFiles([...(e.target.files ?? [])])
            e.target.value = ''
          }}
        />
        <button
          type="button"
          onClick={() => chooser.current?.click()}
          data-testid="touchbar-attach"
          title={t('compose.attach')}
          className="vp-control vp-tap shrink-0 text-ink-2"
        >
          <ImagePlus size={14} />
        </button>

        <span className="min-w-0 flex-1" />
      </div>

      {/* Rendered only when open rather than hidden with a class: the key bar
          is eighteen focusable controls, and a screen reader walking a
          collapsed one finds all of them. */}
      {open && <MobileKeyBar onSend={onSend} onPaste={onPaste} />}
    </div>
  )
}
