import { useQuery, useQueryClient } from '@tanstack/react-query'
import { QRCodeSVG } from 'qrcode.react'
import { useState } from 'react'
import { api, type PanelSettings } from '../api/client'
import { ApiError, formatRelativeTime } from '../api/errors'
import { useAppStore } from '../store/useAppStore'
import { evaluatePasswordStrength } from '../api/passwordStrength'
import { ModalDialog } from './Dialogs'

type SecurityModalState =
  | { kind: 'none' }
  | { kind: 'rebind-verify' }
  | { kind: 'rebind-qr'; otpauthUri: string; secret: string }
  | { kind: 'regen-verify' }
  | { kind: 'codes'; title: string; codes: string[] }

export function SecurityPage() {
  const queryClient = useQueryClient()
  const showToast = useAppStore((s) => s.showToast)

  const securityQuery = useQuery({
    queryKey: ['security'],
    queryFn: () => api.getSecurity(),
  })

  const sessionsQuery = useQuery({
    queryKey: ['sessions'],
    queryFn: () => api.getSessions(),
  })

  const settingsQuery = useQuery({
    queryKey: ['settings'],
    queryFn: () => api.getSettings(),
  })
  const [settingBusy, setSettingBusy] = useState(false)

  const changeSetting = async (change: Partial<PanelSettings>, label: string) => {
    setSettingBusy(true)
    try {
      const value = await api.changeSettings(change)
      queryClient.setQueryData(['settings'], value)
      const enabled = Object.values(change)[0]
      showToast(`已${enabled ? '开启' : '关闭'}${label}`)
    } catch (err) {
      showToast(err instanceof ApiError ? err.message : '操作失败')
    } finally {
      setSettingBusy(false)
    }
  }

  const [curPw, setCurPw] = useState('')
  const [newPw1, setNewPw1] = useState('')
  const [newPw2, setNewPw2] = useState('')
  const [pwBusy, setPwBusy] = useState(false)

  const [modal, setModal] = useState<SecurityModalState>({ kind: 'none' })
  const [verifyPw, setVerifyPw] = useState('')
  const [verifyCode, setVerifyCode] = useState('')
  const [newTotpCode, setNewTotpCode] = useState('')
  const [modalErr, setModalErr] = useState('')
  const [modalBusy, setModalBusy] = useState(false)

  const pwStrength = evaluatePasswordStrength(newPw1)

  const handleSavePassword = async () => {
    if (!curPw) {
      showToast('请输入当前密码')
      return
    }
    if (newPw1.length < 12) {
      showToast('新密码至少 12 位')
      return
    }
    if (newPw1 !== newPw2) {
      showToast('两次输入的新密码不一致')
      return
    }
    setPwBusy(true)
    const c = curPw
    const n = newPw1
    setCurPw('')
    setNewPw1('')
    setNewPw2('')
    try {
      await api.changePassword(c, n)
      showToast('密码已修改，其他设备已退出')
      await queryClient.invalidateQueries({ queryKey: ['sessions'] })
    } catch (err) {
      showToast(err instanceof ApiError ? err.message : '密码修改失败')
    } finally {
      setPwBusy(false)
    }
  }

  const openRebindVerify = () => {
    setVerifyPw('')
    setVerifyCode('')
    setModalErr('')
    setModal({ kind: 'rebind-verify' })
  }

  const openRegenVerify = () => {
    setVerifyPw('')
    setVerifyCode('')
    setModalErr('')
    setModal({ kind: 'regen-verify' })
  }

  const handleRebindStart = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!verifyPw || !verifyCode) {
      setModalErr('请输入当前密码和 6 位验证码')
      return
    }
    setModalBusy(true)
    setModalErr('')
    const pw = verifyPw
    const code = verifyCode
    setVerifyPw('')
    setVerifyCode('')
    try {
      const res = await api.startTotpRebind(pw, code)
      setNewTotpCode('')
      setModal({
        kind: 'rebind-qr',
        otpauthUri: res.otpauthUri,
        secret: res.secret,
      })
    } catch (err) {
      setModalErr(err instanceof ApiError ? err.message : '验证失败')
    } finally {
      setModalBusy(false)
    }
  }

  const handleRebindConfirm = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!newTotpCode) {
      setModalErr('请输入新验证器显示的 6 位验证码')
      return
    }
    setModalBusy(true)
    setModalErr('')
    try {
      const res = await api.confirmTotpRebind(newTotpCode)
      setNewTotpCode('')
      await queryClient.invalidateQueries({ queryKey: ['security'] })
      setModal({
        kind: 'codes',
        title: '验证器已更换',
        codes: res.recoveryCodes ?? [],
      })
    } catch (err) {
      setModalErr(err instanceof ApiError ? err.message : '确认更换失败')
    } finally {
      setModalBusy(false)
    }
  }

  const handleRegenSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!verifyPw || !verifyCode) {
      setModalErr('请输入当前密码和 6 位验证码')
      return
    }
    setModalBusy(true)
    setModalErr('')
    const pw = verifyPw
    const code = verifyCode
    setVerifyPw('')
    setVerifyCode('')
    try {
      const res = await api.regenerateRecoveryCodes(pw, code)
      await queryClient.invalidateQueries({ queryKey: ['security'] })
      setModal({
        kind: 'codes',
        title: '新的恢复码',
        codes: res.recoveryCodes ?? [],
      })
    } catch (err) {
      setModalErr(err instanceof ApiError ? err.message : '生成恢复码失败')
    } finally {
      setModalBusy(false)
    }
  }

  const handleCopyCodes = (codes: string[]) => {
    if (navigator.clipboard && typeof navigator.clipboard.writeText === 'function') {
      void navigator.clipboard.writeText(codes.join('\n')).catch(() => {})
    }
    showToast('恢复码已复制')
  }

  const handleRevokeSession = async (publicId: string) => {
    try {
      await api.revokeSession(publicId)
      showToast('该设备已退出')
      await queryClient.invalidateQueries({ queryKey: ['sessions'] })
    } catch (err) {
      showToast(err instanceof ApiError ? err.message : '注销失败')
    }
  }

  const handleRevokeOthers = async () => {
    try {
      await api.revokeOtherSessions()
      showToast('其他设备已全部退出')
      await queryClient.invalidateQueries({ queryKey: ['sessions'] })
    } catch (err) {
      showToast(err instanceof ApiError ? err.message : '操作失败')
    }
  }

  const sessions = sessionsQuery.data?.sessions ?? []
  const hasOtherSessions = sessions.some((s) => !s.current)
  const totpBoundAt = securityQuery.data?.totpBoundAt
    ? securityQuery.data.totpBoundAt.slice(0, 10)
    : '—'
  const codesLeft = securityQuery.data?.recoveryCodesLeft ?? '—'

  return (
    <>
      <div className="page-head">
        <h1>登录与安全</h1>
      </div>

      <section className="card card-wide">
        <h2>安全开关</h2>
        <div className="setting-row">
          <label className="switch" htmlFor="setting-reauth">
            <input
              id="setting-reauth"
              type="checkbox"
              checked={settingsQuery.data?.requireReauth ?? true}
              disabled={!settingsQuery.data || settingBusy}
              onChange={(e) =>
                void changeSetting({ requireReauth: e.target.checked }, '敏感操作二次确认')
              }
            />
            敏感操作二次确认
          </label>
          <p className="muted">
            开启后，登录超过 5 分钟再添加、修改或移除账号，修改通知渠道，发布或删除端口，保存或删除快照，删除文件或机器时，要重新输入密码和两步验证码。关闭后这些操作不再询问：如果有人拿到了你已登录的浏览器，就能直接改动账号和删除机器。修改密码、更换验证器、生成恢复码和注销设备始终需要验证，不受此开关影响。
          </p>
        </div>
        <div className="setting-row">
          <label className="switch" htmlFor="setting-ssh">
            <input
              id="setting-ssh"
              type="checkbox"
              checked={settingsQuery.data?.blockInternalSsh ?? true}
              disabled={!settingsQuery.data || settingBusy}
              onChange={(e) =>
                void changeSetting({ blockInternalSsh: e.target.checked }, '拦截内网地址')
              }
            />
            拦截内网地址
          </label>
          <p className="muted">
            此开关同时作用于终端 SSH 和 Webhook。打开终端时，面板会替你连接 InstaBox 返回的 SSH 地址。开启后，只连接公网地址，拒绝本机、内网和保留地址，防止错误或被篡改的地址让面板去访问你的内部服务。关闭后，任何地址都可能被连接。只有在本机代理使用 fake-IP（地址为 198.18.x.x）导致终端打不开时才建议关闭，并建议只在本地测试时使用；服务器部署请保持开启。SSH 服务器的身份校验不受此开关影响。
          </p>
        </div>
        <p className="muted">关闭任一开关前，都需要重新验证身份。</p>
      </section>

      <section className="card">
        <h2>修改密码</h2>
        <label className="lbl" htmlFor="pw0">
          当前密码
          <input
            className="field"
            id="pw0"
            type="password"
            autoComplete="off"
            value={curPw}
            onChange={(e) => setCurPw(e.target.value)}
          />
        </label>
        <div className="grid2">
          <label className="lbl" htmlFor="pw1">
            新密码
            <input
              className="field"
              id="pw1"
              type="password"
              autoComplete="off"
              value={newPw1}
              onChange={(e) => setNewPw1(e.target.value)}
            />
          </label>
          <label className="lbl" htmlFor="pw2">
            确认新密码
            <input
              className="field"
              id="pw2"
              type="password"
              autoComplete="off"
              value={newPw2}
              onChange={(e) => setNewPw2(e.target.value)}
            />
          </label>
        </div>
        <div>
          <div
            className="meter pw-meter"
            style={{ '--c': pwStrength.color } as React.CSSProperties}
          >
            <i id="pwm" style={{ width: pwStrength.width }} />
          </div>
          <div className="muted" id="pwt" style={{ fontSize: '12px', marginTop: '5px' }}>
            {pwStrength.text}
          </div>
        </div>
        <div className="notice">
          修改成功后，其他设备的登录会立即退出，已打开的终端也会关闭。
        </div>
        <div className="btn-row">
          <button
            type="button"
            className={`btn primary ${pwBusy ? 'loading' : ''}`}
            disabled={pwBusy}
            onClick={() => void handleSavePassword()}
          >
            修改密码
          </button>
        </div>
      </section>

      <section className="card">
        <h2>两步验证</h2>
        <dl className="kv">
          <dt>状态</dt>
          <dd>
            <span className="state" style={{ '--c': 'var(--ok)' } as React.CSSProperties}>
              <span className="led led-running" />
              已启用
            </span>
          </dd>
          <dt>绑定时间</dt>
          <dd className="num">{totpBoundAt}</dd>
        </dl>
        <div className="notice">
          这个面板保存了所有账号的 Key，所以两步验证不能关闭，只能更换验证器。
        </div>
        <div className="btn-row">
          <button type="button" className="btn" onClick={openRebindVerify}>
            更换验证器
          </button>
        </div>
      </section>

      <section className="card">
        <h2>恢复码</h2>
        <dl className="kv">
          <dt>剩余可用</dt>
          <dd className="num">{codesLeft} / 10</dd>
        </dl>
        <p className="muted" style={{ margin: 0, maxWidth: '62ch' }}>
          手机丢失时，用恢复码代替验证码登录。每个只能用一次。
        </p>
        <div className="btn-row">
          <button type="button" className="btn" onClick={openRegenVerify}>
            重新生成
          </button>
        </div>
      </section>

      <section className="card card-wide">
        <h2>登录设备</h2>
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>设备</th>
                <th>来源 IP</th>
                <th>最近活动</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {sessions.length > 0 ? (
                sessions.map((s) => (
                  <tr key={s.publicId}>
                    <td>
                      <b style={{ fontWeight: 600 }}>{s.device}</b>
                      {s.current ? (
                        <>
                          {' '}
                          <span className="tag">当前</span>
                        </>
                      ) : null}
                    </td>
                    <td className="muted mono">{s.sourceAddress}</td>
                    <td className="muted num">
                      {s.current ? '刚刚' : formatRelativeTime(s.lastSeen)}
                    </td>
                    <td>
                      <div className="row-actions">
                        {!s.current ? (
                          <button
                            type="button"
                            className="btn"
                            onClick={() => void handleRevokeSession(s.publicId)}
                          >
                            注销
                          </button>
                        ) : null}
                      </div>
                    </td>
                  </tr>
                ))
              ) : (
                <tr>
                  <td colSpan={4} className="muted">
                    暂无会话记录
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
        <div className="btn-row">
          <button
            type="button"
            className="btn"
            disabled={!hasOtherSessions}
            onClick={() => void handleRevokeOthers()}
          >
            退出其他所有设备
          </button>
        </div>
      </section>

      <ModalDialog
        open={modal.kind !== 'none'}
        onClose={() => {
          // Recovery codes dialog can ONLY be closed by clicking "我已保存"
          if (modal.kind === 'codes') return
          setModal({ kind: 'none' })
        }}
      >
        {modal.kind === 'rebind-verify' ? (
          <form onSubmit={(e) => void handleRebindStart(e)} style={{ display: 'contents' }}>
            <h2>更换验证器</h2>
            <p className="muted" style={{ margin: 0 }}>
              先验证当前的密码和验证码。
            </p>
            <label className="lbl" htmlFor="sec-rp">
              密码
              <input
                className="field wide"
                id="sec-rp"
                type="password"
                autoComplete="off"
                value={verifyPw}
                onChange={(e) => setVerifyPw(e.target.value)}
                autoFocus
              />
            </label>
            <label className="lbl" htmlFor="sec-rc">
              当前验证码
              <input
                className="field wide mono"
                id="sec-rc"
                inputMode="numeric"
                maxLength={6}
                placeholder="6 位数字"
                autoComplete="off"
                value={verifyCode}
                onChange={(e) => setVerifyCode(e.target.value)}
              />
            </label>
            {modalErr ? <div className="errline">{modalErr}</div> : null}
            <div className="dlg-foot">
              <button
                type="button"
                className="btn"
                onClick={() => setModal({ kind: 'none' })}
              >
                取消
              </button>
              <button
                type="submit"
                className={`btn primary ${modalBusy ? 'loading' : ''}`}
                disabled={modalBusy}
              >
                验证
              </button>
            </div>
          </form>
        ) : modal.kind === 'rebind-qr' ? (
          <form onSubmit={(e) => void handleRebindConfirm(e)} style={{ display: 'contents' }}>
            <h2>绑定新验证器</h2>
            <p className="muted" style={{ margin: 0 }}>
              用新设备的身份验证器扫描二维码，再输入它显示的 6 位验证码。旧验证器在确认前继续有效。
            </p>
            <div className="qr-box">
              <div className="qr" role="img" aria-label="两步验证二维码">
                <QRCodeSVG value={modal.otpauthUri} size={112} />
              </div>
              <div>
                <div className="section-title">无法扫码时手动输入</div>
                <div className="mono" style={{ overflowWrap: 'anywhere', marginTop: '4px' }}>
                  {modal.secret}
                </div>
              </div>
            </div>
            <label className="lbl" htmlFor="nc">
              新验证码
              <input
                className="field wide mono"
                id="nc"
                inputMode="numeric"
                maxLength={6}
                placeholder="6 位数字"
                autoComplete="off"
                value={newTotpCode}
                onChange={(e) => setNewTotpCode(e.target.value)}
                autoFocus
              />
            </label>
            {modalErr ? <div className="errline">{modalErr}</div> : null}
            <div className="dlg-foot">
              <button
                type="button"
                className="btn"
                onClick={() => setModal({ kind: 'none' })}
              >
                取消
              </button>
              <button
                type="submit"
                className={`btn primary ${modalBusy ? 'loading' : ''}`}
                disabled={modalBusy}
              >
                确认更换
              </button>
            </div>
          </form>
        ) : modal.kind === 'regen-verify' ? (
          <form onSubmit={(e) => void handleRegenSubmit(e)} style={{ display: 'contents' }}>
            <h2>重新生成恢复码</h2>
            <p className="muted" style={{ margin: 0 }}>
              旧的恢复码会立即作废。
            </p>
            <label className="lbl" htmlFor="reg-rp">
              密码
              <input
                className="field wide"
                id="reg-rp"
                type="password"
                autoComplete="off"
                value={verifyPw}
                onChange={(e) => setVerifyPw(e.target.value)}
                autoFocus
              />
            </label>
            <label className="lbl" htmlFor="reg-rc">
              当前验证码
              <input
                className="field wide mono"
                id="reg-rc"
                inputMode="numeric"
                maxLength={6}
                placeholder="6 位数字"
                autoComplete="off"
                value={verifyCode}
                onChange={(e) => setVerifyCode(e.target.value)}
              />
            </label>
            {modalErr ? <div className="errline">{modalErr}</div> : null}
            <div className="dlg-foot">
              <button
                type="button"
                className="btn"
                onClick={() => setModal({ kind: 'none' })}
              >
                取消
              </button>
              <button
                type="submit"
                className={`btn primary ${modalBusy ? 'loading' : ''}`}
                disabled={modalBusy}
              >
                验证
              </button>
            </div>
          </form>
        ) : modal.kind === 'codes' ? (
          <>
            <h2>{modal.title}</h2>
            <div className="notice warn">
              这些恢复码只显示这一次，旧的恢复码已经作废。请保存到安全的地方。
            </div>
            <div className="codes mono">
              {modal.codes.map((c) => (
                <span key={c}>{c}</span>
              ))}
            </div>
            <div className="dlg-foot">
              <button
                type="button"
                className="btn"
                onClick={() => handleCopyCodes(modal.codes)}
              >
                复制全部
              </button>
              <button
                type="button"
                className="btn primary"
                onClick={() => {
                  setModal({ kind: 'none' })
                  showToast('已完成')
                }}
              >
                我已保存
              </button>
            </div>
          </>
        ) : null}
      </ModalDialog>
    </>
  )
}
