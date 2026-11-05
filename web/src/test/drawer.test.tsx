import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse, http } from 'msw'
import { setupServer } from 'msw/node'
import { afterAll, afterEach, beforeAll, beforeEach, expect, it } from 'vitest'
import { api } from '../api/client'
import type { FleetMachine } from '../api/types'
import { MachineDrawer } from '../components/MachineDrawer'
import { computeAccountQuota } from '../hooks/useFleetData'
import { useAppStore } from '../store/useAppStore'

const server = setupServer(
  http.get('/api/v1/machines/m', () => HttpResponse.json({ id: 'm', orgId: 'o', name: 'box' })),
  http.get('/api/v1/machines/m/ports', () => HttpResponse.json({ enabled: true, ports: [] })),
)
beforeAll(() => server.listen())
afterEach(() => server.resetHandlers())
afterAll(() => server.close())
beforeEach(() => useAppStore.setState({ selectedMachineId: 'm', authRequired: false }))

const account = computeAccountQuota(
  {
    credentialId: 'a',
    name: 'main',
    enabled: true,
    status: 'valid',
    capabilities: { read: 'verified', write: 'verified', delete: 'verified' },
  },
  undefined,
  [],
  'red',
)
const machine = {
  id: 'm',
  orgId: 'o',
  name: 'box',
  state: 'running',
  fetchedAt: new Date().toISOString(),
  stale: false,
  conflict: false,
  sources: [{ credentialId: 'a', stale: false, canDelete: true }],
} as unknown as FleetMachine

function show() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={client}>
      <main data-testid="blank">
        <button type="button">其他按钮</button>
        <table>
          <tbody>
            <tr className="rowlink">
              <td>另一台机器</td>
            </tr>
          </tbody>
        </table>
      </main>
      <MachineDrawer machines={[machine]} accountMap={new Map([['a', account]])} />
    </QueryClientProvider>,
  )
}

const isOpen = () => useAppStore.getState().selectedMachineId === 'm'

it('点击空白处关闭详情', async () => {
  const user = userEvent.setup()
  show()
  await screen.findByText('操作')
  await user.click(screen.getByTestId('blank'))
  await waitFor(() => expect(isOpen()).toBe(false))
})

it('点击详情内部、按钮和机器行不会关闭详情', async () => {
  const user = userEvent.setup()
  show()
  await user.click(await screen.findByText('操作'))
  expect(isOpen()).toBe(true)
  await user.click(screen.getByRole('button', { name: '其他按钮' }))
  expect(isOpen()).toBe(true)
  await user.click(screen.getByText('另一台机器'))
  expect(isOpen()).toBe(true)
})

it('打开对话框时点击空白处不会关闭详情', async () => {
  const user = userEvent.setup()
  show()
  await user.click(await screen.findByRole('button', { name: '重命名' }))
  await user.click(screen.getByTestId('blank'))
  expect(isOpen()).toBe(true)
})

it('操作排在快照、端口和机器信息之前', async () => {
  show()
  const actions = await screen.findByText('操作')
  const snapshot = screen.getByRole('button', { name: '保存快照' })
  const spec = screen.getByText('规格')
  expect(actions.compareDocumentPosition(snapshot) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  expect(actions.compareDocumentPosition(spec) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
})

it('输错密码或验证码不会跳回登录，会话失效才会', async () => {
  useAppStore.setState({ authRequired: false })
  server.use(
    http.post('/api/v1/security/recovery-codes', () =>
      HttpResponse.json({ error: { code: 'authentication_failed' } }, { status: 401 }),
    ),
    http.get('/api/v1/sessions', () =>
      HttpResponse.json({ error: { code: 'unauthorized' } }, { status: 401 }),
    ),
  )
  await expect(api.regenerateRecoveryCodes('x', '000000')).rejects.toMatchObject({
    code: 'authentication_failed',
  })
  expect(useAppStore.getState().authRequired).toBe(false)
  await expect(api.getSessions()).rejects.toMatchObject({ code: 'unauthorized' })
  expect(useAppStore.getState().authRequired).toBe(true)
})
