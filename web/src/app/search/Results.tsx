import { keepPreviousData, useQuery, useQueryClient } from '@tanstack/react-query'
import { useVirtualizer } from '@tanstack/react-virtual'
import * as Popover from '@radix-ui/react-popover'
import { ArrowDown, ArrowUp, Columns3, Download, X } from 'lucide-react'
import { useEffect, useEffectEvent, useMemo, useRef, useState, type KeyboardEvent } from 'react'
import { api } from '../../lib/api'
import { entryChanges } from '../../lib/changes'
import { cellValue, toCSV } from '../../lib/csv'
import { sameDN } from '../../lib/dn'
import { visible } from '../../lib/display'
import { entriesToLDIF } from '../../lib/ldif'
import { defaultColumns } from '../../lib/nav'
import { rowHeight, usePrefs } from '../../lib/prefs'
import {
  NAME_ATTRS,
  NAME_COLUMN,
  RESULT_LIMITS,
  asText,
  nameOf,
  nextLimit,
  nextSort,
  sortEntries,
  withColumns,
  type Sort,
} from '../../lib/results'
import { isModKey } from '../../lib/shortcuts'
import type { SearchState } from '../../lib/storage'
import { ChangeDot } from '../../ui/Badge'
import { Banner, ErrorBanner } from '../../ui/Banner'
import { Button } from '../../ui/Button'
import { cn } from '../../ui/cn'
import { download } from '../../ui/download'
import { Spinner } from '../../ui/Spinner'
import { useBatch } from '../batch'
import { useWorkspace } from '../context'
import { FOCUS_LIST } from '../events'
import { searchKey } from '../queries'
import { ColumnPicker } from './ColumnPicker'

// The sticky header row (h-7 buttons plus a 1px bottom border) sits in the scroll element
// above the rows. The virtualizer does not know about it, so scrolling to a row reserves
// this much at the bottom of the viewport. ponytail: assumes the 16px rem that h-7 is
// based on; measure the header if the root font size ever changes.
const HEADER_HEIGHT = 29

// The smallest widths of the Name column and of each other column. Name, uid and mail fit a
// 22rem pane; more columns scroll sideways, header and rows together.
const NAME_REM = 10
const COLUMN_REM = 6

const popover = 'z-50 elevated rounded-[10px] border border-border bg-bg'
const menuItem =
  'flex w-full items-baseline justify-between gap-2 rounded-md px-2 py-1 text-left hover:bg-hover'

/** The list pane: the results of ws.search, or of List children. */
export function Results({ search }: { search: SearchState }) {
  const ws = useWorkspace()
  // Keyed per run so the limit, the sort and the scroll position start over.
  return <Listing key={[...searchKey(ws.conn, search), ws.runs].join('\0')} search={search} />
}

function Listing({ search }: { search: SearchState }) {
  const ws = useWorkspace()
  const qc = useQueryClient()
  const { density } = usePrefs()
  const batch = useBatch(ws.conn)
  const key = searchKey(ws.conn, search)
  const attrs = search.attrs.length ? search.attrs : defaultColumns(ws.info)
  // Load more runs the same search again with the next limit; the server keeps nothing between
  // requests. The rows shown stay until the bigger answer arrives.
  const [limit, setLimit] = useState(RESULT_LIMITS[0])
  const prev = RESULT_LIMITS.findLast((l) => l < limit)
  const q = useQuery({
    queryKey: [...key, limit],
    queryFn: ({ signal }) =>
      api.search(
        ws.conn,
        {
          base: search.base || ws.info.baseDN,
          scope: search.scope,
          filter: search.filter,
          // Duplicates are harmless: the server returns each attribute once.
          attributes: [...NAME_ATTRS, ...attrs],
          sizeLimit: limit,
        },
        signal,
      ),
    placeholderData: keepPreviousData,
    staleTime: Infinity,
    gcTime: 5 * 60_000,
  })
  const more = !!q.data?.more
  const next = nextLimit(limit)
  const [sort, setSort] = useState<Sort>(null)
  // Columns picked in the Columns popover; closing it runs the search with them.
  const [picked, setPicked] = useState(attrs)
  const entries = q.data?.entries
  const rows = useMemo(() => sortEntries(entries ?? [], sort), [entries, sort])
  // The active row is the selected entry, so it stays right after sorting, paging or a
  // selection made elsewhere (tree, breadcrumbs).
  const active = useMemo(
    () => (ws.dn ? rows.findIndex((r) => sameDN(r.dn, ws.dn)) : -1),
    [rows, ws.dn],
  )
  const refetch = () => void q.refetch()
  const cancel = () => {
    void qc.cancelQueries({ queryKey: key })
    // A canceled Load more leaves the smaller answer on screen: go back to its limit.
    if (q.isPlaceholderData && prev) setLimit(prev)
  }

  const scrollRef = useRef<HTMLDivElement>(null)
  // eslint-disable-next-line react-hooks/incompatible-library -- TanStack Virtual cannot be memoized; the app does not use the React Compiler, so this is informational only
  const virt = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => rowHeight(density),
    overscan: 12,
    scrollPaddingEnd: HEADER_HEIGHT,
  })
  useEffect(() => virt.measure(), [density, virt])

  const move = (i: number) => {
    if (i < 0 || i >= rows.length) return
    virt.scrollToIndex(i, { align: 'auto' })
    // Replace, so arrowing through rows does not leave one history entry per row.
    ws.select(rows[i].dn, { replace: true })
  }
  // Down in the query bar: the list takes focus on its selected row, else on the first.
  const onFocusList = useEffectEvent(() => {
    scrollRef.current?.focus()
    move(Math.max(active, 0))
  })
  useEffect(() => {
    const fn = () => onFocusList()
    window.addEventListener(FOCUS_LIST, fn)
    return () => window.removeEventListener(FOCUS_LIST, fn)
  }, [])

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    const step: Record<string, number> = { ArrowDown: 1, ArrowUp: -1, PageDown: 10, PageUp: -10 }
    if (e.key in step) {
      e.preventDefault()
      move(Math.min(Math.max(active + step[e.key], 0), rows.length - 1))
    } else if (e.key === 'Home' || e.key === 'End') {
      e.preventDefault()
      move(e.key === 'Home' ? 0 : rows.length - 1)
    } else if (isModKey(e, 'c') && active >= 0 && !getSelection()?.toString()) {
      // The row shows the name; Mod+C copies its DN unless text is selected.
      void navigator.clipboard?.writeText(rows[active].dn)
    }
  }

  const columns = [NAME_COLUMN, ...attrs]
  // One template for the header and every row, all inside one box at least as wide as the
  // columns' minimums, so the cells line up and the row backgrounds reach the last column.
  const grid = {
    gridTemplateColumns: `minmax(${NAME_REM}rem, 2fr) repeat(${attrs.length}, minmax(${COLUMN_REM}rem, 1fr))`,
  }
  const minWidth = `${NAME_REM + attrs.length * COLUMN_REM}rem`
  const items = virt.getVirtualItems()
  const canceled = q.isPending && q.fetchStatus === 'idle'

  return (
    <div
      className="flex h-full flex-col"
      onKeyDown={(e) => {
        // Esc with focus in the pane closes the list. A popover is portaled outside the pane:
        // Esc there closes only the popover.
        if (e.key === 'Escape' && e.currentTarget.contains(e.target as Node)) {
          e.preventDefault()
          ws.closeList()
        }
      }}
    >
      <div className="flex h-9 shrink-0 items-center gap-1 border-b border-border pr-1 pl-2 text-xs">
        <span className="min-w-0 truncate font-semibold">
          {q.isPending ? 'Searching…' : `${rows.length} ${rows.length === 1 ? 'entry' : 'entries'}`}
          <span className="font-normal text-muted">
            {more && ', more on the server'}
            {sort && more && ' (sorting loaded rows)'}
          </span>
        </span>
        {q.isFetching && <Spinner />}
        <span
          className="min-w-0 flex-1 truncate px-1 font-mono text-[11px]"
          title={`The filter that ran: ${search.filter} (${search.scope} under ${search.base || ws.info.baseDN})`}
        >
          {search.filter}
        </span>
        <div className="flex shrink-0 items-center gap-1">
          {q.isFetching && (
            <Button size="sm" onClick={cancel}>
              Cancel
            </Button>
          )}
          {more && next !== null && (
            <Button
              size="sm"
              disabled={q.isFetching}
              title={`Run the search again for up to ${next} entries`}
              onClick={() => setLimit(next)}
            >
              Load more
            </Button>
          )}
          <Popover.Root
            onOpenChange={(open) => {
              if (!open && picked.join() !== attrs.join())
                ws.runSearch({ ...search, attrs: picked })
            }}
          >
            <Popover.Trigger asChild>
              <Button
                size="icon"
                variant="ghost"
                aria-label="Columns"
                title={`Columns: ${['Name', ...attrs].join(', ')}. Pick others from the schema`}
              >
                <Columns3 className="h-3.5 w-3.5" />
              </Button>
            </Popover.Trigger>
            <Popover.Portal>
              <Popover.Content
                align="end"
                sideOffset={4}
                aria-label="Columns"
                className={cn(popover, 'flex w-80 p-2')}
              >
                <ColumnPicker columns={picked} onChange={setPicked} />
              </Popover.Content>
            </Popover.Portal>
          </Popover.Root>
          <Popover.Root>
            <Popover.Trigger asChild>
              <Button
                size="icon"
                variant="ghost"
                aria-label="Export"
                title="Export the loaded rows as CSV or LDIF"
                disabled={!rows.length}
              >
                <Download className="h-3.5 w-3.5" />
              </Button>
            </Popover.Trigger>
            <Popover.Portal>
              <Popover.Content
                align="end"
                sideOffset={4}
                aria-label="Export"
                className={cn(popover, 'w-64 p-1')}
              >
                <Popover.Close asChild>
                  <button
                    type="button"
                    className={menuItem}
                    onClick={() =>
                      download(`${ws.conn}-search.csv`, toCSV(rows, attrs), 'text/csv')
                    }
                  >
                    CSV <span className="text-xs text-muted">the DN and the columns</span>
                  </button>
                </Popover.Close>
                <Popover.Close asChild>
                  <button
                    type="button"
                    className={menuItem}
                    onClick={() =>
                      download(
                        `${ws.conn}-search.ldif`,
                        entriesToLDIF(withColumns(rows, attrs)),
                        'text/plain',
                      )
                    }
                  >
                    LDIF <span className="text-xs text-muted">the selected columns only</span>
                  </button>
                </Popover.Close>
              </Popover.Content>
            </Popover.Portal>
          </Popover.Root>
          <Button
            size="icon"
            variant="ghost"
            aria-label="Close the list"
            title="Close the list (Esc)"
            onClick={ws.closeList}
          >
            <X className="h-3.5 w-3.5" />
          </Button>
        </div>
      </div>
      {canceled && (
        <Banner
          tone="info"
          action={
            <Button size="sm" onClick={refetch}>
              Run again
            </Button>
          }
        >
          Search canceled.
        </Banner>
      )}
      {q.isError && (
        <ErrorBanner
          error={q.error}
          action={
            <>
              {prev && (
                // The smaller answer stays cached for gcTime, so going back is instant.
                <Button size="sm" onClick={() => setLimit(prev)}>
                  Show the first {prev}
                </Button>
              )}
              <Button size="sm" onClick={refetch}>
                Retry
              </Button>
            </>
          }
        />
      )}
      {more && next === null && !q.isPlaceholderData && (
        <Banner tone="info">
          Showing the first {limit.toLocaleString('en-US')} entries; refine the search to see the
          rest
        </Banner>
      )}
      {q.data?.truncated && (
        <Banner tone="warn">
          The search stopped early (a size, time or response limit). Showing what arrived.
        </Banner>
      )}
      {!!q.data?.skipped?.length && (
        <Banner tone="warn">
          The server refused to search {q.data.skipped.join(' and ')} (usually because{' '}
          {q.data.skipped.length === 1 ? 'it is' : 'they are'} not indexed there), so entries that
          match only {q.data.skipped.length === 1 ? 'that part are' : 'those parts are'} missing.
        </Banner>
      )}
      {!!q.data?.referrals?.length && (
        <Banner tone="info">
          Referrals (not followed):{' '}
          {q.data.referrals.map((r) => (
            <span key={r} className="mr-2 font-mono text-xs">
              {r}
            </span>
          ))}
        </Banner>
      )}
      <div
        ref={scrollRef}
        role="grid"
        aria-label="Search results"
        aria-rowcount={rows.length + 1}
        tabIndex={0}
        aria-activedescendant={active >= 0 ? `result-${active}` : undefined}
        onKeyDown={onKeyDown}
        className="min-h-0 flex-1 overflow-auto outline-none"
      >
        <div style={{ minWidth }}>
          <div
            role="row"
            style={grid}
            className="sticky top-0 z-10 grid border-b border-border bg-panel text-xs"
          >
            {columns.map((c) => {
              const sorted = sort?.column === c
              return (
                <div
                  key={c}
                  role="columnheader"
                  aria-sort={sorted ? (sort.desc ? 'descending' : 'ascending') : 'none'}
                  className="min-w-0"
                >
                  <button
                    type="button"
                    onClick={() => setSort(nextSort(sort, c))}
                    className="flex h-7 w-full items-center gap-1 px-2 text-left font-semibold hover:bg-hover"
                  >
                    <span className="truncate">{c === NAME_COLUMN ? 'Name' : c}</span>
                    {sorted &&
                      (sort.desc ? (
                        <ArrowDown className="h-3 w-3 shrink-0" />
                      ) : (
                        <ArrowUp className="h-3 w-3 shrink-0" />
                      ))}
                  </button>
                </div>
              )
            })}
          </div>
          {q.isSuccess && rows.length === 0 && <p className="p-4 text-muted">No entries match.</p>}
          {/* Rows stay in normal flow under the header; the padding stands in for the rows
              scrolled past, the height for all of them. */}
          <div style={{ height: virt.getTotalSize(), paddingTop: items[0]?.start ?? 0 }}>
            {items.map((v) => {
              const e = rows[v.index]
              const selected = v.index === active
              return (
                <div
                  key={e.dn}
                  id={`result-${v.index}`}
                  role="row"
                  aria-selected={selected}
                  title={visible(e.dn)}
                  onClick={() => ws.select(e.dn)}
                  style={{ ...grid, height: v.size }}
                  className={cn(
                    'grid cursor-default items-center border-b border-border/50 hover:bg-hover',
                    selected && 'bg-selected shadow-[inset_2px_0_0_var(--accent)]',
                  )}
                >
                  <div role="gridcell" className="truncate px-2 font-medium">
                    {entryChanges(batch, e.dn) && (
                      <span className="mr-1 align-middle">
                        <ChangeDot />
                      </span>
                    )}
                    {visible(nameOf(asText(e)))}
                  </div>
                  {attrs.map((c) => (
                    <div
                      key={c}
                      role="gridcell"
                      className="truncate px-2 font-mono text-xs"
                      title={cellValue(e, c)}
                    >
                      {cellValue(e, c)}
                    </div>
                  ))}
                </div>
              )
            })}
          </div>
        </div>
      </div>
    </div>
  )
}
