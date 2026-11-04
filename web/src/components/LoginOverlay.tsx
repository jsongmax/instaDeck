import { useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { api } from '../api/client'
import { ApiError } from '../api/errors'
import { useAppStore } from '../store/useAppStore'
import { LogoSvg } from './Icons'

function LoginFormBox({ onSuccess }: { onSuccess: () => void }) {
  const queryClient = useQueryClient()
  const setAuthRequired = useAppStore((s) => s.setAuthRequired)
  const showToast = useAppStore((s) => s.showToast)

  const [step, setStep] = useState<1 | 2>(1)
  const [useRecovery, setUseRecovery] = useState(false)
  const [username, setUsername] = useState('admin')
  const [password, setPassword] = useState('')
  const [code, setCode] = useState('')
  const [errMsg, setErrMsg] = useState('')
  const [submitting, setSubmitting] = useState(false)

  const handleStep1 = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!username.trim() || !password) {
      setErrMsg('请输入用户名和密码')
      return
    }
    setSubmitting(true)
    setErrMsg('')
    const pw = password
    setPassword('')
    try {
      await api.loginPassword(username.trim(), pw)
      setStep(2)
      setUseRecovery(false)
      setCode('')
    } catch (err) {
      setErrMsg(err instanceof ApiError ? err.message : '用户名或密码错误')
    } finally {
      setSubmitting(false)
    }
  }

  const handleStep2 = async (e: React.FormEvent) => {
    e.preventDefault()
    const trimmedCode = code.trim()
    if (!trimmedCode) {
      setErrMsg(useRecovery ? '请输入恢复码' : '请输入 6 位验证码')
      return
    }
    setSubmitting(true)
    setErrMsg('')
    setCode('')
    try {
      if (useRecovery) {
        await api.loginRecovery(trimmedCode)
      } else {
        await api.loginTotp(trimmedCode)
      }
      setAuthRequired(false)
      showToast('已登录')
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['accounts'] }),
        queryClient.invalidateQueries({ queryKey: ['fleet'] }),
      ])
      onSuccess()
    } catch (err) {
      setErrMsg(err instanceof ApiError ? err.message : '验证码错误，请重试')
    } finally {
      setSubmitting(false)
    }
  }

  return step === 1 ? (
    <form id="lf" onSubmit={(e) => void handleStep1(e)}>
      <LogoSvg size={34} />
      <h1>登录 InstaDeck</h1>
      <label className="lbl" htmlFor="lu">
        用户名
        <input
          className="field wide"
          id="lu"
          autoComplete="off"
          value={username}
          onChange={(e) => setUsername(e.target.value)}
        />
      </label>
      <label className="lbl" htmlFor="lp">
        密码
        <input
          className="field wide"
          id="lp"
          type="password"
          autoComplete="off"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          autoFocus
        />
      </label>
      {errMsg ? <div className="errline">{errMsg}</div> : null}
      <button
        type="submit"
        className={`btn primary ${submitting ? 'loading' : ''}`}
        style={{ justifyContent: 'center', height: '32px' }}
        disabled={submitting}
      >
        下一步
      </button>
    </form>
  ) : (
    <form id="lf" onSubmit={(e) => void handleStep2(e)}>
      <h1>{useRecovery ? '输入恢复码' : '输入验证码'}</h1>
      <p className="muted" style={{ margin: 0 }}>
        {useRecovery
          ? '输入你保存的恢复码（每个只能用一次）。'
          : '打开身份验证器，输入 6 位验证码。'}
      </p>
      <label className="lbl" htmlFor="lc">
        {useRecovery ? '恢复码' : '验证码'}
        <input
          className="field wide mono"
          id="lc"
          inputMode={useRecovery ? 'text' : 'numeric'}
          maxLength={useRecovery ? undefined : 6}
          placeholder={useRecovery ? '恢复码' : undefined}
          autoComplete="off"
          value={code}
          onChange={(e) => setCode(e.target.value)}
          autoFocus
        />
      </label>
      {errMsg ? <div className="errline">{errMsg}</div> : null}
      <button
        type="submit"
        className={`btn primary ${submitting ? 'loading' : ''}`}
        style={{ justifyContent: 'center', height: '32px' }}
        disabled={submitting}
      >
        登录
      </button>
      <button
        type="button"
        className="linkish"
        id="lr"
        style={{ alignSelf: 'flex-start' }}
        onClick={() => {
          setUseRecovery((prev) => !prev)
          setCode('')
          setErrMsg('')
        }}
      >
        {useRecovery ? '改用验证码' : '改用恢复码'}
      </button>
    </form>
  )
}

export function LoginOverlay() {
  const authRequired = useAppStore((s) => s.authRequired)
  const [fadingOut, setFadingOut] = useState(false)

  if (!authRequired && !fadingOut) return null

  return (
    <div className={`login ${fadingOut ? 'out' : ''}`} id="login">
      <LoginFormBox
        onSuccess={() => {
          setFadingOut(true)
          setTimeout(() => {
            setFadingOut(false)
          }, 320)
        }}
      />
    </div>
  )
}
