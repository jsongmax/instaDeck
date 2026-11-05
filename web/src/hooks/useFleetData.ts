import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback, useEffect, useRef, useState } from 'react'
import { api } from '../api/client'
import { ApiError, formatActionError, getAccountColor, getMachineStateMeta } from '../api/errors'
import type { Account, ActionKind, FleetAccount, FleetMachine, FleetResponse } from '../api/types'
import { useAppStore } from '../store/useAppStore'

export interface EnrichedAccount extends Account {
  color: string
  fleetInfo?: FleetAccount
  machineCount: number
  maxMachines: number
  hasLimits: boolean
  isUnlimitedMachines: boolean
  isQuotaFull: boolean
}

export function computeAccountQuota(
  account: Account,
  fleetInfo: FleetAccount | undefined,
  machines: FleetMachine[],
  color: string,
): EnrichedAccount {
  const visibleMachines = machines.filter(
    (m) => m.state !== 'deleted' && m.sources.some((s) => s.credentialId === account.credentialId),
  )
  // Usage is measured about hourly, so it can lag behind the live machine list.
  const used = Math.max(fleetInfo?.usage?.machines ?? 0, visibleMachines.length)
  const limits = fleetInfo?.usage?.limits
  const unlimitedList = limits?.unlimited ?? []
  const isUnlimitedMachines =
    unlimitedList.includes('maxMachines') || unlimitedList.includes('machines')
  const maxMachines = limits ? limits.maxMachines : 0
  const isQuotaFull = machineQuotaFull(limits, used)

  return {
    ...account,
    color,
    fleetInfo,
    machineCount: used,
    maxMachines,
    hasLimits: Boolean(limits),
    isUnlimitedMachines,
    isQuotaFull,
  }
}

// A 0 not listed in `unlimited` means none allowed. Servers that omit the list
// leave a 0 ambiguous, so it does not block anything there.
export function machineQuotaFull(
  limits: { maxMachines: number; unlimited?: string[] | null } | undefined,
  used: number,
): boolean {
  if (!limits) return false
  const listed = limits.unlimited
  if (listed?.includes('maxMachines') || listed?.includes('machines')) return false
  if (limits.maxMachines === 0 && !Array.isArray(listed)) return false
  return used >= limits.maxMachines
}

export function useFleetData() {
  const queryClient = useQueryClient()
  const session = useAppStore((s) => s.session)
  const authRequired = useAppStore((s) => s.authRequired)
  const sessionChecked = useAppStore((s) => s.sessionChecked)
  const trackedActions = useAppStore((s) => s.trackedActions)
  const setLastSyncAt = useAppStore((s) => s.setLastSyncAt)
  const removeTrackedAction = useAppStore((s) => s.removeTrackedAction)
  const upsertTrackedAction = useAppStore((s) => s.upsertTrackedAction)
  const setMachineFailedAction = useAppStore((s) => s.setMachineFailedAction)
  const setMachineUnknownAction = useAppStore((s) => s.setMachineUnknownAction)
  const triggerRowFlash = useAppStore((s) => s.triggerRowFlash)

  const [pageHidden, setPageHidden] = useState<boolean>(() =>
    typeof document !== 'undefined' ? document.hidden : false,
  )

  useEffect(() => {
    const onVis = () => setPageHidden(document.hidden)
    document.addEventListener('visibilitychange', onVis)
    return () => document.removeEventListener('visibilitychange', onVis)
  }, [])

  const enabled = Boolean(session) && sessionChecked && !authRequired && !pageHidden

  const accountsQuery = useQuery({
    queryKey: ['accounts'],
    queryFn: () => api.getAccounts(),
    enabled,
    staleTime: 10_000,
    retry: false,
  })

  const fleetQuery = useQuery({
    queryKey: ['fleet'],
    queryFn: async () => {
      const data = await api.getFleet()
      setLastSyncAt(Date.now())
      return data
    },
    enabled,
    retry: false,
    refetchInterval: (query) => {
      if (!enabled) return false
      const hasTracked = Object.keys(useAppStore.getState().trackedActions).length > 0
      const fleetData = query.state.data as FleetResponse | undefined
      const hasBusyMachine =
        fleetData?.machines?.some(
          (m) => m.state !== 'deleted' && getMachineStateMeta(m.state).isBusy,
        ) ?? false
      const baseMs = hasTracked || hasBusyMachine ? 5_000 : 10_000
      const errCount = query.state.fetchFailureCount
      if (errCount > 0) {
        return Math.min(60_000, baseMs * Math.pow(2, errCount))
      }
      return baseMs
    },
  })

  // Poll active actions every 1.5s until state is completed or unknown
  const trackedActionIds = Object.keys(trackedActions)
  const pollingBusyRef = useRef<Set<string>>(new Set())

  const pollSingleAction = useCallback(
    async (actionId: string) => {
      if (pollingBusyRef.current.has(actionId)) return
      pollingBusyRef.current.add(actionId)
      try {
        const updated = await api.getAction(actionId)
        if (!useAppStore.getState().session || !useAppStore.getState().trackedActions[actionId]) return
        const isCreate = updated.kind === 'create' || Boolean(useAppStore.getState().createRecords[actionId])
        const isTerminal = updated.state === 'completed' || updated.state === 'unknown'
        if (!isTerminal) {
          upsertTrackedAction(updated)
          return
        }

        removeTrackedAction(actionId)
        const machineId = updated.machineId

        if (updated.upstreamState === 'failed' || Boolean(updated.errorCode)) {
          if (machineId && !isCreate) {
            setMachineUnknownAction(machineId, false)
            setMachineFailedAction(machineId, {
              actionId: updated.id,
              kind: (updated.kind as ActionKind) || 'start',
              credentialId: updated.credentialId,
              errorCode: updated.errorCode || 'operation_rejected',
            })
            triggerRowFlash(machineId, 'bad')
          } else {
            // A failed create has no machine row to show the failure on.
            const message = formatActionError(updated.errorCode)
            useAppStore.getState().failCreateRecord(updated.id, message)
            useAppStore.getState().showToast(`创建机器失败：${message}`)
          }
        } else if (updated.state === 'unknown') {
          if (machineId && !isCreate) {
            setMachineFailedAction(machineId, null)
            setMachineUnknownAction(machineId, true)
          } else {
            const message = '结果未知，请稍后刷新机器列表确认'
            useAppStore.getState().failCreateRecord(updated.id, message)
            useAppStore.getState().showToast(`创建机器${message}`)
          }
        } else {
          if (machineId && !isCreate) {
            setMachineFailedAction(machineId, null)
            setMachineUnknownAction(machineId, false)
            triggerRowFlash(machineId, 'ok')
          }
        }

        await queryClient.invalidateQueries({ queryKey: ['fleet'] })
        // The new machine now has its own row, so the placeholder can go.
        if (useAppStore.getState().createRecords[actionId]?.status === 'running') {
          useAppStore.getState().removeCreateRecord(actionId)
        }
      } catch (err) {
        if (err instanceof ApiError && err.status === 404) {
          useAppStore.getState().removeCreateRecord(actionId)
          removeTrackedAction(actionId)
        }
        // transient network error while polling action; will retry next tick
      } finally {
        pollingBusyRef.current.delete(actionId)
      }
    },
    [
      queryClient,
      removeTrackedAction,
      setMachineFailedAction,
      setMachineUnknownAction,
      triggerRowFlash,
      upsertTrackedAction,
    ],
  )

  useEffect(() => {
    if (!enabled || trackedActionIds.length === 0) return
    const timer = setInterval(() => {
      const currentIds = Object.keys(useAppStore.getState().trackedActions)
      currentIds.forEach((id) => {
        void pollSingleAction(id)
      })
    }, 1500)
    return () => clearInterval(timer)
  }, [enabled, trackedActionIds.length, pollSingleAction])

  const rawAccounts = accountsQuery.data?.accounts ?? []
  const fleetData = fleetQuery.data
  const machines = (fleetData?.machines ?? []).filter((m) => m.state !== 'deleted')
  const fleetAccounts = fleetData?.accounts ?? []

  // Merge accounts from /accounts and /fleet
  const accountMap = new Map<string, EnrichedAccount>()
  rawAccounts.forEach((acct, idx) => {
    const fInfo = fleetAccounts.find((fa) => fa.credentialId === acct.credentialId)
    const enriched = computeAccountQuota(acct, fInfo, machines, getAccountColor(idx))
    accountMap.set(acct.credentialId, enriched)
  })

  // Also include any fleetAccounts not yet in rawAccounts (e.g. if /fleet loaded first)
  fleetAccounts.forEach((fa, idx) => {
    if (!accountMap.has(fa.credentialId)) {
      const placeholder: Account = {
        credentialId: fa.credentialId,
        name: fa.credentialId,
        enabled: true,
        status: fa.error ? 'invalid' : 'valid',
        capabilities: { read: 'verified', write: 'declared', delete: 'unknown' },
      }
      accountMap.set(
        fa.credentialId,
        computeAccountQuota(
          placeholder,
          fa,
          machines,
          getAccountColor(rawAccounts.length + idx),
        ),
      )
    }
  })

  const accounts = Array.from(accountMap.values())

  return {
    accounts,
    accountMap,
    machines,
    fleetAccounts,
    partial: fleetData?.partial ?? false,
    isLoading: (accountsQuery.isLoading || fleetQuery.isLoading) && !fleetData,
    isError: fleetQuery.isError,
    refetchFleet: () => queryClient.invalidateQueries({ queryKey: ['fleet'] }),
    refetchAccounts: () => queryClient.invalidateQueries({ queryKey: ['accounts'] }),
  }
}
