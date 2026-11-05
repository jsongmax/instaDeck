import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { api } from '../api/client'
import {
  ApiError,
  formatErrorCode,
  formatRelativeTime,
  getMachineStateMeta,
  selectPreferredSource,
} from '../api/errors'
import type { ActionKind, FleetMachine } from '../api/types'
import type { EnrichedAccount } from '../hooks/useFleetData'
import { useAppStore } from '../store/useAppStore'
import { DeleteMachineDialog, RenameMachineDialog } from './Dialogs'
import { FilesDialog } from './FilesDialog'
import { PortsPanel } from './PortsPanel'
import { SaveSnapshot } from './SnapshotsPage'
import { MachineStateBadge, Swatch } from './Icons'

const OUTSIDE_CLICK_IGNORED =
  '.drawer, tr.rowlink, button, a, input, select, textarea, label, .win, .login, .toast'

export function MachineDrawer({
  machines,
  accountMap,
}: {
  machines: FleetMachine[]
  accountMap: Map<string, EnrichedAccount>
}) {
  const queryClient = useQueryClient()
  const selectedMachineId = useAppStore((s) => s.selectedMachineId)
  const setSelectedMachineId = useAppStore((s) => s.setSelectedMachineId)
  const trackedActions = useAppStore((s) => s.trackedActions)
  const upsertTrackedAction = useAppStore((s) => s.upsertTrackedAction)
  const setMachineFailedAction = useAppStore((s) => s.setMachineFailedAction)
  const setMachineUnknownAction = useAppStore((s) => s.setMachineUnknownAction)
  const openTerminal = useAppStore((s) => s.openTerminal)
  const showToast = useAppStore((s) => s.showToast)

  const machine = machines.find((m) => m.id === selectedMachineId) ?? null
  const [selectedCredByMachine, setSelectedCredByMachine] = useState<Record<string, string>>({})
  const [deleteTargetId, setDeleteTargetId] = useState<string | null>(null)
  const [renameTargetId, setRenameTargetId] = useState<string | null>(null)
  // Pinned when opened: the preferred account can change on any refresh, and that must
  // not restart the dialog and its uploads.
  const [filesTarget, setFilesTarget] = useState<{ machineId: string; credentialId: string } | null>(
    null,
  )


  const preferredCredId = machine ? selectPreferredSource(machine)?.credentialId ?? '' : ''
  const customCredId = machine ? selectedCredByMachine[machine.id] : undefined
  const activeCredId =
    machine && customCredId && machine.sources.some((s) => s.credentialId === customCredId)
      ? customCredId
      : preferredCredId
  const deleteDialogOpen = Boolean(machine && deleteTargetId === machine.id)

  // A click on blank space outside closes the details. Controls, rows and text being
  // selected keep working as they did; rows switch or toggle the details themselves.
  useEffect(() => {
    if (!selectedMachineId) return
    const closeOnOutsideClick = (event: MouseEvent) => {
      const target = event.target
      if (!(target instanceof Element) || !target.isConnected) return
      if (document.querySelector('dialog[open]')) return
      if (!window.getSelection()?.isCollapsed) return
      if (target.closest(OUTSIDE_CLICK_IGNORED)) return
      setSelectedMachineId(null)
    }
    document.addEventListener('click', closeOnOutsideClick)
    return () => document.removeEventListener('click', closeOnOutsideClick)
  }, [selectedMachineId, setSelectedMachineId])

  const detailQuery = useQuery({
    queryKey: ['machineDetail', machine?.id, activeCredId],
    queryFn: () => api.getMachineDetail(machine!.id, activeCredId),
    enabled: Boolean(machine && activeCredId),
    staleTime: 10_000,
    retry: false,
  })

  if (!machine) return null

  const activeAccount = accountMap.get(activeCredId)
  const activeSource = machine.sources.find((s) => s.credentialId === activeCredId)
  const activeAction = Object.values(trackedActions).find(
    (a) => a.machineId === machine.id && a.state !== 'completed' && a.state !== 'unknown',
  )
  const isBusy = getMachineStateMeta(machine.state).isBusy || Boolean(activeAction)
  const isAccountInvalid =
    !activeAccount ||
    activeAccount.status === 'invalid' ||
    Boolean(activeAccount.fleetInfo?.error) ||
    !activeAccount.enabled
  const locked = isAccountInvalid || isBusy

  const canDeleteByAccount = Boolean(
    activeAccount &&
      activeAccount.enabled &&
      activeAccount.status === 'valid' &&
      (activeAccount.capabilities.delete === 'declared' ||
        activeAccount.capabilities.delete === 'verified') &&
      (activeSource ? activeSource.canDelete !== false : true),
  )

  const spec = detailQuery.data?.spec
  const memGiB = spec ? Math.round((spec.memMiB / 1024) * 10) / 10 : null
  const diskGiB = spec ? spec.persistentDiskGiB || spec.rootDiskGiB : null
  const specText = spec
    ? `${spec.vcpus} vCPU · ${memGiB} GiB · ${diskGiB} GiB`
    : detailQuery.isLoading
      ? '加载中…'
      : '—'

  const runAction = async (kind: Exclude<ActionKind, 'create' | 'delete'>) => {
    if (!activeCredId) return
    const clientRequestId = crypto.randomUUID()
    setMachineFailedAction(machine.id, null)
    setMachineUnknownAction(machine.id, false)
    try {
      const action = await api.runMachineAction(machine.id, {
        clientRequestId,
        credentialId: activeCredId,
        kind,
      })
      upsertTrackedAction(action)
      await queryClient.invalidateQueries({ queryKey: ['fleet'] })
    } catch (err) {
      const code = err instanceof ApiError ? err.code : 'operation_rejected'
      setMachineFailedAction(machine.id, {
        actionId: clientRequestId,
        kind,
        credentialId: activeCredId,
        errorCode: code,
      })
      showToast(err instanceof ApiError ? err.message : formatErrorCode(code))
    }
  }

  return (
    <div id="drawer">
      <aside className="drawer enter" aria-label="机器详情">
        <div className="drawer-head">
          <div className="drawer-title">
            <Swatch color={activeAccount?.color ?? 'var(--muted)'} />
            <span className="muted">{activeAccount?.name ?? activeCredId} /</span>
            <h2>{machine.name}</h2>
            <span className="spacer" />
            <button
              type="button"
              className="btn"
              aria-label="关闭详情"
              onClick={() => setSelectedMachineId(null)}
            >
              关闭
            </button>
          </div>
          <div>
            <MachineStateBadge
              state={machine.state}
              phase={activeAction?.phase}
              forceBusy={Boolean(activeAction)}
            />
          </div>
        </div>

        <div className="drawer-body">
          {machine.conflict ? (
            <div className="notice warn">账号之间状态不一致，请确认各账号同步状态。</div>
          ) : null}

          {machine.sources.length > 1 ? (
            <div>
              <div className="section-title" style={{ marginBottom: '4px' }}>
                操作账号（这台机器有 {machine.sources.length} 个账号可见）
              </div>
              <select
                className="field"
                style={{ width: '100%' }}
                aria-label="选择操作账号"
                value={activeCredId}
                onChange={(e) =>
                  setSelectedCredByMachine((prev) => ({
                    ...prev,
                    [machine.id]: e.target.value,
                  }))
                }
              >
                {machine.sources.map((src) => {
                  const a = accountMap.get(src.credentialId)
                  return (
                    <option key={src.credentialId} value={src.credentialId}>
                      {a?.name ?? src.credentialId}
                      {src.stale ? '（数据过期）' : ''}
                    </option>
                  )
                })}
              </select>
            </div>
          ) : null}

          <div>
            <div className="section-title" style={{ marginBottom: '6px' }}>
              操作
            </div>
            <div className="btn-row">
              {machine.state === 'running' ? (
                <>
                  <button
                    type="button"
                    className="btn"
                    disabled={locked}
                    onClick={() =>
                      openTerminal({
                        machineId: machine.id,
                        machineName: machine.name,
                        credentialId: activeCredId,
                        accountName: activeAccount?.name ?? activeCredId,
                        accountColor: activeAccount?.color ?? 'var(--a1)',
                      })
                    }
                  >
                    打开终端
                  </button>
                  <button
                    type="button"
                    className="btn"
                    disabled={locked}
                    onClick={() => void runAction('suspend')}
                  >
                    暂停
                  </button>
                  <button
                    type="button"
                    className="btn"
                    disabled={locked}
                    onClick={() => void runAction('stop')}
                  >
                    停止
                  </button>
                </>
              ) : null}
              {machine.state === 'suspended' ? (
                <button
                  type="button"
                  className="btn"
                  disabled={locked}
                  onClick={() => void runAction('resume')}
                >
                  恢复
                </button>
              ) : null}
              {machine.state === 'stopped' ? (
                <button
                  type="button"
                  className="btn"
                  disabled={locked}
                  onClick={() => void runAction('start')}
                >
                  启动
                </button>
              ) : null}
              <button
                type="button"
                className="btn"
                disabled={
                  locked ||
                  machine.state !== 'running' ||
                  activeAccount?.capabilities.write === 'denied'
                }
                onClick={() => setFilesTarget({ machineId: machine.id, credentialId: activeCredId })}
              >
                文件
              </button>
              <button
                type="button"
                className="btn"
                disabled={
                  locked ||
                  machine.state !== 'running' ||
                  activeAccount?.capabilities.write === 'denied'
                }
                onClick={() =>
                  openTerminal({ kind: 'desktop', machineId: machine.id, machineName: machine.name, credentialId: activeCredId, accountName: activeAccount?.name ?? activeCredId, accountColor: activeAccount?.color ?? 'var(--a1)' })
                }
              >
                桌面
              </button>
              {/* Upstream allows a rename in any state, also while an operation runs. */}
              <button
                type="button"
                className="btn"
                disabled={isAccountInvalid || activeAccount?.capabilities.write === 'denied'}
                onClick={() => setRenameTargetId(machine.id)}
              >
                重命名
              </button>
              <button
                type="button"
                className="btn danger"
                disabled={locked || !canDeleteByAccount}
                onClick={() => setDeleteTargetId(machine.id)}
              >
                删除机器
              </button>
            </div>
            {!canDeleteByAccount ? (
              <div className="muted" style={{ fontSize: '12px', marginTop: '5px' }}>
                这个账号的 Key 没有声明删除权限或已失效，所以不能删除。
              </div>
            ) : null}
          </div>

 <SaveSnapshot key={`snapshot:${machine.id}:${activeCredId}`} machineId={machine.id} credentialId={activeCredId}/>
 <PortsPanel key={`ports:${machine.id}:${activeCredId}`} machineId={machine.id} credentialId={activeCredId}/>

          <dl className="kv">
            <dt>规格</dt>
            <dd>{specText}</dd>
            <dt>机器 ID</dt>
            <dd className="mono">{machine.id}</dd>
            <dt>组织 ID</dt>
            <dd className="mono">{detailQuery.data?.orgId || machine.orgId || '—'}</dd>
            <dt>数据更新</dt>
            <dd>{formatRelativeTime(machine.fetchedAt)}</dd>
          </dl>

          {isAccountInvalid ? (
            <div className="notice warn">
              Key 已失效或已暂停，这里显示的是最后一次成功获取的数据。
            </div>
          ) : null}
        </div>
      </aside>

      <FilesDialog
        open={filesTarget?.machineId === machine.id}
        machineId={machine.id}
        machineName={machine.name}
        credentialId={filesTarget?.credentialId ?? activeCredId}
        onClose={() => setFilesTarget(null)}
      />

      <RenameMachineDialog
        open={renameTargetId === machine.id}
        machineId={machine.id}
        machineName={machine.name}
        credentialId={activeCredId}
        onClose={() => setRenameTargetId(null)}
      />

      <DeleteMachineDialog
        open={deleteDialogOpen}
        machineId={machine.id}
        machineName={machine.name}
        account={activeAccount}
        onClose={() => setDeleteTargetId(null)}
        onDeleted={() => setSelectedMachineId(null)}
      />
    </div>
  )
}
