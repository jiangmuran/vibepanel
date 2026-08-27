import { useState } from 'react'
import { ArrowDown, ArrowLeft, ArrowRight, ArrowUp, CornerDownLeft } from 'lucide-react'

import { KEY_SEQUENCES, withAlt, withCtrl } from './keys'
import type { KeyName } from './keys'
import { t, useLang } from '../../i18n'

/**
 * A keyboard for the keys a phone does not have.
 *
 * Not a replacement for the software keyboard — a replacement would have to
 * reimplement every input method, and typing Chinese through a hand-rolled key
 * grid is worse than not offering one. This covers what an agent conversation
 * needs and a phone cannot produce: Escape, Tab, Ctrl, arrows, and the
 * one-character answers that are most of what anybody types from a phone.
 */
export function MobileKeyBar({ onSend }: { onSend: (bytes: string) => void }) {
  useLang()
  // Sticky modifiers: tap, then tap the key it applies to. Holding two places
  // at once is not a gesture a thumb can make.
  const [ctrl, setCtrl] = useState(false)
  const [alt, setAlt] = useState(false)

  // Raw sequences do not consume the modifiers.
  //
  // They used to clear them, which meant arming ctrl and tapping y sent a
  // plain "y" and quietly dropped the modifier — the user believing they had
  // just sent Ctrl-C to a runaway agent, and the agent receiving a yes. A
  // modifier that disappears without doing anything is worse than not having
  // one. It stays armed until a key it can actually apply to is pressed.
  const sendRaw = (s: string) => {
    onSend(s)
  }

  const sendChar = (ch: string) => {
    let out = ch
    if (ctrl) out = withCtrl(out)
    if (alt) out = withAlt(out)
    onSend(out)
    setCtrl(false)
    setAlt(false)
  }

  const key = (name: KeyName) => () => sendRaw(KEY_SEQUENCES[name])

  return (
    <div
      data-testid="key-bar"
      // vp-safe-bottom: this is the lowest thing on a phone, and the viewport
      // is set to extend under the home indicator. See the class for why.
      className="flex shrink-0 flex-col gap-1 border-t border-hairline px-1 py-1.5 vp-blur vp-safe-bottom"
    >
      {/* Two rows, because eighteen keys do not fit across a phone and a
          single scrolling row hides whichever ones are not in view — which,
          the first time this was tried, meant y, n and Escape. This row holds
          what an agent conversation actually needs, and nothing in it is ever
          hidden.
          
          It wraps rather than scrolls. Eight keys at the 44px a thumb needs
          come to 380px, which does not fit a 320px phone — measured on one,
          after the touch targets were widened: the row overflowed by 56px, the
          page did not scroll, and `alt` and `ctrl` were simply unreachable. A
          second line costs 44px of a screen that has room for it; a key you
          cannot press costs the feature. */}
      <div
        data-testid="key-row-primary"
        // Packed from the left, not spread. justify-between stretches the
        // *last* line too, so a wrapped row put ctrl against one edge and alt
        // against the other with a hand's width of nothing between them —
        // two keys that look like they belong to different things.
        className="flex flex-wrap items-center justify-start gap-1"
      >
        {/* The one key this panel exists for.
            Ctrl is a sticky modifier and there is no letter row, so before
            this existed the combination that stops a runaway agent could not
            be typed from a phone at all — the modifier had nothing to apply
            to. Two taps for the most urgent thing in the product was the
            wrong trade even if it had worked. */}
        <Key label="^C" onPress={() => sendRaw(withCtrl('c'))} wide
          title={t('key.interrupt')} />
        <Key label="y" onPress={() => sendRaw('y\r')} wide />
        <Key label="n" onPress={() => sendRaw('n\r')} wide />
        <Key label="enter" onPress={key('enter')} title={t('key.enter')}>
          <CornerDownLeft size={13} />
        </Key>
        <Key label="esc" onPress={key('escape')} wide />
        <Key label="tab" onPress={key('tab')} wide />
        <Key label="ctrl" onPress={() => setCtrl((v) => !v)} active={ctrl} wide
          title={t('key.sticky')} />
        <Key label="alt" onPress={() => setAlt((v) => !v)} active={alt} wide
          title={t('key.sticky')} />
      </div>

      {/* Everything else. This one may scroll: losing sight of "~" costs far
          less than losing sight of Escape. */}
      <div
        data-testid="key-row-secondary"
        className="flex items-center gap-1 overflow-x-auto"
        style={{ touchAction: 'pan-x' }}
      >
        <Key label="up" onPress={key('up')} title={t('key.up')}><ArrowUp size={13} /></Key>
        <Key label="down" onPress={key('down')} title={t('key.down')}><ArrowDown size={13} /></Key>
        <Key label="left" onPress={key('left')} title={t('key.left')}><ArrowLeft size={13} /></Key>
        <Key label="right" onPress={key('right')} title={t('key.right')}><ArrowRight size={13} /></Key>
        <Divider />
        <Key label="home" onPress={key('home')} wide title={t('key.home')} />
        <Key label="end" onPress={key('end')} wide title={t('key.end')} />
        <Key label="pgup" onPress={key('pageUp')} wide title={t('key.pageUp')} />
        <Key label="pgdn" onPress={key('pageDown')} wide title={t('key.pageDown')} />
        <Divider />
        {['1', '2', '3'].map((d) => (
          <Key key={d} label={d} onPress={() => sendChar(d)} />
        ))}
        {['/', '-', '|', '~'].map((c) => (
          <Key key={c} label={c} onPress={() => sendChar(c)} />
        ))}
      </div>
    </div>
  )
}

function Divider() {
  return <span className="mx-0.5 h-5 w-px shrink-0" style={{ background: 'var(--vp-hairline)' }} />
}

function Key({
  label,
  onPress,
  active,
  wide,
  title,
  children,
}: {
  label: string
  onPress: () => void
  active?: boolean
  wide?: boolean
  title?: string
  children?: React.ReactNode
}) {
  return (
    <button
      type="button"
      data-testid={`key-${label}`}
      data-active={active ? 'true' : 'false'}
      title={title ?? label}
      // pointerdown, not click: a thumb that slides a pixel between press and
      // release still counts, and the key fires without the browser first
      // ruling out a double tap.
      onPointerDown={(e) => {
        e.preventDefault()
        onPress()
      }}
      className={`flex h-8 shrink-0 items-center justify-center rounded-vp border border-hairline text-[12px] transition-colors duration-150 ease-vp ${
        wide ? 'min-w-11 px-2' : 'w-8'
      } ${active ? 'text-accent-ink' : 'text-ink'}`}
      style={active ? { background: 'var(--vp-accent)', borderColor: 'var(--vp-accent)' } : undefined}
    >
      {children ?? label}
    </button>
  )
}
