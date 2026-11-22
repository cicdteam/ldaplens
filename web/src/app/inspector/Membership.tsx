import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useVirtualizer } from '@tanstack/react-virtual'
import { Lock, Plus, Undo2, X } from 'lucide-react'
import { useEffect, useMemo, useRef, useState, type Dispatch, type SetStateAction } from 'react'
import { api, errorMessage } from '../../lib/api'
import { batches } from '../../lib/batchStore'
import { applied, entryChanges } from '../../lib/changes'
import { looksLikeDN, parentDN, rdnDisplay, splitDN } from '../../lib/dn'
import { visible } from '../../lib/display'
import {
  filterRefs,
  groupFilter,
  inheritedHint,
  isGroup,
  memberOfView,
  membersView,
  membersEditable,
  membershipEdit,
  sortRefs,
} from '../../lib/membership'
import { rowHeight, usePrefs } from '../../lib/prefs'
import type { Entry, Membership as MembershipData } from '../../lib/types'
import { Banner, ErrorBanner } from '../../ui/Banner'
import { Button } from '../../ui/Button'
import { cn } from '../../ui/cn'
import { Input } from '../../ui/Input'
import { Spinner } from '../../ui/Spinner'
import { stageOnce, useBatch } from '../batch'
import { useWorkspace } from '../context'
import { entryKey } from '../queries'
import { MarkGlyph, MARKS } from './Value'
import { ValueEditor } from './ValueEditor'

type Kind = 'memberof' | 'members'

/** An add field's state: 0 while closed; each new number opens it afresh, focused. */
export type Adding = [number, Dispatch<SetStateAction<number>>]

function DNButton({ dn, onSelect }: { dn: string; onSelect: (dn: string) => void }) {
  const first = splitDN(dn)[0]
  return (
    <button
      type="button"
      onClick={() => onSelect(dn)}
      title={visible(dn)}
      className="min-w-0 truncate text-left hover:underline"
    >
      <span className="text-accent-text">{first ? rdnDisplay(first) : visible(dn)}</span>
      <span className="ml-1 font-mono text-[11px] text-muted">{visible(parentDN(dn))}</span>
    </button>
  )
}

/**
 * The field that adds a group (Member of) or a member (Members), with suggestions. It takes focus
 * a tick after mounting: opened from the palette, the closing palette hands focus back in a
 * timeout of its own, which would otherwise blur, and so close, the empty field.
 */
function AddField({
  kind,
  exclude,
  onAdd,
  onClose,
}: {
  kind: Kind
  exclude: string[]
  onAdd: (text: string) => void
  onClose: () => void
}) {
  const box = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const t = setTimeout(() => box.current?.querySelector('input')?.focus(), 0)
    return () => clearTimeout(t)
  }, [])
  return (
    <div ref={box} className="flex shrink-0 items-center gap-1 px-2 py-1">
      <ValueEditor
        attr={kind}
        label={kind === 'memberof' ? 'Add to group' : 'Add member'}
        value=""
        kind="dn"
        suggest={kind === 'memberof' ? groupFilter : undefined}
        exclude={exclude}
        onCommit={onAdd}
        onCancel={onClose}
      />
    </div>
  )
}

function RefList({
  kind,
  entry,
  nested,
  editing,
  adding: [adding, setAdding],
}: {
  kind: Kind
  entry: Entry
  nested: boolean
  editing: boolean
  adding: Adding
}) {
  const ws = useWorkspace()
  const qc = useQueryClient()
  const { density } = usePrefs()
  const batch = useBatch(ws.conn)
  const [q, setQ] = useState('')
  const [problem, setProblem] = useState('')
  const query = useQuery({
    queryKey: ['c', ws.conn, kind, entry.dn, nested],
    queryFn: ({ signal }): Promise<MembershipData> =>
      kind === 'memberof'
        ? api.memberOf(ws.conn, entry.dn, nested, signal)
        : api.members(ws.conn, entry.dn, nested, signal),
  })
  // What the server lists, with the staged membership changes on top.
  const all = useMemo(() => {
    const listed = query.data?.refs ?? []
    return sortRefs(
      kind === 'memberof'
        ? memberOfView(listed, entry, batch)
        : membersView(listed, entry, entryChanges(batch, entry.dn)),
    )
  }, [query.data, kind, entry, batch])
  const refs = filterRefs(all, q)
  const scrollRef = useRef<HTMLDivElement>(null)
  // eslint-disable-next-line react-hooks/incompatible-library -- TanStack Virtual cannot be memoized; the app does not use the React Compiler, so this is informational only
  const virt = useVirtualizer({
    count: refs.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => rowHeight(density),
    overscan: 10,
  })
  useEffect(() => virt.measure(), [density, virt])
  const title = kind === 'memberof' ? 'Member of' : 'Members'
  // Any entry can join groups; a group's Members only when they are DNs (membersEditable).
  const editable = editing && (kind === 'memberof' || membersEditable(entry))
  // Never suggested: the entry itself and what it is in, or has, directly or staged. Memoized so
  // DNSuggestions normalizes it once per change of the list, not on every render of this one.
  const exclude = useMemo(
    () => [entry.dn, ...all.filter((r) => !r.via && r.mark !== 'deleted').map((r) => r.dn)],
    [entry.dn, all],
  )

  /**
   * Stages adding or removing a membership on the group. The other side (the group to join or
   * leave, the member to add) is read for its classes and stored values; a member to remove is
   * only named, so one whose entry is gone can still go. The same change asked again while it
   * reads (Enter, then the blur) runs once, and the review waits for it.
   */
  const change = (dn: string, op: 'add' | 'delete'): Promise<boolean> =>
    stageOnce(JSON.stringify([ws.conn, entry.dn, kind, op, dn]), async () => {
      setProblem('')
      try {
        const other: Entry =
          kind === 'members' && op === 'delete'
            ? { dn }
            : await qc.fetchQuery({
                queryKey: entryKey(ws.conn, dn),
                queryFn: ({ signal }) => api.entry(ws.conn, dn, signal),
              })
        const group = kind === 'memberof' ? other : entry
        const edit = membershipEdit(
          applied(group, entryChanges(batches.get(ws.conn), group.dn)),
          kind === 'memberof' ? entry : other,
          op,
        )
        if (typeof edit === 'string') {
          setProblem(edit)
          return false
        }
        batches.stage(ws.conn, group.dn, edit)
        return true
      } catch (e) {
        setProblem(errorMessage(e))
        return false
      }
    })

  return (
    <section aria-label={title} className="flex min-h-0 flex-1 flex-col border-b border-border">
      <div className="flex h-8 shrink-0 items-center gap-2 bg-panel px-2 text-[11px] font-semibold tracking-[.04em] text-muted uppercase">
        {title.toUpperCase()} {query.data && <span className="font-normal">({all.length})</span>}
        {query.isFetching && <Spinner className="h-3 w-3" />}
        {/* Kept while q is set, so a filter typed on a long list can still be cleared after Direct/Nested shrinks it. */}
        {(all.length > 10 || q !== '') && (
          <Input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Filter"
            aria-label={`Filter ${title}`}
            className="ml-auto h-6! w-40 text-xs"
          />
        )}
      </div>
      {editable &&
        (adding ? (
          <AddField
            key={adding}
            kind={kind}
            exclude={exclude}
            onAdd={(text) => {
              const dn = text.trim()
              if (!looksLikeDN(dn)) setProblem('Choose an entry from the list')
              else
                void change(dn, 'add').then((ok) => {
                  if (ok) setAdding(0)
                })
            }}
            onClose={() => {
              setProblem('')
              setAdding(0)
            }}
          />
        ) : (
          <Button
            size="sm"
            variant="ghost"
            className="mx-2 my-1 self-start"
            onClick={() => setAdding((n) => n + 1)}
          >
            <Plus className="h-3.5 w-3.5" /> {kind === 'memberof' ? 'Add to group…' : 'Add member…'}
          </Button>
        ))}
      {problem && (
        <p role="alert" className="shrink-0 px-2 py-1 text-xs text-danger">
          {problem}
        </p>
      )}
      {query.isError && (
        <ErrorBanner
          error={query.error}
          action={
            <Button size="sm" onClick={() => void query.refetch()}>
              Retry
            </Button>
          }
        />
      )}
      {query.data?.truncated && (
        <Banner tone="warn">
          The list may be incomplete: it reached a limit (10 levels of nesting, 10,000 entries or a
          server limit).
        </Banner>
      )}
      {!!query.data?.skipped?.length && (
        <Banner tone="warn">
          The server refused to search {query.data.skipped.join(' and ')} (usually because it is not
          indexed there), so groups that list this entry only in{' '}
          {query.data.skipped.length === 1 ? 'that attribute' : 'those attributes'} are missing.
        </Banner>
      )}
      {query.isSuccess && all.length === 0 && <p className="px-2 py-1 text-muted">None.</p>}
      <div ref={scrollRef} className="min-h-0 flex-1 overflow-auto">
        <div style={{ height: virt.getTotalSize(), position: 'relative' }}>
          {virt.getVirtualItems().map((v) => {
            const r = refs[v.index]
            const undo = r.undo
            const hint = inheritedHint(r)
            // The group the row's change goes to.
            const group = kind === 'memberof' ? r.dn : entry.dn
            return (
              <div
                key={`${r.mark ?? ''}:${r.dn}`}
                data-ref={r.dn}
                style={{ height: v.size, transform: `translateY(${v.start}px)` }}
                className={cn(
                  'absolute top-0 left-0 flex w-full items-center gap-1 px-2',
                  r.mark && MARKS[r.mark].row,
                )}
              >
                <MarkGlyph mark={r.mark} />
                <div
                  className={cn(
                    'min-w-0 flex-1 items-center gap-2',
                    nested ? 'grid grid-cols-[3fr_2fr]' : 'flex',
                  )}
                >
                  <DNButton dn={r.dn} onSelect={ws.select} />
                  {nested &&
                    (r.via ? (
                      <span className="flex min-w-0 items-center gap-1 text-xs text-muted">
                        via <DNButton dn={r.via} onSelect={ws.select} />
                      </span>
                    ) : (
                      <span className="text-xs text-muted">direct</span>
                    ))}
                </div>
                {editable &&
                  (undo ? (
                    <Button
                      size="icon"
                      variant="ghost"
                      className="h-full!"
                      aria-label={`${r.mark === 'deleted' ? 'Restore' : 'Remove'} ${r.dn}`}
                      title={r.mark === 'deleted' ? 'Restore' : 'Remove'}
                      onClick={() => batches.stage(ws.conn, group, undo)}
                    >
                      {r.mark === 'deleted' ? (
                        <Undo2 className="h-3.5 w-3.5" />
                      ) : (
                        <X className="h-3.5 w-3.5" />
                      )}
                    </Button>
                  ) : hint ? (
                    <span
                      role="img"
                      aria-label={hint}
                      title={visible(hint)}
                      className="flex h-full w-7 shrink-0 items-center justify-center text-muted"
                    >
                      <Lock className="h-3.5 w-3.5" />
                    </span>
                  ) : (
                    <Button
                      size="icon"
                      variant="ghost"
                      className="h-full!"
                      aria-label={`Remove ${r.dn}`}
                      title="Remove"
                      onClick={() => void change(r.dn, 'delete')}
                    >
                      <X className="h-3.5 w-3.5" />
                    </Button>
                  ))}
              </div>
            )
          })}
        </div>
      </div>
    </section>
  )
}

/** Direct or nested memberships: the card's tab bar shows it, and the choice stays across entries. */
export function Depth({
  nested,
  setNested,
}: {
  nested: boolean
  setNested: (nested: boolean) => void
}) {
  return (
    <div
      role="radiogroup"
      aria-label="Membership depth"
      className="flex shrink-0 rounded-md border border-border-strong p-0.5 text-xs"
    >
      {[false, true].map((n) => (
        <button
          key={String(n)}
          type="button"
          role="radio"
          aria-checked={nested === n}
          title={
            n
              ? 'Also through other groups, with the group each one comes through'
              : 'Direct memberships only'
          }
          onClick={() => setNested(n)}
          className={cn(
            'rounded px-2 py-0.5',
            nested === n ? 'bg-selected font-semibold text-fg' : 'text-muted hover:text-fg',
          )}
        >
          {n ? 'Nested' : 'Direct'}
        </button>
      ))}
    </div>
  )
}

/**
 * Direct or nested (see Depth) memberships. In Edit they change through the 3a batch: staged adds
 * and deletes of the group's member, uniqueMember or memberUid. addingGroup is the Member of add
 * field, which the inspector opens for the palette.
 */
export function Membership({
  entry,
  editing,
  nested,
  addingGroup,
}: {
  entry: Entry
  editing: boolean
  nested: boolean
  addingGroup: Adding
}) {
  const addingMember = useState(0)
  return (
    <div className="flex h-full flex-col">
      <RefList
        kind="memberof"
        entry={entry}
        nested={nested}
        editing={editing}
        adding={addingGroup}
      />
      {isGroup(entry) && (
        <RefList
          kind="members"
          entry={entry}
          nested={nested}
          editing={editing}
          adding={addingMember}
        />
      )}
    </div>
  )
}
