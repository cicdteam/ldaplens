import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { api, profileLabel, toProfileInput } from '../../lib/api'
import type { Profile, ProfileInput } from '../../lib/types'
import { ErrorBanner } from '../../ui/Banner'
import { Button } from '../../ui/Button'
import { Dialog } from '../../ui/Dialog'
import { Input, inputLook } from '../../ui/Input'

const empty: ProfileInput = {
  name: '',
  label: '',
  url: 'ldaps://',
  startTLS: false,
  baseDN: '',
  bindDN: '',
  caPEM: '',
  insecureSkipVerify: false,
  color: '',
  readOnly: false,
}

/** Drops empty optional strings so the server applies its defaults. */
function clean(p: ProfileInput): ProfileInput {
  const out: ProfileInput = { ...p }
  for (const k of ['label', 'baseDN', 'bindDN', 'caPEM', 'color'] as const)
    if (!out[k]) delete out[k]
  return out
}

export function ProfileForm({
  profile,
  onClose,
}: {
  profile: Profile | null
  onClose: () => void
}) {
  const qc = useQueryClient()
  const [form, setForm] = useState<ProfileInput>(profile ? toProfileInput(profile) : empty)
  const set = (p: Partial<ProfileInput>) => setForm((f) => ({ ...f, ...p }))
  const ldaps = form.url.toLowerCase().startsWith('ldaps://')
  const save = useMutation({
    // The StartTLS box shows unchecked for ldaps://; send what it shows.
    mutationFn: () => api.saveProfile(clean({ ...form, startTLS: form.startTLS && !ldaps })),
    // A connected workspace reads its badges from the info query.
    onSuccess: (p) =>
      Promise.all([
        qc.invalidateQueries({ queryKey: ['profiles'] }),
        qc.invalidateQueries({ queryKey: ['c', p.name, 'info'] }),
      ]),
  })
  const field = 'block space-y-1'
  const label = 'text-xs text-muted'
  return (
    <Dialog
      open
      onOpenChange={(o) => !o && onClose()}
      title={profile ? `Edit ${profileLabel(profile)}` : 'New connection'}
      wide
    >
      <form
        className="grid grid-cols-2 gap-3"
        onSubmit={(e) => {
          e.preventDefault()
          // Per-call: runs only while the form is still open.
          save.mutate(undefined, { onSuccess: () => onClose() })
        }}
      >
        <label className={field}>
          <span className={label}>Name</span>
          <Input
            required
            autoFocus={!profile}
            readOnly={!!profile}
            value={form.name}
            onChange={(e) => set({ name: e.target.value })}
            className="w-full"
          />
        </label>
        <label className={field}>
          <span className={label}>URL</span>
          <Input
            required
            mono
            value={form.url}
            onChange={(e) => set({ url: e.target.value })}
            placeholder="ldaps://dc1.example.org:636"
            className="w-full"
          />
        </label>
        <label className={field}>
          <span className={label}>Base DN (empty: server default)</span>
          <Input
            mono
            value={form.baseDN ?? ''}
            onChange={(e) => set({ baseDN: e.target.value })}
            className="w-full"
          />
        </label>
        <label className={field}>
          <span className={label}>Bind DN (empty: anonymous)</span>
          <Input
            mono
            value={form.bindDN ?? ''}
            onChange={(e) => set({ bindDN: e.target.value })}
            className="w-full"
          />
        </label>
        <label className={field}>
          <span className={label}>Label (optional, shown in the UI)</span>
          <Input
            value={form.label ?? ''}
            maxLength={80}
            onChange={(e) => set({ label: e.target.value })}
            className="w-full"
          />
        </label>
        <label className={field}>
          <span className={label}>Color</span>
          <span className="flex items-center gap-2">
            <input
              type="color"
              value={form.color || '#888888'}
              onChange={(e) => set({ color: e.target.value })}
              className={`h-8 w-12 ${inputLook}`}
            />
            {form.color && (
              <Button size="sm" variant="ghost" onClick={() => set({ color: '' })}>
                Clear
              </Button>
            )}
          </span>
        </label>
        <div className="col-span-2 flex flex-wrap gap-x-6 gap-y-2">
          <label className="flex items-center gap-2">
            <input
              type="checkbox"
              checked={form.startTLS && !ldaps}
              disabled={ldaps}
              onChange={(e) => set({ startTLS: e.target.checked })}
            />{' '}
            StartTLS
          </label>
          <label className="flex items-center gap-2">
            <input
              type="checkbox"
              checked={form.readOnly}
              onChange={(e) => set({ readOnly: e.target.checked })}
            />{' '}
            Read-only
          </label>
          <label className="flex items-center gap-2 text-danger">
            <input
              type="checkbox"
              checked={form.insecureSkipVerify}
              onChange={(e) => set({ insecureSkipVerify: e.target.checked })}
            />{' '}
            Skip TLS verification (insecure)
          </label>
        </div>
        <label className={`${field} col-span-2`}>
          <span className={label}>CA certificate (PEM, optional)</span>
          <textarea
            rows={4}
            value={form.caPEM ?? ''}
            onChange={(e) => set({ caPEM: e.target.value })}
            className={`w-full p-2 font-mono text-[11px] ${inputLook}`}
            placeholder="-----BEGIN CERTIFICATE-----"
          />
        </label>
        <p className="col-span-2 text-xs text-muted">
          Passwords are never saved: ldaplens asks for one when you connect.
        </p>
        {save.isError && (
          <div className="col-span-2">
            <ErrorBanner error={save.error} />
          </div>
        )}
        <div className="col-span-2 flex justify-end gap-2">
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={save.isPending}>
            Save
          </Button>
        </div>
      </form>
    </Dialog>
  )
}
