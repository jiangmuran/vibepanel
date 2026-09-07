import { useRef } from 'react'
import { ImagePlus, Keyboard } from 'lucide-react'

import { MobileKeyBar } from './MobileKeyBar'
import { t, useLang } from '../../i18n'

/**
 * The two controls a tablet needs, in the row that already exists.
 *
 * A tablet is 820 css pixels, so it is not `narrow`, so it gets the desktop
 * layout — which assumes a keyboard and a mouse. 「iPad 端没法摁 ESC，没法上传
 * 图片」. There was no Escape anywhere on the screen, which for an agent that
 * asks yes-or-no questions is most of the product.
 *
 * These were a bar of their own at first, and that was the wrong shape:
 * 「你没有必要单开一个横杠吧... 单开一条有点浪费空间」. A full-width row for two
 * buttons costs a line of terminal on the device with the least of it. The
 * session header already has a control cluster — the panel toggle, settings,
 * the theme, sign-out — and two more belong in it.
 *
 * So this is two buttons for the header, and the key bar itself is rendered
 * separately by the caller, below the terminal, only while it is open. The
 * split is what lets the toggle live in the header while the thing it toggles
 * is somewhere it has room to be.
 */
export function TouchControls({
  open,
  onToggle,
  onFiles,
}: {
  open: boolean
  onToggle: () => void
  onFiles: (files: File[]) => void
}) {
  useLang()
  const chooser = useRef<HTMLInputElement | null>(null)

  return (
    <>
      <button
        type="button"
        onClick={onToggle}
        aria-pressed={open}
        data-testid="touch-keys"
        title={open ? t('touch.hideKeys') : t('touch.showKeys')}
        className="vp-control"
      >
        <Keyboard size={15} />
      </button>
      {/* Attaching is one press whether the keys are open or not: it is the
          other half of what was missing, and putting it behind the toggle
          would trade one hidden thing for another. */}
      <input
        ref={chooser}
        type="file"
        multiple
        accept="image/*,application/pdf,text/*"
        data-testid="touch-file"
        className="hidden"
        onChange={(e) => {
          onFiles([...(e.target.files ?? [])])
          e.target.value = ''
        }}
      />
      <button
        type="button"
        onClick={() => chooser.current?.click()}
        data-testid="touch-attach"
        title={t('compose.attach')}
        className="vp-control"
      >
        <ImagePlus size={15} />
      </button>
    </>
  )
}

/** The keys themselves, shown under the terminal while the toggle is on. */
export function TouchKeys({
  onSend,
  onPaste,
}: {
  onSend: (bytes: string) => void
  onPaste: (text: string, submit: boolean) => void
}) {
  return (
    <div className="shrink-0 border-t border-hairline vp-blur" data-testid="touchkeys">
      <MobileKeyBar onSend={onSend} onPaste={onPaste} />
    </div>
  )
}
