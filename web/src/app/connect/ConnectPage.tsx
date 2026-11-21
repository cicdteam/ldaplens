import { useQuery } from '@tanstack/react-query'
import { Pencil, Plus, Server } from 'lucide-react'
import { useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router'
import { api, profileLabel } from '../../lib/api'
import { safeNext } from '../../lib/nav'
import type { Profile } from '../../lib/types'
import { Badge, ColorDot, ProfileBadges } from '../../ui/Badge'
import { ErrorBanner } from '../../ui/Banner'
import { Button } from '../../ui/Button'
import { PrefsToggles } from '../../ui/PrefsToggles'
import { Spinner } from '../../ui/Spinner'
import { useServer } from '../queries'
import { ConnectDialog } from './ConnectDialog'
import { ProfileForm } from './ProfileForm'

export function ConnectPage() {
  const [params, setParams] = useSearchParams()
  const navigate = useNavigate()
  const profiles = useQuery({ queryKey: ['profiles'], queryFn: () => api.profiles() })
  // Server mode saves no profiles: no new ones, no edits, no yaml marks.
  const mode = useServer()
  const local = mode.data?.mode === 'local'
  const [editing, setEditing] = useState<Profile | 'new' | null>(null)
  const target = params.get('connect')
  const next = safeNext(params.get('next'))

  const open = (p: Profile) => {
    if (p.connected) navigate(`/c/${encodeURIComponent(p.name)}`)
    else setParams({ connect: p.name, ...(next ? { next } : {}) })
  }
  const targetProfile = profiles.data?.find((p) => p.name === target)

  return (
    // The page itself never scrolls (index.css): this view scrolls on its own.
    <div className="h-full overflow-auto">
      <main className="mx-auto max-w-2xl px-4 py-8">
        <header className="mb-6 flex items-center gap-2">
          <Server className="h-5 w-5 text-accent-text" />
          <h1 className="text-base font-semibold">ldaplens</h1>
          <span className="text-muted">Connections</span>
          <div className="ml-auto flex items-center gap-1">
            <PrefsToggles />
            {local && (
              <Button size="sm" onClick={() => setEditing('new')}>
                <Plus className="h-3.5 w-3.5" /> New connection
              </Button>
            )}
          </div>
        </header>
        {target && next && (
          <p className="mb-3 text-muted">
            The session for {target} ended. Connect again to continue.
          </p>
        )}
        {profiles.isPending && <Spinner />}
        {profiles.isError && <ErrorBanner error={profiles.error} />}
        {profiles.data?.length === 0 && (
          <p className="text-muted">
            No connections yet.
            {local && (
              <>
                {' '}
                Create one, or mount a <span className="font-mono">connections.yaml</span>.
              </>
            )}
          </p>
        )}
        <ul className="divide-y divide-border rounded-lg border border-border">
          {profiles.data?.map((p) => (
            <li key={p.name} className="flex items-center gap-3 px-3 py-2 hover:bg-hover">
              <button
                type="button"
                className="flex min-w-0 flex-1 items-center gap-3 text-left"
                onClick={() => open(p)}
              >
                <ColorDot color={p.color} />
                <span className="min-w-0 flex-1">
                  <span className="flex items-center gap-2 font-medium">
                    {profileLabel(p)}
                    {p.connected && <Badge tone="ok">CONNECTED</Badge>}
                  </span>
                  <span className="block truncate font-mono text-xs text-muted">
                    {[p.label && p.name, p.url].filter(Boolean).join(' · ')}
                  </span>
                </span>
                <span className="flex gap-1 empty:hidden">
                  <ProfileBadges profile={p} />
                </span>
              </button>
              {p.editable ? (
                <Button
                  size="icon"
                  variant="ghost"
                  aria-label={`Edit ${profileLabel(p)}`}
                  title="Edit"
                  onClick={() => setEditing(p)}
                >
                  <Pencil className="h-3.5 w-3.5" />
                </Button>
              ) : (
                local && (
                  <span
                    className="w-7 text-center text-[10px] text-muted"
                    title="Defined in connections.yaml"
                  >
                    yaml
                  </span>
                )
              )}
            </li>
          ))}
        </ul>
        {/* Keyed per target: a password typed for one profile never reaches another. */}
        <ConnectDialog
          key={target ?? ''}
          profile={targetProfile}
          onClose={() => setParams({})}
          onConnected={(p) =>
            navigate(next ?? `/c/${encodeURIComponent(p.name)}`, { replace: true })
          }
        />
        {editing && (
          <ProfileForm
            profile={editing === 'new' ? null : editing}
            onClose={() => setEditing(null)}
          />
        )}
      </main>
    </div>
  )
}
