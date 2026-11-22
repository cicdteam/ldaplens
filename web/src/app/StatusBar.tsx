import { useSyncExternalStore } from 'react'
import { isEditableTarget, keyLabel } from '../lib/shortcuts'
import { connectionStatus, type StatusTone } from '../lib/status'
import { Kbd } from '../ui/Kbd'
import { useWorkspace } from './context'
import { useServer } from './queries'

const tones: Record<StatusTone, string> = {
  ok: 'text-ok',
  warn: 'text-warn',
  danger: 'text-danger',
}

type Pane = 'tree' | 'list' | 'card' | 'value' | 'none'

/** The keys that act in each pane, as keyLabel combos. */
const hints: Record<Pane, [string, string][]> = {
  tree: [
    ['/', 'search'],
    ['↑↓', 'move'],
    ['→', 'expand'],
    ['Mod+K', 'commands'],
  ],
  list: [
    ['/', 'search'],
    ['↑↓', 'next entry'],
    ['Esc', 'close list'],
    ['Mod+K', 'commands'],
  ],
  card: [
    ['E', 'edit'],
    ['Mod+Z', 'undo'],
    ['Mod+S', 'review'],
    ['Mod+K', 'commands'],
  ],
  value: [
    ['Enter', 'stage value'],
    ['Mod+S', 'review'],
    ['Mod+K', 'commands'],
  ],
  none: [
    ['/', 'search'],
    ['Mod+K', 'commands'],
  ],
}

const onFocusChange = (fn: () => void) => {
  document.addEventListener('focusin', fn)
  document.addEventListener('focusout', fn)
  return () => {
    document.removeEventListener('focusin', fn)
    document.removeEventListener('focusout', fn)
  }
}

/** The pane holding focus, by its panel id (Workspace's tree, list and inspector). */
function focusedPane(readOnly: boolean): Pane {
  const el = document.activeElement
  const panel = el?.closest('[data-panel]')?.id
  if (panel === 'tree' || panel === 'list') return panel
  if (panel !== 'inspector' || readOnly) return 'none'
  return el?.closest('[data-attr]') && isEditableTarget(el as HTMLElement) ? 'value' : 'card'
}

/**
 * The connection at a glance (address, transport, server type), the focused pane's keys and
 * ldaplens's version.
 */
export function StatusBar() {
  const ws = useWorkspace()
  const s = connectionStatus(ws.info)
  const pane = useSyncExternalStore(onFocusChange, () => focusedPane(!!ws.info.readOnly))
  const version = useServer().data?.version
  return (
    <footer
      aria-label="Connection status"
      className="flex h-6 shrink-0 items-center gap-3 overflow-hidden border-t border-border bg-chrome px-2 font-mono text-[11px] whitespace-nowrap text-muted [[data-density='compact']_&]:h-[22px]"
    >
      <span className="min-w-0 truncate">{s.address}</span>
      <span className={tones[s.transport.tone]}>{s.transport.text}</span>
      <span>{s.server}</span>
      <span className="ml-auto flex gap-3.5 font-sans">
        {hints[pane].map(([key, what]) => (
          <span key={key}>
            <Kbd>{keyLabel(key)}</Kbd> {what}
          </span>
        ))}
      </span>
      {version && <span>ldaplens {version}</span>}
    </footer>
  )
}
