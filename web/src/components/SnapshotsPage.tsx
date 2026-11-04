import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { apiRequest } from '../api/client'
import { formatErrorCode } from '../api/errors'
import type { Snapshot } from '../api/types'
import type { EnrichedAccount } from '../hooks/useFleetData'
import { CreateMachineDialog, ModalDialog } from './Dialogs'

type SnapshotInput = { name: string; description: string; rootOnly: boolean; clientRequestId: string }

export function SaveSnapshot({ machineId, credentialId }: { machineId: string; credentialId: string }) {
  const client = useQueryClient()
  const [open, setOpen] = useState(false)
  return (
    <>
      <button className="btn" onClick={() => setOpen(true)}>保存快照</button>
      <ModalDialog open={open} onClose={() => setOpen(false)}>
        {open && (
          <SnapshotForm onClose={() => setOpen(false)} onSave={async body => {
            await apiRequest(`/machines/${encodeURIComponent(machineId)}/snapshots`, {
              method: 'POST', body: { ...body, credentialId },
            })
            void client.invalidateQueries({ queryKey: ['snapshots'] })
          }} />
        )}
      </ModalDialog>
    </>
  )
}

function SnapshotForm({ onSave, onClose }: { onSave: (body: SnapshotInput) => Promise<void>; onClose: () => void }) {
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [rootOnly, setRootOnly] = useState(false)
  const [requestId] = useState(() => crypto.randomUUID())
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const submit = async (event: React.FormEvent) => {
    event.preventDefault()
    setBusy(true)
    setError('')
    try {
      await onSave({ name, description, rootOnly, clientRequestId: requestId })
      onClose()
    } catch (error) {
      setError(error instanceof Error ? error.message : '保存失败')
    } finally {
      setBusy(false)
    }
  }
  return (
    <form style={{ display: 'contents' }} onSubmit={event => void submit(event)}>
      <h2>保存快照</h2>
      <p className="notice warn">快照可能包含磁盘中的凭据和文件，请妥善管理。</p>
      <label className="lbl">快照名称
        <input className="field" required pattern="[a-z0-9][a-z0-9\-]{0,62}" maxLength={63} value={name} onChange={event => setName(event.target.value)} />
      </label>
      <label className="lbl">描述
        <textarea className="field" maxLength={8192} value={description} onChange={event => setDescription(event.target.value)} />
      </label>
      <label><input type="checkbox" checked={rootOnly} onChange={event => setRootOnly(event.target.checked)} />只保存系统盘</label>
      {error && <p className="errline">{error}</p>}
      <div className="dlg-foot">
        <button type="button" className="btn" onClick={onClose}>取消</button>
        <button className="btn primary" disabled={busy}>保存快照</button>
      </div>
    </form>
  )
}

export function SnapshotsPage({ accounts }: { accounts: EnrichedAccount[] }) {
  const client = useQueryClient()
  const [create, setCreate] = useState<Snapshot | null>(null)
  const [remove, setRemove] = useState<Snapshot | null>(null)
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [requestId, setRequestId] = useState('')
  const query = useQuery({
    queryKey: ['snapshots'],
    queryFn: () => apiRequest<{ snapshots: Snapshot[]; errors: { credentialId: string; code: string }[] }>('/snapshots'),
    refetchInterval: query => query.state.data?.snapshots.some(snapshot => snapshot.status === 'pending') ? 5000 : false,
  })
  const deleteSnapshot = async () => {
    if (!remove) return
    setBusy(true)
    setError('')
    try {
      await apiRequest(`/snapshots/${encodeURIComponent(remove.id)}?credentialId=${encodeURIComponent(remove.credentialId)}`, {
        method: 'DELETE', body: { confirmName: confirm, clientRequestId: requestId },
      })
      setRemove(null)
      await client.invalidateQueries({ queryKey: ['snapshots'] })
    } catch (error) {
      setError(error instanceof Error ? error.message : '删除失败')
    } finally {
      setBusy(false)
    }
  }
  return (
    <>
      <div className="page-head"><h1>快照</h1></div>
      <p className="muted">API Key 需要 images:read / write / delete 权限。</p>
      {query.data?.errors.map(error => (
        <p className="errline" key={error.credentialId}>
          {accounts.find(account => account.credentialId === error.credentialId)?.name ?? error.credentialId}：{formatErrorCode(error.code)}
        </p>
      ))}
      {query.error && <p className="errline">{query.error.message}</p>}
      <div className="table-wrap">
        <table>
          <thead><tr>{['名称', '账号', '来源机器', '状态', '系统盘', '数据盘', '创建时间', '操作'].map(label => <th key={label}>{label}</th>)}</tr></thead>
          <tbody>
            {query.data?.snapshots.map(snapshot => (
              <tr key={`${snapshot.credentialId}:${snapshot.id}`}>
                <td>{snapshot.name}</td><td>{snapshot.accountName}</td><td>{snapshot.sourceMachineId}</td>
                <td>
                  {snapshot.status === 'pending' ? '进行中' : snapshot.status === 'failed' ? '失败' : '可用'}
                  {snapshot.status === 'failed' && <p className="errline">{snapshot.error?.slice(0, 200)}</p>}
                </td>
                <td>{(snapshot.rootSizeBytes / 1073741824).toFixed(1)} GiB</td>
                <td>{(snapshot.dataSizeBytes / 1073741824).toFixed(1)} GiB</td>
                <td>{new Date(snapshot.createdAt).toLocaleString('zh-CN')}</td>
                <td>
                  <div className="btn-row">
                    <button className="btn" disabled={snapshot.status !== 'ready'} onClick={() => setCreate(snapshot)}>新建机器</button>
                    <button className="btn danger" onClick={() => {
                      setRemove(snapshot); setConfirm(''); setError(''); setRequestId(crypto.randomUUID())
                    }}>删除</button>
                  </div>
                </td>
              </tr>
            ))}
            {!query.data?.snapshots.length && <tr><td colSpan={8} className="muted">{query.isLoading ? '正在加载快照…' : '暂无快照'}</td></tr>}
          </tbody>
        </table>
      </div>
      <CreateMachineDialog open={!!create} accounts={accounts} defaultCredentialId={create?.credentialId} snapshotId={create?.id} onClose={() => setCreate(null)} />
      <ModalDialog open={!!remove} onClose={() => setRemove(null)}>
        <h2>删除快照</h2>
        <p>已基于它创建的机器不受影响</p>
        <label className="lbl">输入快照名称确认
          <input className="field" value={confirm} placeholder={remove?.name} onChange={event => setConfirm(event.target.value)} />
        </label>
        {error && <p className="errline">{error}</p>}
        <div className="dlg-foot">
          <button className="btn" onClick={() => setRemove(null)}>取消</button>
          <button className="btn danger" disabled={busy || confirm !== remove?.name} onClick={() => void deleteSnapshot()}>确认删除</button>
        </div>
      </ModalDialog>
    </>
  )
}
