import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { SetupGate, SetupWizard } from '../components/SetupWizard'
import { useAppStore } from '../store/useAppStore'
import { formatErrorCode } from '../api/errors'

const secret = 'JBSWY3DPEHPK3PXP'
const recoveryCodes = Array.from({ length: 10 }, (_, index) => `recovery-private-${index}`)
const server = setupServer(
  http.get('/api/v1/setup/status', () => HttpResponse.json({ required: true, codeRequired: true })),
  http.post('/api/v1/setup/start', () => HttpResponse.json({ secret, otpauthUri: `otpauth://totp/InstaDeck?secret=${secret}` })),
  http.post('/api/v1/setup/complete', () => HttpResponse.json({ administratorId: 'administrator', csrfToken: 'csrf-setup', expiresAt: '2030-01-01', recentlyAuthenticated: true, recoveryCodes })),
)

beforeAll(() => server.listen())
afterEach(() => { server.resetHandlers(); useAppStore.getState().setSession(null) })
afterAll(() => server.close())

describe('首次设置', () => {
  it('status 决定向导或登录，且向导阻止挂载工作台', async () => {
    const workspace = vi.fn()
    function Workspace() { workspace(); return <p>登录界面</p> }
    const { unmount } = render(<SetupGate><Workspace /></SetupGate>)
    expect(await screen.findByText('欢迎使用 InstaDeck')).toBeInTheDocument()
    expect(workspace).not.toHaveBeenCalled()
    expect(screen.getByLabelText(/设置码/)).toBeInTheDocument()
    unmount()
    server.use(http.get('/api/v1/setup/status', () => HttpResponse.json({ required: false, codeRequired: false })))
    render(<SetupGate><Workspace /></SetupGate>)
    expect(await screen.findByText('登录界面')).toBeInTheDocument()
  })

  it('本机隐藏设置码，显示二维码和密钥，保存恢复码后才可进入', async () => {
    const user = userEvent.setup()
    const finish = vi.fn()
    render(<SetupWizard codeRequired={false} onInitialized={vi.fn()} onFinish={finish} />)
    expect(screen.queryByLabelText(/设置码/)).not.toBeInTheDocument()
    await user.type(screen.getByLabelText(/^密码/), 'strong-password-123')
    await user.type(screen.getByLabelText('确认密码'), 'strong-password-123')
    await user.click(screen.getByRole('button', { name: '下一步' }))
    expect(await screen.findByRole('img', { name: '两步验证二维码' })).toBeInTheDocument()
    expect(screen.getByText(secret)).toBeInTheDocument()
    expect(JSON.stringify(useAppStore.getState())).not.toContain(secret)
    expect(localStorage.getItem('secret')).toBeNull()
    await user.type(screen.getByLabelText('6 位验证码'), '123456')
    await user.click(screen.getByRole('button', { name: '完成验证' }))
    expect(await screen.findByText(recoveryCodes[0])).toBeInTheDocument()
    const enter = screen.getByRole('button', { name: '进入工作台' })
    expect(enter).toBeDisabled()
    await user.click(enter)
    expect(finish).not.toHaveBeenCalled()
    expect(JSON.stringify(useAppStore.getState())).not.toContain('recovery-private')
    expect(JSON.stringify(useAppStore.getState())).not.toContain(secret)
    await user.click(screen.getByLabelText('我已保存'))
    await user.click(enter)
    expect(finish).toHaveBeenCalledOnce()
  })

  it('HTTPS 错误显示中文，已初始化时重新读取状态', async () => {
    const user = userEvent.setup()
    const initialized = vi.fn()
    server.use(http.post('/api/v1/setup/start', () => HttpResponse.json({ error: { code: 'https_required' } }, { status: 403 })))
    render(<SetupWizard codeRequired={false} onInitialized={initialized} onFinish={vi.fn()} />)
    await user.type(screen.getByLabelText(/^密码/), 'strong-password-123')
    await user.type(screen.getByLabelText('确认密码'), 'strong-password-123')
    await user.click(screen.getByRole('button', { name: '下一步' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('请通过 HTTPS 访问，或在服务器本机用 127.0.0.1 打开')
    server.use(http.post('/api/v1/setup/start', () => HttpResponse.json({ error: { code: 'already_initialized' } }, { status: 409 })))
    await user.click(screen.getByRole('button', { name: '下一步' }))
    await waitFor(() => expect(initialized).toHaveBeenCalledOnce())
    for (const errorCode of ['origin_rejected', 'invalid_setup_code', 'weak_password', 'authentication_failed', 'rate_limited']) {
      expect(formatErrorCode(errorCode)).not.toBe('请求失败，请稍后重试')
    }
  })
})
