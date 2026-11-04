import { QueryClient } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse, http } from 'msw'
import { setupServer } from 'msw/node'
import {
  afterAll,
  afterEach,
  beforeAll,
  beforeEach,
  describe,
  expect,
  it,
  vi,
} from 'vitest'
import type { ITheme } from '@xterm/xterm'
import { getMachineStateMeta } from '../api/errors'
import type { Account, ActionRecord, FleetResponse, SessionInfo } from '../api/types'
import App from '../App'
import {
  MAX_TERMINAL_FRAME_BYTES,
  chunkTerminalInput,
  sendTerminalInput,
} from '../components/terminalUtils'
import { useAppStore } from '../store/useAppStore'

// Mock @xterm/xterm and @xterm/addon-fit
interface MockTerminalInstance {
  options: { theme?: ITheme; fontSize?: number }
  cols: number
  rows: number
  written: Array<string | Uint8Array>
  dataListeners: Array<(data: string) => void>
  loadAddon: ReturnType<typeof vi.fn>
  open: ReturnType<typeof vi.fn>
  write: ReturnType<typeof vi.fn>
  writeln: ReturnType<typeof vi.fn>
  dispose: ReturnType<typeof vi.fn>
  onData: (cb: (data: string) => void) => { dispose: () => void }
  emitData: (data: string) => void
}

const mockTerminals: MockTerminalInstance[] = []
const mockFitCalls: Array<() => void> = []
let nextFitDimensions = { cols: 80, rows: 24 }

vi.mock('@xterm/xterm', () => {
  class MockTerminal implements MockTerminalInstance {
    options: { theme?: ITheme; fontSize?: number }
    cols = 80
    rows = 24
    written: Array<string | Uint8Array> = []
    dataListeners: Array<(data: string) => void> = []
    loadAddon = vi.fn()
    open = vi.fn()
    write = vi.fn((data: string | Uint8Array) => {
      this.written.push(data)
    })
    writeln = vi.fn((data: string) => {
      this.written.push(data)
    })
    dispose = vi.fn()

    constructor(opts?: { theme?: ITheme; fontSize?: number }) {
      this.options = { ...(opts ?? {}) }
      mockTerminals.push(this)
    }

    onData(cb: (data: string) => void) {
      this.dataListeners.push(cb)
      return {
        dispose: () => {
          this.dataListeners = this.dataListeners.filter((x) => x !== cb)
        },
      }
    }

    emitData(data: string) {
      this.dataListeners.forEach((cb) => cb(data))
    }
  }
  return { Terminal: MockTerminal }
})

vi.mock('@xterm/addon-fit', () => {
  class MockFitAddon {
    fit = vi.fn(() => {
      mockFitCalls.push(() => {})
      const lastTerm = mockTerminals[mockTerminals.length - 1]
      if (lastTerm) {
        lastTerm.cols = nextFitDimensions.cols
        lastTerm.rows = nextFitDimensions.rows
      }
    })
  }
  return { FitAddon: MockFitAddon }
})

// Mock WebSocket for terminal tests
class FakeWebSocket {
  static instances: FakeWebSocket[] = []
  url: string
  protocols: string | string[] | undefined
  binaryType = 'blob'
  readyState = 1 // OPEN
  sent: Array<string | Uint8Array | ArrayBuffer> = []
  onopen: (() => void) | null = null
  onmessage: ((ev: { data: unknown }) => void) | null = null
  onerror: (() => void) | null = null
  onclose: (() => void) | null = null

  constructor(url: string, protocols?: string | string[]) {
    this.url = url
    this.protocols = protocols
    FakeWebSocket.instances.push(this)
    setTimeout(() => {
      this.onopen?.()
    }, 0)
  }

  send(data: string | Uint8Array | ArrayBuffer) {
    this.sent.push(data)
  }

  close() {
    this.readyState = 3
    this.onclose?.()
  }

  simulateServerMessage(data: unknown) {
    this.onmessage?.({ data })
  }

  simulateClose() {
    this.readyState = 3
    this.onclose?.()
  }
}

const defaultSession: SessionInfo = {
  administratorId: 'admin-1',
  csrfToken: 'csrf-initial-111',
  expiresAt: '2026-12-31T00:00:00Z',
  recentlyAuthenticated: true,
}

const defaultAccounts: Account[] = [
  {
    credentialId: 'cred-1',
    name: '主账号',
    orgId: 'org-1',
    enabled: true,
    status: 'valid',
    capabilities: { read: 'verified', write: 'declared', delete: 'declared' },
  },
  {
    credentialId: 'cred-2',
    name: '备用号',
    orgId: 'org-2',
    enabled: true,
    status: 'valid',
    capabilities: { read: 'verified', write: 'declared', delete: 'denied' },
  },
]

const defaultFleet: FleetResponse = {
  partial: false,
  accounts: [
    {
      credentialId: 'cred-1',
      complete: true,
      stale: false,
      fetchedAt: new Date().toISOString(),
      error: null,
      usage: {
        machines: 2,
        snapshots: 0,
        storedBytes: 0,
        logicalBytes: 0,
        exclusiveBytes: 0,
        computedAt: new Date().toISOString(),
        limits: {
          maxMachines: 5,
          maxSnapshots: 5,
          maxStoredGiB: 100,
          unlimited: [],
        },
      },
      usageFetchedAt: new Date().toISOString(),
      usageError: null,
    },
    {
      credentialId: 'cred-2',
      complete: true,
      stale: false,
      fetchedAt: new Date().toISOString(),
      error: null,
      usage: {
        machines: 1,
        snapshots: 0,
        storedBytes: 0,
        logicalBytes: 0,
        exclusiveBytes: 0,
        computedAt: new Date().toISOString(),
        limits: {
          maxMachines: 0,
          maxSnapshots: 0,
          maxStoredGiB: 0,
          unlimited: ['maxMachines'],
        },
      },
      usageFetchedAt: new Date().toISOString(),
      usageError: null,
    },
  ],
  machines: [
    {
      id: 'm-1',
      orgId: 'org-1',
      name: 'dev-box',
      state: 'running',
      ready: true,
      stale: false,
      fetchedAt: new Date().toISOString(),
      conflict: false,
      sources: [
        {
          credentialId: 'cred-1',
          stale: false,
          fetchedAt: new Date().toISOString(),
          canDelete: true,
        },
      ],
    },
    {
      id: 'm-2',
      orgId: 'org-1',
      name: 'ci-worker',
      state: 'stopped',
      ready: false,
      stale: false,
      fetchedAt: new Date().toISOString(),
      conflict: false,
      sources: [
        {
          credentialId: 'cred-1',
          stale: false,
          fetchedAt: new Date().toISOString(),
          canDelete: true,
        },
      ],
    },
  ],
}

const server = setupServer(
 http.get('/api/v1/setup/status', () => HttpResponse.json({ required: false, codeRequired: false })),
  http.get('/api/v1/session', () => HttpResponse.json(defaultSession)),
  http.get('/api/v1/accounts', () => HttpResponse.json({ accounts: defaultAccounts })),
  http.get('/api/v1/fleet', () => HttpResponse.json(defaultFleet)),
  http.get('/api/v1/machines/:id', ({ params }) =>
    HttpResponse.json({
      id: params.id,
      orgId: 'org-1',
      name: params.id === 'm-1' ? 'dev-box' : 'ci-worker',
      state: params.id === 'm-1' ? 'running' : 'stopped',
      ready: true,
      spec: { vcpus: 4, memMiB: 8192, persistentDiskGiB: 20, rootDiskGiB: 20 },
      createdAt: new Date().toISOString(),
      updatedAt: new Date().toISOString(),
    }),
  ),
)

beforeAll(() => {
  server.listen()
  vi.stubGlobal('WebSocket', FakeWebSocket)
})

beforeEach(() => {
  mockTerminals.length = 0
  mockFitCalls.length = 0
  FakeWebSocket.instances.length = 0
  nextFitDimensions = { cols: 80, rows: 24 }
  sessionStorage.setItem('instadeck-booted', '1')
  window.location.hash = 'machines'
  useAppStore.setState({
    session: defaultSession,
    sessionChecked: true,
    authRequired: false,
    reauthOpen: false,
    reauthWaiters: [],
    route: 'machines',
    selectedMachineId: null,
    skin: 'auto',
    termSize: 13,
    toast: null,
    lastSyncAt: Date.now(),
    trackedActions: {},
    failedActionsByMachine: {},
    unknownActionMachines: {},
    rowFlashes: {},
    terminals: [],
    topZ: 10,
    winSeq: 0,
  })
})

afterEach(() => {
  server.resetHandlers()
})

afterAll(() => {
  server.close()
  vi.unstubAllGlobals()
})

function renderApp() {
  const qc = new QueryClient({
    defaultOptions: {
      queries: { retry: false, refetchOnWindowFocus: false },
    },
  })
  return render(<App queryClient={qc} />)
}

describe('InstaDeck Frontend Verification Suite', () => {
  it('1. 全部状态映射含未知状态（不崩溃且显示原值，deleted 不显示）', async () => {
    const knownCases: Array<[string, string, boolean]> = [
      ['creating', '创建中', true],
      ['starting', '启动中', true],
      ['running', '运行中', false],
      ['stopping', '停止中', true],
      ['stopped', '已停止', false],
      ['suspending', '暂停中', true],
      ['suspended', '已暂停', false],
      ['resuming', '恢复中', true],
      ['deleting', '删除中', true],
      ['deleted', '已删除', false],
      ['error', '出错', false],
    ]
    for (const [st, expectedLabel, expectedBusy] of knownCases) {
      const meta = getMachineStateMeta(st)
      expect(meta.label).toBe(expectedLabel)
      expect(meta.isBusy).toBe(expectedBusy)
    }

    const unknownMeta = getMachineStateMeta('migrating_v2')
    expect(unknownMeta.label).toBe('migrating_v2')
    expect(unknownMeta.isBusy).toBe(false)

    server.use(
      http.get('/api/v1/fleet', () =>
        HttpResponse.json({
          ...defaultFleet,
          machines: [
            {
              ...defaultFleet.machines[0],
              id: 'm-unk',
              name: 'mystery-node',
              state: 'custom_weird_state',
            },
            {
              ...defaultFleet.machines[0],
              id: 'm-del',
              name: 'ghost-deleted',
              state: 'deleted',
            },
          ],
        }),
      ),
    )

    renderApp()
    expect(await screen.findByText('mystery-node')).toBeInTheDocument()
    expect(screen.getByText('custom_weird_state')).toBeInTheDocument()
    expect(screen.queryByText('ghost-deleted')).not.toBeInTheDocument()
  })

  it('2. 账号局部失败和全部失败的显示（全部失败不显示成“没有机器”）', async () => {
    // Partial failure first
    server.use(
      http.get('/api/v1/fleet', () =>
        HttpResponse.json({
          partial: true,
          accounts: [
            defaultFleet.accounts[0],
            {
              credentialId: 'cred-2',
              complete: true,
              stale: true,
              fetchedAt: new Date().toISOString(),
              error: { code: 'credential_unavailable', retryable: false },
              usage: null,
              usageFetchedAt: null,
              usageError: null,
            },
          ],
          machines: [defaultFleet.machines[0]],
        }),
      ),
    )

    const { unmount } = renderApp()
    expect(await screen.findByText('dev-box')).toBeInTheDocument()
    const alertBanners = await screen.findAllByRole('alert')
    expect(alertBanners.some((b) => b.textContent?.includes('备用号'))).toBe(true)
    unmount()

    // Full failure across all accounts (200 OK with empty machines)
    server.use(
      http.get('/api/v1/fleet', () =>
        HttpResponse.json({
          partial: false,
          accounts: [
            {
              credentialId: 'cred-1',
              complete: false,
              stale: false,
              fetchedAt: null,
              error: { code: 'credential_unavailable', retryable: false },
              usage: null,
              usageFetchedAt: null,
              usageError: null,
            },
            {
              credentialId: 'cred-2',
              complete: false,
              stale: false,
              fetchedAt: null,
              error: { code: 'upstream_timeout', retryable: true },
              usage: null,
              usageFetchedAt: null,
              usageError: null,
            },
          ],
          machines: [],
        }),
      ),
    )

    renderApp()
    const alerts = await screen.findAllByRole('alert')
    expect(alerts.length).toBeGreaterThanOrEqual(2)
    expect(screen.getByText(/全部账号获取失败/)).toBeInTheDocument()
    expect(screen.queryByText('没有符合条件的机器。')).not.toBeInTheDocument()
  })

  it('3. 操作生命周期（成功、失败、unknown，重试生成新 clientRequestId，同一次重试保持不变）', async () => {
    const user = userEvent.setup()
    const receivedRequestIds: string[] = []
    let currentActionState: ActionRecord = {
      id: 'act-1',
      credentialId: 'cred-1',
      machineId: 'm-2',
      kind: 'start',
      state: 'tracking',
      phase: '正在挂载存储卷',
      createdAt: new Date().toISOString(),
      updatedAt: new Date().toISOString(),
    }

    server.use(
      http.post('/api/v1/machines/:id/actions', async ({ request }) => {
        const body = (await request.json()) as { clientRequestId: string }
        receivedRequestIds.push(body.clientRequestId)
        return HttpResponse.json(currentActionState, { status: 202 })
      }),
      http.get('/api/v1/actions/:id', () => HttpResponse.json(currentActionState)),
    )

    renderApp()
    const startBtn = await screen.findByRole('button', { name: '启动：ci-worker' })
    await user.click(startBtn)

    // Phase text shown while tracking
    expect(await screen.findByText('正在挂载存储卷')).toBeInTheDocument()
    expect(receivedRequestIds).toHaveLength(1)
    const firstRequestId = receivedRequestIds[0]

    // Transition action to failed
    currentActionState = {
      ...currentActionState,
      state: 'completed',
      upstreamState: 'failed',
      errorCode: 'no_capacity',
    }

    await waitFor(
      () => {
        expect(screen.getByText(/该区域暂无可用容量/)).toBeInTheDocument()
      },
      { timeout: 3500 },
    )

    // Click Retry -> must generate a new clientRequestId
    currentActionState = {
      ...currentActionState,
      id: 'act-2',
      state: 'unknown',
      upstreamState: undefined,
      errorCode: undefined,
      phase: undefined,
    }
    const retryBtn = screen.getByRole('button', { name: '重试' })
    await user.click(retryBtn)

    await waitFor(() => {
      expect(receivedRequestIds).toHaveLength(2)
    })
    expect(receivedRequestIds[1]).not.toBe(firstRequestId)

    // Unknown state displays "结果未知，请刷新确认" and NOT failure
    await waitFor(
      () => {
        expect(screen.getByText('结果未知，请刷新确认')).toBeInTheDocument()
      },
      { timeout: 3500 },
    )
    expect(screen.queryByText(/失败：/)).not.toBeInTheDocument()
  })

  it('4. 401 不卸载工作台（保留已输入内容和终端，弹出登录层）', async () => {
    const user = userEvent.setup()
    renderApp()

    const searchInput = await screen.findByLabelText('搜索机器名称')
    await user.type(searchInput, 'dev-box')
    expect(searchInput).toHaveValue('dev-box')

    // Trigger a 401 response on an action
    server.use(
      http.post('/api/v1/machines/:id/actions', () =>
        HttpResponse.json(
          { error: { code: 'unauthorized', message: 'expired' } },
          { status: 401 },
        ),
      ),
    )

    const pauseBtn = screen.getByRole('button', { name: '暂停：dev-box' })
    await user.click(pauseBtn)

    // Login overlay appears
    expect(await screen.findByText('登录 InstaDeck')).toBeInTheDocument()
    // Underlying workspace search input is NOT unmounted and still holds value
    expect(screen.getByLabelText('搜索机器名称')).toHaveValue('dev-box')
  })

  it('5 & 6. reauthentication_required 后自动重试（同一次重试保持 clientRequestId 不变）且更新旋转后的 CSRF token', async () => {
    const user = userEvent.setup()
    const attempts: Array<{ csrf: string | null; clientRequestId: string }> = []

    server.use(
      http.post('/api/v1/machines/:id/actions', async ({ request }) => {
        const body = (await request.json()) as { clientRequestId: string }
        const csrf = request.headers.get('X-CSRF-Token')
        attempts.push({ csrf, clientRequestId: body.clientRequestId })
        if (attempts.length === 1) {
          return HttpResponse.json(
            {
              error: {
                code: 'reauthentication_required',
                message: 'reauth needed',
              },
            },
            { status: 403 },
          )
        }
        return HttpResponse.json(
          {
            id: 'act-reauth',
            credentialId: 'cred-1',
            machineId: 'm-2',
            kind: 'start',
            state: 'completed',
            createdAt: new Date().toISOString(),
            updatedAt: new Date().toISOString(),
          },
          { status: 202 },
        )
      }),
      http.post('/api/v1/auth/reauthenticate', () =>
        HttpResponse.json({
          administratorId: 'admin-1',
          csrfToken: 'csrf-rotated-999',
          expiresAt: '2026-12-31T00:00:00Z',
          recentlyAuthenticated: true,
        }),
      ),
    )

    renderApp()
    const startBtn = await screen.findByRole('button', { name: '启动：ci-worker' })
    await user.click(startBtn)

    // Reauthentication dialog opens
    expect(await screen.findByText('需要重新验证')).toBeInTheDocument()
    await user.type(screen.getByLabelText('密码'), 'MySecretPass123!')
    await user.type(screen.getByLabelText('验证码'), '123456')
    await user.click(screen.getByRole('button', { name: '验证' }))

    await waitFor(() => {
      expect(attempts).toHaveLength(2)
    })

    // Same clientRequestId preserved on retry, and new rotated CSRF token used!
    expect(attempts[0].clientRequestId).toBe(attempts[1].clientRequestId)
    expect(attempts[0].csrf).toBe('csrf-initial-111')
    expect(attempts[1].csrf).toBe('csrf-rotated-999')
  })

  it('7. 删除机器必须输入匹配的机器名才可点击删除', async () => {
    const user = userEvent.setup()
    let deleteBody: Record<string, unknown> | null = null

    server.use(
      http.post('/api/v1/machines/:id/actions', async ({ request }) => {
        deleteBody = (await request.json()) as Record<string, unknown>
        return HttpResponse.json(
          {
            id: 'act-del',
            credentialId: 'cred-1',
            machineId: 'm-1',
            kind: 'delete',
            state: 'tracking',
            createdAt: new Date().toISOString(),
            updatedAt: new Date().toISOString(),
          },
          { status: 202 },
        )
      }),
    )

    renderApp()
    const row = await screen.findByText('dev-box')
    await user.click(row)

    const openDelBtn = await screen.findByRole('button', { name: '删除机器' })
    await user.click(openDelBtn)

    const confirmBtn = screen.getByRole('button', { name: '删除 dev-box' })
    const nameInput = screen.getByLabelText('输入机器名称确认')

    expect(confirmBtn).toBeDisabled()
    await user.type(nameInput, 'wrong-name')
    expect(confirmBtn).toBeDisabled()

    await user.clear(nameInput)
    await user.type(nameInput, 'dev-box')
    expect(confirmBtn).toBeEnabled()

    await user.click(confirmBtn)
    await waitFor(() => {
      expect(deleteBody).not.toBeNull()
    })
    expect(deleteBody).toMatchObject({
      kind: 'delete',
      confirmName: 'dev-box',
    })
  })

  it('8. API Key 提交后立即被清空且不保存在 store 中', async () => {
    const user = userEvent.setup()
    let submittedPayload: { name: string; key: string } | null = null

    server.use(
      http.post('/api/v1/accounts', async ({ request }) => {
        submittedPayload = (await request.json()) as { name: string; key: string }
        return HttpResponse.json({
          credentialId: 'cred-new',
          name: submittedPayload.name,
          enabled: true,
          status: 'valid',
          capabilities: { read: 'declared', write: 'declared', delete: 'unknown' },
        })
      }),
    )

    window.location.hash = 'accounts'
    renderApp()

    const addBtn = await screen.findByRole('button', { name: '添加账号' })
    await user.click(addBtn)

    const aliasInput = screen.getByLabelText('别名')
    const keyInput = screen.getByLabelText('API Key') as HTMLInputElement

    await user.type(aliasInput, '新测试号')
    await user.type(keyInput, 'ibk_super_secret_test_token')
    expect(keyInput.value).toBe('ibk_super_secret_token'.replace('_token', '_test_token'))

    const saveBtn = screen.getByRole('button', { name: '校验并保存' })
    await user.click(saveBtn)

    // Key input is immediately cleared and never stored in Zustand store
    expect(keyInput.value).toBe('')
    await waitFor(() => {
      expect(submittedPayload?.key).toBe('ibk_super_secret_test_token')
    })
    expect(JSON.stringify(useAppStore.getState())).not.toContain(
      'ibk_super_secret_test_token',
    )
  })

  it('9. 终端输入分片（超过 16 KiB 的大段粘贴拆分为不超过 16 KiB 的二进制帧）', () => {
    const largeString = 'A'.repeat(40_000)
    const chunks = chunkTerminalInput(largeString)
    expect(chunks.length).toBe(3)
    expect(chunks[0].byteLength).toBe(MAX_TERMINAL_FRAME_BYTES)
    expect(chunks[1].byteLength).toBe(MAX_TERMINAL_FRAME_BYTES)
    expect(chunks[2].byteLength).toBe(40_000 - MAX_TERMINAL_FRAME_BYTES * 2)

    const sentFrames: Array<Uint8Array | ArrayBuffer | string> = []
    const count = sendTerminalInput(
      {
        readyState: 1,
        send: (data) => sentFrames.push(data),
      },
      largeString,
    )
    expect(count).toBe(3)
    expect(sentFrames).toHaveLength(3)
  })

  it('10, 11, 12, 13. 终端：票据不在 URL 中、隐藏窗口不发 resize、断线重连重新申请票据、切换主题立即更新 xterm 主题', async () => {
    const user = userEvent.setup()
    let ticketCounter = 0

    server.use(
      http.post('/api/v1/machines/:id/terminal-tickets', () => {
        ticketCounter += 1
        return HttpResponse.json({
          ticket: `tk-secret-${ticketCounter}`,
          protocol: 'instadeck.terminal.v1',
        })
      }),
    )

    renderApp()
    const termBtn = await screen.findByRole('button', { name: '打开终端：dev-box' })
    await user.click(termBtn)

    await waitFor(() => {
      expect(FakeWebSocket.instances).toHaveLength(1)
    })

    const ws1 = FakeWebSocket.instances[0]
    // 12. Ticket is NOT in URL, passed via subprotocol
    expect(ws1.url).toBe('ws://localhost:3000/api/v1/terminal/ws')
    expect(ws1.url).not.toContain('tk-secret')
    expect(ws1.protocols).toEqual(['instadeck.terminal.v1', 'instadeck.ticket.tk-secret-1'])

    // Server sends ready frame -> initial resize sent while visible
    act(() => {
      ws1.simulateServerMessage(JSON.stringify({ type: 'ready' }))
    })

    const initialResizes = ws1.sent.filter(
      (f) => typeof f === 'string' && f.includes('"type":"resize"'),
    )
    expect(initialResizes.length).toBeGreaterThanOrEqual(1)

    // 10. Minimize window -> hidden window must NOT send resize
    const countBeforeMin = ws1.sent.length
    const winId = useAppStore.getState().terminals[0].id
    act(() => {
      useAppStore.getState().minimizeTerminal(winId)
      nextFitDimensions = { cols: 120, rows: 40 }
      useAppStore.getState().updateTerminalGeometry(winId, { w: 800, h: 500 })
    })

    const resizesWhileHidden = ws1.sent
      .slice(countBeforeMin)
      .filter((f) => typeof f === 'string' && f.includes('"type":"resize"'))
    expect(resizesWhileHidden).toHaveLength(0)

    // Restore window
    act(() => {
      useAppStore.getState().focusTerminal(winId)
    })

    // 13. Switching theme immediately updates xterm theme
    const termInstance = mockTerminals[0]
    const prevBg = termInstance.options.theme?.background
    act(() => {
      useAppStore.getState().setSkin('nord', true)
    })
    await waitFor(() => {
      expect(termInstance.options.theme?.background).toBe('#272c36')
    })
    expect(termInstance.options.theme?.background).not.toBe(prevBg)

    // A View Transition applies the attribute after the skin state changes, so
    // the terminal must follow the attribute rather than the state.
    document.documentElement.dataset.skin = 'plum'
    await waitFor(() => {
      expect(termInstance.options.theme?.background).toBe('#19131f')
    })
    act(() => {
      useAppStore.getState().setSkin('nord', true)
    })

    // 11. Disconnect and click Reconnect -> requests a brand new ticket
    act(() => {
      ws1.simulateClose()
    })

    const winSection = screen.getByRole('region', { name: /dev-box 终端/ })
    const reconnectBtn = await within(winSection).findByRole('button', { name: '重连' })
    await user.click(reconnectBtn)

    await waitFor(() => {
      expect(ticketCounter).toBe(2)
      expect(FakeWebSocket.instances).toHaveLength(2)
    })
    expect(FakeWebSocket.instances[1].protocols).toEqual([
      'instadeck.terminal.v1',
      'instadeck.ticket.tk-secret-2',
    ])
  })
})
