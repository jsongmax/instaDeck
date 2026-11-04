import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useRef, useState } from 'react'
import { apiRequest } from '../api/client'
import type { PublicPort } from '../api/types'
import { ModalDialog } from './Dialogs'

const errorText = (error: unknown) => error instanceof Error ? error.message : '请求失败'
type PortInput = Pick<PublicPort, 'port' | 'name' | 'access' | 'protocol'> & { rotateToken?: boolean }

function SafeURL({ value }: { value: string }) {
  let safe = false
  try {
    const url = new URL(value)
    safe = ['https:', 'http:'].includes(url.protocol) && !url.username && !url.password
  } catch { /* Unrecognized URLs remain plain text. */ }
  return safe ? (
    <a href={value} target="_blank" rel="noopener noreferrer">{value}</a>
  ) : (
    <span className="mono">
      {value}
      <button className="btn" onClick={() => void navigator.clipboard.writeText(value)}>复制</button>
    </span>
  )
}

export function PortsPanel({ machineId, credentialId }: { machineId: string; credentialId: string }) {
  const client = useQueryClient()
  const [open, setOpen] = useState(false)
  const [once, setOnce] = useState<PublicPort | null>(null)
  const [remove, setRemove] = useState<PublicPort | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const generation = useRef(0)
  useEffect(() => () => { generation.current++ }, [])
  const path = `/machines/${encodeURIComponent(machineId)}/ports`
  const queryKey = ['ports', credentialId, machineId]
  const query = useQuery({
    queryKey,
    queryFn: () => apiRequest<{ enabled: boolean; ports: PublicPort[] }>(
      `${path}?credentialId=${encodeURIComponent(credentialId)}`,
    ),
  })
  const close = () => {
    generation.current++
    setOpen(false)
    setOnce(null)
    setError('')
  }
  const publish = async (body: PortInput) => {
    const currentGeneration = generation.current
    setBusy(true)
    setError('')
    try {
      // Do not cache publish responses: private tokens live only in this dialog.
      const value = await apiRequest<PublicPort>(path, {
        method: 'POST',
        body: {
          credentialId, port: body.port, name: body.name, access: body.access,
          protocol: body.protocol, rotateToken: body.rotateToken,
        },
      })
      if (generation.current === currentGeneration) {
        if (value.token || value.tokenUrl) {
          setOnce(value)
          setOpen(true)
        } else close()
      }
      void client.invalidateQueries({ queryKey })
    } catch (error) {
      if (generation.current === currentGeneration) setError(errorText(error))
    } finally {
      setBusy(false)
    }
  }
  const deletePort = async () => {
    if (!remove) return
    setBusy(true)
    setError('')
    try {
      await apiRequest(`${path}/${remove.port}?credentialId=${encodeURIComponent(credentialId)}`, { method: 'DELETE' })
      setRemove(null)
      await client.invalidateQueries({ queryKey })
    } catch (error) {
      setError(errorText(error))
    } finally {
      setBusy(false)
    }
  }

  return (
    <section>
      <h3>端口</h3>
      {query.data?.enabled === false ? (
        <p className="notice">这个账号所在的服务器未开启公开端口</p>
      ) : (
        <>
          <button className="btn" onClick={() => setOpen(true)}>发布端口</button>
          {query.data?.ports.map(port => (
            <div className="card" key={port.port}>
              <strong>{port.port} · {port.name}</strong>
              <p>{port.protocol} · {port.access === 'private' ? '私有' : '公开'}</p>
              <SafeURL value={port.url} />
              <div className="btn-row">
                {port.access === 'private' && (
                  <button className="btn" disabled={busy} onClick={() => void publish({ ...port, rotateToken: true })}>更换令牌</button>
                )}
                <button className="btn danger" onClick={() => { setRemove(port); setError('') }}>删除端口</button>
              </div>
            </div>
          ))}
        </>
      )}
      {query.error && <p className="errline">{errorText(query.error)}</p>}
      {error && !open && !remove && <p className="errline">{error}</p>}
      <ModalDialog open={open} onClose={close}>
        {once ? (
          <>
            <h2>请现在复制令牌</h2>
            <p className="notice warn">令牌只显示一次，关闭后无法再次查看。</p>
            <p className="mono">{once.token}</p>
            <button className="btn" onClick={() => void navigator.clipboard.writeText(once.token ?? '')}>复制令牌</button>
            <SafeURL value={once.tokenUrl ?? ''} />
            <button className="btn" onClick={close}>关闭</button>
          </>
        ) : <PortForm busy={busy} onSubmit={publish} onClose={close} />}
        {error && <p className="errline">{error}</p>}
      </ModalDialog>
      <ModalDialog open={!!remove} onClose={() => setRemove(null)}>
        <h2>删除端口 {remove?.port}</h2>
        <p>此网址将停止提供访问。</p>
        {error && <p className="errline">{error}</p>}
        <div className="dlg-foot">
          <button className="btn" onClick={() => setRemove(null)}>取消</button>
          <button className="btn danger" disabled={busy} onClick={() => void deletePort()}>确认删除</button>
        </div>
      </ModalDialog>
    </section>
  )
}

function PortForm({ busy, onSubmit, onClose }: {
  busy: boolean; onSubmit: (port: PortInput) => Promise<void>; onClose: () => void
}) {
  const [port, setPort] = useState(3000)
  const [name, setName] = useState('')
  const [access, setAccess] = useState<'private' | 'public'>('private')
  const [protocol, setProtocol] = useState<'http' | 'tcp'>('http')
  return (
    <form style={{ display: 'contents' }} onSubmit={event => { event.preventDefault(); void onSubmit({ port, name, access, protocol }) }}>
      <h2>发布端口</h2>
      <label className="lbl">端口号
        <input className="field" type="number" min={1024} max={65535} value={port} onChange={event => setPort(Number(event.target.value))} />
      </label>
      <label className="lbl">名称
        <input className="field" maxLength={100} value={name} onChange={event => setName(event.target.value)} />
      </label>
      <label className="lbl">协议
        <select className="field" value={protocol} onChange={event => {
          setProtocol(event.target.value as 'http' | 'tcp')
          if (event.target.value === 'tcp') setAccess('public')
        }}>
          <option value="http">HTTP</option><option value="tcp">TCP</option>
        </select>
      </label>
      <label className="lbl">访问方式
        <select className="field" value={access} onChange={event => setAccess(event.target.value as 'private' | 'public')}>
          <option value="private" disabled={protocol === 'tcp'}>私有</option><option value="public">公开</option>
        </select>
      </label>
      {access === 'public' && <p className="notice warn" role="alert">任何拿到这个网址的人都能访问这个端口上的服务</p>}
      <div className="dlg-foot">
        <button type="button" className="btn" onClick={onClose}>取消</button>
        <button className="btn primary" disabled={busy}>发布</button>
      </div>
    </form>
  )
}
