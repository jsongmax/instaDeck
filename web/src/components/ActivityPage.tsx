import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { api } from '../api/client'
import { formatAuditEventType } from '../api/errors'
import type { AuditEventItem, FleetMachine } from '../api/types'
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
  const [extraItems, setExtraItems] = useState<AuditEventItem[]>([])
  const [nextCursor, setNextCursor] = useState<string | undefined>(undefined)
  const [hasMoreOverride, setHasMoreOverride] = useState<boolean | null>(null)
  const [loadingMore, setLoadingMore] = useState<boolean>(false)

  const initialQuery = useQuery({
    queryKey: ['auditEventsInitial'],
    queryFn: () => api.getAuditEvents(undefined),
  })

  const firstPage = initialQuery.data?.events ?? []
  const existingCursors = new Set(firstPage.map((x) => x.Cursor))
  const combined = [
    ...firstPage,
    ...extraItems.filter((x) => !existingCursors.has(x.Cursor)),
  ]
  const activeCursor =
    nextCursor ?? (combined.length > 0 ? combined[combined.length - 1].Cursor : undefined)
  const hasMore = hasMoreOverride !== null ? hasMoreOverride : firstPage.length >= 100

  const handleLoadMore = async () => {
    if (!activeCursor || loadingMore) return
    setLoadingMore(true)
    try {
      const res = await api.getAuditEvents(activeCursor)
      const batch = res.events ?? []
      setExtraItems((prev) => {
        const seen = new Set(prev.map((x) => x.Cursor))
        return [...prev, ...batch.filter((x) => !seen.has(x.Cursor))]
      })
      if (batch.length > 0) {
        setNextCursor(batch[batch.length - 1].Cursor)
      }
      setHasMoreOverride(batch.length >= 100)
    } catch {
      // handled by api client
    } finally {
      setLoadingMore(false)
    }
  }

  // Display newest first
  const sortedItems = [...combined].reverse()
  const machineNameMap = new Map<string, string>()
  machines.forEach((m) => machineNameMap.set(m.id, m.name))

  return (
    <>
      <div className="page-head">
        <h1>操作记录</h1>
      </div>
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
                const mName = ev.MachineID
                  ? machineNameMap.get(ev.MachineID) ?? ev.MachineID
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
