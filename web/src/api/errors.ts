export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly retryable: boolean
  readonly requestId?: string

  constructor(
    status: number,
    code: string,
    message: string,
    retryable = false,
    requestId?: string,
  ) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.retryable = retryable
    this.requestId = requestId
  }
}

const ERROR_CODE_MAP: Record<string, string> = {
  confirmation_mismatch: '确认名称不匹配',
  capability_denied: '该账号 Key 没有此操作的权限',
  machine_not_visible: '找不到该机器或当前账号不可见',
  invalid_body: '提交的参数无效',
  request_conflict: '已有相同或冲突的操作正在进行',
  credential_unavailable: '账号凭据不可用或 Key 已失效',
  operation_rejected: '操作被上游拒绝',
  terminal_rejected: '无法打开终端，请求被拒绝',
  terminal_unavailable: '终端服务暂不可用',
  invalid_connection: '终端连接无效',
  machine_not_running: '机器未在运行中',
  pty_unsupported: '该机器不支持伪终端 (PTY)',
  terminal_limit: '终端连接数已达上限',
  terminal_protocol: '终端协议错误',
  terminal_timeout: '终端连接超时',
  terminal_closed: '终端连接已关闭',
  invalid_ticket: '终端票据无效或已过期',
  weak_password: '新密码强度不足（至少 12 位）',
  password_unchanged: '新密码不能与当前密码相同',
  authentication_failed: '密码或验证码错误',
  totp_pending_missing: '待确认的验证器绑定已过期，请重新开始',
  cannot_revoke_current: '不能注销当前正在使用的会话',
  reauthentication_required: '需要重新验证身份',
  csrf_rejected: '安全令牌已过期，请重试',
  no_capacity: '该区域暂无可用容量',
  rate_limited: '请求过于频繁，请稍后重试',
  upstream_timeout: '上游服务响应超时',
  upstream_error: '上游服务响应异常',
  invalid_credentials: 'API Key 已失效或认证失败',
  // Session expiry opens the login overlay; a visible unauthorized is a bad API Key.
  unauthorized: 'API Key 无效或已失效',
}

export function formatErrorCode(code: string | undefined | null): string {
  if (!code) return '请求失败，请稍后重试'
  return ERROR_CODE_MAP[code] ?? '请求失败，请稍后重试'
}

export type StateIconType = 'running' | 'stopped' | 'suspended' | 'busy' | 'error'

export interface StateMeta {
  label: string
  color: string
  icon: StateIconType
  isBusy: boolean
}

const STATE_MAP: Record<string, StateMeta> = {
  running: { label: '运行中', color: 'var(--ok)', icon: 'running', isBusy: false },
  stopped: { label: '已停止', color: 'var(--off)', icon: 'stopped', isBusy: false },
  suspended: { label: '已暂停', color: 'var(--warn)', icon: 'suspended', isBusy: false },
  error: { label: '出错', color: 'var(--bad)', icon: 'error', isBusy: false },
  creating: { label: '创建中', color: 'var(--busy)', icon: 'busy', isBusy: true },
  starting: { label: '启动中', color: 'var(--busy)', icon: 'busy', isBusy: true },
  stopping: { label: '停止中', color: 'var(--busy)', icon: 'busy', isBusy: true },
  suspending: { label: '暂停中', color: 'var(--busy)', icon: 'busy', isBusy: true },
  resuming: { label: '恢复中', color: 'var(--busy)', icon: 'busy', isBusy: true },
  deleting: { label: '删除中', color: 'var(--busy)', icon: 'busy', isBusy: true },
  deleted: { label: '已删除', color: 'var(--off)', icon: 'stopped', isBusy: false },
}

export function getMachineStateMeta(state: string): StateMeta {
  const known = STATE_MAP[state]
  if (known) return known
  return {
    label: state || '未知',
    color: 'var(--off)',
    icon: 'stopped',
    isBusy: false,
  }
}

const AUDIT_TYPE_MAP: Record<string, string> = {
  login: '登录',
  'login.password': '密码验证',
  logout: '退出登录',
  reauthentication: '重新验证',
  'credential.change': '凭据变更',
  'credential.validate': '凭据校验',
  'terminal.open': '打开终端',
  'terminal.close': '关闭终端',
  'operation.intent': '机器操作',
  'security.password': '修改密码',
  'security.start': '发起安全设置',
  'security.totp': '更换验证器',
  'security.recovery': '生成恢复码',
  'sessions.revoke': '注销设备会话',
}

export function formatAuditEventType(type: string): string {
  return AUDIT_TYPE_MAP[type] ?? type
}

import type { FleetMachine } from './types'

const ACCOUNT_COLORS = ['var(--a1)', 'var(--a2)', 'var(--a3)', 'var(--a4)']

export function getAccountColor(index: number): string {
  return ACCOUNT_COLORS[Math.max(0, index) % ACCOUNT_COLORS.length]
}

export function selectPreferredSource(machine: FleetMachine) {
  if (!machine.sources || machine.sources.length === 0) return undefined
  return machine.sources.find((s) => !s.stale) ?? machine.sources[0]
}

export function formatRelativeTime(iso: string | null | undefined): string {
  if (!iso) return '—'
  const t = Date.parse(iso)
  if (Number.isNaN(t)) return iso
  const diffSec = Math.max(0, Math.floor((Date.now() - t) / 1000))
  if (diffSec < 15) return '刚刚'
  if (diffSec < 60) return `${diffSec} 秒前`
  const diffMin = Math.floor(diffSec / 60)
  if (diffMin < 60) return `${diffMin} 分钟前`
  const diffHour = Math.floor(diffMin / 60)
  if (diffHour < 24) return `${diffHour} 小时前`
  const d = new Date(t)
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
}
