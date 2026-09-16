import { useEffect, useState } from 'react'
import { toDataURL } from 'qrcode'

/**
 * A QR code for a phone to scan, drawn from a URL the IM handed over.
 *
 * The one dependency this page adds. 微信's sign-in is "scan this", and the
 * thing to scan is a URL the panel is given, not an image: something has to
 * turn it into squares, and a QR encoder is a few hundred lines of
 * Reed–Solomon that would be wrong in a way nobody could see until a phone
 * refused it. The library does nothing else and reaches nothing else.
 */
export function QR({ value, size = 220 }: { value: string; size?: number }) {
  const [src, setSrc] = useState<string | null>(null)
  useEffect(() => {
    let cancelled = false
    toDataURL(value, { width: size, margin: 1, errorCorrectionLevel: 'M' }).then(
      (url) => {
        if (!cancelled) setSrc(url)
      },
      () => {
        if (!cancelled) setSrc(null)
      },
    )
    return () => {
      cancelled = true
    }
  }, [value, size])
  if (!src) return <div style={{ width: size, height: size }} className="rounded-vp bg-surface-2" />
  return <img src={src} width={size} height={size} alt="" className="rounded-vp bg-white" />
}
