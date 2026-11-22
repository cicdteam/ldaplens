import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState, type ReactNode } from 'react'
import { api, profileLabel, toProfileInput } from '../../lib/api'
import {
  buildURL,
  defaultName,
  defaultTemplate,
  parseServer,
  splitURL,
  templateOK,
  type DirKind,
  type Security,
} from '../../lib/connWizard'
import type { Profile, ProfileInput } from '../../lib/types'
import { ErrorBanner } from '../../ui/Banner'
import { Button } from '../../ui/Button'
import { cn } from '../../ui/cn'
import { Dialog } from '../../ui/Dialog'
import { Input, inputLook } from '../../ui/Input'

type Auth = 'user' | 'dn' | 'anon'
const STEPS = ['Server', 'Sign in', 'Name'] as const

/** What the wizard edits; null template and name follow their suggestions until typed. */
interface State {
  host: string
  port: string
  security: Security
  baseDN: string
  caPEM: string
  insecureSkipVerify: boolean
  auth: Auth
  dir: DirKind
  template: string | null
  sample: string
  bindDN: string
  name: string | null
  label: string
  color: string
  readOnly: boolean
}

function initial(p: Profile | null): State {
  const i = p ? toProfileInput(p) : null
  const t = i?.bindDNTemplate ?? ''
  return {
    ...splitURL(i?.url ?? '', !!i?.startTLS),
    baseDN: i?.baseDN ?? '',
    caPEM: i?.caPEM ?? '',
    insecureSkipVerify: !!i?.insecureSkipVerify,
    auth: !i || t ? 'user' : i.bindDN ? 'dn' : 'anon',
    dir: t && !t.includes('=') ? 'ad' : 'openldap',
    template: t || null,
    sample: 'jdoe',
    bindDN: i?.bindDN ?? '',
    name: i?.name ?? null,
    label: i?.label ?? '',
    color: i?.color ?? '',
    readOnly: !!i?.readOnly,
  }
}

/** The profile the wizard saves; empty optional strings left out for the server's defaults. */
function toInput(s: State, template: string, name: string): ProfileInput {
  const out: ProfileInput = {
    name,
    ...buildURL(s.host, s.port, s.security),
    insecureSkipVerify: s.security !== 'none' && s.insecureSkipVerify,
    readOnly: s.readOnly,
  }
  if (s.label.trim()) out.label = s.label
  if (s.baseDN.trim()) out.baseDN = s.baseDN.trim()
  if (s.security !== 'none' && s.caPEM.trim()) out.caPEM = s.caPEM
  if (s.color) out.color = s.color
  if (s.auth === 'user') out.bindDNTemplate = template
  if (s.auth === 'dn') out.bindDN = s.bindDN.trim()
  return out
}

/**
 * New connection and Edit: three steps, one question each (where is the server, how do you
 * sign in, what to call it). Edit reaches every step from the top and saves from any of them.
 */
export function ProfileForm({
  profile,
  onClose,
}: {
  profile: Profile | null
  onClose: () => void
}) {
  const qc = useQueryClient()
  const [s, setS] = useState(() => initial(profile))
  const [step, setStep] = useState(0)
  const set = (p: Partial<State>) => setS((v) => ({ ...v, ...p }))
  const template = s.template ?? defaultTemplate(s.dir, s.baseDN)
  const name = s.name ?? defaultName(s.host)
  const valid = [
    !!s.host.trim() && /^\d{1,5}$/.test(s.port.trim()),
    s.auth === 'user' ? templateOK(template) : s.auth === 'dn' ? !!s.bindDN.trim() : true,
    !!name,
  ]
  const reachable = (i: number) => !!profile || valid.slice(0, i).every(Boolean)
  const save = useMutation({
    mutationFn: () => api.saveProfile(toInput(s, template, name)),
    // A connected workspace reads its badges from the info query.
    onSuccess: (p) =>
      Promise.all([
        qc.invalidateQueries({ queryKey: ['profiles'] }),
        qc.invalidateQueries({ queryKey: ['c', p.name, 'info'] }),
      ]),
  })
  const last = step === STEPS.length - 1
  const canSave = valid.every(Boolean)
  const { url, startTLS } = buildURL(s.host || 'host', s.port, s.security)

  return (
    <Dialog
      open
      onOpenChange={(o) => !o && onClose()}
      title={profile ? `Edit ${profileLabel(profile)}` : 'New connection'}
      wide
    >
      <form
        className="grid gap-4"
        onSubmit={(e) => {
          e.preventDefault()
          if (!last && !profile) {
            if (valid[step]) setStep(step + 1)
            return
          }
          // Per-call: runs only while the form is still open.
          if (canSave) save.mutate(undefined, { onSuccess: () => onClose() })
        }}
      >
        <nav aria-label="Steps" className="-mt-1 flex flex-wrap gap-1 border-b border-border pb-3">
          {STEPS.map((label, i) => (
            <button
              key={label}
              type="button"
              disabled={!reachable(i)}
              aria-current={i === step ? 'step' : undefined}
              onClick={() => setStep(i)}
              className={cn(
                'flex items-center gap-1.5 rounded-md px-2 py-1 text-muted disabled:opacity-50',
                i === step && 'bg-selected text-fg',
              )}
            >
              <span
                aria-hidden
                className={cn(
                  'grid h-[18px] w-[18px] place-items-center rounded-full border border-border-strong text-[11px]',
                  i === step && 'border-accent bg-accent text-accent-fg',
                )}
              >
                {i + 1}
              </span>
              {label}
            </button>
          ))}
        </nav>

        <div className="grid min-h-80 content-start gap-3">
          {step === 0 && (
            <>
              <div className="grid grid-cols-[1fr_6rem] gap-3">
                <Field label="Server">
                  <Input
                    required
                    mono
                    data-autofocus
                    value={s.host}
                    onChange={(e) => set(parseServer(e.target.value))}
                    placeholder="ldap.example.org"
                    autoComplete="off"
                    className="w-full"
                  />
                </Field>
                <Field label="Port">
                  <Input
                    required
                    mono
                    inputMode="numeric"
                    value={s.port}
                    onChange={(e) => set({ port: e.target.value })}
                    className="w-full"
                  />
                </Field>
              </div>
              <p className="-mt-2 text-xs text-muted">
                A URL such as <code className="font-mono">ldaps://dc1.corp.example:636</code> pasted
                here fills the port and security too.
              </p>
              <div role="radiogroup" aria-label="Security" className="grid gap-1.5">
                <span className="text-xs text-muted">Security</span>
                <span className="inline-flex w-fit overflow-hidden rounded-md border border-border-strong">
                  {(
                    [
                      ['ldaps', 'LDAPS', '636'],
                      ['starttls', 'StartTLS', '389'],
                      ['none', 'None', '389'],
                    ] as const
                  ).map(([v, label, port]) => (
                    <button
                      key={v}
                      type="button"
                      role="radio"
                      aria-checked={s.security === v}
                      onClick={() =>
                        set({
                          security: v,
                          // A default port follows the security; a typed one stays.
                          port: ['636', '389'].includes(s.port) ? port : s.port,
                        })
                      }
                      className={cn(
                        'border-r border-border px-3 py-1.5 last:border-r-0',
                        s.security === v ? 'bg-selected font-medium text-accent-text' : 'bg-bg',
                      )}
                    >
                      {label} <span className="text-xs text-muted">{port}</span>
                    </button>
                  ))}
                </span>
                {s.security === 'none' && (
                  <p className="text-xs text-warn">
                    Without TLS, your password crosses the network in clear text.
                  </p>
                )}
              </div>
              <div
                aria-label="URL"
                className="rounded-md bg-panel px-2 py-1.5 font-mono text-xs break-all"
              >
                {url}
                {startTLS && ' + StartTLS'}
              </div>
              <Field label="Base DN (optional: empty reads it from the server)">
                <Input
                  mono
                  value={s.baseDN}
                  onChange={(e) => set({ baseDN: e.target.value })}
                  placeholder="dc=example,dc=org"
                  className="w-full"
                />
              </Field>
              {s.security !== 'none' && (
                <details open={!!s.caPEM || s.insecureSkipVerify} className="grid gap-3">
                  <summary className="cursor-pointer text-xs text-muted">
                    TLS certificate options
                  </summary>
                  <div className="mt-3 grid gap-3">
                    <Field label="CA certificate (PEM), when the server's certificate is from a private CA">
                      <textarea
                        rows={3}
                        value={s.caPEM}
                        onChange={(e) => set({ caPEM: e.target.value })}
                        className={`w-full p-2 font-mono text-[11px] ${inputLook}`}
                        placeholder="-----BEGIN CERTIFICATE-----"
                      />
                    </Field>
                    <label className="flex items-center gap-2 text-danger">
                      <input
                        type="checkbox"
                        checked={s.insecureSkipVerify}
                        onChange={(e) => set({ insecureSkipVerify: e.target.checked })}
                      />
                      Skip certificate verification (insecure)
                    </label>
                  </div>
                </details>
              )}
            </>
          )}

          {step === 1 && (
            <fieldset className="grid gap-2">
              <legend className="mb-2 text-xs text-muted">
                How do you sign in to this directory?
              </legend>
              <Choice
                checked={s.auth === 'user'}
                onCheck={() => set({ auth: 'user' })}
                title="With my username"
                text="Connect asks for a username and a password, like any login form."
              >
                <span className="inline-flex w-fit overflow-hidden rounded-md border border-border-strong">
                  {(
                    [
                      ['openldap', 'OpenLDAP and others'],
                      ['ad', 'Active Directory'],
                    ] as const
                  ).map(([v, label]) => (
                    <button
                      key={v}
                      type="button"
                      aria-pressed={s.dir === v}
                      onClick={() => set({ dir: v, template: null })}
                      className={cn(
                        'border-r border-border px-3 py-1 text-xs last:border-r-0',
                        s.dir === v ? 'bg-selected font-medium text-accent-text' : 'bg-bg',
                      )}
                    >
                      {label}
                    </button>
                  ))}
                </span>
                <Field
                  label={
                    s.dir === 'ad'
                      ? 'Sign-in name, with {user} where the username goes (or CORP\\{user})'
                      : "Your entry's DN, with {user} where the username goes"
                  }
                >
                  <Input
                    mono
                    value={template}
                    aria-invalid={!templateOK(template)}
                    onChange={(e) => set({ template: e.target.value })}
                    className={cn('w-full', !templateOK(template) && 'border-danger')}
                  />
                </Field>
                {templateOK(template) ? (
                  <div className="flex flex-wrap items-center gap-2 text-xs">
                    <label className="flex items-center gap-2">
                      <span className="text-muted">Try it:</span>
                      <Input
                        mono
                        aria-label="Sample username"
                        value={s.sample}
                        onChange={(e) => set({ sample: e.target.value })}
                        className="h-7 w-32"
                      />
                    </label>
                    <span>signs in as</span>
                    <code className="min-w-0 rounded bg-panel px-1.5 py-0.5 font-mono break-all">
                      {template.replace('{user}', s.sample || 'jdoe')}
                    </code>
                  </div>
                ) : (
                  <p className="text-xs text-danger">
                    Put {'{user}'} in exactly one place, where your username goes.
                  </p>
                )}
              </Choice>
              <Choice
                checked={s.auth === 'dn'}
                onCheck={() => set({ auth: 'dn' })}
                title="As one fixed DN"
                text="Always the same account, such as a service account; connect asks only for its password."
              >
                <Field label="Bind DN">
                  <Input
                    mono
                    required
                    value={s.bindDN}
                    onChange={(e) => set({ bindDN: e.target.value })}
                    placeholder={`cn=reader,ou=services,${s.baseDN || 'dc=example,dc=org'}`}
                    className="w-full"
                  />
                </Field>
              </Choice>
              <Choice
                checked={s.auth === 'anon'}
                onCheck={() => set({ auth: 'anon' })}
                title="Anonymously"
                text="No sign-in. You see what the server shows anyone, often little or nothing."
              />
            </fieldset>
          )}

          {step === 2 && (
            <>
              <div className="grid grid-cols-2 gap-3">
                <Field label="Name (in links and logs)">
                  <Input
                    required
                    mono
                    data-autofocus
                    readOnly={!!profile}
                    value={name}
                    onChange={(e) => set({ name: e.target.value })}
                    className="w-full"
                  />
                </Field>
                <Field label="Label (optional, shown in the UI)">
                  <Input
                    value={s.label}
                    maxLength={80}
                    placeholder={name}
                    onChange={(e) => set({ label: e.target.value })}
                    className="w-full"
                  />
                </Field>
              </div>
              <Field label="Color">
                <span className="flex items-center gap-2">
                  <input
                    type="color"
                    value={s.color || '#888888'}
                    onChange={(e) => set({ color: e.target.value })}
                    className={`h-8 w-12 ${inputLook}`}
                  />
                  {s.color && (
                    <Button size="sm" variant="ghost" onClick={() => set({ color: '' })}>
                      Clear
                    </Button>
                  )}
                </span>
              </Field>
              <label className="flex items-center gap-2">
                <input
                  type="checkbox"
                  checked={s.readOnly}
                  onChange={(e) => set({ readOnly: e.target.checked })}
                />
                Read-only: browse and search, no edits
              </label>
              <dl className="grid grid-cols-[8rem_1fr] gap-x-3 gap-y-1 rounded-md bg-panel px-3 py-2 text-xs">
                <dt className="text-muted">Server</dt>
                <dd className="min-w-0 font-mono break-all">
                  {url}
                  {startTLS && ' + StartTLS'}
                </dd>
                <dt className="text-muted">Base</dt>
                <dd className="min-w-0 font-mono break-all">
                  {s.baseDN.trim() || 'from the server'}
                </dd>
                <dt className="text-muted">Connect asks for</dt>
                <dd className="min-w-0 break-all">
                  {s.auth === 'user'
                    ? `username and password, signs in as ${template}`
                    : s.auth === 'dn'
                      ? `the password of ${s.bindDN.trim()}`
                      : 'nothing: anonymous'}
                </dd>
              </dl>
            </>
          )}
        </div>

        {save.isError && <ErrorBanner error={save.error} />}
        <div className="flex flex-wrap items-center justify-between gap-2 border-t border-border pt-3">
          <span className="text-xs text-muted">
            Passwords are never saved: ldaplens asks for one when you connect.
          </span>
          <span className="flex gap-2">
            <Button onClick={onClose}>Cancel</Button>
            {step > 0 && <Button onClick={() => setStep(step - 1)}>Back</Button>}
            {!last && (
              <Button
                type={profile ? 'button' : 'submit'}
                variant={profile ? 'default' : 'primary'}
                disabled={!valid[step]}
                onClick={profile ? () => setStep(step + 1) : undefined}
              >
                Next
              </Button>
            )}
            {(last || profile) && (
              <Button type="submit" variant="primary" disabled={!canSave || save.isPending}>
                Save
              </Button>
            )}
          </span>
        </div>
      </form>
    </Dialog>
  )
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label className="grid min-w-0 gap-1">
      <span className="text-xs text-muted">{label}</span>
      {children}
    </label>
  )
}

/** One answer of the sign-in question: a radio card, its fields shown while it is chosen. */
function Choice({
  checked,
  onCheck,
  title,
  text,
  children,
}: {
  checked: boolean
  onCheck: () => void
  title: string
  text: string
  children?: ReactNode
}) {
  return (
    <div
      className={cn(
        'grid gap-3 rounded-lg border px-3 py-2.5',
        checked ? 'border-accent bg-selected/50' : 'border-border-strong',
      )}
    >
      <label className="flex cursor-pointer items-baseline gap-2.5">
        <input type="radio" name="auth" checked={checked} onChange={onCheck} />
        <span>
          <span className="block font-semibold">{title}</span>
          <span className="block text-xs text-muted">{text}</span>
        </span>
      </label>
      {checked && children}
    </div>
  )
}
