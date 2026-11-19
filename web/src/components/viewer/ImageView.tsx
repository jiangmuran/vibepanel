import { useLayoutEffect, useRef, useState } from 'react'

import { t } from '../../i18n'
import { clampZoom, zoomAbout, type Zoom } from './zoom'

/**
 * A picture, fitted to the window when it opens and zoomed from there: the
 * zoom keys and the footer's buttons about the middle of the view, Ctrl with
 * the wheel about the pointer, a double click between fitted and 100%, a drag
 * to pan, two fingers to pinch. A transparent picture sits on a checkerboard, so its
 * transparency is something the reader can see rather than guess.
 *
 * Streamed from the inline route as an <img>, which runs nothing, an SVG
 * included -- the sandboxed frame it used to open in is for a project's pages.
 *
 * A picture larger than the window starts at its top-left, and this is held
 * the way the preview learned to hold it: `m-auto` on the image, never
 * centring on the box. Flex centring puts the overflow at negative offsets
 * that a scroll container cannot reach, and the top of the picture is gone
 * (「图片预览有bug 没法看到开头」).
 */
export function ImageView({
  url,
  alt,
  zoom,
  onZoom,
  onSize,
  scaleRef,
  zoomRef,
}: {
  url: string
  alt: string
  zoom: Zoom
  onZoom: (z: Zoom) => void
  /** The picture's own size, once it is known, for the footer. */
  onSize: (w: number, h: number) => void
  /** Where the zoom keys read the scale the picture is drawn at now. */
  scaleRef: React.RefObject<() => number>
  /**
   * Where the keys and the footer's buttons zoom: about the middle of the
   * view. Set while the picture is on screen and null otherwise.
   */
  zoomRef: React.RefObject<((z: Zoom) => void) | null>
}) {
  const boxRef = useRef<HTMLDivElement | null>(null)
  const imgRef = useRef<HTMLImageElement | null>(null)
  const [natural, setNatural] = useState<{ w: number; h: number } | null>(null)

  /** The scale the picture is drawn at now: fitted, or set. */
  const scaleNow = () => {
    const img = imgRef.current
    if (!img || !natural) return 1
    return zoom === 'fit' ? img.getBoundingClientRect().width / natural.w : zoom
  }
  useLayoutEffect(() => {
    scaleRef.current = scaleNow
  })
  // The point of the picture to keep where it is through a zoom: a point of
  // the picture in its own pixels, and where in the view it was. Set by the
  // gesture and applied on the next layout, whatever else changed, which is
  // why there is no dependency list. In the picture's pixels rather than as a
  // ratio of the scroll, because a fitted picture is centred by its margins
  // and a zoomed one is not, and a ratio of the scroll would put a double click's
  // 100% a margin's width away from the point clicked.
  const anchor = useRef<{ px: number; py: number; x: number; y: number } | null>(null)
  useLayoutEffect(() => {
    const a = anchor.current
    const box = boxRef.current
    const img = imgRef.current
    if (!a || !box || !img || !natural) return
    anchor.current = null
    const to = scaleNow()
    const r = img.getBoundingClientRect()
    const b = box.getBoundingClientRect()
    box.scrollLeft += r.left + a.px * to - (b.left + a.x)
    box.scrollTop += r.top + a.py * to - (b.top + a.y)
  })
  const zoomAt = (next: number, clientX: number, clientY: number) => {
    const box = boxRef.current
    const img = imgRef.current
    if (!box || !img) return
    const from = scaleNow()
    const r = img.getBoundingClientRect()
    const b = box.getBoundingClientRect()
    anchor.current = { px: (clientX - r.left) / from, py: (clientY - r.top) / from, x: clientX - b.left, y: clientY - b.top }
    onZoom(clampZoom(next))
  }
  useLayoutEffect(() => {
    zoomRef.current = (z: Zoom) => {
      const box = boxRef.current
      if (z === 'fit' || !box) return onZoom(z)
      const b = box.getBoundingClientRect()
      zoomAt(z, b.left + box.clientWidth / 2, b.top + box.clientHeight / 2)
    }
  })
  useLayoutEffect(
    () => () => {
      zoomRef.current = null
    },
    [zoomRef],
  )
  // Fitted to 100% about the point clicked, and anything else back to fitted:
  // what a double click does in every picture viewer.
  const onDoubleClick = (e: React.MouseEvent) => {
    if (zoom === 'fit') zoomAt(1, e.clientX, e.clientY)
    else onZoom('fit')
  }

  const onWheel = (e: React.WheelEvent) => {
    // Ctrl with the wheel is what a trackpad pinch arrives as, too. Without
    // Ctrl the wheel scrolls, as it does everywhere else.
    if (!e.ctrlKey && !e.metaKey) return
    e.preventDefault()
    zoomAt(zoomAbout(scaleNow(), e.deltaY < 0 ? 1 : -1, Math.min(4, Math.abs(e.deltaY) / 50)), e.clientX, e.clientY)
  }

  // Pointers: one drags, two pinch.
  const pointers = useRef(new Map<number, { x: number; y: number }>())
  const pinch = useRef<{ dist: number; scale: number } | null>(null)
  const onPointerDown = (e: React.PointerEvent) => {
    if (e.button !== 0) return
    boxRef.current?.setPointerCapture(e.pointerId)
    pointers.current.set(e.pointerId, { x: e.clientX, y: e.clientY })
    if (pointers.current.size === 2) {
      const [a, b] = [...pointers.current.values()]
      pinch.current = { dist: Math.hypot(a.x - b.x, a.y - b.y), scale: scaleNow() }
    }
  }
  const onPointerMove = (e: React.PointerEvent) => {
    const prev = pointers.current.get(e.pointerId)
    const box = boxRef.current
    if (!prev || !box) return
    pointers.current.set(e.pointerId, { x: e.clientX, y: e.clientY })
    if (pointers.current.size === 2 && pinch.current) {
      const [a, b] = [...pointers.current.values()]
      const dist = Math.hypot(a.x - b.x, a.y - b.y)
      zoomAt(pinch.current.scale * (dist / pinch.current.dist), (a.x + b.x) / 2, (a.y + b.y) / 2)
      return
    }
    box.scrollLeft -= e.clientX - prev.x
    box.scrollTop -= e.clientY - prev.y
  }
  const onPointerUp = (e: React.PointerEvent) => {
    pointers.current.delete(e.pointerId)
    if (pointers.current.size < 2) pinch.current = null
  }

  const style =
    zoom === 'fit' || !natural
      ? undefined
      : { width: `${natural.w * zoom}px`, height: `${natural.h * zoom}px`, maxWidth: 'none', maxHeight: 'none' }
  return (
    <div
      ref={boxRef}
      data-testid="viewer-image"
      data-zoom={zoom === 'fit' ? 'fit' : zoom.toFixed(3)}
      onWheel={onWheel}
      onDoubleClick={onDoubleClick}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={onPointerUp}
      onPointerCancel={onPointerUp}
      // touch-action none: the browser would otherwise take two fingers as its
      // own page zoom before the pinch here saw them.
      className="flex min-h-0 flex-1 cursor-grab touch-none overflow-auto bg-surface-2 p-3 select-none active:cursor-grabbing"
    >
      <img
        ref={imgRef}
        src={url}
        alt={alt}
        draggable={false}
        data-testid="preview-image"
        onLoad={(e) => {
          const { naturalWidth: w, naturalHeight: h } = e.currentTarget
          setNatural({ w, h })
          onSize(w, h)
        }}
        style={style}
        className="vp-checker m-auto max-h-full max-w-full object-contain"
      />
      <span className="sr-only">{t('viewer.imageHelp')}</span>
    </div>
  )
}
