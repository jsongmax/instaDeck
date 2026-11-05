import { create } from 'zustand'
import type { ActionKind, ActionRecord, SessionInfo } from '../api/types'

export interface TerminalWindowItem {
  id: string
  machineId: string
  machineName: string
  credentialId: string
  accountName: string
  accountColor: string
  minimized: boolean
  maximized: boolean
  z: number
  x: number
  y: number
  w: number
  h: number
  fresh: boolean
}

// A create has no machine row until it succeeds, so it gets a placeholder row.
export interface CreateRecord {
  id: string
  name: string
  credentialId: string
  timestamp: number
  status: 'running' | 'failed'
  message?: string
}

export interface FailedActionInfo {
  actionId: string
  kind: ActionKind
  credentialId: string
  errorCode: string
  confirmName?: string
}

const createStorageKey = 'instadeck-create-records'

function saveCreateRecords(records: Record<string, CreateRecord>) {
  try {
    if (!Object.keys(records).length) localStorage.removeItem(createStorageKey)
    else localStorage.setItem(createStorageKey, JSON.stringify(Object.values(records).map(
      ({ id, name, credentialId, status, message, timestamp }) => ({ id, name, credentialId, status, message, timestamp }),
    )))
  } catch { }
}

function loadCreateRecords(): Record<string, CreateRecord> {
  try {
    const values: unknown = JSON.parse(localStorage.getItem(createStorageKey) || '[]')
    if (!Array.isArray(values)) return {}
    const records: Record<string, CreateRecord> = {}
    for (const value of values) {
      if (!value || typeof value !== 'object') continue
      const { id, name, credentialId, status, message, timestamp } = value
      if (typeof id !== 'string' || !id || typeof name !== 'string' ||
          typeof credentialId !== 'string' || !credentialId ||
          (status !== 'running' && status !== 'failed') ||
          (message !== undefined && typeof message !== 'string') ||
          typeof timestamp !== 'number' || !Number.isFinite(timestamp) ||
          timestamp > Date.now() || Date.now() - timestamp >= 24 * 3600_000) continue
      Object.defineProperty(records, id, { value: { id, name, credentialId, status, message, timestamp }, enumerable: true, configurable: true, writable: true })
    }
    return records
  } catch { return {} }
}

function loadSavedSkin(): string {
  try {
    return localStorage.getItem('instadeck-skin') || 'auto'
  } catch {
    return 'auto'
  }
}

function loadSavedTermSize(): number {
  try {
    const raw = localStorage.getItem('instadeck-term-size')
    const parsed = raw ? Number(raw) : 13
    return [12, 13, 14, 15, 16].includes(parsed) ? parsed : 13
  } catch {
    return 13
  }
}

export function applySkinToDocument(skin: string, withTransition = false) {
  const update = () => {
    if (skin === 'auto') {
      delete document.documentElement.dataset.skin
    } else {
      document.documentElement.dataset.skin = skin
    }
  }
  const canTransition =
    withTransition &&
    typeof document !== 'undefined' &&
    'startViewTransition' in document &&
    typeof document.startViewTransition === 'function' &&
    typeof window !== 'undefined' &&
    typeof window.matchMedia === 'function' &&
    !window.matchMedia('(prefers-reduced-motion: reduce)').matches

  if (canTransition) {
    document.startViewTransition(update)
  } else {
    update()
  }
}

export function applyTermSizeToDocument(size: number) {
  if (typeof document !== 'undefined') {
    document.documentElement.style.setProperty('--term-size', `${size}px`)
  }
}

interface AppState {
  session: SessionInfo | null
  sessionChecked: boolean
  authRequired: boolean
  reauthOpen: boolean
  reauthWaiters: Array<(ok: boolean) => void>

  route: string
  selectedMachineId: string | null
  skin: string
  termSize: number

  toast: { id: number; text: string; closing?: boolean } | null
  lastSyncAt: number | null

  trackedActions: Record<string, ActionRecord>
  createRecords: Record<string, CreateRecord>
  failedActionsByMachine: Record<string, FailedActionInfo>
  unknownActionMachines: Record<string, boolean>
  rowFlashes: Record<string, 'ok' | 'bad'>

  terminals: TerminalWindowItem[]
  topZ: number
  winSeq: number

  setSession: (session: SessionInfo | null) => void
  updateCsrfToken: (csrfToken: string) => void
  setAuthRequired: (required: boolean) => void
  clearCreateRecords: () => void
  setSessionChecked: (checked: boolean) => void

  requestReauth: () => Promise<boolean>
  resolveReauth: (ok: boolean) => void

  setRoute: (route: string) => void
  setSelectedMachineId: (id: string | null) => void
  setSkin: (skin: string, persist?: boolean) => void
  setTermSize: (size: number, persist?: boolean) => void
  showToast: (text: string) => void
  setLastSyncAt: (ts: number) => void

  upsertTrackedAction: (action: ActionRecord) => void
  addCreateRecord: (actionId: string, name: string, credentialId: string) => void
  failCreateRecord: (actionId: string, message: string) => void
  removeCreateRecord: (actionId: string) => void
  removeTrackedAction: (actionId: string) => void
  setMachineFailedAction: (machineId: string, info: FailedActionInfo | null) => void
  setMachineUnknownAction: (machineId: string, unknown: boolean) => void
  triggerRowFlash: (machineId: string, kind: 'ok' | 'bad') => void

  openTerminal: (params: {
    machineId: string
    machineName: string
    credentialId: string
    accountName: string
    accountColor: string
  }) => { opened: boolean; reason?: string }
  focusTerminal: (id: string) => void
  minimizeTerminal: (id: string) => void
  toggleMaximizeTerminal: (id: string) => void
  closeTerminal: (id: string) => void
  closeTerminalsByMachine: (machineId: string) => void
  updateTerminalGeometry: (id: string, geo: Partial<Pick<TerminalWindowItem, 'x' | 'y' | 'w' | 'h'>>) => void
}

let toastTimer1: ReturnType<typeof setTimeout> | null = null
let toastTimer2: ReturnType<typeof setTimeout> | null = null
let toastSeq = 0

export const useAppStore = create<AppState>((set, get) => ({
  session: null,
  sessionChecked: false,
  authRequired: false,
  reauthOpen: false,
  reauthWaiters: [],

  route: 'machines',
  selectedMachineId: null,
  skin: loadSavedSkin(),
  termSize: loadSavedTermSize(),

  toast: null,
  lastSyncAt: null,

  trackedActions: {},
  createRecords: {},
  failedActionsByMachine: {},
  unknownActionMachines: {},
  rowFlashes: {},

  terminals: [],
  topZ: 10,
  winSeq: 0,

  setSession: (session) => {
    const records = session ? (get().session ? get().createRecords : loadCreateRecords()) : {}
    const trackedActions = session ? { ...get().trackedActions } : {}
    if (session && !get().session) {
      for (const record of Object.values(records)) {
        if (record.status === 'running') trackedActions[record.id] = {
          id: record.id, credentialId: record.credentialId, machineId: '', kind: 'create',
          state: 'tracking', createdAt: new Date(record.timestamp).toISOString(), updatedAt: new Date(record.timestamp).toISOString(),
        }
      }
    }
    // Being signed out does not forget a create still running: it resumes at the next
    // login. Only an explicit logout clears what is stored.
    if (session) saveCreateRecords(records)
    set({ session, createRecords: records, trackedActions, authRequired: session === null, sessionChecked: true })
  },

  updateCsrfToken: (csrfToken) =>
    set((s) => ({
      session: s.session
        ? { ...s.session, csrfToken }
        : {
            administratorId: 'admin',
            csrfToken,
            expiresAt: new Date(Date.now() + 3600_000).toISOString(),
            recentlyAuthenticated: true,
          },
    })),

  setAuthRequired: (required) => set({ authRequired: required, sessionChecked: true }),

  clearCreateRecords: () => {
    saveCreateRecords({})
    set({ createRecords: {}, trackedActions: {} })
  },

  setSessionChecked: (checked) => set({ sessionChecked: checked }),

  requestReauth: () =>
    new Promise<boolean>((resolve) => {
      set((s) => ({
        reauthOpen: true,
        reauthWaiters: [...s.reauthWaiters, resolve],
      }))
    }),

  resolveReauth: (ok) => {
    const waiters = get().reauthWaiters
    set({ reauthOpen: false, reauthWaiters: [] })
    waiters.forEach((fn) => fn(ok))
  },

  setRoute: (route) => set({ route }),
  setSelectedMachineId: (id) => set({ selectedMachineId: id }),

  setSkin: (skin, persist = true) => {
    applySkinToDocument(skin, persist)
    if (persist) {
      try {
        localStorage.setItem('instadeck-skin', skin)
      } catch {
        // ignore storage errors
      }
    }
    set({ skin })
  },

  setTermSize: (size, persist = true) => {
    applyTermSizeToDocument(size)
    if (persist) {
      try {
        localStorage.setItem('instadeck-term-size', String(size))
      } catch {
        // ignore storage errors
      }
    }
    set({ termSize: size })
  },

  showToast: (text) => {
    if (toastTimer1) clearTimeout(toastTimer1)
    if (toastTimer2) clearTimeout(toastTimer2)
    const id = ++toastSeq
    set({ toast: { id, text, closing: false } })
    toastTimer1 = setTimeout(() => {
      set((s) => (s.toast && s.toast.id === id ? { toast: { ...s.toast, closing: true } } : s))
      toastTimer2 = setTimeout(() => {
        set((s) => (s.toast && s.toast.id === id ? { toast: null } : s))
      }, 200)
    }, 2600)
  },

  setLastSyncAt: (ts) => set({ lastSyncAt: ts }),

  addCreateRecord: (actionId, name, credentialId) =>
    set((s) => ({
      createRecords: {
        ...s.createRecords,
        [actionId]: { id: actionId, name, credentialId, status: 'running', timestamp: Date.now() },
      },
    })),

  failCreateRecord: (actionId, message) =>
    set((s) => {
      const record = s.createRecords[actionId]
      if (!record) return {}
      return {
        createRecords: { ...s.createRecords, [actionId]: { ...record, status: 'failed', message } },
      }
    }),

  removeCreateRecord: (actionId) =>
    set((s) => {
      const next = { ...s.createRecords }
      delete next[actionId]
      return { createRecords: next }
    }),

  upsertTrackedAction: (action) =>
    set((s) => ({
      trackedActions: { ...s.trackedActions, [action.id]: action },
    })),

  removeTrackedAction: (actionId) =>
    set((s) => {
      const next = { ...s.trackedActions }
      delete next[actionId]
      return { trackedActions: next }
    }),

  setMachineFailedAction: (machineId, info) =>
    set((s) => {
      const next = { ...s.failedActionsByMachine }
      if (info) {
        next[machineId] = info
      } else {
        delete next[machineId]
      }
      return { failedActionsByMachine: next }
    }),

  setMachineUnknownAction: (machineId, unknown) =>
    set((s) => {
      const next = { ...s.unknownActionMachines }
      if (unknown) {
        next[machineId] = true
      } else {
        delete next[machineId]
      }
      return { unknownActionMachines: next }
    }),

  triggerRowFlash: (machineId, kind) => {
    set((s) => ({
      rowFlashes: { ...s.rowFlashes, [machineId]: kind },
    }))
    setTimeout(() => {
      set((s) => {
        const next = { ...s.rowFlashes }
        delete next[machineId]
        return { rowFlashes: next }
      })
    }, 1050)
  },

  openTerminal: ({ machineId, machineName, credentialId, accountName, accountColor }) => {
    const state = get()
    const existing = state.terminals.find(
      (t) => t.machineId === machineId && t.credentialId === credentialId,
    )
    if (existing) {
      const nextZ = state.topZ + 1
      set({
        topZ: nextZ,
        terminals: state.terminals.map((t) =>
          t.id === existing.id ? { ...t, minimized: false, z: nextZ, fresh: true } : t,
        ),
      })
      return { opened: true }
    }
    if (state.terminals.length >= 8) {
      state.showToast('窗口已达 8 个上限，先关闭一个。')
      return { opened: false, reason: 'limit' }
    }
    const seq = state.winSeq + 1
    const nextZ = state.topZ + 1
    const offset = (seq % 6) * 28
    const w = 560
    const h = 320
    const vw = typeof window !== 'undefined' ? window.innerWidth : 1280
    const vh = typeof window !== 'undefined' ? window.innerHeight : 800
    const x = Math.max(16, vw - w - 40 - offset)
    const y = Math.max(66, vh - h - 70 - offset)
    const win: TerminalWindowItem = {
      id: `w${seq}`,
      machineId,
      machineName,
      credentialId,
      accountName,
      accountColor,
      minimized: false,
      maximized: false,
      z: nextZ,
      x,
      y,
      w,
      h,
      fresh: true,
    }
    set({
      winSeq: seq,
      topZ: nextZ,
      terminals: [...state.terminals, win],
    })
    return { opened: true }
  },

  focusTerminal: (id) =>
    set((s) => {
      const target = s.terminals.find((t) => t.id === id)
      if (!target) return s
      const nextZ = s.topZ + 1
      return {
        topZ: nextZ,
        terminals: s.terminals.map((t) =>
          t.id === id
            ? { ...t, minimized: false, z: nextZ, fresh: t.minimized ? true : false }
            : t,
        ),
      }
    }),

  minimizeTerminal: (id) =>
    set((s) => ({
      terminals: s.terminals.map((t) => (t.id === id ? { ...t, minimized: true, fresh: false } : t)),
    })),

  toggleMaximizeTerminal: (id) =>
    set((s) => ({
      terminals: s.terminals.map((t) => (t.id === id ? { ...t, maximized: !t.maximized } : t)),
    })),

  closeTerminal: (id) =>
    set((s) => ({
      terminals: s.terminals.filter((t) => t.id !== id),
    })),

  closeTerminalsByMachine: (machineId) =>
    set((s) => ({
      terminals: s.terminals.filter((t) => t.machineId !== machineId),
    })),

  updateTerminalGeometry: (id, geo) =>
    set((s) => ({
      terminals: s.terminals.map((t) => (t.id === id ? { ...t, ...geo } : t)),
    })),
}))

useAppStore.subscribe((state, previous) => {
  if (state.createRecords !== previous.createRecords && state.session) saveCreateRecords(state.createRecords)
})
