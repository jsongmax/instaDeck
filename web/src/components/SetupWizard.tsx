import { useEffect, useState, type ReactNode } from 'react'
import { QRCodeSVG } from 'qrcode.react'
import { api, type SetupEnrollment, type SetupStatus } from '../api/client'
import { ApiError } from '../api/errors'
import { applySkinToDocument, useAppStore } from '../store/useAppStore'
import { evaluatePasswordStrength } from '../api/passwordStrength'
import { LogoSvg } from './Icons'

export function SetupGate({ children }: { children: ReactNode }) {
  const [status, setStatus] = useState<SetupStatus | null>(null)
  const [error, setError] = useState('')
  const refresh = async () => {
    setError('')
    try { setStatus(await api.setupStatus()) }
    catch { setError('无法读取设置状态，请重试') }
  }
  useEffect(() => {
    applySkinToDocument(useAppStore.getState().skin, false)
    void api.setupStatus().then(setStatus).catch(() => setError('无法读取设置状态，请重试'))
  }, [])
  if (!status) return <div className="login"><form onSubmit={(event) => { event.preventDefault(); void refresh() }}><h1>InstaDeck</h1><p>{error || '正在读取设置状态…'}</p>{error && <button className="btn" type="submit">重试</button>}</form></div>
  if (!status.required) return children
  return <SetupWizard codeRequired={status.codeRequired} onInitialized={() => void refresh()} onFinish={() => {
    window.location.hash = 'accounts'
    useAppStore.getState().setRoute('accounts')
    useAppStore.getState().setAuthRequired(false)
    useAppStore.getState().showToast('设置完成，请添加第一个 InstaBox 账号')
    setStatus({ required: false, codeRequired: false })
  }} />
}

export function SetupWizard({ codeRequired, onInitialized, onFinish }: { codeRequired: boolean; onInitialized: () => void; onFinish: () => void }) {
  const [step, setStep] = useState(1)
  const [username, setUsername] = useState('admin')
  const [password, setPassword] = useState('')
  const [confirmation, setConfirmation] = useState('')
  const [setupCode, setSetupCode] = useState('')
  const [enrollment, setEnrollment] = useState<SetupEnrollment | null>(null)
  const [code, setCode] = useState('')
  const [codes, setCodes] = useState<string[]>([])
  const [saved, setSaved] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const strength = evaluatePasswordStrength(password)

  const submit = async (event: React.FormEvent) => {
    event.preventDefault()
    if (step === 3) {
      if (saved) { setCodes([]); onFinish() }
      return
    }
    setError('')
    if (step === 1 && password !== confirmation) { setError('两次输入的密码不一致'); return }
    setBusy(true)
    try {
      if (step === 1) {
        const result = await api.setupStart(username, password, setupCode || undefined)
        setPassword(''); setConfirmation(''); setSetupCode('')
        setEnrollment(result); setStep(2)
      } else {
        const result = await api.setupComplete(code)
        setEnrollment(null); setCode(''); setCodes(result.recoveryCodes); setStep(3)
      }
    } catch (caught) {
      if (caught instanceof ApiError && caught.code === 'already_initialized') { onInitialized(); return }
      setError(caught instanceof ApiError ? caught.message : '请求失败，请稍后重试')
    } finally { setBusy(false) }
  }

  const copy = async (value: string) => {
    try { await navigator.clipboard.writeText(value) }
    catch { setError('复制失败，请手动选择并复制') }
  }

  return <div className="login setup-login"><form key={step} onSubmit={(event) => void submit(event)}>
    <LogoSvg size={34} />
    <span className="muted">首次设置 · {step} / 3</span>
    <h1>{step === 1 ? '欢迎使用 InstaDeck' : step === 2 ? '绑定两步验证' : '保存恢复码'}</h1>
    {step === 1 && <>
      <p className="muted">这是首次设置。创建管理员后，使用密码和验证器登录。</p>
      {codeRequired && <label className="lbl">设置码<input className="field wide mono" required autoComplete="off" value={setupCode} onChange={(event) => setSetupCode(event.target.value)} /><span className="muted">设置码打印在服务器日志中，只有能访问服务器的人才能看到，用来防止别人抢先设置。查看方式：<code>docker compose logs instadeck</code> 或 <code>journalctl -u instadeck</code></span></label>}
      <label className="lbl">用户名<input className="field wide" required maxLength={64} autoComplete="username" value={username} onChange={(event) => setUsername(event.target.value)} /></label>
      <label className="lbl">密码<input className="field wide" type="password" required minLength={12} maxLength={1024} autoComplete="new-password" value={password} onChange={(event) => setPassword(event.target.value)} /><span style={{ color: strength.color }}>{strength.text}</span></label>
      <label className="lbl">确认密码<input className="field wide" type="password" required autoComplete="new-password" value={confirmation} onChange={(event) => setConfirmation(event.target.value)} /></label>
    </>}
    {step === 2 && enrollment && <>
      <p className="muted">用身份验证器扫描二维码，也可以手动输入下方密钥。</p>
      <div className="qr-box"><div className="setup-qr" role="img" aria-label="两步验证二维码"><QRCodeSVG value={enrollment.otpauthUri} size={224} marginSize={4} bgColor="#ffffff" fgColor="#000000" /></div><code className="setup-secret">{enrollment.secret}</code><button className="btn" type="button" onClick={() => void copy(enrollment.secret)}>复制密钥</button></div>
      <label className="lbl">6 位验证码<input className="field wide mono" inputMode="numeric" autoComplete="one-time-code" pattern="[0-9]{6}" required maxLength={6} value={code} onChange={(event) => setCode(event.target.value)} /></label>
      <button className="btn" type="button" disabled={busy} onClick={() => { setEnrollment(null); setCode(''); setError(''); setStep(1) }}>返回上一步，重新开始</button>
    </>}
    {step === 3 && <>
      <p className="muted">恢复码只显示这一次。验证器不可用时，每个恢复码可登录一次，请妥善保管。</p>
      <div className="codes">{codes.map((recoveryCode) => <code key={recoveryCode}>{recoveryCode}</code>)}</div>
      <button className="btn" type="button" onClick={() => void copy(codes.join('\n'))}>复制全部</button>
      <label className="lbl"><span><input type="checkbox" checked={saved} onChange={(event) => setSaved(event.target.checked)} /> 我已保存</span></label>
    </>}
    {error && <div className="errline" role="alert">{error}</div>}
    <button className="btn primary" type="submit" disabled={busy || step === 3 && !saved}>{busy ? '正在处理…' : step === 1 ? '下一步' : step === 2 ? '完成验证' : '进入工作台'}</button>
  </form></div>
}
