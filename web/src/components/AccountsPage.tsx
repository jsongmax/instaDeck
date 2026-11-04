import { useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { api } from '../api/client'
import { ApiError } from '../api/errors'
import type { Account } from '../api/types'
import type { EnrichedAccount } from '../hooks/useFleetData'
import { useAppStore } from '../store/useAppStore'
import { AccountKeyDialog, ModalDialog } from './Dialogs'
import { ErrorIcon, RunningCircleIcon, StoppedCircleIcon, Swatch } from './Icons'

function formatCapabilities(cap: Account['capabilities']): string {
  const parts: string[] = []
  if (cap.read === 'declared' || cap.read === 'verified') parts.push('读取')
  if (cap.write === 'declared' || cap.write === 'verified') parts.push('写入')
  if (cap.delete === 'declared' || cap.delete === 'verified') parts.push('删除')
  return parts.length > 0 ? parts.join(' · ') : '只读'
}

export function AccountsPage({ accounts }: { accounts: EnrichedAccount[] }) {
  const queryClient = useQueryClient()
  const showToast = useAppStore((s) => s.showToast)

  const [keyDialogOpen, setKeyDialogOpen] = useState(false)
  const [editingAccount, setEditingAccount] = useState<Account | null>(null)
  const [removingAccount, setRemovingAccount] = useState<Account | null>(null)
  const [busyId, setBusyId] = useState<string | null>(null)

  const refresh = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ['accounts'] }),
      queryClient.invalidateQueries({ queryKey: ['fleet'] }),
    ])
  }

  const handleToggleEnabled = async (acct: EnrichedAccount) => {
    setBusyId(acct.credentialId)
    try {
      await api.updateAccount(acct.credentialId, { enabled: !acct.enabled })
      showToast(acct.enabled ? `已暂停 ${acct.name} 同步` : `已启用 ${acct.name} 同步`)
      await refresh()
    } catch (err) {
      showToast(err instanceof ApiError ? err.message : '操作失败')
    } finally {
      setBusyId(null)
    }
  }

  const handleValidate = async (acct: EnrichedAccount) => {
    setBusyId(acct.credentialId)
    try {
      const updated = await api.validateAccount(acct.credentialId)
      showToast(
        updated.status === 'valid'
          ? `${acct.name} Key 校验通过`
          : `${acct.name} Key 已失效`,
      )
      await refresh()
    } catch (err) {
      showToast(err instanceof ApiError ? err.message : '校验失败')
    } finally {
      setBusyId(null)
    }
  }

  const handleConfirmRemove = async () => {
    if (!removingAccount) return
    const target = removingAccount
    setBusyId(target.credentialId)
    try {
      await api.deleteAccount(target.credentialId)
      showToast(`已移除账号 ${target.name}`)
      setRemovingAccount(null)
      await refresh()
    } catch (err) {
      showToast(err instanceof ApiError ? err.message : '移除失败')
    } finally {
      setBusyId(null)
    }
  }

  return (
    <>
      <div className="page-head">
        <h1>InstaBox 账号</h1>
        <span className="spacer" />
        <button
          type="button"
          className="btn primary"
          onClick={() => {
            setEditingAccount(null)
            setKeyDialogOpen(true)
          }}
        >
          添加账号
        </button>
      </div>

      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <th>别名</th>
              <th>组织 / 用户</th>
              <th>机器</th>
              <th>权限</th>
              <th>状态</th>
              <th style={{ textAlign: 'right' }}>操作</th>
            </tr>
          </thead>
          <tbody>
            {accounts.length > 0 ? (
              accounts.map((a) => {
                const invalid = a.status === 'invalid' || Boolean(a.fleetInfo?.error)
                const quotaDisplay = a.isUnlimitedMachines
                  ? `${a.machineCount} / 不限`
                  : a.hasLimits
                    ? `${a.machineCount} / ${a.maxMachines}`
                    : '未知'

                return (
                  <tr key={a.credentialId}>
                    <td>
                      <span className="chip">
                        <Swatch color={a.color} />
                        <b style={{ fontWeight: 500 }}>{a.name}</b>
                      </span>
                    </td>
                    <td className="mono muted">{a.orgId || a.userId || '—'}</td>
                    <td className="num">{quotaDisplay}</td>
                    <td className="muted" style={{ fontSize: '12.5px' }}>
                      {formatCapabilities(a.capabilities)}
                    </td>
                    <td>
                      {invalid ? (
                        <span
                          className="state"
                          style={{ '--c': 'var(--bad)' } as React.CSSProperties}
                        >
                          <ErrorIcon />
                          Key 已失效
                        </span>
                      ) : a.enabled ? (
                        <span
                          className="state"
                          style={{ '--c': 'var(--ok)' } as React.CSSProperties}
                        >
                          <RunningCircleIcon />
                          同步中
                        </span>
                      ) : (
                        <span
                          className="state"
                          style={{ '--c': 'var(--off)' } as React.CSSProperties}
                        >
                          <StoppedCircleIcon />
                          已暂停同步
                        </span>
                      )}
                    </td>
                    <td>
                      <div className="row-actions">
                        <button
                          type="button"
                          className="btn"
                          disabled={busyId === a.credentialId}
                          onClick={() => void handleValidate(a)}
                        >
                          校验
                        </button>
                        <button
                          type="button"
                          className="btn"
                          disabled={busyId === a.credentialId}
                          onClick={() => void handleToggleEnabled(a)}
                        >
                          {a.enabled ? '暂停同步' : '启用同步'}
                        </button>
                        <button
                          type="button"
                          className="btn"
                          disabled={busyId === a.credentialId}
                          onClick={() => {
                            setEditingAccount(a)
                            setKeyDialogOpen(true)
                          }}
                        >
                          替换 Key
                        </button>
                        <button
                          type="button"
                          className="btn"
                          disabled={busyId === a.credentialId}
                          onClick={() => setRemovingAccount(a)}
                        >
                          移除
                        </button>
                      </div>
                    </td>
                  </tr>
                )
              })
            ) : (
              <tr>
                <td colSpan={6} className="muted">
                  暂无已添加的 InstaBox 账号。
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      <p
        className="muted"
        style={{ fontSize: '12.5px', margin: '10px 0 0', maxWidth: '68ch' }}
      >
        添加后只显示别名和权限，页面上不会再显示 Key。多个 Key 看到同一台机器时只显示一行，操作时再选用哪个账号。
      </p>

      <AccountKeyDialog
        open={keyDialogOpen}
        targetAccount={editingAccount}
        onClose={() => {
          setKeyDialogOpen(false)
          setEditingAccount(null)
        }}
      />

      <ModalDialog
        open={removingAccount !== null}
        onClose={() => setRemovingAccount(null)}
      >
        <h2>移除账号</h2>
        <p className="muted" style={{ margin: 0 }}>
          确定要从面板移除账号 <b>{removingAccount?.name}</b> 吗？这不会删除上游的机器。
        </p>
        <div className="dlg-foot">
          <button
            type="button"
            className="btn"
            onClick={() => setRemovingAccount(null)}
          >
            取消
          </button>
          <button
            type="button"
            className="btn danger"
            onClick={() => void handleConfirmRemove()}
          >
            确认移除
          </button>
        </div>
      </ModalDialog>
    </>
  )
}
