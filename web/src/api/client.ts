import { useAppStore } from '../store/useAppStore'
import { ApiError, formatErrorCode } from './errors'
import type {
  Account,
  AccountCapabilities,
  AccountsResponse,
  ActionKind,
  ActionRecord,
  AuditEventsResponse,
  FleetResponse,
  MachineDetail,
  RecoveryCodesResponse,
  SecurityInfo,
  SessionInfo,
  SessionsResponse,
  TemplatesResponse,
  TerminalTicketResponse,
  TotpConfirmResponse,
  TotpStartResponse,
} from './types'

const API_BASE = '/api/v1'

interface RequestOptions {
  method?: 'GET' | 'POST' | 'PATCH' | 'DELETE'
  body?: unknown
  skipAuthRedirect?: boolean
  csrfRetried?: boolean
  reauthRetried?: boolean
}

function isSessionPayload(data: unknown): data is SessionInfo {
  if (!data || typeof data !== 'object') return false
  const obj = data as Record<string, unknown>
  return typeof obj.csrfToken === 'string' && typeof obj.administratorId === 'string'
}

export async function apiRequest<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const method = options.method ?? 'GET'
  const store = useAppStore.getState()
  const csrfToken = store.session?.csrfToken ?? ''

  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
  }
  if (method !== 'GET' && csrfToken) {
    headers['X-CSRF-Token'] = csrfToken
  }

  const res = await fetch(`${API_BASE}${path}`, {
    method,
    credentials: 'same-origin',
    headers,
    body: options.body !== undefined ? JSON.stringify(options.body) : undefined,
  })

  let payload: unknown = null
  const contentType = res.headers.get('content-type') ?? ''
  if (contentType.includes('application/json') || res.status !== 204) {
    try {
      payload = await res.json()
    } catch {
      payload = null
    }
  }

  if (res.ok) {
    if (isSessionPayload(payload)) {
      useAppStore.getState().setSession({
        administratorId: payload.administratorId,
        csrfToken: payload.csrfToken,
        expiresAt: payload.expiresAt,
        recentlyAuthenticated: payload.recentlyAuthenticated,
      })
    }
    return payload as T
  }

  const errObj =
    payload && typeof payload === 'object' && 'error' in payload
      ? (payload as {
          error?: {
            code?: string
            message?: string
            retryable?: boolean
            requestId?: string
          }
        }).error
      : undefined

  const code = errObj?.code ?? `http_${res.status}`
  const retryable = Boolean(errObj?.retryable)
  const requestId = errObj?.requestId
  const message = formatErrorCode(errObj?.code)

  if (res.status === 403 && code === 'csrf_rejected' && !options.csrfRetried) {
    const freshSession = await apiRequest<SessionInfo>('/session', {
      method: 'GET',
      skipAuthRedirect: options.skipAuthRedirect,
      csrfRetried: true,
    })
    useAppStore.getState().setSession(freshSession)
    return apiRequest<T>(path, { ...options, csrfRetried: true })
  }

  if (res.status === 403 && code === 'reauthentication_required' && !options.reauthRetried) {
    const reauthed = await useAppStore.getState().requestReauth()
    if (reauthed) {
      return apiRequest<T>(path, { ...options, reauthRetried: true })
    }
    throw new ApiError(res.status, code, message, retryable, requestId)
  }

  if (res.status === 401 && !options.skipAuthRedirect) {
    useAppStore.getState().setAuthRequired(true)
  }

  throw new ApiError(res.status, code, message, retryable, requestId)
}

export interface SetupStatus { required: boolean; codeRequired: boolean }
export interface SetupEnrollment { otpauthUri: string; secret: string }
export interface SetupResult extends SessionInfo { recoveryCodes: string[] }

export const api = {
  setupStatus: () => apiRequest<SetupStatus>('/setup/status', { skipAuthRedirect: true }),
  setupStart: (username: string, password: string, setupCode?: string) =>
    apiRequest<SetupEnrollment>('/setup/start', { method: 'POST', body: { username, password, setupCode }, skipAuthRedirect: true }),
  setupComplete: (code: string) =>
    apiRequest<SetupResult>('/setup/complete', { method: 'POST', body: { code }, skipAuthRedirect: true }),

  getSession: (skipAuthRedirect = false) =>
    apiRequest<SessionInfo>('/session', { method: 'GET', skipAuthRedirect }),

  loginPassword: (username: string, password: string) =>
    apiRequest<{ challengeRequired: boolean }>('/auth/password', {
      method: 'POST',
      body: { username, password },
      skipAuthRedirect: true,
    }),

  loginTotp: (code: string) =>
    apiRequest<SessionInfo>('/auth/totp', {
      method: 'POST',
      body: { code },
      skipAuthRedirect: true,
    }),

  loginRecovery: (code: string) =>
    apiRequest<SessionInfo>('/auth/recovery', {
      method: 'POST',
      body: { code },
      skipAuthRedirect: true,
    }),

  reauthenticate: (password: string, code: string) =>
    apiRequest<SessionInfo>('/auth/reauthenticate', {
      method: 'POST',
      body: { password, code },
      skipAuthRedirect: true,
    }),

  logout: () => apiRequest<{ ok: boolean }>('/auth/logout', { method: 'POST' }),

  getAccounts: () => apiRequest<AccountsResponse>('/accounts', { method: 'GET' }),

  createAccount: (body: {
    name: string
    key: string
    declarations: { read: boolean; write: boolean; delete: boolean }
  }) =>
    apiRequest<Account>('/accounts', {
      method: 'POST',
      body,
    }),

  updateAccount: (
    id: string,
    body: {
      name?: string
      key?: string
      declarations?: Partial<Record<keyof AccountCapabilities, boolean>>
      enabled?: boolean
    },
  ) =>
    apiRequest<Account>(`/accounts/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body,
    }),

  deleteAccount: (id: string) =>
    apiRequest<{ ok: boolean }>(`/accounts/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    }),

  validateAccount: (id: string) =>
    apiRequest<Account>(`/accounts/${encodeURIComponent(id)}/validate`, {
      method: 'POST',
    }),

  getFleet: () => apiRequest<FleetResponse>('/fleet', { method: 'GET' }),

  getMachineDetail: (id: string, credentialId: string) =>
    apiRequest<MachineDetail>(
      `/machines/${encodeURIComponent(id)}?credentialId=${encodeURIComponent(credentialId)}`,
      { method: 'GET' },
    ),

  getTemplates: (credentialId: string) =>
    apiRequest<TemplatesResponse>(
      `/templates?credentialId=${encodeURIComponent(credentialId)}`,
      { method: 'GET' },
    ),

  createMachine: (params: {
    clientRequestId: string
    credentialId: string
    body: { name: string; templateId: string; vcpus: number; memMiB: number }
  }) =>
    apiRequest<ActionRecord>('/machines', {
      method: 'POST',
      body: params,
    }),

  runMachineAction: (
    machineId: string,
    params: {
      clientRequestId: string
      credentialId: string
      kind: Exclude<ActionKind, 'create'>
      confirmName?: string
    },
  ) =>
    apiRequest<ActionRecord>(`/machines/${encodeURIComponent(machineId)}/actions`, {
      method: 'POST',
      body: params,
    }),

  getAction: (actionId: string) =>
    apiRequest<ActionRecord>(`/actions/${encodeURIComponent(actionId)}`, {
      method: 'GET',
    }),

  createTerminalTicket: (machineId: string, credentialId: string) =>
    apiRequest<TerminalTicketResponse>(
      `/machines/${encodeURIComponent(machineId)}/terminal-tickets`,
      {
        method: 'POST',
        body: { credentialId },
      },
    ),

  getAuditEvents: (after?: string) =>
    apiRequest<AuditEventsResponse>(
      after ? `/audit-events?after=${encodeURIComponent(after)}` : '/audit-events',
      { method: 'GET' },
    ),

  getSecurity: () => apiRequest<SecurityInfo>('/security', { method: 'GET' }),

  changePassword: (currentPassword: string, newPassword: string) =>
    apiRequest<SessionInfo>('/security/password', {
      method: 'POST',
      body: { currentPassword, newPassword },
    }),

  startTotpRebind: (password: string, code: string) =>
    apiRequest<TotpStartResponse>('/security/totp/start', {
      method: 'POST',
      body: { password, code },
    }),

  confirmTotpRebind: (code: string) =>
    apiRequest<TotpConfirmResponse>('/security/totp/confirm', {
      method: 'POST',
      body: { code },
    }),

  regenerateRecoveryCodes: (password: string, code: string) =>
    apiRequest<RecoveryCodesResponse>('/security/recovery-codes', {
      method: 'POST',
      body: { password, code },
    }),

  getSessions: () => apiRequest<SessionsResponse>('/sessions', { method: 'GET' }),

  revokeSession: (publicId: string) =>
    apiRequest<{ ok: boolean }>(`/sessions/${encodeURIComponent(publicId)}`, {
      method: 'DELETE',
    }),

  revokeOtherSessions: () =>
    apiRequest<{ ok: boolean }>('/sessions/revoke-others', {
      method: 'POST',
    }),
}
