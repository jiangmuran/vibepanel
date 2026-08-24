import { useState } from 'react'
import { ArrowDown, ArrowLeft, ArrowRight, ArrowUp, CornerDownLeft } from 'lucide-react'

import { KEY_SEQUENCES, withAlt, withCtrl } from './keys'
import type { KeyName } from './keys'

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
  // Sticky modifiers: tap, then tap the key it applies to. Holding two places
  // at once is not a gesture a thumb can make.
  const [ctrl, setCtrl] = useState(false)
  const [alt, setAlt] = useState(false)

  const sendRaw = (s: string) => {
    onSend(s)
    setCtrl(false)
    setAlt(false)
  }

  const sendChar = (ch: string) => {
    let out = ch
    if (ctrl) out = withCtrl(out)
    if (alt) out = withAlt(out)
    sendRaw(out)
  }

  const key = (name: KeyName) => () => sendRaw(KEY_SEQUENCES[name])

  return (
    <div
      data-testid="key-bar"
      className="flex shrink-0 flex-col gap-1 border-t border-hairline px-1 py-1.5 vp-blur"
    >
      {/* Two rows, because eighteen keys do not fit across a phone and a
          single scrolling row hides whichever ones are not in view — which,
          the first time this was tried, meant y, n and Escape. This row holds
          what an agent conversation actually needs and never scrolls. */}
      <div data-testid="key-row-primary" className="flex items-center justify-between gap-1">
        <Key label="y" onPress={() => sendRaw('y\r')} wide />
        <Key label="n" onPress={() => sendRaw('n\r')} wide />
        <Key label="enter" onPress={key('enter')} title="Enter">
          <CornerDownLeft size={13} />
        </Key>
        <Key label="esc" onPress={key('escape')} wide />
        <Key label="tab" onPress={key('tab')} wide />
        <Key label="ctrl" onPress={() => setCtrl((v) => !v)} active={ctrl} wide
          title="Applies to the next key" />
        <Key label="alt" onPress={() => setAlt((v) => !v)} active={alt} wide
          title="Applies to the next key" />
      </div>

      {/* Everything else. This one may scroll: losing sight of "~" costs far
          less than losing sight of Escape. */}
      <div
        data-testid="key-row-secondary"
        className="flex items-center gap-1 overflow-x-auto"
        style={{ touchAction: 'pan-x' }}
      >
        <Key label="up" onPress={key('up')} title="Up"><ArrowUp size={13} /></Key>
        <Key label="down" onPress={key('down')} title="Down"><ArrowDown size={13} /></Key>
        <Key label="left" onPress={key('left')} title="Left"><ArrowLeft size={13} /></Key>
        <Key label="right" onPress={key('right')} title="Right"><ArrowRight size={13} /></Key>
        <Divider />
        <Key label="home" onPress={key('home')} wide title="Home" />
        <Key label="end" onPress={key('end')} wide title="End" />
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
