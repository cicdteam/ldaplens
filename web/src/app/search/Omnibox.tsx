import * as Popover from '@radix-ui/react-popover'
import { ChevronDown, Crosshair, History, Search, SlidersHorizontal, Trash2, X } from 'lucide-react'
import { useEffect, useEffectEvent, useRef, useState } from 'react'
import { format, fromGroup, parse, serialize, toGroup, type Group } from '../../lib/filter'
import { defaultColumns, exampleSearches } from '../../lib/nav'
import { classifyQuery, nameQuery } from '../../lib/query'
import { keys, pushHistory, type SavedSearch, type SearchState } from '../../lib/storage'
import type { Scope } from '../../lib/types'
import { Button } from '../../ui/Button'
import { cn } from '../../ui/cn'
import { Input, inputLook } from '../../ui/Input'
import { Kbd } from '../../ui/Kbd'
import { useWorkspace } from '../context'
import { FOCUS_FILTER, FOCUS_LIST, FORMAT_FILTER, RUN_SEARCH } from '../events'
import { searchKey } from '../queries'
import { useStored } from '../useStored'
import { ColumnPicker } from './ColumnPicker'
import { FilterBuilder } from './FilterBuilder'
import { FilterEditor, type FilterEditorHandle } from './FilterEditor'

interface Draft {
  text: string
  base: string
  scope: Scope
  columns: string[]
}

// A name search shows the name typed, not its filter.
export const queryText = (s: SearchState) => nameQuery(s.filter) ?? s.filter

function toDraft(s: SearchState | null, fallbackColumns: string[]): Draft {
  return {
    text: s ? queryText(s) : '',
    base: s?.base ?? '',
    scope: s?.scope ?? 'sub',
    columns: s?.attrs.length ? s.attrs : fallbackColumns,
  }
}

/** What Enter does with a draft: run a search, open a DN, or say why the filter cannot run. */
function resolve(
  d: Draft,
  baseDN: string,
): { state: SearchState } | { dn: string } | { error: string } {
  const q = classifyQuery(d.text, baseDN)
  if (q.kind === 'dn') return { dn: q.dn }
  let filter = '(objectClass=*)'
  if (q.kind === 'name') filter = q.filter
  if (q.kind === 'filter') {
    const r = parse(q.filter)
    if (!r.ok) return { error: r.error.message }
    filter = serialize(r.filter)
  }
  return { state: { base: d.base.trim(), scope: d.scope, filter, attrs: d.columns } }
}

/**
 * List children's search (every entry one level under a container). It fills the list, not the
 * bar: a name typed next still runs under the bar's own base and scope.
 */
const isListing = (s: SearchState | null) => s?.scope === 'one' && s.filter === '(objectClass=*)'

const SCOPES: unknown[] = ['base', 'one', 'sub']
const SCOPE_LABEL: Record<Scope, string> = { base: 'base', one: 'one level', sub: 'subtree' }

/** load() hands back any JSON; only well-formed stored searches are used. */
function isSearch(x: unknown): x is SearchState {
  const s = x as Partial<SearchState> | null
  return (
    typeof s?.filter === 'string' &&
    SCOPES.includes(s.scope) &&
    typeof s.base === 'string' &&
    Array.isArray(s.attrs)
  )
}

export const isSaved = (x: unknown): x is SavedSearch =>
  isSearch(x) && typeof (x as Partial<SavedSearch>).name === 'string'

export const list = (v: unknown): unknown[] => (Array.isArray(v) ? v : [])

const menuItem = 'block w-full rounded-md px-2 py-1 text-left hover:bg-hover'
const popover = 'z-50 elevated rounded-[10px] border border-border bg-bg'
const heading = 'px-2 pt-1 text-[11px] font-semibold text-muted uppercase'

function SearchLine({ s }: { s: SearchState }) {
  return (
    <>
      <span className="block truncate font-mono text-xs">{queryText(s)}</span>
      <span className="block truncate text-[11px] text-muted">
        {s.scope} {s.base ? `under ${s.base}` : 'under base DN'}
      </span>
    </>
  )
}

/**
 * The header's query bar: a name, an LDAP filter or a DN (classifyQuery); Enter runs the search
 * or opens the DN. Base, scope and columns, the filter builder and the recent and saved searches
 * open from it.
 */
export function Omnibox() {
  const ws = useWorkspace()
  const columns = defaultColumns(ws.info)
  const [draft, setDraft] = useState<Draft>(() =>
    toDraft(isListing(ws.search) ? null : ws.search, columns),
  )
  const [builder, setBuilder] = useState(false)
  const [group, setGroup] = useState<Group>({ type: 'and', children: [] })
  const [notice, setNotice] = useState('')
  const [saveName, setSaveName] = useState('')
  const editor = useRef<FilterEditorHandle>(null)
  const [storedHistory, setHistory] = useStored<unknown>(keys.history(ws.conn), [])
  const [storedSaved, setSaved] = useStored<unknown>(keys.saved(ws.conn), [])
  const history = list(storedHistory).filter(isSearch)
  const saved = list(storedSaved).filter(isSaved)

  const current = resolve(draft, ws.info.baseDN)
  const same = (a: SearchState, b: SearchState) =>
    searchKey(ws.conn, a).join('\0') === searchKey(ws.conn, b).join('\0')

  // A search arriving through the URL (history, palette, tree) replaces the
  // draft; our own run comes back the same way and keeps the text as typed.
  const [synced, setSynced] = useState(ws.search)
  if (ws.search !== synced) {
    setSynced(ws.search)
    if (
      !isListing(ws.search) &&
      !(ws.search && 'state' in current && same(ws.search, current.state))
    ) {
      setDraft(toDraft(ws.search, columns))
    }
  }

  const focus = () => editor.current?.focus()
  const run = (d: Draft = draft) => {
    const r = resolve(d, ws.info.baseDN)
    if ('error' in r) {
      setNotice(`Fix the filter first: ${r.error}`)
      focus()
      return
    }
    setNotice('')
    if ('dn' in r) {
      ws.select(r.dn)
      return
    }
    setHistory(pushHistory(history, r.state))
    ws.runSearch(r.state)
  }
  const formatFilter = () => {
    const q = classifyQuery(draft.text, ws.info.baseDN)
    if (q.kind !== 'filter') return
    const r = parse(q.filter)
    if (!r.ok) {
      setNotice(`Fix the filter first: ${r.error.message}`)
      focus()
      return
    }
    setNotice('')
    setDraft((d) => ({ ...d, text: format(r.filter) }))
  }

  // Down from the bar steps into an open list (see Results).
  const toList = () => {
    if (!ws.search) return false
    window.dispatchEvent(new Event(FOCUS_LIST))
    return true
  }

  // Requests from the command palette and the global "/" key.
  const onFocusFilter = useEffectEvent(focus)
  const onRunSearch = useEffectEvent(() => run())
  const onFormatFilter = useEffectEvent(formatFilter)
  useEffect(() => {
    const handlers: [string, () => void][] = [
      [FOCUS_FILTER, () => onFocusFilter()],
      [RUN_SEARCH, () => onRunSearch()],
      [FORMAT_FILTER, () => onFormatFilter()],
    ]
    for (const [type, fn] of handlers) window.addEventListener(type, fn)
    return () => {
      for (const [type, fn] of handlers) window.removeEventListener(type, fn)
    }
  }, [])

  // The builder starts from the filter the text runs: a name shows its name filter.
  const openBuilder = (open: boolean) => {
    if (open) {
      const q = classifyQuery(draft.text, ws.info.baseDN)
      const text = q.kind === 'filter' || q.kind === 'name' ? q.filter : ''
      const r = text ? parse(text) : null
      if (r && !r.ok) {
        setNotice(`The builder needs a valid filter: ${r.error.message}`)
        return
      }
      setGroup(r ? toGroup(r.filter) : { type: 'and', children: [] })
      setNotice('')
    }
    setBuilder(open)
  }
  const fromBuilder = (g: Group) => {
    setGroup(g)
    setDraft((d) => ({ ...d, text: g.children.length ? serialize(fromGroup(g)) : '' }))
  }
  const clear = () => {
    setDraft((d) => ({ ...d, text: '' }))
    setNotice('')
    focus()
  }
  // Stored searches go through the draft, so they run canonical like typed ones.
  const pick = (s: SearchState) => {
    const d = toDraft(s, columns)
    setDraft(d)
    run(d)
  }

  return (
    <div
      role="search"
      className="relative flex h-8 min-w-[27rem] flex-1 items-center gap-1 rounded-md border border-border-strong bg-bg pr-1 pl-2 focus-within:border-accent focus-within:ring-3 focus-within:ring-accent/30 focus-within:outline-hidden"
    >
      <Search aria-hidden className="h-3.5 w-3.5 shrink-0 text-muted" />
      <div className="relative h-full min-w-16 flex-1">
        <FilterEditor
          ref={editor}
          value={draft.text}
          onChange={(text) => {
            setDraft((d) => ({ ...d, text }))
            setNotice('')
          }}
          onRun={run}
          onDown={toList}
          onFormat={formatFilter}
          schema={ws.schema}
          baseDN={ws.info.baseDN}
        />
      </div>
      {draft.text ? (
        <Button
          size="icon"
          variant="ghost"
          aria-label="Clear the search"
          title="Clear"
          onClick={clear}
        >
          <X className="h-3.5 w-3.5" />
        </Button>
      ) : (
        <span title="Press / to focus the search">
          <Kbd>/</Kbd>
        </span>
      )}
      <Popover.Root>
        <Popover.Trigger asChild>
          <button
            type="button"
            title="Search base, scope and columns"
            className="flex h-6 min-w-0 shrink items-center gap-1.5 border-l border-border-strong pr-1 pl-2 text-xs text-muted hover:text-fg"
          >
            <span className="min-w-16 truncate font-mono font-medium text-fg">
              {draft.base.trim() || ws.info.baseDN}
            </span>
            <span className="shrink-0 rounded bg-panel px-1.5 text-[11px]">
              {SCOPE_LABEL[draft.scope]}
            </span>
            <ChevronDown aria-hidden className="h-3.5 w-3.5 shrink-0" />
          </button>
        </Popover.Trigger>
        <Popover.Portal>
          <Popover.Content
            align="end"
            sideOffset={8}
            className={cn(popover, 'w-[30rem] space-y-2 p-2')}
          >
            <div className="flex items-center gap-1">
              <label className="flex min-w-0 flex-1 items-center gap-1">
                <span className="w-16 shrink-0 text-xs text-muted">Base</span>
                <Input
                  mono
                  value={draft.base}
                  placeholder={ws.info.baseDN}
                  onChange={(e) => setDraft((d) => ({ ...d, base: e.target.value }))}
                  className="min-w-0 flex-1"
                />
              </label>
              <Button
                size="icon"
                variant="ghost"
                aria-label="Search under the selected entry"
                title="Use the entry selected in the tree as the search base"
                disabled={!ws.dn}
                onClick={() => setDraft((d) => ({ ...d, base: ws.dn }))}
              >
                <Crosshair className="h-3.5 w-3.5" />
              </Button>
            </div>
            <label className="flex items-center gap-1">
              <span className="w-16 text-xs text-muted">Scope</span>
              <select
                value={draft.scope}
                onChange={(e) => setDraft((d) => ({ ...d, scope: e.target.value as Scope }))}
                className={`h-[var(--ctl)] px-1 text-xs ${inputLook}`}
              >
                <option value="sub">Subtree</option>
                <option value="one">One level</option>
                <option value="base">Base entry</option>
              </select>
            </label>
            <div role="group" aria-label="Columns" className="flex items-start gap-1">
              <span className="w-16 shrink-0 pt-1.5 text-xs text-muted">Columns</span>
              <ColumnPicker
                columns={draft.columns}
                onChange={(columns) => setDraft((d) => ({ ...d, columns }))}
              />
            </div>
          </Popover.Content>
        </Popover.Portal>
      </Popover.Root>
      <Popover.Root open={builder} onOpenChange={openBuilder}>
        <Popover.Trigger asChild>
          <Button size="icon" variant="ghost" aria-label="Filter builder" title="Filter builder">
            <SlidersHorizontal className="h-4 w-4" />
          </Button>
        </Popover.Trigger>
        <Popover.Portal>
          <Popover.Content
            align="end"
            sideOffset={8}
            className={cn(popover, 'flex w-[40rem] max-w-[90vw] p-2')}
          >
            <FilterBuilder group={group} onChange={fromBuilder} attrNames={ws.schema.attrNames} />
          </Popover.Content>
        </Popover.Portal>
      </Popover.Root>
      <Popover.Root>
        <Popover.Trigger asChild>
          <Button
            size="icon"
            variant="ghost"
            aria-label="Recent and saved searches"
            title="Recent and saved searches"
          >
            <History className="h-4 w-4" />
          </Button>
        </Popover.Trigger>
        <Popover.Portal>
          <Popover.Content
            align="end"
            sideOffset={8}
            className={cn(popover, 'max-h-[70vh] w-96 overflow-auto p-1')}
          >
            <form
              className="flex gap-1 p-1"
              onSubmit={(e) => {
                e.preventDefault()
                const name = saveName.trim()
                if (!name || !('state' in current)) return
                setSaved([...saved.filter((x) => x.name !== name), { ...current.state, name }])
                setSaveName('')
              }}
            >
              <Input
                value={saveName}
                onChange={(e) => setSaveName(e.target.value)}
                placeholder="Name this search"
                className="flex-1"
              />
              <Button type="submit" size="sm" disabled={!saveName.trim() || !('state' in current)}>
                Save
              </Button>
            </form>
            <p className={heading}>Saved</p>
            {saved.length === 0 && (
              <p className="px-2 py-1 text-xs text-muted">No saved searches yet.</p>
            )}
            {saved.map((s) => (
              <div key={s.name} className="flex items-center gap-1">
                <Popover.Close asChild>
                  <button
                    type="button"
                    className={cn(menuItem, 'min-w-0 flex-1')}
                    onClick={() => pick(s)}
                  >
                    <span className="block font-medium">{s.name}</span>
                    <SearchLine s={s} />
                  </button>
                </Popover.Close>
                <Button
                  size="icon"
                  variant="ghost"
                  aria-label={`Delete ${s.name}`}
                  onClick={() => setSaved(saved.filter((x) => x.name !== s.name))}
                >
                  <Trash2 className="h-3.5 w-3.5" />
                </Button>
              </div>
            ))}
            <p className={heading}>Recent</p>
            {history.length === 0 && (
              <p className="px-2 py-1 text-xs text-muted">No recent searches yet.</p>
            )}
            {history.map((s, i) => (
              <Popover.Close asChild key={i}>
                <button type="button" className={menuItem} onClick={() => pick(s)}>
                  <SearchLine s={s} />
                </button>
              </Popover.Close>
            ))}
            <p className={heading}>Examples</p>
            {exampleSearches(ws.info).map(([label, filter]) => (
              <Popover.Close asChild key={label}>
                <button
                  type="button"
                  className={menuItem}
                  onClick={() => pick({ base: '', scope: 'sub', filter, attrs: columns })}
                >
                  <span className="block font-medium">{label}</span>
                  <span className="block truncate font-mono text-xs">{filter}</span>
                </button>
              </Popover.Close>
            ))}
          </Popover.Content>
        </Popover.Portal>
      </Popover.Root>
      <button
        type="button"
        title="Run the search (Enter)"
        onClick={() => run()}
        className={cn(
          'flex h-6 shrink-0 items-center gap-1.5 rounded-[5px] border pr-1 pl-2 text-xs font-semibold',
          draft.text.trim()
            ? 'border-accent bg-accent text-accent-fg hover:opacity-90'
            : 'border-border-strong text-muted hover:bg-hover hover:text-fg',
        )}
      >
        Run
        <kbd
          aria-hidden
          className="rounded border border-current/40 px-1 font-mono text-[10px] leading-4"
        >
          Enter
        </kbd>
      </button>
      {notice && (
        <p
          role="alert"
          className="absolute top-full left-0 z-40 mt-1 elevated rounded-md border border-border bg-bg px-2 py-1 text-xs text-danger"
        >
          {notice}
        </p>
      )}
    </div>
  )
}
