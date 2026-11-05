import type { RFBOptions } from '@novnc/novnc'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse, http } from 'msw'
import { setupServer } from 'msw/node'
import type { ReactNode } from 'react'
import { afterAll, afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'
import {
  type DesktopDTO,
  type RFBConstructor,
  arrowCursor,
  buildDesktopWsUrl,
  desktopSeam,
} from '../api/desktop'
import { DesktopContent } from '../components/DesktopWindow'
import { TerminalWindowsLayer, TerminalDock } from '../components/TerminalWindow'
import { useAppStore } from '../store/useAppStore'

class FakeRFB extends EventTarget {
  static instances: FakeRFB[] = []
  static autoConnect = true

  target: HTMLElement
  url: string
  options: RFBOptions | undefined
  scaleViewport = false
  showDotCursor = false
  resizeSession = false
  viewOnly = false
  disconnected = false
  focused = false
  ctrlAltDelCount = 0

  constructor(
    target: HTMLElement,
    urlOrChannel: string | WebSocket | RTCDataChannel,
    options?: RFBOptions,
  ) {
    super()
    this.target = target
    this.url = String(urlOrChannel)
    this.options = options
    FakeRFB.instances.push(this)

    if (FakeRFB.autoConnect) {
      queueMicrotask(() => {
        if (!this.disconnected) {
          this.dispatchEvent(new CustomEvent('connect'))
        }
      })
    }
  }

  disconnect(): void {
    if (this.disconnected) return
    this.disconnected = true
    this.dispatchEvent(
      new CustomEvent('disconnect', { detail: { clean: true } }),
    )
  }

  sendCtrlAltDel(): void {
    this.ctrlAltDelCount += 1
  }

  focus(): void {
    this.focused = true
  }

  blur(): void {
    this.focused = false
  }
}

const server = setupServer()

beforeAll(() => server.listen())
beforeEach(() => {
  useAppStore.setState({ terminals: [], topZ: 10, winSeq: 0 })
  FakeRFB.instances = []
  FakeRFB.autoConnect = true
  desktopSeam.pollIntervalMs = 30
  desktopSeam.loadRFB = async () => FakeRFB as unknown as RFBConstructor
})
afterEach(() => {
  server.resetHandlers()
})
afterAll(() => server.close())

function show(node: ReactNode) {
  useAppStore.setState({
    authRequired: false,
    session: {
      administratorId: 'admin',
      csrfToken: 'csrf-token-1',
      expiresAt: new Date(Date.now() + 3600_000).toISOString(),
      recentlyAuthenticated: true,
    },
  })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={client}>{node}</QueryClientProvider>)
}

it('未安装 -> 点击安装 -> 轮询至已安装 -> 点击启动 -> 运行中自动连接', async () => {
  const user = userEvent.setup()
  const actions: string[] = []
  let pollCount = 0

  server.use(
    http.post('/api/v1/machines/m/desktop', async ({ request }) => {
      const body = (await request.json()) as { credentialId: string; action: string }
      actions.push(`${body.credentialId}:${body.action}`)
      if (body.action === 'status') {
        if (actions.length === 1) {
          return HttpResponse.json<DesktopDTO>({
            installed: false,
            running: false,
            installing: false,
            failed: false,
          })
        }
        pollCount += 1
        if (pollCount < 2) {
          return HttpResponse.json<DesktopDTO>({
            installed: false,
            running: false,
            installing: true,
            failed: false,
          })
        }
        return HttpResponse.json<DesktopDTO>({
          installed: true,
          running: false,
          installing: false,
          failed: false,
        })
      }
      if (body.action === 'install') {
        return HttpResponse.json<DesktopDTO>({
          installed: false,
          running: false,
          installing: true,
          failed: false,
        })
      }
      return HttpResponse.json<DesktopDTO>({
        installed: true,
        running: true,
        installing: false,
        failed: false,
      })
    }),
    http.post('/api/v1/machines/m/desktop/tickets', () =>
      HttpResponse.json({ ticket: 'tk-start-1' }),
    ),
  )

  show(
    <DesktopContent
      machineId="m"
      machineName="box"
      credentialId="cred-1"
      onClose={() => {}}
    />,
  )

  expect(await screen.findByText(/1\.4 GiB/)).toBeInTheDocument()
  expect(FakeRFB.instances).toHaveLength(0)

  await user.click(screen.getByRole('button', { name: '安装桌面' }))
  expect(await screen.findByText(/正在安装桌面环境/)).toBeInTheDocument()

  expect(await screen.findByRole('button', { name: '启动桌面' })).toBeInTheDocument()
  const countAfterInstalled = actions.length
  await new Promise((r) => setTimeout(r, 80))
  expect(actions.length).toBe(countAfterInstalled)

  await user.click(screen.getByRole('button', { name: '启动桌面' }))
  await waitFor(() => expect(FakeRFB.instances).toHaveLength(1))
  await waitFor(() => expect(screen.getByText('已连接')).toBeInTheDocument())

  const rfb = FakeRFB.instances[0]
  expect(rfb.scaleViewport).toBe(true)
  expect(rfb.resizeSession).toBe(false)
  expect(rfb.focused).toBe(true)
})

it('打开时已处于运行状态则直接连接，且票据只放在 wsProtocols 不出现在 URL 中', async () => {
  const user = userEvent.setup()

  server.use(
    http.post('/api/v1/machines/m/desktop', () =>
      HttpResponse.json<DesktopDTO>({
        installed: true,
        running: true,
        installing: false,
        failed: false,
      }),
    ),
    http.post('/api/v1/machines/m/desktop/tickets', () =>
      HttpResponse.json({ ticket: 'secret-ticket-99' }),
    ),
  )

  show(
    <DesktopContent
      machineId="m"
      machineName="box"
      credentialId="cred-1"
      onClose={() => {}}
    />,
  )

  await waitFor(() => expect(FakeRFB.instances).toHaveLength(1))
  await waitFor(() => expect(screen.getByText('已连接')).toBeInTheDocument())

  const rfb = FakeRFB.instances[0]
  expect(rfb.url).toMatch(/^ws:\/\/[^/]+\/api\/v1\/desktop\/ws$/)
  expect(rfb.url).not.toContain('secret-ticket-99')
  expect(rfb.options?.wsProtocols).toEqual([
    'instadeck.desktop.v1',
    'instadeck.ticket.secret-ticket-99',
  ])
  expect(buildDesktopWsUrl({ protocol: 'https:', host: 'deck.example.com' })).toBe(
    'wss://deck.example.com/api/v1/desktop/ws',
  )

  await user.click(screen.getByRole('button', { name: '发送 Ctrl+Alt+Del' }))
  expect(rfb.ctrlAltDelCount).toBe(1)
})

it('failed 状态显示失败提示并允许重试，轮询出错时显示错误且不快速死循环', async () => {
  const user = userEvent.setup()
  let installAttempt = 0
  let statusCalls = 0

  server.use(
    http.post('/api/v1/machines/m/desktop', async ({ request }) => {
      const body = (await request.json()) as { action: string }
      if (body.action === 'status') {
        statusCalls += 1
        if (statusCalls === 1) {
          return HttpResponse.json<DesktopDTO>({
            installed: false,
            running: false,
            installing: false,
            failed: true,
          })
        }
        return HttpResponse.json(
          { error: { code: 'desktop_unavailable', retryable: true } },
          { status: 503 },
        )
      }
      installAttempt += 1
      return HttpResponse.json<DesktopDTO>({
        installed: false,
        running: false,
        installing: true,
        failed: false,
      })
    }),
  )

  show(
    <DesktopContent
      machineId="m"
      machineName="box"
      credentialId="cred-1"
      onClose={() => {}}
    />,
  )

  expect(await screen.findByText('上次安装远程桌面失败，请重试。')).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: '安装桌面' }))
  expect(installAttempt).toBe(1)

  expect(await screen.findByText('远程桌面服务暂不可用，请稍后重试')).toBeInTheDocument()
  const callsAfterError = statusCalls
  await new Promise((r) => setTimeout(r, 90))
  expect(statusCalls).toBe(callsAfterError)
})

it('异常断开后点击重新连接会申请新票据，双击连接按钮只创建一个连接', async () => {
  const user = userEvent.setup()
  let ticketCounter = 0

  server.use(
    http.post('/api/v1/machines/m/desktop', () =>
      HttpResponse.json<DesktopDTO>({
        installed: true,
        running: true,
        installing: false,
        failed: false,
      }),
    ),
    http.post('/api/v1/machines/m/desktop/tickets', () => {
      ticketCounter += 1
      return HttpResponse.json({ ticket: `tk-${ticketCounter}` })
    }),
  )

  show(
    <DesktopContent
      machineId="m"
      machineName="box"
      credentialId="cred-1"
      onClose={() => {}}
    />,
  )

  await waitFor(() => expect(screen.getByText('已连接')).toBeInTheDocument())
  expect(FakeRFB.instances).toHaveLength(1)
  expect(ticketCounter).toBe(1)

  act(() => {
    FakeRFB.instances[0].dispatchEvent(
      new CustomEvent('disconnect', { detail: { clean: false } }),
    )
  })

  expect(await screen.findByText('无法连接到远程桌面。')).toBeInTheDocument()

  FakeRFB.autoConnect = false
  const reconnectButtons = screen.getAllByRole('button', { name: '重新连接' })
  await user.dblClick(reconnectButtons[reconnectButtons.length - 1])

  await waitFor(() => expect(FakeRFB.instances).toHaveLength(2))
  expect(ticketCounter).toBe(2)
  expect(FakeRFB.instances[1].options?.wsProtocols).toEqual([
    'instadeck.desktop.v1',
    'instadeck.ticket.tk-2',
  ])
})

it('桌面获得焦点时 Escape 不关闭窗口，包括断开之后', async () => {
  server.use(...runningDesktop())
  const close = vi.fn()
  show(<DesktopContent machineId="m" machineName="box" credentialId="c" onClose={close} />)
  await waitFor(() => expect(screen.getByText('已连接')).toBeInTheDocument())
  const target = screen.getByTestId('desktop-screen')
  target.focus()
  await userEvent.keyboard('{Escape}')
  expect(close).not.toHaveBeenCalled()
  act(() => FakeRFB.instances[0].dispatchEvent(new CustomEvent('disconnect', { detail: { clean: false } })))
  await userEvent.keyboard('{Escape}')
  expect(close).not.toHaveBeenCalled()
})

it('主动关闭对话框会调用 disconnect 且不产生错误提示，关闭时停止安装轮询', async () => {
  const user = userEvent.setup()
  let closed = false
  let pollCalls = 0

  server.use(
    http.post('/api/v1/machines/m/desktop', () =>
      HttpResponse.json<DesktopDTO>({
        installed: true,
        running: true,
        installing: false,
        failed: false,
      }),
    ),
    http.post('/api/v1/machines/m/desktop/tickets', () =>
      HttpResponse.json({ ticket: 'tk-close' }),
    ),
  )

  const { unmount } = show(
    <DesktopContent
      machineId="m"
      machineName="box"
      credentialId="cred-1"
      onClose={() => {
        closed = true
      }}
    />,
  )

  await waitFor(() => expect(screen.getByText('已连接')).toBeInTheDocument())
  const rfb = FakeRFB.instances[0]

  await user.click(screen.getByRole('button', { name: '关闭' }))
  expect(closed).toBe(true)
  expect(rfb.disconnected).toBe(true)
  expect(screen.queryByText(/失败/)).toBeNull()
  unmount()

  server.use(
    http.post('/api/v1/machines/m2/desktop', () => {
      pollCalls += 1
      return HttpResponse.json<DesktopDTO>({
        installed: false,
        running: false,
        installing: true,
        failed: false,
      })
    }),
  )

  const sub = show(
    <DesktopContent
      machineId="m2"
      machineName="box2"
      credentialId="cred-1"
      onClose={() => {}}
    />,
  )
  await waitFor(() => expect(pollCalls).toBeGreaterThanOrEqual(1))
  sub.unmount()
  const callsAtUnmount = pollCalls
  await new Promise((r) => setTimeout(r, 90))
  expect(pollCalls).toBe(callsAtUnmount)
})

it('动态 import 尚未完成时卸载组件不会创建 RFB 连接', async () => {
  let resolveLoader: ((ctor: RFBConstructor) => void) | null = null
  desktopSeam.loadRFB = () =>
    new Promise<RFBConstructor>((resolve) => {
      resolveLoader = resolve
    })

  server.use(
    http.post('/api/v1/machines/m/desktop', () =>
      HttpResponse.json<DesktopDTO>({
        installed: true,
        running: true,
        installing: false,
        failed: false,
      }),
    ),
    http.post('/api/v1/machines/m/desktop/tickets', () =>
      HttpResponse.json({ ticket: 'tk-late' }),
    ),
  )

  const { unmount } = show(
    <DesktopContent
      machineId="m"
      machineName="box"
      credentialId="cred-1"
      onClose={() => {}}
    />,
  )

  await waitFor(() => expect(resolveLoader).not.toBeNull())
  unmount()

  await act(async () => {
    resolveLoader!(FakeRFB as unknown as RFBConstructor)
  })

  expect(FakeRFB.instances).toHaveLength(0)
})

const runningDesktop = () => [
  http.post('/api/v1/machines/m/desktop', () =>
    HttpResponse.json<DesktopDTO>({ installed: true, running: true, installing: false, failed: false }),
  ),
  http.post('/api/v1/machines/m/desktop/tickets', () => HttpResponse.json({ ticket: 'tk' })),
]

it('服务器断开时说明可能的原因，并提供恢复入口', async () => {
  server.use(...runningDesktop())
  show(<DesktopContent machineId="m" machineName="box" credentialId="c" onClose={() => {}} />)
  await waitFor(() => expect(screen.getByText('已连接')).toBeInTheDocument())
  act(() => {
    FakeRFB.instances[0].dispatchEvent(new CustomEvent('disconnect', { detail: { clean: true } }))
  })
  expect(await screen.findByText(/可能是桌面已停止或网络中断/)).toBeInTheDocument()
  expect(screen.queryByText(/正常断开/)).toBeNull()
  expect(screen.getByRole('button', { name: '启动桌面', hidden: true })).toBeInTheDocument()
  expect(screen.getByRole('button', { name: '重新安装', hidden: true })).toBeInTheDocument()
})

it('全屏包含工具栏，并把焦点交还给桌面', async () => {
  const user = userEvent.setup()
  const requestFullscreen = vi.fn(async () => {})
  const original = HTMLElement.prototype.requestFullscreen
  HTMLElement.prototype.requestFullscreen = requestFullscreen
  try {
    server.use(...runningDesktop())
    useAppStore.getState().openTerminal({ kind: 'desktop', machineId: 'm', machineName: 'box', credentialId: 'c', accountName: 'account', accountColor: '#fff' })
    show(<TerminalWindowsLayer />)
    await waitFor(() => expect(screen.getByText('已连接')).toBeInTheDocument())
    FakeRFB.instances[0].focused = false
    await user.click(screen.getByRole('button', { name: '全屏', hidden: true }))
    await waitFor(() => expect(requestFullscreen).toHaveBeenCalled())
    expect((requestFullscreen.mock.contexts[0] as unknown as Element).contains(screen.getByRole('button', { name: '最小化', hidden: true }))).toBe(true)
    await waitFor(() => expect(FakeRFB.instances[0].focused).toBe(true))
  } finally {
    HTMLElement.prototype.requestFullscreen = original
  }
})


it('桌面共享窗口和 dock：最小化保留连接，重开恢复并聚焦，关闭断开', async () => {
  let tickets = 0
  server.use(...runningDesktop())
  server.use(http.post('/api/v1/machines/m/desktop/tickets', () => {
    tickets += 1
    return HttpResponse.json({ ticket: `tk-${tickets}` })
  }))
  const params = { kind: 'desktop' as const, machineId: 'm', machineName: 'box', credentialId: 'c', accountName: 'account', accountColor: '#fff' }
  useAppStore.getState().openTerminal(params)
  show(<><TerminalWindowsLayer /><TerminalDock /></>)
  await waitFor(() => expect(FakeRFB.instances).toHaveLength(1))
  await waitFor(() => expect(screen.getByText('已连接')).toBeInTheDocument())
  const rfb = FakeRFB.instances[0]
  const win = screen.getByRole('region', { name: 'account box 桌面' })
  expect(document.querySelector('dialog')).toBeNull()
  const z = useAppStore.getState().terminals[0].z
  await userEvent.click(screen.getByRole('button', { name: '最小化' }))
  await waitFor(() => expect(win).not.toBeVisible())
  expect(rfb.disconnected).toBe(false)
  expect(tickets).toBe(1)
  rfb.focused = false
  await userEvent.click(screen.getByRole('button', { name: /account \/ box · 桌面/ }))
  await waitFor(() => expect(win).toBeVisible())
  await waitFor(() => expect(rfb.focused).toBe(true))
  expect(useAppStore.getState().terminals[0].z).toBeGreaterThan(z)
  act(() => useAppStore.getState().minimizeTerminal(useAppStore.getState().terminals[0].id))
  act(() => { useAppStore.getState().openTerminal(params) })
  await waitFor(() => expect(win).toBeVisible())
  expect(useAppStore.getState().terminals).toHaveLength(1)
  expect(FakeRFB.instances).toEqual([rfb])
  expect(tickets).toBe(1)
  rfb.focused = false
  await userEvent.click(win)
  await waitFor(() => expect(rfb.focused).toBe(true))
  await userEvent.click(win.querySelector('[data-act="winClose"]')!)
  await waitFor(() => expect(rfb.disconnected).toBe(true))
  expect(useAppStore.getState().terminals).toHaveLength(0)
})

it('终端和桌面按种类去重并共享八个窗口的上限', () => {
  const open = useAppStore.getState().openTerminal
  const params = { machineId: 'm', machineName: 'box', credentialId: 'c', accountName: 'account', accountColor: '#fff' }
  open(params)
  open({ ...params, kind: 'desktop' })
  for (let n = 0; n < 6; n++) open({ ...params, machineId: `m${n}` })
  expect(open({ ...params, kind: 'desktop', machineId: 'other' }).opened).toBe(false)
  expect(useAppStore.getState().terminals).toHaveLength(8)
  expect(open({ ...params, kind: 'desktop' }).opened).toBe(true)
})

it('服务器没有发送光标时也能看到鼠标', async () => {
  server.use(
    http.post('/api/v1/machines/m/desktop', () =>
      HttpResponse.json<DesktopDTO>({ installed: true, running: true, installing: false, failed: false }),
    ),
    http.post('/api/v1/machines/m/desktop/tickets', () => HttpResponse.json({ ticket: 'tk' })),
  )
  show(<DesktopContent machineId="m" machineName="box" credentialId="c" onClose={() => {}} />)
  await waitFor(() => expect(FakeRFB.instances).toHaveLength(1))
  expect(FakeRFB.instances[0].showDotCursor).toBe(true)
})

it('服务器没有发送光标时显示正常大小的箭头', () => {
  const arrow = arrowCursor()
  expect(arrow.w).toBeGreaterThanOrEqual(12)
  expect(arrow.h).toBeGreaterThanOrEqual(16)
  expect(arrow.rgbaPixels).toHaveLength(arrow.w * arrow.h * 4)
  // The hot spot is the tip, which must be opaque.
  expect(arrow.rgbaPixels[3]).toBe(255)
})
