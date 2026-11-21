import { useQuery, useQueryClient } from '@tanstack/react-query'
import { LogOut, PanelLeft, PanelRight } from 'lucide-react'
import type { ReactNode } from 'react'
import { Link, useNavigate } from 'react-router'
import { api, profileLabel } from '../lib/api'
import { batches } from '../lib/batchStore'
import { touchedAttributes } from '../lib/changes'
import { ColorDot, ProfileBadges } from '../ui/Badge'
import { Button } from '../ui/Button'
import { plural } from '../lib/status'
import { PrefsToggles } from '../ui/PrefsToggles'
import { useWorkspace } from './context'
import { Omnibox } from './search/Omnibox'

/**
 * Disconnects the workspace's profile and goes back to the connections screen.
 * Unapplied changes are discarded, after a confirm.
 */
export function useDisconnect(): () => Promise<void> {
  const ws = useWorkspace()
  const qc = useQueryClient()
  const navigate = useNavigate()
  return async () => {
    const batch = batches.get(ws.conn)
    if (batch.length) {
      const n = touchedAttributes(batch)
      const what = plural(n, 'unapplied change', 'unapplied changes')
      if (!window.confirm(`Discard ${what} and disconnect?`)) return
    }
    // Undo history goes too: after a reconnect, Mod+Z must not bring back discarded work.
    batches.reset(ws.conn, [])
    await api.disconnect(ws.conn).catch(() => {})
    await qc.invalidateQueries({ queryKey: ['profiles'] })
    // Leave before dropping the cache: a removed query that the still-mounted
    // workspace refetches gets 401 not_connected, which outside /c/ is ignored
    // instead of sending the user to reconnect.
    navigate('/')
    qc.removeQueries({ queryKey: ['c', ws.conn] })
  }
}

/** extra: slot for the command palette button. */
export function Header({ extra }: { extra?: ReactNode }) {
  const ws = useWorkspace()
  const disconnect = useDisconnect()
  // A fixed bind DN is the profile's, not a person's: only userLogin names who signed in.
  const profiles = useQuery({ queryKey: ['profiles'], queryFn: () => api.profiles() })
  const profile = profiles.data?.find((p) => p.name === ws.conn)
  const userLogin = profile?.userLogin
  return (
    <header className="flex h-10 shrink-0 items-center gap-2 border-b border-border bg-chrome px-2">
      <Link
        to="/"
        title="All connections"
        className="flex min-w-0 max-w-xs shrink-0 items-center gap-1.5 rounded px-1 font-medium hover:bg-hover"
      >
        <ColorDot color={ws.info.color} />
        <span className="truncate">{profileLabel({ name: ws.conn, label: profile?.label })}</span>
      </Link>
      <span className="flex shrink-0 gap-1 empty:hidden">
        <ProfileBadges profile={ws.info} />
      </span>
      <Omnibox />
      <div className="flex shrink-0 items-center gap-1">
        {userLogin && ws.info.bindDN && (
          <span className="max-w-md truncate px-1 text-xs text-muted" title={ws.info.bindDN}>
            Signed in as <span className="font-mono">{ws.info.bindDN}</span>
          </span>
        )}
        {extra}
        <Button
          size="icon"
          variant="ghost"
          aria-label="Toggle tree"
          title="Show or hide the directory tree"
          onClick={ws.toggleTree}
        >
          <PanelLeft className="h-4 w-4" />
        </Button>
        <Button
          size="icon"
          variant="ghost"
          aria-label="Toggle inspector"
          title="Show or hide the entry panel"
          onClick={ws.toggleInspector}
        >
          <PanelRight className="h-4 w-4" />
        </Button>
        <PrefsToggles />
        <Button
          size="icon"
          variant="ghost"
          aria-label="Disconnect"
          title="Disconnect and go back to the connections"
          onClick={() => void disconnect()}
        >
          <LogOut className="h-4 w-4" />
        </Button>
      </div>
    </header>
  )
}
