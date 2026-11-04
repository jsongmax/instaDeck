import { useInfiniteQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { api } from '../api/client'
import { formatAuditEventType, formatActionError } from '../api/errors'
import type { FleetMachine } from '../api/types'
import type { EnrichedAccount } from '../hooks/useFleetData'
import { ErrorIcon, RunningCircleIcon, Swatch } from './Icons'

const AUDIT_RESULTS: Record<string, string> = {
  ok: '成功',
  success: '成功',
  failure: '失败',
  intent: '已发起',
  challenge: '密码已通过',
  closed: '已关闭',
  pending: '已发起',
  unknown: '结果未知',
  enabled: '已开启',
  disabled: '已关闭',
}

function formatEventTime(iso: string): string {
  const ts = Date.parse(iso)
  if (Number.isNaN(ts)) return iso
  const d = new Date(ts)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(
    d.getMinutes(),
  )}:${pad(d.getSeconds())}`
}

export function ActivityPage({
  accountMap,
  machines,
}: {
  accountMap: Map<string, EnrichedAccount>
  machines: FleetMachine[]
}) {
  const [filters, setFilters] = useState<Record<string, string>>({})
  const [search, setSearch] = useState('')
  const changeFilter = (key: string, value: string) => setFilters(prev => ({ ...prev, [key]: value }))
  const activeFilters = {
    ...filters,
    ...(search ? {
      machineIds: machines.filter(machine => machine.name.includes(search) || machine.id.includes(search))
        .map(machine => machine.id).join(',') || '__no_matching_machine__',
    } : {}),
  }
  const initialQuery = useInfiniteQuery({
    queryKey: ['auditEventsInitial', activeFilters],
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam }) => api.getAuditEvents(pageParam, activeFilters),
    getNextPageParam: page => page.events.length >= 100 ? page.events[page.events.length - 1].Cursor : undefined,
  })
  const combined = initialQuery.data?.pages.flatMap(page => page.events) ?? []
  const sortedItems = [...new Map(combined.map(item => [item.Cursor, item])).values()]
  const hasMore = initialQuery.hasNextPage
  const loadingMore = initialQuery.isFetchingNextPage
  const handleLoadMore = () => initialQuery.fetchNextPage()
  const machineNameMap = new Map<string, string>()
  machines.forEach((m) => machineNameMap.set(m.id, m.name))

  return (
    <>
      <div className="page-head">
        <h1>操作记录</h1>
      </div>
      <div className="btn-row" style={{marginBottom: 12}}>
        <select className="field" aria-label="筛选账号" value={filters.credentialId ?? ''} onChange={e=>changeFilter('credentialId',e.target.value)}><option value="">全部账号</option>{[...accountMap.values()].map(a=><option key={a.credentialId} value={a.credentialId}>{a.name}</option>)}</select>
        <select className="field" aria-label="筛选结果" value={filters.result ?? ''} onChange={e=>changeFilter('result',e.target.value)}><option value="">全部结果</option>{['success','failure','unknown','intent'].map(v=><option key={v} value={v}>{AUDIT_RESULTS[v]}</option>)}</select>
        <select className="field" aria-label="筛选类型" value={filters.type ?? ''} onChange={e=>changeFilter('type',e.target.value)}><option value="">全部类型</option>{[['operation.','机器操作'],['credential.','账号'],['port.','端口'],['snapshot.','快照'],['notification.','通知']].map(([v,label])=><option key={v} value={v}>{label}</option>)}</select>
        <input className="field" aria-label="搜索机器名" placeholder="搜索机器名" value={search} onChange={e=>setSearch(e.target.value)}/>
        <a className="btn" href={`/api/v1/audit-events/export?${new URLSearchParams({ ...activeFilters, order: 'desc' })}`} download>导出 CSV</a>
      </div>
      {initialQuery.error && <p className="errline">{initialQuery.error.message}</p>}
      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <th>时间</th>
              <th>操作</th>
              <th>机器</th>
              <th>账号</th>
              <th>来源 IP</th>
              <th>结果</th>
            </tr>
          </thead>
          <tbody>
            {initialQuery.isLoading && sortedItems.length === 0 ? (
              <tr>
                <td colSpan={6} className="muted">
                  正在加载操作记录…
                </td>
              </tr>
            ) : sortedItems.length > 0 ? (
              sortedItems.map((item) => {
                const ev = item.Event
                const acct = ev.CredentialID
                  ? accountMap.get(ev.CredentialID)
                  : undefined
                const operationType = item.operationKind
                  ? formatAuditEventType(`operation.${item.operationKind}`)
                  : formatAuditEventType(ev.Type)
                const mName = ev.MachineID
                  ? machineNameMap.get(ev.MachineID) ?? ev.MachineID
                  : item.operationKind
                    ? operationType
                    : '—'
                const resStr = (ev.Result || 'ok').toLowerCase()
                const isBad =
                  resStr.includes('fail') ||
                  resStr.includes('err') ||
                  resStr.includes('denied') ||
                  resStr.includes('reject')
                const isStarted = resStr === 'pending' || resStr === 'intent' || resStr === 'unknown'
                const color = isBad
                  ? 'var(--bad)'
                  : isStarted
                    ? 'var(--muted)'
                    : 'var(--ok)'
                const resLabel = AUDIT_RESULTS[resStr] ?? ev.Result

                return (
                  <tr key={item.Cursor}>
                    <td className="num muted">{formatEventTime(ev.Time)}</td>
                    <td>{formatAuditEventType(ev.Type)}</td>
                    <td className="mono">{mName}</td>
                    <td>
                      {acct ? (
                        <span className="chip">
                          <Swatch color={acct.color} />
                          {acct.name}
                        </span>
                      ) : ev.CredentialID ? (
                        <span className="mono muted">{ev.CredentialID}</span>
                      ) : (
                        '—'
                      )}
                    </td>
                    <td className="mono muted">{ev.SourceAddress || '—'}</td>
                    <td>
                      <span
                        className="state"
                        style={{ '--c': color } as React.CSSProperties}
                      >
                        {isBad ? (
                          <ErrorIcon />
                        ) : (
                          <RunningCircleIcon />
                        )}
                        {resLabel}
                      </span>
                      {(isBad || resStr === 'unknown') && ev.detail ? <div className="errline">{formatActionError(ev.detail)}</div> : null}
                    </td>
                  </tr>
                )
              })
            ) : (
              <tr>
                <td colSpan={6} className="muted">
                  暂无操作记录。
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
      {hasMore ? (
        <div className="btn-row" style={{ marginTop: '12px' }}>
          <button
            type="button"
            className={`btn ${loadingMore ? 'loading' : ''}`}
            disabled={loadingMore}
            onClick={() => void handleLoadMore()}
          >
            加载更多
          </button>
        </div>
      ) : null}
    </>
  )
}
