import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { apiRequest } from '../api/client'
import type { NotificationChannel } from '../api/types'
import { ModalDialog } from './Dialogs'

const events = [
  ['credential.invalid', '账号 Key 失效'],
  ['operation.failed', '操作失败或结果未知'],
  ['machine.error', '机器进入 error'],
  ['machine.stopped', '运行中的机器停止'],
]

export function NotificationsPage() {
  const client = useQueryClient()
  const query = useQuery({
    queryKey: ['notificationChannels'],
    queryFn: () => apiRequest<{ channels: NotificationChannel[] }>('/notification-channels'),
  })
  const [edit, setEdit] = useState<NotificationChannel | null | undefined>(undefined)
  const [remove, setRemove] = useState<NotificationChannel | null>(null)
  const [message, setMessage] = useState('')
  const [busy, setBusy] = useState(false)
  const refresh = () => client.invalidateQueries({ queryKey: ['notificationChannels'] })
  const action = async (path: string, method: 'POST' | 'PATCH' | 'DELETE', body?: unknown) => {
    setBusy(true)
    setMessage('')
    try {
      await apiRequest(`/notification-channels/${path}`, { method, body })
      setMessage('操作成功')
      await refresh()
      return true
    } catch (error) {
      setMessage(error instanceof Error ? error.message : '请求失败')
      return false
    } finally {
      setBusy(false)
    }
  }
  return (
    <>
      <div className="page-head">
        <h1>通知渠道</h1>
        <button className="btn primary" onClick={() => setEdit(null)}>添加渠道</button>
      </div>
      {message && <p role="status">{message}</p>}
      {query.error && <p className="errline">{query.error.message}</p>}
      <div className="table-wrap">
        <table>
          <thead><tr>{['名称', '类型', '状态', '订阅事件', '操作'].map(label => <th key={label}>{label}</th>)}</tr></thead>
          <tbody>
            {query.data?.channels.map(channel => (
              <tr key={channel.id}>
                <td>{channel.name}<p className="muted">{channel.hint}</p></td>
                <td>{channel.type}</td>
                <td>
                  <label>
                    <input type="checkbox" aria-label={`启用 ${channel.name}`} checked={channel.enabled} disabled={busy} onChange={event => void action(channel.id, 'PATCH', { enabled: event.target.checked })} />启用
                  </label>
                </td>
                <td>{channel.events.map(value => events.find(([key]) => key === value)?.[1] ?? value).join('、')}</td>
                <td>
                  <div className="btn-row">
                    <button className="btn" onClick={() => setEdit(channel)}>编辑</button>
                    <button className="btn" disabled={busy} onClick={() => void action(`${channel.id}/test`, 'POST')}>测试发送</button>
                    <button className="btn danger" onClick={() => setRemove(channel)}>删除</button>
                  </div>
                </td>
              </tr>
            ))}
            {!query.data?.channels.length && <tr><td colSpan={5} className="muted">{query.isLoading ? '正在加载渠道…' : '尚未添加通知渠道'}</td></tr>}
          </tbody>
        </table>
      </div>
      <ModalDialog open={edit !== undefined} onClose={() => setEdit(undefined)}>
        {edit !== undefined && <ChannelForm channel={edit} onClose={() => setEdit(undefined)} onSaved={refresh} />}
      </ModalDialog>
      <ModalDialog open={!!remove} onClose={() => setRemove(null)}>
        <h2>删除通知渠道</h2>
        <p>确认删除 {remove?.name}？</p>
        <div className="dlg-foot">
          <button className="btn" onClick={() => setRemove(null)}>取消</button>
          <button className="btn danger" disabled={busy} onClick={() => {
            if (remove) void action(remove.id, 'DELETE').then(ok => { if (ok) setRemove(null) })
          }}>确认删除</button>
        </div>
      </ModalDialog>
    </>
  )
}

function ChannelForm({ channel, onClose, onSaved }: {
  channel: NotificationChannel | null; onClose: () => void; onSaved: () => Promise<unknown>
}) {
  const [type, setType] = useState(channel?.type ?? 'telegram')
  const [name, setName] = useState(channel?.name ?? '')
  const [enabled, setEnabled] = useState(channel?.enabled ?? true)
  const [subscribed, setSubscribed] = useState(channel?.events ?? events.map(([value]) => value))
  const [botToken, setBotToken] = useState('')
  const [chatId, setChatId] = useState('')
  const [url, setUrl] = useState('')
  const [signingKey, setSigningKey] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const submit = async (event: React.FormEvent) => {
    event.preventDefault()
    setBusy(true)
    setError('')
    const config = { botToken, chatId, url, signingKey }
    setBotToken('')
    setUrl('')
    setSigningKey('')
    try {
      await apiRequest(`/notification-channels${channel ? `/${channel.id}` : ''}`, {
        method: channel ? 'PATCH' : 'POST', body: { name, type, enabled, events: subscribed, config },
      })
      await onSaved()
      onClose()
    } catch (error) {
      setError(error instanceof Error ? error.message : '保存失败')
    } finally {
      setBusy(false)
    }
  }
  return (
    <form style={{ display: 'contents' }} onSubmit={event => void submit(event)}>
      <h2>{channel ? '编辑' : '添加'}通知渠道</h2>
      <label className="lbl">名称
        <input className="field" required maxLength={128} value={name} onChange={event => setName(event.target.value)} />
      </label>
      <label className="lbl">类型
        <select className="field" disabled={!!channel} value={type} onChange={event => setType(event.target.value as 'telegram' | 'webhook')}>
          <option value="telegram">Telegram</option><option value="webhook">Webhook</option>
        </select>
      </label>
      {channel && <p className="muted">密钥输入留空表示不修改，保存后不会回显。</p>}
      {type === 'telegram' ? (
        <>
          <p className="muted">在 Telegram 找到 @BotFather，发送 /newbot 获取 Bot Token。先给 Bot 发消息，或把 Bot 加入群；通过 Telegram getUpdates 接口查看消息的 chat.id，作为 Chat ID。</p>
          <label className="lbl">Bot Token
            <input className="field" type="password" autoComplete="new-password" required={!channel} value={botToken} onChange={event => setBotToken(event.target.value)} />
          </label>
          <label className="lbl">Chat ID
            <input className="field" required={!channel} value={chatId} onChange={event => setChatId(event.target.value)} />
          </label>
        </>
      ) : (
        <>
          <label className="lbl">HTTPS URL
            <input className="field" type="password" autoComplete="new-password" required={!channel} value={url} onChange={event => setUrl(event.target.value)} />
          </label>
          <label className="lbl">签名密钥（可选）
            <input className="field" type="password" autoComplete="new-password" value={signingKey} onChange={event => setSigningKey(event.target.value)} />
          </label>
        </>
      )}
      <label><input type="checkbox" checked={enabled} onChange={event => setEnabled(event.target.checked)} />启用</label>
      <fieldset>
        <legend>订阅事件</legend>
        {events.map(([value, label]) => (
          <label className="lbl" key={value}>
            <input type="checkbox" checked={subscribed.includes(value)} onChange={event => setSubscribed(previous => event.target.checked ? [...previous, value] : previous.filter(item => item !== value))} />{label}
          </label>
        ))}
      </fieldset>
      {error && <p className="errline">{error}</p>}
      <div className="dlg-foot">
        <button type="button" className="btn" onClick={onClose}>取消</button>
        <button className="btn primary" disabled={busy}>保存</button>
      </div>
    </form>
  )
}
