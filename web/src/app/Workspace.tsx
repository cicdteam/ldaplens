import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Search } from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  Group,
  Panel,
  Separator,
  useDefaultLayout,
  type LayoutStorage,
  type PanelImperativeHandle,
} from 'react-resizable-panels'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router'
import { api, errorKind } from '../lib/api'
import { defaultColumns, readSearch, workspaceURL } from '../lib/nav'
import { SchemaIndex } from '../lib/schema'
import { isEditableTarget, isModKey, keyLabel } from '../lib/shortcuts'
import { load, save, type SearchState } from '../lib/storage'
import { Banner, ErrorBanner } from '../ui/Banner'
import { Button } from '../ui/Button'
import { Kbd } from '../ui/Kbd'
import { Spinner } from '../ui/Spinner'
import { stagingSettled, stepBatch, useBatch } from './batch'
import { WorkspaceContext, type WorkspaceCtx } from './context'
import type { EntryActionKind } from './entry/actions'
import { CreateDialog } from './entry/CreateDialog'
import { DeleteDialog } from './entry/DeleteDialog'
import { ImportDialog } from './entry/ImportDialog'
import { PasswordDialog } from './entry/PasswordDialog'
import { RenameDialog } from './entry/RenameDialog'
import { ENTRY_ACTION, FOCUS_FILTER, OPEN_IMPORT, OPEN_REVIEW } from './events'
import { Header } from './Header'
import { Inspector } from './inspector/Inspector'
import { CommandPalette } from './palette/CommandPalette'
import { PendingBar } from './PendingBar'
import { searchKey } from './queries'
import { ReviewDialog } from './review/ReviewDialog'
import { Results } from './search/Results'
import { StatusBar } from './StatusBar'
import { Tree } from './tree/Tree'

/** Remounts the workspace per connection so no state leaks between profiles. */
export function WorkspaceRoute() {
  const { conn = '' } = useParams()
  return <Workspace key={conn} />
}

// data-separator is "hover" or "active" (dragging) over the whole hit area,
// which is wider than the 4px bar.
const handle =
  'w-1 bg-border/60 transition-colors data-[separator=active]:bg-accent data-[separator=hover]:bg-accent'

// Panel sizes persist through lib/storage, which never throws, so a blocked
// storage does not break the layout (the library's JSON text gets JSON-encoded
// once more).
const layoutStorage: LayoutStorage = {
  getItem: (k) => load<string | null>(k, null),
  setItem: (k, v) => save(k, v),
}

function toggle(p: PanelImperativeHandle | null) {
  if (!p) return
  if (p.isCollapsed()) p.expand()
  else p.collapse()
}

export function Workspace() {
  const { conn = '' } = useParams()
  const [params] = useSearchParams()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const treeRef = useRef<PanelImperativeHandle>(null)
  const inspectorRef = useRef<PanelImperativeHandle>(null)
  // Sizes persist per configuration: tree and card, or tree, list and card.
  const layout2 = useDefaultLayout({ id: 'ldaplens:panels:2', storage: layoutStorage })
  const layout3 = useDefaultLayout({ id: 'ldaplens:panels:3', storage: layoutStorage })

  const info = useQuery({
    queryKey: ['c', conn, 'info'],
    queryFn: ({ signal }) => api.info(conn, signal),
    staleTime: Infinity,
  })
  const schema = useQuery({
    queryKey: ['c', conn, 'schema'],
    queryFn: ({ signal }) => api.schema(conn, false, signal),
    staleTime: Infinity,
    enabled: info.isSuccess,
  })
  const index = useMemo(() => new SchemaIndex(schema.data), [schema.data])

  const dn = params.get('dn') ?? ''
  // Memoized on the search parameters only, so selecting an entry (dn) keeps
  // the same search object and the query bar keeps unsaved edits.
  const filterP = params.get('filter')
  const baseP = params.get('base')
  const scopeP = params.get('scope')
  const attrsP = params.get('attrs')
  const search = useMemo(() => {
    const p = new URLSearchParams()
    const parts = { filter: filterP, base: baseP, scope: scopeP, attrs: attrsP }
    for (const [k, v] of Object.entries(parts)) if (v !== null) p.set(k, v)
    return readSearch(p)
  }, [filterP, baseP, scopeP, attrsP])

  const select = useCallback(
    (next: string, opts?: { replace?: boolean }) =>
      navigate(workspaceURL(conn, { dn: next, search }, 'entry'), { replace: opts?.replace }),
    [conn, search, navigate],
  )
  const [runs, setRuns] = useState(0)
  const runSearch = useCallback(
    (s: SearchState | null) => {
      // The same search again: drop its cached answers and start the results over, so the new
      // run asks the server for the first limit. Another search starts over by its own key.
      if (s && search && searchKey(conn, s).join('\0') === searchKey(conn, search).join('\0')) {
        qc.removeQueries({ queryKey: searchKey(conn, s) })
        setRuns((n) => n + 1)
      }
      navigate(workspaceURL(conn, { dn, search: s }, s ? 'search' : 'entry'))
    },
    [conn, dn, search, navigate, qc],
  )

  // Capture phase, so the keys also work while CodeMirror has focus.
  const [palette, setPalette] = useState(false)
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const t = e.target instanceof Element ? e.target : null
      if (isModKey(e, 'k')) {
        e.preventDefault()
        setPalette((p) => !p)
      } else if (isModKey(e, 's')) {
        // Instead of the browser's "Save page", from anywhere. The blur commits the value
        // being typed first, so the review has it.
        e.preventDefault()
        if (document.activeElement instanceof HTMLElement) document.activeElement.blur()
        window.dispatchEvent(new Event(OPEN_REVIEW))
      } else if (isModKey(e, 'z') && !isEditableTarget(t) && !t?.closest('.cm-editor')) {
        // Fields and CodeMirror keep their own undo.
        e.preventDefault()
        stepBatch(qc, conn, e.shiftKey)
      } else if (
        e.key === '/' &&
        !e.metaKey &&
        !e.ctrlKey &&
        !e.altKey &&
        !isEditableTarget(e.target as HTMLElement | null)
      ) {
        e.preventDefault()
        window.dispatchEvent(new Event(FOCUS_FILTER))
      }
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [conn, qc])

  // Leaving the page with unapplied changes asks first (they survive a reload, not a closed tab).
  const dirty = useBatch(conn).length > 0
  useEffect(() => {
    if (!dirty) return
    const onUnload = (e: BeforeUnloadEvent) => {
      e.preventDefault()
      e.returnValue = ''
    }
    window.addEventListener('beforeunload', onUnload)
    return () => window.removeEventListener('beforeunload', onUnload)
  }, [dirty])

  const [review, setReview] = useState(false)
  useEffect(() => {
    // After any staging still reading (a membership add): the review then has it.
    const onOpen = () => void stagingSettled().then(() => setReview(true))
    window.addEventListener(OPEN_REVIEW, onOpen)
    return () => window.removeEventListener(OPEN_REVIEW, onOpen)
  }, [])

  // An entry action's dialog, for the entry open when it was asked for. An open import keeps its
  // dialog: Mod+K works over it, and a running import must not lose its report.
  const [action, setAction] = useState<{ kind: EntryActionKind; dn: string } | null>(null)
  useEffect(() => {
    const onAction = (e: Event) => {
      const kind = (e as CustomEvent<EntryActionKind>).detail
      if (dn) setAction((a) => (a?.kind === 'import' ? a : { kind, dn }))
    }
    window.addEventListener(ENTRY_ACTION, onAction)
    return () => window.removeEventListener(ENTRY_ACTION, onAction)
  }, [dn])
  // The palette's Import LDIF…: an import with no entry to place it under.
  useEffect(() => {
    const onImport = () => setAction((a) => (a?.kind === 'import' ? a : { kind: 'import', dn: '' }))
    window.addEventListener(OPEN_IMPORT, onImport)
    return () => window.removeEventListener(OPEN_IMPORT, onImport)
  }, [])

  if (info.isPending) {
    return (
      <div className="flex h-full items-center justify-center">
        <Spinner />
      </div>
    )
  }
  if (info.isError) {
    // Session errors are redirected to reconnect by the query cache; show the rest.
    return (
      <main className="mx-auto mt-[15vh] max-w-lg space-y-3 px-4">
        {errorKind(info.error) === 'session' ? <Spinner /> : <ErrorBanner error={info.error} />}
        <Link to="/" className="text-accent-text">
          Back to connections
        </Link>
      </main>
    )
  }

  const ctx: WorkspaceCtx = {
    conn,
    info: info.data,
    schema: index,
    schemaError: schema.error,
    reloadSchema: async () => {
      const fresh = await api.schema(conn, true)
      qc.setQueryData(['c', conn, 'schema'], fresh)
    },
    dn,
    search,
    select,
    runSearch,
    closeList: () => {
      // Before the list unmounts, so focus does not fall to the page: the card's heading, or
      // the tree with no entry open.
      const to =
        document.getElementById('card-heading') ??
        document.querySelector<HTMLElement>('[role="tree"]')
      to?.focus()
      runSearch(null)
    },
    listChildren: (base) =>
      runSearch({
        base,
        scope: 'one',
        filter: '(objectClass=*)',
        attrs: defaultColumns(info.data),
      }),
    runs,
    toggleTree: () => toggle(treeRef.current),
    toggleInspector: () => toggle(inspectorRef.current),
  }

  return (
    <WorkspaceContext.Provider value={ctx}>
      <div className="flex h-full flex-col">
        <Header
          extra={
            <button
              type="button"
              onClick={() => setPalette(true)}
              title={`Search or jump (${keyLabel('Mod+K')})`}
              className="flex h-7 shrink-0 items-center gap-2 rounded-md border border-border-strong bg-panel px-2 text-xs whitespace-nowrap text-muted hover:bg-hover hover:text-fg"
            >
              <Search className="h-3.5 w-3.5" />
              {/* Below 1280 px, the icon and the key: the header needs the room. */}
              <span className="max-xl:sr-only">Search or jump</span>
              <Kbd>{keyLabel('Mod+K')}</Kbd>
            </button>
          }
        />
        {schema.isError && (
          <Banner
            tone="warn"
            action={
              <Button size="sm" onClick={() => void ctx.reloadSchema().catch(() => {})}>
                Reload schema
              </Button>
            }
          >
            The schema could not be read: attribute groups and completion are off.
          </Banner>
        )}
        {/* One group, so the tree and the card stay mounted while the list opens and closes.
            The card has no default size: it takes what the tree and the list leave. */}
        <Group
          orientation="horizontal"
          defaultLayout={(search ? layout3 : layout2).defaultLayout}
          onLayoutChanged={(l, meta) => ('list' in l ? layout3 : layout2).onLayoutChanged(l, meta)}
          className="min-h-0 flex-1"
        >
          <Panel panelRef={treeRef} id="tree" defaultSize="18%" minSize="12%" collapsible>
            <Tree />
          </Panel>
          <Separator className={handle} />
          {search && (
            <>
              <Panel id="list" defaultSize="31%" minSize="20%">
                <Results search={search} />
              </Panel>
              <Separator className={handle} />
            </>
          )}
          <Panel panelRef={inspectorRef} id="inspector" minSize="20%" collapsible>
            <Inspector />
          </Panel>
        </Group>
        <PendingBar />
        <StatusBar />
      </div>
      <CommandPalette open={palette} onOpenChange={setPalette} />
      <ReviewDialog open={review} onOpenChange={setReview} />
      {action && (action.kind === 'new' || action.kind === 'clone') && (
        <CreateDialog mode={action.kind} dn={action.dn} close={() => setAction(null)} />
      )}
      {action?.kind === 'import' && (
        <ImportDialog here={action.dn || undefined} close={() => setAction(null)} />
      )}
      {action?.kind === 'rename' && <RenameDialog dn={action.dn} close={() => setAction(null)} />}
      {action?.kind === 'delete' && <DeleteDialog dn={action.dn} close={() => setAction(null)} />}
      {action?.kind === 'password' && (
        <PasswordDialog dn={action.dn} close={() => setAction(null)} />
      )}
    </WorkspaceContext.Provider>
  )
}
