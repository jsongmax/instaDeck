import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { api } from '../api/client'
import { ApiError } from '../api/errors'
import type { ActionRecord, SessionInfo } from '../api/types'
import { CreateMachineDialog } from '../components/Dialogs'
import { computeAccountQuota, useFleetData } from '../hooks/useFleetData'
import { useAppStore } from '../store/useAppStore'

const session: SessionInfo = { administratorId: 'admin', csrfToken: 'csrf', expiresAt: '', recentlyAuthenticated: true }
const key = 'instadeck-create-records'
const record = { id: 'action', name: 'machine', credentialId: 'a', status: 'running', timestamp: Date.now() }
const account = computeAccountQuota({ credentialId: 'a', name: 'Account A', enabled: true, status: 'valid', capabilities: { read: 'verified', write: 'verified', delete: 'verified' } }, undefined, [], 'red')

function show(node: React.ReactNode) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const result = render(<QueryClientProvider client={client}>{node}</QueryClientProvider>)
  return { ...result, client }
}
function Poller() { useFleetData(); return null }
function reload() {
  useAppStore.setState({ session: null, trackedActions: {}, createRecords: {} })
  useAppStore.getState().setSession(session)
}
beforeEach(() => {
  localStorage.clear()
  useAppStore.getState().setSession(null)
  vi.spyOn(api, 'getAccounts').mockResolvedValue({ accounts: [] })
  vi.spyOn(api, 'getFleet').mockResolvedValue({ machines: [], accounts: [], partial: false })
})
afterEach(() => { vi.restoreAllMocks(); useAppStore.getState().setSession(null); localStorage.clear() })

it.each([
  ['free', false], ['paid', true], ['missing tier', true], ['me failure', true],
])('template availability for %s selects an allowed default', async (_tier, allowed) => {
  useAppStore.getState().setSession(session)
  vi.spyOn(api, 'getTemplates').mockResolvedValue({ templates: [
    { id: 'ubuntu', creatable: true, default: true, planAllowed: allowed, version: '', arch: '', defaultSpec: {} },
    { id: 'ci', creatable: true, planAllowed: true, version: '', arch: '', defaultSpec: {} },
  ] })
  show(<CreateMachineDialog open accounts={[account]} onClose={() => {}} />)
  const option = await screen.findByRole('option', { name: allowed ? 'ubuntu' : 'ubuntu（当前套餐不可用）' })
  expect(option).toHaveProperty('disabled', !allowed)
  expect(screen.getByLabelText('模板')).toHaveValue(allowed ? 'ubuntu' : 'ci')
})

it('switching accounts updates availability and blocks accounts with no usable template', async () => {
  useAppStore.getState().setSession(session)
  const user = userEvent.setup()
  vi.spyOn(api, 'getTemplates').mockImplementation(async (id) => ({ templates: [{ id: 'ubuntu', creatable: true, planAllowed: id === 'a', version: '', arch: '', defaultSpec: {} }] }))
  show(<CreateMachineDialog open accounts={[account, { ...account, credentialId: 'b', name: 'Account B' }]} onClose={() => {}} />)
  await screen.findByRole('option', { name: 'ubuntu' })
  await user.click(screen.getByRole('radio', { name: /Account B/ }))
  await screen.findByText('这个账号暂无可用模板')
  expect(screen.getByRole('button', { name: '创建' })).toBeDisabled()
  expect(screen.getByRole('option', { name: 'ubuntu（当前套餐不可用）' })).toBeDisabled()
})

it('restores only after login, resumes polling and removes completed creates', async () => {
  localStorage.setItem(key, JSON.stringify([record]))
  const action = vi.spyOn(api, 'getAction').mockResolvedValue({ id: record.id, kind: 'create', state: 'completed', upstreamState: 'succeeded', machineId: 'm' } as ActionRecord)
  const { unmount, client } = show(<Poller />)
  expect(useAppStore.getState().createRecords).toEqual({})
  expect(action).not.toHaveBeenCalled()
  act(() => useAppStore.getState().setSession(session))
  expect(useAppStore.getState().trackedActions.action.kind).toBe('create')
  await waitFor(() => expect(action).toHaveBeenCalledWith('action'), { timeout: 3500 })
  await waitFor(() => expect(useAppStore.getState().createRecords).toEqual({}))
  expect(localStorage.getItem(key)).toBeNull()
  unmount(); client.clear()
})

it('failed creates survive refresh without marking a machine failed, and closing removes storage', async () => {
  localStorage.setItem(key, JSON.stringify([record]))
  reload()
  vi.spyOn(api, 'getAction').mockResolvedValue({ id: record.id, kind: 'create', state: 'completed', upstreamState: 'failed', errorCode: 'no_capacity', machineId: 'new-machine' } as ActionRecord)
  const { unmount, client } = show(<Poller />)
  await waitFor(() => expect(useAppStore.getState().createRecords.action.status).toBe('failed'), { timeout: 3500 })
  expect(useAppStore.getState().failedActionsByMachine['new-machine']).toBeUndefined()
  unmount(); client.clear(); reload()
  expect(useAppStore.getState().createRecords.action.status).toBe('failed')
  expect(useAppStore.getState().trackedActions).toEqual({})
  useAppStore.getState().removeCreateRecord('action')
  expect(localStorage.getItem(key)).toBeNull()
})

it('retries transient errors and removes 404 actions', async () => {
  localStorage.setItem(key, JSON.stringify([record])); reload()
  const action = vi.spyOn(api, 'getAction').mockRejectedValueOnce(new Error('network')).mockRejectedValue(new ApiError(404, 'not_found', 'missing'))
  const { unmount, client } = show(<Poller />)
  await waitFor(() => expect(action).toHaveBeenCalledTimes(1), { timeout: 2500 })
  expect(useAppStore.getState().createRecords.action.status).toBe('running')
  await waitFor(() => expect(useAppStore.getState().createRecords).toEqual({}), { timeout: 2500 })
  expect(useAppStore.getState().trackedActions).toEqual({})
  expect(localStorage.getItem(key)).toBeNull()
  unmount(); client.clear()
})

it.each(['broken', '{}', '[null,{}, {"id":7}]'])('ignores malformed storage %s', (raw) => {
  localStorage.setItem(key, raw); reload()
  expect(useAppStore.getState().createRecords).toEqual({})
})
it('expires records after 24 hours and stores only permitted fields', () => {
  localStorage.setItem(key, JSON.stringify([{ ...record, secret: 'never persist' }, { ...record, id: 'expired', timestamp: Date.now() - 24 * 3600_000 }]))
  reload()
  expect(Object.keys(useAppStore.getState().createRecords)).toEqual(['action'])
  expect(localStorage.getItem(key)).not.toContain('secret')
  expect(localStorage.getItem(key)).not.toContain('expired')
})
it('explicit logout clears persisted names and tracked actions', () => {
  localStorage.setItem(key, JSON.stringify([record])); reload()
  useAppStore.getState().clearCreateRecords()
  expect(localStorage.getItem(key)).toBeNull()
  expect(useAppStore.getState().trackedActions).toEqual({})
  expect(useAppStore.getState().createRecords).toEqual({})
})
it('an expired session keeps a running create, which resumes at the next login', () => {
  localStorage.setItem(key, JSON.stringify([record])); reload()
  useAppStore.getState().setAuthRequired(true)
  expect(useAppStore.getState().createRecords.action.status).toBe('running')
  useAppStore.getState().setSession(session)
  expect(useAppStore.getState().trackedActions.action.kind).toBe('create')
  // A refresh while signed out finds no session, then logs in again.
  useAppStore.getState().setSession(null)
  expect(localStorage.getItem(key)).toContain('action')
  useAppStore.getState().setSession(session)
  expect(useAppStore.getState().createRecords.action.status).toBe('running')
  expect(useAppStore.getState().trackedActions.action.kind).toBe('create')
})
it('says so when the template list cannot be read', async () => {
  useAppStore.getState().setSession(session)
  vi.spyOn(api, 'getTemplates').mockRejectedValue(new Error('down'))
  show(<CreateMachineDialog open accounts={[account]} onClose={() => {}} />)
  expect(await screen.findByText(/无法读取模板列表/, undefined, { timeout: 3000 })).toBeInTheDocument()
  expect(screen.getByRole('button', { name: '创建' })).toBeDisabled()
})
it('storage failures never prevent state changes', () => {
  vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('blocked') })
  vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('blocked') })
  vi.spyOn(Storage.prototype, 'removeItem').mockImplementation(() => { throw new Error('blocked') })
  reload()
  useAppStore.getState().addCreateRecord('action', 'machine', 'a')
  useAppStore.getState().failCreateRecord('action', 'failed')
  expect(useAppStore.getState().createRecords.action.status).toBe('failed')
  useAppStore.getState().removeCreateRecord('action')
  useAppStore.getState().setSession(null)
})
it('lists usable templates before the ones the plan refuses', async () => {
  useAppStore.getState().setSession(session)
  vi.spyOn(api, 'getTemplates').mockResolvedValue({ templates: [
    { id: 'ubuntu', creatable: true, planAllowed: false, version: '', arch: '', defaultSpec: {} },
    { id: 'ci', creatable: true, planAllowed: true, version: '', arch: '', defaultSpec: {} },
  ] })
  show(<CreateMachineDialog open accounts={[account]} onClose={() => {}} />)
  await screen.findByRole('option', { name: 'ci' })
  const names = (screen.getByLabelText('模板') as HTMLSelectElement).options
  expect([...names].map((o) => o.value)).toEqual(['ci', 'ubuntu'])
})
