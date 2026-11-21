import { Info } from 'lucide-react'
import { textOn } from '../lib/color'
import { cn } from '../ui/cn'
import { useServer } from './queries'

/**
 * The operator's --notice, a strip above every page. Plain text, never markup; a newline in it
 * breaks the line, and a long one wraps rather than being cut off. --notice-color fills it, with
 * black or white text, whichever reads better.
 */
export function Notice() {
  const server = useServer().data
  const notice = server?.notice
  if (!notice) return null
  const bg = server.noticeColor
  return (
    <div
      role="note"
      aria-label="Notice"
      style={bg ? { backgroundColor: bg, color: textOn(bg) } : undefined}
      className={cn(
        'flex shrink-0 items-start gap-2 border-b px-3 py-[5px] text-[12.5px] leading-[1.4]',
        bg ? 'border-black/20 font-medium' : 'border-accent/20 bg-selected',
      )}
    >
      <Info className={cn('mt-0.5 h-3.5 w-3.5 shrink-0', !bg && 'text-accent-text')} />
      <p className="min-w-0 wrap-anywhere whitespace-pre-line">{notice}</p>
    </div>
  )
}
