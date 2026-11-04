import { useQueryClient } from '@tanstack/react-query'
import { useEffect, useRef, useState } from 'react'
import { api } from '../api/client'
import {
  ApiError,
  formatErrorCode,
  formatRelativeTime,
  getMachineStateMeta,
  selectPreferredSource,
} from '../api/errors'
import type { Account, ActionKind, FleetAccount, FleetMachine } from '../api/types'
import type { EnrichedAccount } from '../hooks/useFleetData'
import { useAppStore } from '../store/useAppStore'
import { AccountKeyDialog, CreateMachineDialog } from './Dialogs'
import { ErrorIcon, Icon, MachineStateBadge, Swatch } from './Icons'

function CountUpNumber({ value }: { value: number }) {
  const spanRef = useRef<HTMLSpanElement | null>(null)
  const rafRef = useRef<number>(0)

  useEffect(() => {
    const el = spanRef.current
    if (!el) return
    const reduced =
      typeof window !== 'undefined' &&
      typeof window.matchMedia === 'function' &&
      window.matchMedia('(prefers-reduced-motion: reduce)').matches
    if (!value || reduced) {
      el.textContent = String(value)
      return
    }
    const start = performance.now()
    const step = (now: number) => {
      if (!el.isConnected) return
      const p = Math.min(1, (now - start) / 620)
      el.textContent = String(Math.round(value * (1 - Math.pow(1 - p, 3))))
      if (p < 1) {
        rafRef.current = requestAnimationFrame(step)
      }
    }
    rafRef.current = requestAnimationFrame(step)
    return () => cancelAnimationFrame(rafRef.current)
  }, [value])

  return (
    <span ref={spanRef} className="tile-n num" data-count={value}>
      {value}
    </span>
  )
}

export function MachinesPage({
  accounts,
  accountMap,
  machines,
  fleetAccounts,
  filterAccountId,
  isLoading,
}: {
  accounts: EnrichedAccount[]
  accountMap: Map<string, EnrichedAccount>
  machines: FleetMachine[]
  fleetAccounts: FleetAccount[]
  filterAccountId?: string
  isLoading: boolean
}) {
  const queryClient = useQueryClient()
  const selectedMachineId = useAppStore((s) => s.selectedMachineId)
  const setSelectedMachineId = useAppStore((s) => s.setSelectedMachineId)
  const trackedActions = useAppStore((s) => s.trackedActions)
  const failedActionsByMachine = useAppStore((s) => s.failedActionsByMachine)
  const unknownActionMachines = useAppStore((s) => s.unknownActionMachines)
  const rowFlashes = useAppStore((s) => s.rowFlashes)
  const upsertTrackedAction = useAppStore((s) => s.upsertTrackedAction)
  const setMachineFailedAction = useAppStore((s) => s.setMachineFailedAction)
  const setMachineUnknownAction = useAppStore((s) => s.setMachineUnknownAction)
  const openTerminal = useAppStore((s) => s.openTerminal)
  const showToast = useAppStore((s) => s.showToast)

  const [searchQuery, setSearchQuery] = useState('')
  const [statusFilter, setStatusFilter] = useState<string>('all')
  const [createOpen, setCreateOpen] = useState(false)
  const [replaceKeyAccount, setReplaceKeyAccount] = useState<Account | null>(null)

  const currentAccount = filterAccountId ? accountMap.get(filterAccountId) : undefined

  const activeActionForMachine = (machineId: string) =>
    Object.values(trackedActions).find(
      (a) => a.machineId === machineId && a.state !== 'completed' && a.state !== 'unknown',
    )

  const isMachineBusy = (m: FleetMachine) =>
    getMachineStateMeta(m.state).isBusy || Boolean(activeActionForMachine(m.id))

  const scopedMachines = machines.filter((m) => {
    if (m.state === 'deleted') return false
    if (!filterAccountId) return true
    return m.sources.some((s) => s.credentialId === filterAccountId)
  })

  const filteredMachines = scopedMachines.filter((m) => {
    if (searchQuery && !m.name.toLowerCase().includes(searchQuery.toLowerCase())) {
      return false
    }
    if (statusFilter === 'all') return true
    if (statusFilter === 'busy') return isMachineBusy(m)
    return m.state === statusFilter
  })

  const scopedAccounts = filterAccountId
    ? accounts.filter((a) => a.credentialId === filterAccountId)
    : accounts

  const scopedFleetAccounts = filterAccountId
    ? fleetAccounts.filter((fa) => fa.credentialId === filterAccountId)
    : fleetAccounts

  const erroredAccounts = scopedAccounts.filter(
    (a) => a.status === 'invalid' || Boolean(a.fleetInfo?.error),
  )

  const incompleteAccounts = scopedAccounts.filter(
    (a) => a.fleetInfo && !a.fleetInfo.complete && !a.fleetInfo.error && a.status === 'valid',
  )

  const allScopedAccountsFailed =
    (scopedFleetAccounts.length > 0 &&
      scopedFleetAccounts.every((fa) => fa.error !== null)) ||
    (scopedAccounts.length > 0 &&
      scopedAccounts.every((a) => a.status === 'invalid' || Boolean(a.fleetInfo?.error)))

  const executeAction = async (
    machine: FleetMachine,
    kind: Exclude<ActionKind, 'create' | 'delete'>,
    explicitCredentialId?: string,
  ) => {
    const preferred = selectPreferredSource(machine)
    const credentialId =
      explicitCredentialId ?? filterAccountId ?? preferred?.credentialId ?? ''
    if (!credentialId) return

    // Generate a fresh clientRequestId for this user operation (including when clicking Retry)
    const clientRequestId = crypto.randomUUID()
    setMachineFailedAction(machine.id, null)
    setMachineUnknownAction(machine.id, false)

    try {
      const action = await api.runMachineAction(machine.id, {
        clientRequestId,
        credentialId,
        kind,
      })
      upsertTrackedAction(action)
      await queryClient.invalidateQueries({ queryKey: ['fleet'] })
    } catch (err) {
      const code = err instanceof ApiError ? err.code : 'operation_rejected'
      setMachineFailedAction(machine.id, {
        actionId: clientRequestId,
        kind,
        credentialId,
        errorCode: code,
      })
      showToast(err instanceof ApiError ? err.message : formatErrorCode(code))
    }
  }

  const handleOpenTerminal = (machine: FleetMachine) => {
    if (machine.state !== 'running') return
    const preferred = selectPreferredSource(machine)
    const credId = filterAccountId ?? preferred?.credentialId ?? ''
    const acct = accountMap.get(credId)
    openTerminal({
      machineId: machine.id,
      machineName: machine.name,
      credentialId: credId,
      accountName: acct?.name ?? credId,
      accountColor: acct?.color ?? 'var(--a1)',
    })
  }

  const runningCount = scopedMachines.filter((m) => m.state === 'running').length
  const suspendedCount = scopedMachines.filter((m) => m.state === 'suspended').length
  const stoppedCount = scopedMachines.filter((m) => m.state === 'stopped').length
  const busyCount = scopedMachines.filter((m) => isMachineBusy(m)).length

  const statTiles = [
    { id: 'running', label: '运行中', count: runningCount, color: 'var(--ok)' },
    { id: 'suspended', label: '已暂停', count: suspendedCount, color: 'var(--warn)' },
    { id: 'stopped', label: '已停止', count: stoppedCount, color: 'var(--off)' },
    { id: 'busy', label: '进行中', count: busyCount, color: 'var(--busy)' },
  ]

  if (isLoading) {
    return (
      <>
        <div className="page-head">
          {currentAccount ? (
            <>
              <Swatch color={currentAccount.color} />
              <h1>{currentAccount.name}</h1>
            </>
          ) : (
            <h1>全部机器</h1>
          )}
        </div>
        <div className="stats">
          {statTiles.map((t) => (
            <button
              key={t.id}
              type="button"
              className="tile"
              style={{ '--c': t.color } as React.CSSProperties}
            >
              <span className="skel" style={{ width: '34px', height: '26px' }} />
              <span className="tile-l">
                <span className="tile-dot" />
                {t.label}
              </span>
            </button>
          ))}
        </div>
        <div className="table-wrap skel-table">
          {Array.from({ length: 6 }, (_, i) => (
            <div
              key={i}
              className="skel-row"
              style={{ '--i': i } as React.CSSProperties}
            >
              <span className="skel" style={{ width: '22%' }} />
              <span className="skel" style={{ width: '12%' }} />
              <span className="skel" style={{ width: '14%' }} />
              <span className="skel" style={{ width: '9%' }} />
              <span className="skel" style={{ width: '18%', marginLeft: 'auto' }} />
            </div>
          ))}
        </div>
      </>
    )
  }

  return (
    <>
      <div className="page-head">
        {currentAccount ? (
          <>
            <Swatch color={currentAccount.color} />
            <h1>{currentAccount.name}</h1>
          </>
        ) : (
          <h1>全部机器</h1>
        )}
        <span className="spacer" />
        <button
          type="button"
          className="btn primary"
          onClick={() => setCreateOpen(true)}
        >
          <Icon name="plus" />
          新建机器
        </button>
      </div>

      <div className="stats">
        {statTiles.map((t) => (
          <button
            key={t.id}
            type="button"
            className={`tile ${statusFilter === t.id ? 'on' : ''}`}
            style={{ '--c': t.color } as React.CSSProperties}
            aria-pressed={statusFilter === t.id}
            onClick={() => setStatusFilter((prev) => (prev === t.id ? 'all' : t.id))}
          >
            <CountUpNumber value={t.count} />
            <span className="tile-l">
              <span className="tile-dot" />
              {t.label}
            </span>
          </button>
        ))}
      </div>

      {erroredAccounts.map((a) => {
        const errCode = a.fleetInfo?.error?.code
        const errText = errCode ? formatErrorCode(errCode) : 'API Key 已失效'
        const notComplete = a.fleetInfo && !a.fleetInfo.complete
        return (
          <div key={a.credentialId} className="banner" role="alert">
            <ErrorIcon />
            <span>
              <b>{a.name}</b> 同步异常（{errText}
              {errCode ? `：${errCode}` : ''}）。
              {notComplete
                ? '该账号尚未成功获取过机器数据。'
                : '下面它的机器显示的是缓存数据，暂时不能操作。其他账号不受影响。'}
            </span>
            <button
              type="button"
              className="btn"
              onClick={() => setReplaceKeyAccount(a)}
            >
              替换 Key
            </button>
          </div>
        )
      })}

      {incompleteAccounts.map((a) => (
        <div key={a.credentialId} className="banner warn">
          <span>
            <b>{a.name}</b> 尚未完成首次获取，正在后台同步中…
          </span>
        </div>
      ))}

      <div className="tools">
        <input
          className="field search"
          id="q"
          placeholder="搜索机器名称"
          aria-label="搜索机器名称"
          value={searchQuery}
          onChange={(e) => setSearchQuery(e.target.value)}
        />
        <select
          className="field"
          id="status"
          aria-label="按状态筛选"
          value={statusFilter}
          onChange={(e) => setStatusFilter(e.target.value)}
        >
          <option value="all">全部状态</option>
          <option value="running">运行中</option>
          <option value="suspended">已暂停</option>
          <option value="stopped">已停止</option>
          <option value="busy">进行中</option>
        </select>
        <span className="muted num" style={{ fontSize: '12.5px' }}>
          {filteredMachines.length} 台
        </span>
      </div>

      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <th>名称</th>
              <th>账号</th>
              <th>状态</th>
              <th>更新于</th>
              <th style={{ textAlign: 'right' }}>操作</th>
            </tr>
          </thead>
          <tbody>
            {filteredMachines.length > 0 ? (
              filteredMachines.map((m, index) => {
                const prefSource = selectPreferredSource(m)
                const primaryCredId =
                  filterAccountId && m.sources.some((s) => s.credentialId === filterAccountId)
                    ? filterAccountId
                    : prefSource?.credentialId ?? ''
                const acct = accountMap.get(primaryCredId)
                const activeAction = activeActionForMachine(m.id)
                const busy = isMachineBusy(m)
                const locked =
                  !acct ||
                  acct.status === 'invalid' ||
                  Boolean(acct.fleetInfo?.error) ||
                  !acct.enabled
                const disabled = locked || busy
                const tip = (label: string) =>
                  locked ? 'Key 已失效或已暂停，无法操作' : label
                const extraSources = Math.max(0, m.sources.length - 1)
                const failedInfo = failedActionsByMachine[m.id]
                const isUnknownResult = unknownActionMachines[m.id]
                const flash = rowFlashes[m.id]
                const relTime = formatRelativeTime(m.fetchedAt)

                return (
                  <tr
                    key={m.id}
                    className={`rowlink ${selectedMachineId === m.id ? 'sel' : ''} ${
                      m.stale ? 'stale' : ''
                    } ${busy ? 'row-busy' : ''} ${flash ? `flash-${flash}` : ''}`}
                    style={{ '--i': index } as React.CSSProperties}
                    tabIndex={0}
                    onClick={() =>
                      setSelectedMachineId(selectedMachineId === m.id ? null : m.id)
                    }
                    onKeyDown={(e) => {
                      if (e.key === 'Enter' && e.target === e.currentTarget) {
                        setSelectedMachineId(selectedMachineId === m.id ? null : m.id)
                      }
                    }}
                  >
                    <td>
                      <span className="mname">{m.name}</span>
                    </td>
                    <td>
                      <span
                        className={`chip ${extraSources > 0 ? 'shared' : ''}`}
                        data-more={
                          extraSources > 0 ? `+${extraSources} 个账号可见` : undefined
                        }
                      >
                        <Swatch color={acct?.color ?? 'var(--muted)'} />
                        {acct?.name ?? primaryCredId}
                      </span>
                    </td>
                    <td>
                      <MachineStateBadge
                        state={m.state}
                        phase={activeAction?.phase}
                        forceBusy={Boolean(activeAction)}
                      />
                      {m.conflict ? (
                        <div className="warnline">账号之间状态不一致</div>
                      ) : null}
                      {failedInfo ? (
                        <div
                          className="errline"
                          onClick={(e) => e.stopPropagation()}
                        >
                          失败：{formatErrorCode(failedInfo.errorCode)}（
                          {failedInfo.errorCode}）{' '}
                          {failedInfo.kind !== 'create' && failedInfo.kind !== 'delete' ? (
                            <button
                              type="button"
                              className="linkish"
                              onClick={() =>
                                void executeAction(
                                  m,
                                  failedInfo.kind as Exclude<
                                    ActionKind,
                                    'create' | 'delete'
                                  >,
                                  failedInfo.credentialId,
                                )
                              }
                            >
                              重试
                            </button>
                          ) : null}
                        </div>
                      ) : null}
                      {isUnknownResult && !failedInfo ? (
                        <div className="warnline">结果未知，请刷新确认</div>
                      ) : null}
                    </td>
                    <td className="muted num" style={{ fontSize: '12.5px' }}>
                      {m.stale ? (
                        <span style={{ color: 'var(--warn)' }}>{relTime}（过期）</span>
                      ) : (
                        relTime
                      )}
                    </td>
                    <td onClick={(e) => e.stopPropagation()}>
                      <div className="row-actions">
                        {busy ? (
                          <span className="muted" style={{ fontSize: '12px' }}>
                            等待完成…
                          </span>
                        ) : m.state === 'running' ? (
                          <>
                            <button
                              type="button"
                              className="btn icon-only"
                              disabled={disabled}
                              aria-label={`暂停：${m.name}`}
                              data-tip={tip('暂停')}
                              onClick={() => void executeAction(m, 'suspend')}
                            >
                              <Icon name="pause" />
                            </button>
                            <button
                              type="button"
                              className="btn icon-only"
                              disabled={disabled}
                              aria-label={`停止：${m.name}`}
                              data-tip={tip('停止')}
                              onClick={() => void executeAction(m, 'stop')}
                            >
                              <Icon name="stop" />
                            </button>
                            <button
                              type="button"
                              className="btn btn-term"
                              disabled={disabled}
                              aria-label={`打开终端：${m.name}`}
                              data-tip={tip('打开终端')}
                              onClick={() => handleOpenTerminal(m)}
                            >
                              <Icon name="term" />
                              终端
                            </button>
                          </>
                        ) : m.state === 'suspended' ? (
                          <button
                            type="button"
                            className="btn icon-only"
                            disabled={disabled}
                            aria-label={`恢复：${m.name}`}
                            data-tip={tip('恢复')}
                            onClick={() => void executeAction(m, 'resume')}
                          >
                            <Icon name="play" />
                          </button>
                        ) : m.state === 'stopped' ? (
                          <button
                            type="button"
                            className="btn icon-only"
                            disabled={disabled}
                            aria-label={`启动：${m.name}`}
                            data-tip={tip('启动')}
                            onClick={() => void executeAction(m, 'start')}
                          >
                            <Icon name="play" />
                          </button>
                        ) : null}
                      </div>
                    </td>
                  </tr>
                )
              })
            ) : allScopedAccountsFailed ? (
              <tr>
                <td colSpan={5} style={{ color: 'var(--bad)' }}>
                  全部账号获取失败，无法加载机器列表，请根据上方错误提示检查或替换 API Key。
                </td>
              </tr>
            ) : incompleteAccounts.length > 0 && scopedMachines.length === 0 ? (
              <tr>
                <td colSpan={5} className="muted">
                  账号正在进行首次同步，请稍候…
                </td>
              </tr>
            ) : (
              <tr>
                <td colSpan={5} className="muted">
                  {accounts.length === 0 ? (
                    <>
                      还没有添加 InstaBox 账号。<a href="#accounts">去添加账号</a>
                    </>
                  ) : (
                    '没有符合条件的机器。'
                  )}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      <CreateMachineDialog
        open={createOpen}
        accounts={accounts}
        defaultCredentialId={filterAccountId}
        onClose={() => setCreateOpen(false)}
      />

      <AccountKeyDialog
        open={replaceKeyAccount !== null}
        targetAccount={replaceKeyAccount}
        onClose={() => setReplaceKeyAccount(null)}
      />
    </>
  )
}
