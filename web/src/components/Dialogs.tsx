import { useQuery, useQueryClient } from '@tanstack/react-query'
import { type ReactNode, useEffect, useRef, useState } from 'react'
import { api } from '../api/client'
import { ApiError } from '../api/errors'
import { type Account, SKIN_META } from '../api/types'
import type { EnrichedAccount } from '../hooks/useFleetData'
import { useAppStore } from '../store/useAppStore'
import { Swatch } from './Icons'

export function ModalDialog({
  open,
  onClose,
  wide = false,
  className = 'dlg',
  children,
}: {
  open: boolean
  onClose: () => void
  wide?: boolean
  className?: string
  children: ReactNode
}) {
  const dlgRef = useRef<HTMLDialogElement | null>(null)

  useEffect(() => {
    const el = dlgRef.current
    if (!el) return
    if (open) {
      if (!el.open && typeof el.showModal === 'function') {
        try {
          el.showModal()
        } catch {
          el.setAttribute('open', '')
        }
      } else if (!el.open) {
        el.setAttribute('open', '')
      }
    } else if (el.open) {
      if (typeof el.close === 'function') {
        try {
          el.close()
        } catch {
          el.removeAttribute('open')
        }
      } else {
        el.removeAttribute('open')
      }
    }
  }, [open])

  if (!open) return null

  return (
    <dialog
      ref={dlgRef}
      className={wide ? 'wide-dlg' : undefined}
      onCancel={(e) => {
        e.preventDefault()
        onClose()
      }}
    >
      <div className={className}>{children}</div>
    </dialog>
  )
}

function ReauthForm({ onResolve }: { onResolve: (ok: boolean) => void }) {
  const showToast = useAppStore((s) => s.showToast)
  const [password, setPassword] = useState('')
  const [code, setCode] = useState('')
  const [errMsg, setErrMsg] = useState('')
  const [submitting, setSubmitting] = useState(false)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!password || !code) {
      setErrMsg('请输入密码和 6 位验证码')
      return
    }
    setSubmitting(true)
    setErrMsg('')
    try {
      const pw = password
      const otp = code
      setPassword('')
      setCode('')
      await api.reauthenticate(pw, otp)
      showToast('验证成功，5 分钟内不再询问')
      onResolve(true)
    } catch (err) {
      setErrMsg(err instanceof ApiError ? err.message : '验证失败，请重试')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <form onSubmit={(e) => void handleSubmit(e)} style={{ display: 'contents' }}>
      <h2>需要重新验证</h2>
      <p className="muted" style={{ margin: 0 }}>
        这是敏感操作，请再次输入密码和验证码。验证后 5 分钟内不再询问。
      </p>
      <label className="lbl" htmlFor="rp">
        密码
        <input
          className="field wide"
          id="rp"
          type="password"
          autoComplete="off"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          autoFocus
        />
      </label>
      <label className="lbl" htmlFor="rc">
        验证码
        <input
          className="field wide mono"
          id="rc"
          inputMode="numeric"
          maxLength={6}
          placeholder="6 位数字"
          autoComplete="off"
          value={code}
          onChange={(e) => setCode(e.target.value)}
        />
      </label>
      {errMsg ? <div className="errline">{errMsg}</div> : null}
      <div className="dlg-foot">
        <button type="button" className="btn" onClick={() => onResolve(false)}>
          取消
        </button>
        <button
          type="submit"
          className={`btn primary ${submitting ? 'loading' : ''}`}
          disabled={submitting}
        >
          验证
        </button>
      </div>
    </form>
  )
}

export function ReauthDialog() {
  const reauthOpen = useAppStore((s) => s.reauthOpen)
  const resolveReauth = useAppStore((s) => s.resolveReauth)

  return (
    <ModalDialog open={reauthOpen} onClose={() => resolveReauth(false)}>
      {reauthOpen ? <ReauthForm onResolve={resolveReauth} /> : null}
    </ModalDialog>
  )
}

function AccountKeyForm({
  targetAccount,
  onClose,
}: {
  targetAccount: Account | null
  onClose: () => void
}) {
  const queryClient = useQueryClient()
  const showToast = useAppStore((s) => s.showToast)
  const isReplace = targetAccount !== null

  const [name, setName] = useState(() => targetAccount?.name ?? '')
  const [apiKey, setApiKey] = useState('')
  const [canWrite, setCanWrite] = useState(() =>
    targetAccount
      ? targetAccount.capabilities.write === 'declared' ||
          targetAccount.capabilities.write === 'verified'
      : true,
  )
  const [canDelete, setCanDelete] = useState(() =>
    targetAccount
      ? targetAccount.capabilities.delete === 'declared' ||
          targetAccount.capabilities.delete === 'verified'
      : false,
  )
  const [submitting, setSubmitting] = useState(false)
  const [errMsg, setErrMsg] = useState('')

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    const trimmedName = name.trim()
    const submittedKey = apiKey.trim()
    // Immediately clear API Key input upon submit (security constraint)
    setApiKey('')

    if (!isReplace && !trimmedName) {
      setErrMsg('请输入账号别名')
      return
    }
    if (!submittedKey) {
      setErrMsg('请输入 API Key')
      return
    }

    setSubmitting(true)
    setErrMsg('')
    try {
      if (isReplace && targetAccount) {
        await api.updateAccount(targetAccount.credentialId, {
          key: submittedKey,
          declarations: {
            read: true,
            write: canWrite,
            delete: canDelete,
          },
        })
        showToast(`${targetAccount.name} 的 Key 已替换`)
      } else {
        await api.createAccount({
          name: trimmedName,
          key: submittedKey,
          declarations: {
            read: true,
            write: canWrite,
            delete: canDelete,
          },
        })
        showToast(`已添加账号 ${trimmedName}`)
      }
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['accounts'] }),
        queryClient.invalidateQueries({ queryKey: ['fleet'] }),
      ])
      onClose()
    } catch (err) {
      setErrMsg(err instanceof ApiError ? err.message : '保存失败，请重试')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <form onSubmit={(e) => void handleSubmit(e)} style={{ display: 'contents' }}>
      <h2>{isReplace && targetAccount ? `替换 ${targetAccount.name} 的 Key` : '添加账号'}</h2>
      {!isReplace ? (
        <label className="lbl" htmlFor="kn">
          别名
          <input
            className="field wide"
            id="kn"
            placeholder="例如：项目C"
            autoComplete="off"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </label>
      ) : null}
      <label className="lbl" htmlFor="kk">
        API Key
        <input
          className="field wide mono"
          id="kk"
          type="password"
          placeholder="ibk_…"
          autoComplete="off"
          value={apiKey}
          onChange={(e) => setApiKey(e.target.value)}
        />
      </label>
      <fieldset
        style={{
          border: 0,
          padding: 0,
          margin: 0,
          display: 'flex',
          flexDirection: 'column',
          gap: '4px',
        }}
      >
        <legend className="muted" style={{ fontSize: '12.5px', padding: 0, marginBottom: '3px' }}>
          这个 Key 有哪些权限？（API 查不到，由你声明）
        </legend>
        <label className="switch">
          <input type="checkbox" checked disabled /> 读取机器（会自动验证）
        </label>
        <label className="switch">
          <input
            type="checkbox"
            checked={canWrite}
            onChange={(e) => setCanWrite(e.target.checked)}
          />{' '}
          写入：启动、停止、创建
        </label>
        <label className="switch">
          <input
            type="checkbox"
            checked={canDelete}
            onChange={(e) => setCanDelete(e.target.checked)}
          />{' '}
          删除机器
        </label>
      </fieldset>
      {errMsg ? <div className="errline">{errMsg}</div> : null}
      <div className="dlg-foot">
        <button type="button" className="btn" onClick={onClose}>
          取消
        </button>
        <button
          type="submit"
          className={`btn primary ${submitting ? 'loading' : ''}`}
          disabled={submitting}
        >
          校验并保存
        </button>
      </div>
    </form>
  )
}

export function AccountKeyDialog({
  open,
  targetAccount,
  onClose,
}: {
  open: boolean
  targetAccount: Account | null
  onClose: () => void
}) {
  return (
    <ModalDialog open={open} onClose={onClose}>
      {open ? (
        <AccountKeyForm
          key={targetAccount?.credentialId ?? 'add'}
          targetAccount={targetAccount}
          onClose={onClose}
        />
      ) : null}
    </ModalDialog>
  )
}

const SPEC_PRESETS = [
  { label: '2 vCPU · 4 GiB', vcpus: 2, memMiB: 4096 },
  { label: '4 vCPU · 8 GiB', vcpus: 4, memMiB: 8192 },
  { label: '8 vCPU · 16 GiB', vcpus: 8, memMiB: 16384 },
]

function CreateMachineForm({
  accounts,
  defaultCredentialId,
  snapshotId,
  onClose,
}: {
  accounts: EnrichedAccount[]
  defaultCredentialId?: string
  snapshotId?: string
  onClose: () => void
}) {
  const queryClient = useQueryClient()
  const upsertTrackedAction = useAppStore((s) => s.upsertTrackedAction)
  const showToast = useAppStore((s) => s.showToast)

  const initialAcct =
    (snapshotId ? accounts.find(a=>a.credentialId===defaultCredentialId) : undefined) ?? accounts.find(
      (a) =>
        (!defaultCredentialId || a.credentialId === defaultCredentialId) &&
        a.status === 'valid' &&
        a.enabled &&
        !a.isQuotaFull,
    ) ??
    accounts.find((a) => a.status === 'valid' && a.enabled && !a.isQuotaFull) ??
    accounts[0]

  const [credentialId, setCredentialId] = useState<string>(initialAcct?.credentialId ?? '')
  const [name, setName] = useState<string>('')
  const [selectedTplId, setSelectedTplId] = useState<string>('')
  const [specIdx, setSpecIdx] = useState<number>(1)
  const [vcpus, setVcpus] = useState<number>(4)
  const [memMiB, setMemMiB] = useState<number>(8192)
  const [submitting, setSubmitting] = useState<boolean>(false)
  const [errMsg, setErrMsg] = useState<string>('')

  const templatesQuery = useQuery({
    queryKey: ['templates', credentialId],
    queryFn: () => api.getTemplates(credentialId),
    enabled: Boolean(credentialId) && !snapshotId,
    staleTime: 30_000,
  })

  const creatableTemplates = (templatesQuery.data?.templates ?? []).filter((t) => t.creatable)
  const activeTemplateId =
    selectedTplId && creatableTemplates.some((t) => t.id === selectedTplId)
      ? selectedTplId
      : (creatableTemplates.find((t) => t.default) ?? creatableTemplates[0])?.id ?? 'default'

  const handleTemplateChange = (id: string) => {
    setSelectedTplId(id)
    const tpl = creatableTemplates.find((t) => t.id === id)
    if (tpl?.defaultSpec?.vcpus && tpl?.defaultSpec?.memMiB) {
      setVcpus(tpl.defaultSpec.vcpus)
      setMemMiB(tpl.defaultSpec.memMiB)
      const idx = SPEC_PRESETS.findIndex(
        (p) => p.vcpus === tpl.defaultSpec.vcpus && p.memMiB === tpl.defaultSpec.memMiB,
      )
      if (idx >= 0) setSpecIdx(idx)
    }
  }

  const handleSpecChange = (idx: number) => {
    setSpecIdx(idx)
    const preset = SPEC_PRESETS[idx]
    if (preset) {
      setVcpus(preset.vcpus)
      setMemMiB(preset.memMiB)
    }
  }

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    const trimmed = name.trim()
    if (!credentialId) {
      setErrMsg('请选择账号')
      return
    }
    if (!trimmed) {
      setErrMsg('请输入机器名称')
      return
    }
    const selected = accounts.find((a) => a.credentialId === credentialId)
    if (selected?.isQuotaFull) {
      setErrMsg(`这个账号额度已满（${selected.machineCount}/${selected.maxMachines}），已停止和已暂停的机器也算，请先删除一台`)
      return
    }
    setSubmitting(true)
    setErrMsg('')
    const clientRequestId = crypto.randomUUID()
    try {
      const action = await api.createMachine({
        clientRequestId,
        credentialId,
        body: {
          name: trimmed,
          ...(snapshotId ? {snapshotId} : {templateId: activeTemplateId}),
          vcpus,
          memMiB,
        },
      })
      upsertTrackedAction(action)
      useAppStore.getState().addCreateRecord(action.id, trimmed, credentialId)
      showToast(`已提交创建 ${trimmed}`)
      await queryClient.invalidateQueries({ queryKey: ['fleet'] })
      onClose()
    } catch (err) {
      setErrMsg(err instanceof ApiError ? err.message : '创建失败，请重试')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <form onSubmit={(e) => void handleSubmit(e)} style={{ display: 'contents' }}>
      <h2>新建机器</h2>
      <fieldset
        style={{
          border: 0,
          padding: 0,
          margin: 0,
          display: 'flex',
          flexDirection: 'column',
          gap: '5px',
        }}
      >
        <legend className="muted" style={{ fontSize: '12.5px', padding: 0, marginBottom: '4px' }}>
          账号
        </legend>
        {accounts.map((a) => {
          const invalid = a.status === 'invalid' || Boolean(a.fleetInfo?.error)
          const disabled = invalid || !a.enabled || a.isQuotaFull || Boolean(snapshotId && a.credentialId !== defaultCredentialId)
          let quotaText = ''
          if (invalid) {
            quotaText = 'Key 已失效'
          } else if (!a.enabled) {
            quotaText = '已暂停同步'
          } else if (a.isUnlimitedMachines) {
            quotaText = '不限额'
          } else if (a.isQuotaFull) {
            quotaText = `额度已满 ${a.machineCount}/${a.maxMachines}`
          } else if (a.maxMachines > 0) {
            quotaText = `还可创建 ${a.maxMachines - a.machineCount} 台`
          } else {
            quotaText = `已用 ${a.machineCount} 台`
          }

          return (
            <label
              key={a.credentialId}
              className={`pick ${disabled ? 'off' : ''}`}
            >
              <input
                type="radio"
                name="ca"
                value={a.credentialId}
                disabled={disabled}
                checked={credentialId === a.credentialId}
                onChange={() => setCredentialId(a.credentialId)}
              />
              <Swatch color={a.color} />
              {a.name}
              <span className="end num">{quotaText}</span>
            </label>
          )
        })}
      </fieldset>
      <label className="lbl" htmlFor="cn">
        名称
        <input
          className="field wide mono"
          id="cn"
          placeholder="my-box"
          autoComplete="off"
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
      </label>
      <div className="grid2">
        {!snapshotId ? <label className="lbl" htmlFor="ct">
          模板
          <select
            className="field wide"
            id="ct"
            value={activeTemplateId}
            onChange={(e) => handleTemplateChange(e.target.value)}
          >
            {creatableTemplates.length > 0 ? (
              creatableTemplates.map((t) => (
                <option key={t.id} value={t.id}>
                  {t.id}
                </option>
              ))
            ) : (
              <option value="default">default</option>
            )}
          </select>
        </label> : <p className="muted">从快照新建机器：{snapshotId}</p>}
        <label className="lbl" htmlFor="cs">
          规格
          <select
            className="field wide"
            id="cs"
            value={specIdx}
            onChange={(e) => handleSpecChange(Number(e.target.value))}
          >
            {SPEC_PRESETS.map((p, idx) => (
              <option key={p.label} value={idx}>
                {p.label}
              </option>
            ))}
          </select>
        </label>
      </div>
      {errMsg ? <div className="errline">{errMsg}</div> : null}
      <div className="dlg-foot">
        <button type="button" className="btn" onClick={onClose}>
          取消
        </button>
        <button
          type="submit"
          className={`btn primary ${submitting ? 'loading' : ''}`}
          disabled={submitting}
        >
          创建
        </button>
      </div>
    </form>
  )
}

export function CreateMachineDialog({
  open,
  accounts,
  defaultCredentialId,
  snapshotId,
  onClose,
}: {
  open: boolean
  accounts: EnrichedAccount[]
  defaultCredentialId?: string
  snapshotId?: string
  onClose: () => void
}) {
  return (
    <ModalDialog open={open} onClose={onClose}>
      {open ? (
        <CreateMachineForm
          accounts={accounts}
          defaultCredentialId={defaultCredentialId}
          snapshotId={snapshotId}
          onClose={onClose}
        />
      ) : null}
    </ModalDialog>
  )
}

function DeleteMachineForm({
  machineId,
  machineName,
  account,
  onClose,
  onDeleted,
}: {
  machineId: string
  machineName: string
  account: EnrichedAccount | undefined
  onClose: () => void
  onDeleted?: () => void
}) {
  const queryClient = useQueryClient()
  const upsertTrackedAction = useAppStore((s) => s.upsertTrackedAction)
  const closeTerminalsByMachine = useAppStore((s) => s.closeTerminalsByMachine)
  const showToast = useAppStore((s) => s.showToast)

  const [confirmName, setConfirmName] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [errMsg, setErrMsg] = useState('')

  const canConfirm = confirmName === machineName && !submitting

  const handleDelete = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!canConfirm || !account) return
    setSubmitting(true)
    setErrMsg('')
    const clientRequestId = crypto.randomUUID()
    try {
      const action = await api.runMachineAction(machineId, {
        clientRequestId,
        credentialId: account.credentialId,
        kind: 'delete',
        confirmName,
      })
      upsertTrackedAction(action)
      closeTerminalsByMachine(machineId)
      showToast(`正在删除 ${machineName}`)
      await queryClient.invalidateQueries({ queryKey: ['fleet'] })
      onClose()
      onDeleted?.()
    } catch (err) {
      setErrMsg(err instanceof ApiError ? err.message : '删除失败，请重试')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <form onSubmit={(e) => void handleDelete(e)} style={{ display: 'contents' }}>
      <h2>删除机器</h2>
      <div className="notice warn">删除后机器和磁盘数据都无法恢复。</div>
      <dl className="kv">
        <dt>账号</dt>
        <dd>
          {account ? (
            <span className="chip">
              <Swatch color={account.color} />
              {account.name}
            </span>
          ) : (
            '—'
          )}
        </dd>
        <dt>机器</dt>
        <dd className="mono">{machineName}</dd>
      </dl>
      <label className="lbl" htmlFor="dn">
        输入机器名称确认
        <input
          className="field wide mono"
          id="dn"
          autoComplete="off"
          placeholder={machineName}
          value={confirmName}
          onChange={(e) => setConfirmName(e.target.value)}
          autoFocus
        />
      </label>
      {errMsg ? <div className="errline">{errMsg}</div> : null}
      <div className="dlg-foot">
        <button type="button" className="btn" onClick={onClose}>
          取消
        </button>
        <button
          type="submit"
          className={`btn danger ${submitting ? 'loading' : ''}`}
          id="dgo"
          disabled={!canConfirm}
        >
          删除 {machineName}
        </button>
      </div>
    </form>
  )
}

export function DeleteMachineDialog({
  open,
  machineId,
  machineName,
  account,
  onClose,
  onDeleted,
}: {
  open: boolean
  machineId: string
  machineName: string
  account: EnrichedAccount | undefined
  onClose: () => void
  onDeleted?: () => void
}) {
  return (
    <ModalDialog open={open} onClose={onClose}>
      {open ? (
        <DeleteMachineForm
          key={machineId}
          machineId={machineId}
          machineName={machineName}
          account={account}
          onClose={onClose}
          onDeleted={onDeleted}
        />
      ) : null}
    </ModalDialog>
  )
}

function RenameMachineForm({
  machineId,
  machineName,
  credentialId,
  onClose,
}: {
  machineId: string
  machineName: string
  credentialId: string
  onClose: () => void
}) {
  const queryClient = useQueryClient()
  const showToast = useAppStore((s) => s.showToast)
  const [name, setName] = useState(machineName)
  const [submitting, setSubmitting] = useState(false)
  const [errMsg, setErrMsg] = useState('')

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (submitting || name === machineName) return
    setSubmitting(true)
    setErrMsg('')
    try {
      const value = await api.renameMachine(machineId, {
        clientRequestId: crypto.randomUUID(),
        credentialId,
        name,
      })
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['fleet'] }),
        queryClient.invalidateQueries({ queryKey: ['machineDetail', machineId] }),
      ])
      showToast(`已重命名为 ${value.name}`)
      onClose()
    } catch (err) {
      setErrMsg(err instanceof ApiError ? err.message : '重命名失败，请重试')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <form onSubmit={(e) => void submit(e)} style={{ display: 'contents' }}>
      <h2>重命名机器</h2>
      <label className="lbl" htmlFor="rename-machine">
        新名称
        <input
          className="field wide mono"
          id="rename-machine"
          autoComplete="off"
          required
          maxLength={63}
          pattern="[a-z0-9][a-z0-9\-]{0,62}"
          title="小写字母、数字或连字符，以字母或数字开头"
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
      </label>
      <p className="muted">
        小写字母、数字或连字符，以字母或数字开头，最多 63 位。机器 ID、端口网址和 SSH
        证书都不变；旧名称立即失效，系统主机名在下次启动或恢复时更新。
      </p>
      {errMsg ? <div className="errline">{errMsg}</div> : null}
      <div className="dlg-foot">
        <button type="button" className="btn" onClick={onClose}>
          取消
        </button>
        <button className="btn primary" disabled={submitting || name === machineName}>
          保存
        </button>
      </div>
    </form>
  )
}

export function RenameMachineDialog({
  open,
  machineId,
  machineName,
  credentialId,
  onClose,
}: {
  open: boolean
  machineId: string
  machineName: string
  credentialId: string
  onClose: () => void
}) {
  return (
    <ModalDialog open={open} onClose={onClose}>
      {open ? (
        <RenameMachineForm
          key={machineId}
          machineId={machineId}
          machineName={machineName}
          credentialId={credentialId}
          onClose={onClose}
        />
      ) : null}
    </ModalDialog>
  )
}

export function SkinCardsGrid() {
  const skin = useAppStore((s) => s.skin)
  const setSkin = useAppStore((s) => s.setSkin)

  return (
    <div className="skins">
      <button
        type="button"
        className={`skin ${skin === 'auto' ? 'on' : ''}`}
        aria-pressed={skin === 'auto'}
        onClick={() => setSkin('auto', true)}
      >
        <span className="skin-view skin-auto">
          <span>A</span>
        </span>
        <span className="skin-name">跟随系统</span>
        <span className="skin-note">白天晴空，夜间石墨</span>
      </button>
      {SKIN_META.map((s) => (
        <button
          key={s.id}
          type="button"
          className={`skin ${skin === s.id ? 'on' : ''}`}
          aria-pressed={skin === s.id}
          onClick={() => setSkin(s.id, true)}
        >
          <span className="skin-view" style={{ background: s.bg }}>
            <span
              className="skin-side"
              style={{ background: s.panel, borderColor: s.line }}
            >
              <i style={{ background: s.accent }} />
              <i style={{ background: s.muted }} />
              <i style={{ background: s.muted }} />
            </span>
            <span
              className="skin-term"
              style={{ background: s.termBg, borderColor: s.line }}
            >
              <i style={{ background: s.termGreen }} />
              <i style={{ background: s.termInk }} />
            </span>
          </span>
          <span className="skin-name">
            {s.name}
            <span className="muted">{s.mode === 'dark' ? '深色' : '浅色'}</span>
          </span>
          <span className="skin-note">{s.note}</span>
        </button>
      ))}
    </div>
  )
}

export function ThemeDialog({
  open,
  onClose,
}: {
  open: boolean
  onClose: () => void
}) {
  return (
    <ModalDialog open={open} onClose={onClose} wide>
      <h2>外观主题</h2>
      <SkinCardsGrid />
      <p className="muted" style={{ margin: 0, fontSize: '12.5px' }}>
        面板和已打开的终端窗口会一起切换。
      </p>
      <div className="dlg-foot">
        <button type="button" className="btn" onClick={onClose}>
          完成
        </button>
      </div>
    </ModalDialog>
  )
}
