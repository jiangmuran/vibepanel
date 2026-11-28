import { Cable } from 'lucide-react'

export function ChannelIcon({ kind, size = 24 }: { kind: string; size?: number }) {
  if (!['telegram', 'feishu', 'weixin'].includes(kind)) return <Cable size={size} aria-hidden="true" />
  return (
    <svg width={size} height={size} viewBox="0 0 32 32" fill="currentColor" aria-hidden="true" className="shrink-0">
      {kind === 'telegram' && <path d="m27.7 5.2-4.1 21c-.3 1.5-1.2 1.8-2.4 1.1l-6.3-4.7-3 2.9c-.4.4-.7.7-1.4.7l.5-6.4L23 8.6c.5-.5-.1-.8-.7-.4L7.5 17.5l-6.2-1.9c-1.4-.4-1.4-1.4.3-2.1l24.3-9.4c1.1-.4 2.1.3 1.8 1.1Z" />}
      {kind === 'feishu' && <><path d="M3 8.1c5.1-.3 9.1 1.5 12.7 5.7l3.7 4.4c-4.2 1-7.7-.2-10.8-3.7L3 8.1Z" /><path d="m9.4 4 8.2 2.6c2.3.7 4.1 2 5.3 3.9l6.5 9.7-7.1-2.4c-2.8-.9-4.7-2.6-6.4-5L9.4 4Z" opacity=".8" /><path d="m4 23.9 9.7-7.1c2.3 2.7 5.3 4 9.1 4l-8.1 6c-3.7 2.6-7.3.9-10.7-2.9Z" /></>}
      {kind === 'weixin' && <><path d="M13 4C6.9 4 2 7.9 2 12.8c0 2.7 1.5 5.1 4 6.7l-1.2 3.8 4.2-2.2c.8.2 1.7.4 2.5.5a9.2 9.2 0 0 1-.5-3c0-5.1 4.6-9.2 10.2-9.2h1C20.7 6.2 17.1 4 13 4ZM9 8.5a1.4 1.4 0 1 1 0 2.8 1.4 1.4 0 0 1 0-2.8Zm7.5 0a1.4 1.4 0 1 1 0 2.8 1.4 1.4 0 0 1 0-2.8Z" /><path d="M30 18.6c0-4-4-7.3-8.8-7.3s-8.7 3.3-8.7 7.3 3.9 7.3 8.7 7.3c.9 0 1.7-.1 2.5-.3L27 27.5l-.8-3.2c2.3-1.3 3.8-3.4 3.8-5.7Zm-12-1.2a1.2 1.2 0 1 1 0-2.4 1.2 1.2 0 0 1 0 2.4Zm6.3 0a1.2 1.2 0 1 1 0-2.4 1.2 1.2 0 0 1 0 2.4Z" /></>}
    </svg>
  )
}
