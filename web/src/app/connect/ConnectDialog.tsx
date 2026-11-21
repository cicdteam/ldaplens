import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useEffect, useId, useRef, useState } from 'react'
import { api, isApiError, passwordHint, profileLabel } from '../../lib/api'
import type { Profile } from '../../lib/types'
import { ErrorBanner } from '../../ui/Banner'
import { Button } from '../../ui/Button'
import { Dialog } from '../../ui/Dialog'
import { Input } from '../../ui/Input'
import { Spinner } from '../../ui/Spinner'
import { CertChain } from './CertChain'

/**
 * Connects to profile; asks for a password when the server has none or the
 * stored one failed. An empty password makes the server use the stored one.
 * A userLogin profile always asks for a username and a password.
 */
export function ConnectDialog({
  profile,
  onClose,
  onConnected,
}: {
  profile: Profile | undefined
  onClose: () => void
  onConnected: (p: Profile) => void
}) {
  const qc = useQueryClient()
  const [password, setPassword] = useState('')
  const [username, setUsername] = useState('')
  const needsPassword = !!profile?.needsPassword
  // Kept across retries: a pending connect clears the error, not the field.
  // The first one wins: it comes from the stored password, later ones from typed ones.
  const [hint, setHint] = useState<string>()
  const hintId = useId()
  const connect = useMutation({
    // The password reaches the request through this closure, not the mutation's variables. The
    // mutation cache keeps both until gcTime after the dialog lets go: 0 drops them at once.
    gcTime: 0,
    mutationFn: (p: Profile) => api.connect(p.name, password, p.userLogin ? username : undefined),
    onSuccess: async (_info, p) => {
      setPassword('')
      qc.removeQueries({ queryKey: ['c', p.name] })
      await qc.invalidateQueries({ queryKey: ['profiles'] })
    },
    onError: (e) => setHint((h) => h ?? passwordHint(e)),
  })
  const { mutate, reset } = connect

  // Profiles with a stored password (or anonymous) connect at once.
  const started = useRef<string | null>(null)
  useEffect(() => {
    if (profile && !needsPassword && started.current !== profile.name) {
      started.current = profile.name
      // Navigation is per-call so a cancelled dialog never navigates later:
      // query-core runs the options' onSuccess even after unmount.
      mutate(profile, { onSuccess: () => onConnected(profile) })
    }
    if (!profile) {
      started.current = null
      reset()
    }
  }, [profile, needsPassword, mutate, reset, onConnected])

  if (!profile) return null
  const err = connect.error
  const certs = isApiError(err) && err.code === 'tls_untrusted' ? (err.body.certificates ?? []) : []

  return (
    <Dialog
      open
      onOpenChange={(open) => !open && onClose()}
      title={`Connect to ${profileLabel(profile)}`}
      description={profile.url}
      wide={certs.length > 0}
    >
      <form
        className="space-y-3"
        onSubmit={(e) => {
          e.preventDefault()
          // Enter in the password field submits even while a connect is in flight.
          if (connect.isPending) return
          connect.mutate(profile, { onSuccess: () => onConnected(profile) })
        }}
      >
        {(needsPassword || hint) && (
          <>
            {profile.userLogin ? (
              <label className="block space-y-1">
                <span className="text-xs text-muted">Username</span>
                <Input
                  autoFocus
                  autoComplete="username"
                  required
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  className="w-full"
                />
              </label>
            ) : (
              /* Password managers key saved passwords by this username, so
                 each bind DN, or each profile where the bind DN is not shown
                 (server mode), gets its own instead of one for every profile. */
              <input
                type="text"
                autoComplete="username"
                value={profile.bindDN || profile.name}
                readOnly
                tabIndex={-1}
                aria-hidden
                className="sr-only"
              />
            )}
            {!needsPassword && (
              <p id={hintId} className="text-xs text-warn">
                {hint}
              </p>
            )}
            <label className="block space-y-1">
              <span className="text-xs text-muted">
                {profile.bindDN ? (
                  <>
                    Password for <span className="font-mono">{profile.bindDN}</span>
                  </>
                ) : (
                  'Password'
                )}
              </span>
              {/* autoFocus also focuses the field when an error makes it appear. */}
              <Input
                type="password"
                autoFocus={!profile.userLogin}
                autoComplete="current-password"
                required={profile.userLogin}
                aria-describedby={needsPassword ? undefined : hintId}
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                className="w-full"
              />
            </label>
          </>
        )}
        {certs.length > 0 && isApiError(err) ? (
          <CertChain
            profile={profile}
            certs={certs}
            message={err.message}
            onTrusted={() => connect.mutate(profile, { onSuccess: () => onConnected(profile) })}
          />
        ) : (
          err && <ErrorBanner error={err} />
        )}
        <div className="flex items-center justify-end gap-2">
          {connect.isPending && <Spinner />}
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={connect.isPending}>
            Connect
          </Button>
        </div>
      </form>
    </Dialog>
  )
}
