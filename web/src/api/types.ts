export interface ApiErrorPayload {
  error: {
    code: string
    message: string
    retryable?: boolean
    requestId?: string
  }
}

export interface SessionInfo {
  administratorId: string
  csrfToken: string
  expiresAt: string
  recentlyAuthenticated: boolean
}

export type CapabilityState = 'unknown' | 'declared' | 'verified' | 'denied'

export interface AccountCapabilities {
  read: CapabilityState
  write: CapabilityState
  delete: CapabilityState
}

export interface Account {
  credentialId: string
  name: string
  userId?: string
  orgId?: string
  version?: number
  enabled: boolean
  status: 'valid' | 'invalid'
  capabilities: AccountCapabilities
  createdAt?: string
  updatedAt?: string
}

export interface AccountsResponse {
  accounts: Account[]
}

export interface MachineSource {
  credentialId: string
  stale: boolean
  fetchedAt: string
  canDelete: boolean
}

export interface FleetMachine {
  id: string
  orgId: string
  name: string
  state: string
  ready: boolean
  stale: boolean
  fetchedAt: string
  conflict: boolean
  sources: MachineSource[]
}

export interface FleetAccountLimits {
  maxMachines: number
  maxSnapshots: number
  maxStoredGiB: number
  unlimited: string[]
}

export interface FleetAccountUsage {
  machines: number
  snapshots: number
  storedBytes: number
  logicalBytes: number
  exclusiveBytes: number
  computedAt: string
  limits: FleetAccountLimits
}

export interface FleetAccountError {
  code: string
  retryable: boolean
  retryAfterSeconds?: number
}

export interface FleetAccount {
  credentialId: string
  complete: boolean
  stale: boolean
  fetchedAt: string | null
  error: FleetAccountError | null
  usage: FleetAccountUsage | null
  usageFetchedAt: string | null
  usageError: { code: string; retryable?: boolean } | null
}

export interface FleetResponse {
  machines: FleetMachine[]
  accounts: FleetAccount[]
  partial: boolean
}

export interface MachineDetailSpec {
  memMiB: number
  persistentDiskGiB: number
  rootDiskGiB: number
  vcpus: number
}

export interface MachineDetail {
  id: string
  orgId: string
  name: string
  state: string
  ready: boolean
  spec: MachineDetailSpec
  createdAt: string
  updatedAt: string
}

export interface MachineTemplate {
  id: string
  version: string
  arch: string
  planAllowed?: boolean
  creatable: boolean
  default?: boolean
  defaultSpec: {
    vcpus?: number
    memMiB?: number
    persistentDiskGiB?: number
    rootDiskGiB?: number
  }
}

export interface TemplatesResponse {
  templates: MachineTemplate[]
}

export type ActionKind = 'start' | 'stop' | 'suspend' | 'resume' | 'delete' | 'create'
export type ActionState = 'pending' | 'submitting' | 'tracking' | 'unknown' | 'completed'

export interface ActionRecord {
  id: string
  credentialId: string
  machineId: string
  kind: string
  state: ActionState
  upstreamState?: string
  phase?: string
  errorCode?: string
  createdAt: string
  updatedAt: string
}

export interface TerminalTicketResponse {
  ticket: string
  protocol?: string
}

export interface AuditEventItem {
  operationKind?: string
  Cursor: string
  Event: {
    detail?: string
    Time: string
    Type: string
    SubjectID?: string
    CredentialID?: string
    MachineID?: string
    ActionID?: string
    Result?: string
    RequestID?: string
    SourceAddress?: string
  }
}

export interface AuditEventsResponse {
  events: AuditEventItem[]
}

export interface SecurityInfo {
  totpBoundAt: string
  recoveryCodesLeft: number
}

export interface TotpStartResponse {
  otpauthUri: string
  secret: string
}

export interface TotpConfirmResponse extends SessionInfo {
  recoveryCodes: string[]
}

export interface RecoveryCodesResponse {
  recoveryCodes: string[]
}

export interface DeviceSession {
  publicId: string
  device: string
  sourceAddress: string
  createdAt: string
  lastSeen: string
  current: boolean
}

export interface SessionsResponse {
  sessions: DeviceSession[]
}

export interface SkinMeta {
  id: string
  name: string
  note: string
  mode: 'dark' | 'light'
  bg: string
  panel: string
  line: string
  accent: string
  muted: string
  termBg: string
  termGreen: string
  termInk: string
}

export const SKIN_META: SkinMeta[] = [
  {
    id: 'graphite',
    name: '石墨',
    note: '中性灰，接近单色，安静耐看',
    mode: 'dark',
    bg: '#111113',
    panel: '#1c1c1f',
    line: 'rgba(255,255,255,.09)',
    accent: '#ececef',
    muted: '#a0a0a8',
    termBg: '#161618',
    termGreen: '#6fd6a3',
    termInk: '#e4e4e7',
  },
  {
    id: 'nord',
    name: '北极',
    note: '低饱和灰蓝，长时间看不累',
    mode: 'dark',
    bg: '#242933',
    panel: '#2e3440',
    line: 'rgba(216,222,233,.11)',
    accent: '#88c0d0',
    muted: '#aab3c4',
    termBg: '#272c36',
    termGreen: '#a3be8c',
    termInk: '#e5e9f0',
  },
  {
    id: 'forest',
    name: '松林',
    note: '墨绿底色配暖金，沉稳',
    mode: 'dark',
    bg: '#0f1613',
    panel: '#18211c',
    line: 'rgba(214,232,220,.1)',
    accent: '#d9b66f',
    muted: '#9fb0a5',
    termBg: '#121a16',
    termGreen: '#7fdba9',
    termInk: '#dfe9e2',
  },
  {
    id: 'plum',
    name: '暮莓',
    note: '深紫红底配玫瑰色，更有个性',
    mode: 'dark',
    bg: '#16111b',
    panel: '#221a2a',
    line: 'rgba(235,220,245,.1)',
    accent: '#f08ab0',
    muted: '#ad9fb9',
    termBg: '#19131f',
    termGreen: '#79dcaf',
    termInk: '#ebe2f0',
  },
  {
    id: 'sky',
    name: '晴空',
    note: '清爽的蓝灰浅色',
    mode: 'light',
    bg: '#e9edf5',
    panel: '#ffffff',
    line: 'rgba(30,48,86,.12)',
    accent: '#2467f2',
    muted: '#5a6680',
    termBg: '#f7f9fc',
    termGreen: '#0b7f50',
    termInk: '#1b2438',
  },
  {
    id: 'paper',
    name: '纸白',
    note: '暖白底色，像在读纸质文档',
    mode: 'light',
    bg: '#f2eee6',
    panel: '#fffdf9',
    line: 'rgba(70,55,35,.13)',
    accent: '#0f6e6e',
    muted: '#74695a',
    termBg: '#fbf8f1',
    termGreen: '#2c7f4f',
    termInk: '#2f281f',
  },
]

export interface PublicPort { port: number; name: string; protocol: 'http'|'tcp'; access: 'public'|'private'; url: string; token?: string; tokenUrl?: string }
export interface Snapshot { id: string; name: string; credentialId: string; accountName: string; sourceMachineId: string; status: string; error?: string; rootSizeBytes: number; dataSizeBytes: number; createdAt: string }
export interface NotificationChannel { id: string; type: 'telegram'|'webhook'; name: string; enabled: boolean; events: string[]; hint: string }
