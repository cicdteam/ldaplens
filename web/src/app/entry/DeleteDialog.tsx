import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { api } from '../../lib/api'
import { batches } from '../../lib/batchStore'
import { dropEntry, entryChanges } from '../../lib/changes'
import { parentDN, splitDN } from '../../lib/dn'
import { childrenNote, deleteLDIF } from '../../lib/entryOps'
import { ErrorBanner } from '../../ui/Banner'
import { Input } from '../../ui/Input'
import { Spinner } from '../../ui/Spinner'
import { useWorkspace } from '../context'
import { afterWrite, childrenKey } from '../queries'
import { WriteDialog } from './WriteDialog'

/** Delete: only a leaf, and only after its RDN is typed. */
export function DeleteDialog({ dn, close }: { dn: string; close: () => void }) {
  const ws = useWorkspace()
  const qc = useQueryClient()
  const rdn = splitDN(dn)[0] ?? dn
  const [typed, setTyped] = useState('')
  // One level, no attributes, at most 101: enough to tell none, a count, or "100+".
  const kids = useQuery({
    queryKey: [...childrenKey(ws.conn, dn), 'count'],
    queryFn: ({ signal }) =>
      api.search(
        ws.conn,
        { base: dn, scope: 'one', filter: '(objectClass=*)', attributes: ['1.1'], sizeLimit: 101 },
        signal,
      ),
    staleTime: 0,
  })
  const n = kids.data?.entries?.length ?? 0
  // The groups that list the entry directly. Active Directory drops a deleted
  // member by itself; other servers may keep its DN, so the dialog names them.
  const groups = useQuery({
    queryKey: ['c', ws.conn, 'memberof', dn, false],
    queryFn: ({ signal }) => api.memberOf(ws.conn, dn, false, signal),
    enabled: !ws.info.activeDirectory,
    staleTime: 0,
  })
  const m = useMutation({
    mutationFn: () => api.deleteEntry(ws.conn, { dn }),
    onSuccess: () => {
      // Delete waits while anything is staged under the entry; this only guards a race.
      const b = batches.get(ws.conn)
      if (entryChanges(b, dn)) batches.set(ws.conn, dropEntry(b, dn))
      afterWrite(qc, ws.conn, dn)
      ws.select(parentDN(dn))
      close()
    },
  })
  return (
    <WriteDialog
      title="Delete entry"
      target={dn}
      ldif={deleteLDIF(dn)}
      action="Delete"
      danger
      ready={kids.isSuccess && n === 0 && typed.trim() === rdn}
      pending={m.isPending}
      error={m.error}
      onSubmit={() => m.mutate()}
      close={close}
    >
      {kids.isPending && <Spinner />}
      {kids.isError && <ErrorBanner error={kids.error} />}
      {n > 0 && <p className="text-sm text-danger">{childrenNote(n)}</p>}
      {n === 0 && groups.data?.refs?.length ? (
        <GroupNote dns={groups.data.refs.map((r) => r.dn)} truncated={!!groups.data.truncated} />
      ) : null}
      {kids.isSuccess && n === 0 && (
        <label className="flex flex-col gap-1 text-xs">
          {/* One flex item; bdi keeps the raw RDN, typed as is, from reordering the words
              around it. */}
          <span>
            Type <bdi>{rdn}</bdi> to confirm
          </span>
          {/* data-autofocus when the count is cached and the field opens with the dialog;
              autoFocus when it appears after the count loads, inside the open dialog. */}
          <Input
            mono
            data-autofocus
            autoFocus
            value={typed}
            onChange={(e) => {
              // Only an error: a reset while the request runs would end its pending state.
              if (m.isError) m.reset()
              setTyped(e.target.value)
            }}
            autoComplete="off"
            spellCheck={false}
          />
        </label>
      )}
    </WriteDialog>
  )
}

const shown = 5

/** A reminder, not a step: the groups that list the entry and may keep its DN. */
function GroupNote({ dns, truncated }: { dns: string[]; truncated: boolean }) {
  const more = dns.length - shown
  return (
    <div
      role="note"
      aria-label="Group membership"
      className="space-y-1 border-l-2 border-warn bg-panel px-3 py-2 text-xs"
    >
      <p>
        A member of {dns.length}
        {truncated && '+'} {dns.length === 1 && !truncated ? 'group' : 'groups'}. Unless the server
        removes a deleted entry from its groups by itself (OpenLDAP does with the refint overlay),
        they keep its DN: remove it from them yourself.
      </p>
      <ul className="font-mono break-all text-muted">
        {dns.slice(0, shown).map((g) => (
          <li key={g}>{g}</li>
        ))}
        {(more > 0 || truncated) && <li>and {truncated ? 'more' : `${more} more`}</li>}
      </ul>
    </div>
  )
}
