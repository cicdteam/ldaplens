import { useQuery, useQueryClient, type QueryClient } from '@tanstack/react-query'
import { Command } from 'cmdk'
import { ArrowRight, FileText, Folder, Globe, Plug, Star, User, Users, Zap } from 'lucide-react'
import { useState, type ReactNode } from 'react'
import { useNavigate } from 'react-router'
import { api, errorMessage, profileLabel } from '../../lib/api'
import { looksLikeDN, rdnDisplay, splitDN } from '../../lib/dn'
import { visible } from '../../lib/display'
import { canSetPassword, stagedHint, stagedUnder } from '../../lib/entryOps'
import { getPrefs, nextTheme, setPrefs } from '../../lib/prefs'
import { keyLabel } from '../../lib/shortcuts'
import { keys, load } from '../../lib/storage'
import { entryKind, type EntryKind } from '../../lib/tree'
import type { Entry } from '../../lib/types'
import { Kbd } from '../../ui/Kbd'
import { Spinner } from '../../ui/Spinner'
import { stepBatch, useBatch } from '../batch'
import { useWorkspace } from '../context'
import { openEntryAction, visibleActions } from '../entry/actions'
import {
  ADD_TO_GROUP,
  FOCUS_FILTER,
  FORMAT_FILTER,
  OPEN_IMPORT,
  OPEN_REVIEW,
  RUN_SEARCH,
} from '../events'
import { useDisconnect } from '../Header'
import { entryKey, useDebounced, useFind } from '../queries'
import { isSaved, list, queryText } from '../search/Omnibox'

const kindIcon: Record<EntryKind, typeof User> = {
  user: User,
  group: Users,
  ou: Folder,
  domain: Globe,
  other: FileText,
}
const item =
  'flex cursor-default items-center gap-2 rounded-md px-2 py-1.5 text-sm aria-selected:bg-selected data-[disabled=true]:opacity-50'
const group =
  'px-1 py-1 [&_[cmdk-group-heading]]:px-2 [&_[cmdk-group-heading]]:py-1 [&_[cmdk-group-heading]]:text-[11px] [&_[cmdk-group-heading]]:font-semibold [&_[cmdk-group-heading]]:text-muted'

interface Action {
  id: string
  label: string
  shortcut?: string
  /** Why the action cannot run now; '' or unset when it can. */
  disabled?: string
  run(): void
}

const fire = (type: string) => () => window.dispatchEvent(new Event(type))

/** Actions the query bar carries out: it listens for their events. */
export const filterActions: Action[] = [
  { id: 'filter', label: 'Focus the search bar', shortcut: '/', run: fire(FOCUS_FILTER) },
  { id: 'run', label: 'Run search', shortcut: 'Enter', run: fire(RUN_SEARCH) },
  { id: 'format', label: 'Format the filter', shortcut: 'Shift+Alt+F', run: fire(FORMAT_FILTER) },
]

/**
 * Actions on the profile's staged batch; undo and redo are no-ops when there is nothing to
 * step or an apply is in flight.
 */
export const batchActions = (conn: string, qc: QueryClient): Action[] => [
  { id: 'review', label: 'Review changes', shortcut: 'Mod+S', run: fire(OPEN_REVIEW) },
  {
    id: 'undo',
    label: 'Undo the last change',
    shortcut: 'Mod+Z',
    run: () => stepBatch(qc, conn, false),
  },
  { id: 'redo', label: 'Redo', shortcut: 'Mod+Shift+Z', run: () => stepBatch(qc, conn, true) },
]

/**
 * The open entry's actions: rename and delete carry hint while something is staged under it; Set
 * password shows when password is set (see canSetPassword).
 */
export const entryActions = (hint: string, password = false): Action[] =>
  visibleActions(password).map((a) => ({
    id: `entry-${a.kind}`,
    label: a.palette ?? `${a.label} entry`,
    shortcut: a.shortcut,
    disabled: a.clean ? hint : '',
    run: () => openEntryAction(a.kind),
  }))

/** Turns on Edit and opens the inspector's Add to group field; the inspector listens. */
export const addToGroup: Action = {
  id: 'add-to-group',
  label: 'Add to group…',
  run: fire(ADD_TO_GROUP),
}

/** Opens the LDIF import with no entry to place it under; the workspace listens. */
export const importLDIF: Action = {
  id: 'import',
  label: 'Import LDIF…',
  run: fire(OPEN_IMPORT),
}

export function CommandPalette({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const ws = useWorkspace()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const disconnect = useDisconnect()
  const batch = useBatch(ws.conn)
  const [q, setQ] = useState('')
  const dq = useDebounced(q.trim(), 250)
  // nameFilter needs at least two characters: an empty one is a malformed (cn=**).
  const find = dq.length >= 2 && !looksLikeDN(dq)
  const found = useFind(ws.conn, ws.info.baseDN, dq, open && find)
  const profiles = useQuery({
    queryKey: ['profiles'],
    queryFn: () => api.profiles(),
    enabled: open,
  })
  const saved = open ? list(load<unknown>(keys.saved(ws.conn), [])).filter(isSaved) : []

  const close = (fn: () => void) => () => {
    onOpenChange(false)
    setQ('')
    fn()
  }
  // The open entry as the inspector read it: whether it can hold a password.
  const current = ws.dn ? qc.getQueryData<Entry>(entryKey(ws.conn, ws.dn)) : undefined
  // Read-only profiles stage and write nothing.
  const entryCommands =
    ws.dn && !ws.info.readOnly
      ? entryActions(
          stagedHint(stagedUnder(batch, ws.dn)),
          !!current && canSetPassword(current, ws.schema, ws.info.activeDirectory),
        )
      : []
  const actions: Action[] = [
    ...filterActions,
    // Read-only profiles stage and write nothing.
    ...(ws.info.readOnly ? [] : [...batchActions(ws.conn, qc), importLDIF]),
    ...entryCommands,
    ...(ws.dn && !ws.info.readOnly ? [addToGroup] : []),
    {
      id: 'theme',
      label: 'Switch theme',
      run: () => setPrefs({ theme: nextTheme(getPrefs().theme) }),
    },
    {
      id: 'density',
      label: 'Toggle compact rows',
      run: () =>
        setPrefs({ density: getPrefs().density === 'compact' ? 'comfortable' : 'compact' }),
    },
    {
      id: 'raw',
      label: 'Toggle raw attribute values',
      run: () => setPrefs({ raw: !getPrefs().raw }),
    },
    { id: 'tree', label: 'Toggle the tree panel', run: ws.toggleTree },
    { id: 'inspector', label: 'Toggle the inspector panel', run: ws.toggleInspector },
    { id: 'schema', label: 'Reload the schema', run: () => void ws.reloadSchema().catch(() => {}) },
    ...(ws.dn
      ? [
          {
            id: 'children',
            label: 'List children of the current entry',
            run: () => ws.listChildren(ws.dn),
          },
          {
            id: 'copy',
            label: 'Copy the current DN',
            run: () => void navigator.clipboard?.writeText(ws.dn),
          },
        ]
      : []),
    {
      id: 'disconnect',
      label: `Disconnect from ${profileLabel({ name: ws.conn, label: profiles.data?.find((p) => p.name === ws.conn)?.label })}`,
      run: () => void disconnect(),
    },
  ]
  const needle = q.trim().toLowerCase()
  const match = (s: string) => !needle || s.toLowerCase().includes(needle)
  // cmdk shows a group heading even when no item matches, so empty groups are not rendered.
  const conns = (profiles.data ?? []).filter(
    (p) => p.connected && p.name !== ws.conn && (match(p.name) || match(profileLabel(p))),
  )
  const manage = match('manage connections')
  const acts = actions.filter((a) => match(a.label))
  const row = (icon: ReactNode, label: ReactNode, hint?: ReactNode) => (
    <>
      {icon}
      <span className="min-w-0 flex-1 truncate">{label}</span>
      {hint}
    </>
  )

  return (
    <Command.Dialog
      open={open}
      onOpenChange={(o) => {
        onOpenChange(o)
        if (!o) setQ('')
      }}
      label="Command palette"
      shouldFilter={false}
      overlayClassName="fixed inset-0 z-40 bg-black/40"
      contentClassName="fixed top-[12vh] z-40 left-1/2 w-[min(92vw,40rem)] -translate-x-1/2 elevated overflow-hidden rounded-[10px] border border-border bg-bg text-fg"
    >
      <Command.Input
        value={q}
        onValueChange={setQ}
        placeholder="Jump to a DN, find an entry by name, or run an action…"
        className="h-11 w-full border-b border-border bg-transparent px-3 outline-none"
      />
      <Command.List className="max-h-[60vh] overflow-auto">
        <Command.Empty className="p-3 text-muted">
          {found.isFetching ? 'Searching…' : 'Nothing found.'}
        </Command.Empty>
        {looksLikeDN(q) && (
          <Command.Group heading="Go to" className={group}>
            <Command.Item
              value={`go:${q}`}
              onSelect={close(() => ws.select(q.trim()))}
              className={item}
            >
              {row(
                <ArrowRight className="h-4 w-4 text-muted" />,
                <span className="font-mono text-xs">{q.trim()}</span>,
              )}
            </Command.Item>
          </Command.Group>
        )}
        {find && (
          <Command.Group heading="Entries" className={group}>
            {found.isFetching && !found.data && (
              <Command.Item value="find:loading" disabled className={item}>
                {row(<Spinner />, 'Searching…')}
              </Command.Item>
            )}
            {found.isError && (
              <Command.Item value="find:error" disabled className={item}>
                {row(null, <span className="text-danger">{errorMessage(found.error)}</span>)}
              </Command.Item>
            )}
            {(found.data?.entries ?? []).map((e) => {
              const get = (n: string) =>
                e.attributes?.find((a) => a.name.toLowerCase() === n)?.values?.[0]?.text
              const Icon =
                kindIcon[
                  entryKind(
                    e.attributes
                      ?.find((a) => a.name.toLowerCase() === 'objectclass')
                      ?.values?.map((v) => v.text) ?? [],
                  )
                ]
              const first = splitDN(e.dn)[0]
              return (
                <Command.Item
                  key={e.dn}
                  value={`entry:${e.dn}`}
                  onSelect={close(() => ws.select(e.dn))}
                  className={item}
                >
                  {row(
                    <Icon className="h-4 w-4 shrink-0 text-muted" />,
                    <>
                      {visible(
                        get('displayname') ?? get('cn') ?? (first ? rdnDisplay(first) : e.dn),
                      )}
                      <span className="ml-2 font-mono text-[11px] text-muted">{visible(e.dn)}</span>
                    </>,
                    get('mail') && <span className="text-xs text-muted">{get('mail')}</span>,
                  )}
                </Command.Item>
              )
            })}
          </Command.Group>
        )}
        {saved.some((s) => match(s.name)) && (
          <Command.Group heading="Saved searches" className={group}>
            {saved
              .filter((s) => match(s.name))
              .map((s) => (
                <Command.Item
                  key={s.name}
                  value={`saved:${s.name}`}
                  onSelect={close(() => ws.runSearch(s))}
                  className={item}
                >
                  {row(
                    <Star className="h-4 w-4 text-muted" />,
                    s.name,
                    <span className="truncate font-mono text-[11px] text-muted">
                      {queryText(s)}
                    </span>,
                  )}
                </Command.Item>
              ))}
          </Command.Group>
        )}
        {(conns.length > 0 || manage) && (
          <Command.Group heading="Connections" className={group}>
            {conns.map((p) => (
              <Command.Item
                key={p.name}
                value={`conn:${p.name}`}
                onSelect={close(() => navigate(`/c/${encodeURIComponent(p.name)}`))}
                className={item}
              >
                {row(<Plug className="h-4 w-4 text-muted" />, `Switch to ${profileLabel(p)}`)}
              </Command.Item>
            ))}
            {manage && (
              <Command.Item
                value="conn:manage"
                onSelect={close(() => navigate('/'))}
                className={item}
              >
                {row(<Plug className="h-4 w-4 text-muted" />, 'Manage connections')}
              </Command.Item>
            )}
          </Command.Group>
        )}
        {acts.length > 0 && (
          <Command.Group heading="Actions" className={group}>
            {acts.map((a) => (
              <Command.Item
                key={a.id}
                value={`action:${a.id}`}
                disabled={!!a.disabled}
                onSelect={close(a.run)}
                className={item}
              >
                {row(
                  <Zap className="h-4 w-4 text-muted" />,
                  a.label,
                  a.disabled ? (
                    <span className="text-xs text-muted">{a.disabled}</span>
                  ) : (
                    a.shortcut && <Kbd>{keyLabel(a.shortcut)}</Kbd>
                  ),
                )}
              </Command.Item>
            ))}
          </Command.Group>
        )}
      </Command.List>
      <div className="flex gap-3 border-t border-border px-3 py-1.5 text-[11px] text-muted">
        <span>
          <Kbd>↑↓</Kbd> move
        </span>
        <span>
          <Kbd>↵</Kbd> open
        </span>
        <span>
          <Kbd>Esc</Kbd> close
        </span>
        <span className="ml-auto">
          <Kbd>{keyLabel('Mod+K')}</Kbd> toggle
        </span>
      </div>
    </Command.Dialog>
  )
}
