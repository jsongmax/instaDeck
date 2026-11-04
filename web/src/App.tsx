import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { useEffect, useRef, useState } from 'react'
import { api } from './api/client'
import { AccountsPage } from './components/AccountsPage'
import { ActivityPage } from './components/ActivityPage'
import { AppearancePage } from './components/AppearancePage'
import { CommandPalette } from './components/CommandPalette'
import { CreateMachineDialog, ReauthDialog, ThemeDialog } from './components/Dialogs'
import { ErrorIcon, Icon, LogoSvg, Swatch } from './components/Icons'
import { SetupGate } from './components/SetupWizard'
import { LoginOverlay } from './components/LoginOverlay'
import { MachineDrawer } from './components/MachineDrawer'
import { MachinesPage } from './components/MachinesPage'
import { SecurityPage } from './components/SecurityPage'
import { TerminalDock, TerminalWindowsLayer } from './components/TerminalWindow'
import { useFleetData } from './hooks/useFleetData'
import {
  applySkinToDocument,
  applyTermSizeToDocument,
  useAppStore,
} from './store/useAppStore'

function parseHashRoute(hash: string): string {
  const raw = hash.replace(/^#/, '').trim()
  if (
    raw === 'machines' ||
    raw === 'accounts' ||
    raw === 'activity' ||
    raw === 'security' ||
    raw === 'appearance' ||
    raw.startsWith('acct-')
  ) {
    return raw
  }
  return 'machines'
}

function shouldPlayBootAnimation(): boolean {
  try {
    if (sessionStorage.getItem('instadeck-booted') === '1') {
      return false
    }
    sessionStorage.setItem('instadeck-booted', '1')
    return true
  } catch {
    return false
  }
}

function WorkspaceShell() {
  const route = useAppStore((s) => s.route)
  const setRoute = useAppStore((s) => s.setRoute)
  const selectedMachineId = useAppStore((s) => s.selectedMachineId)
  const setSelectedMachineId = useAppStore((s) => s.setSelectedMachineId)
  const lastSyncAt = useAppStore((s) => s.lastSyncAt)
  const toast = useAppStore((s) => s.toast)
  const setSession = useAppStore((s) => s.setSession)
  const setAuthRequired = useAppStore((s) => s.setAuthRequired)
  const showToast = useAppStore((s) => s.showToast)

  const { accounts, accountMap, machines, fleetAccounts, isLoading } = useFleetData()

  const [booting, setBooting] = useState<boolean>(() => shouldPlayBootAnimation())
  const [bootDoneClass, setBootDoneClass] = useState<boolean>(false)
  const [appReady, setAppReady] = useState<boolean>(() => !booting)
  const [pageEnter, setPageEnter] = useState<boolean>(true)
  const [paletteOpen, setPaletteOpen] = useState<boolean>(false)
  const [themeOpen, setThemeOpen] = useState<boolean>(false)
  const [createFromPaletteOpen, setCreateFromPaletteOpen] = useState<boolean>(false)
  const [nowTick, setNowTick] = useState<number>(() => Date.now())

  const pageRef = useRef<HTMLDivElement | null>(null)

  // Initial theme & font size sync
  useEffect(() => {
    const st = useAppStore.getState()
    applySkinToDocument(st.skin, false)
    applyTermSizeToDocument(st.termSize)
  }, [])

  // Boot screen animation lifecycle
  useEffect(() => {
    if (!booting) return
    const reduced =
      typeof window !== 'undefined' &&
      typeof window.matchMedia === 'function' &&
      window.matchMedia('(prefers-reduced-motion: reduce)').matches
    const delay = reduced ? 0 : 950
    const t1 = setTimeout(() => {
      setBootDoneClass(true)
      setAppReady(true)
    }, delay)
    const t2 = setTimeout(() => {
      setBooting(false)
    }, delay + 460)
    return () => {
      clearTimeout(t1)
      clearTimeout(t2)
    }
  }, [booting])

  // Check session on startup
  useEffect(() => {
    let active = true
    api
      .getSession()
      .then((sess) => {
        if (active) setSession(sess)
      })
      .catch(() => {
        if (active) setAuthRequired(true)
      })
    return () => {
      active = false
    }
  }, [setAuthRequired, setSession])

  // Hash routing listener
  useEffect(() => {
    const syncRoute = () => {
      const next = parseHashRoute(window.location.hash)
      setRoute(next)
      setPageEnter(true)
    }
    syncRoute()
    window.addEventListener('hashchange', syncRoute)
    window.addEventListener('popstate', syncRoute)
    return () => {
      window.removeEventListener('hashchange', syncRoute)
      window.removeEventListener('popstate', syncRoute)
    }
  }, [setRoute])

  useEffect(() => {
    if (!pageEnter) return
    const timer = setTimeout(() => setPageEnter(false), 900)
    return () => clearTimeout(timer)
  }, [pageEnter, route])

  // Update sync elapsed seconds ticker
  useEffect(() => {
    const timer = setInterval(() => setNowTick(Date.now()), 1000)
    return () => clearInterval(timer)
  }, [])

  // Global keyboard shortcuts & pointer effects (button ripple & stat tile spotlight)
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        setPaletteOpen(true)
      } else if (e.key === 'Escape' && selectedMachineId) {
        const openDialog = document.querySelector('dialog[open]')
        if (!openDialog) {
          setSelectedMachineId(null)
        }
      }
    }

    const onPointerDown = (e: PointerEvent) => {
      const target = e.target as HTMLElement | null
      const button = target?.closest?.('.btn') as HTMLButtonElement | null
      if (!button || button.disabled) return
      const box = button.getBoundingClientRect()
      const dot = document.createElement('span')
      dot.className = 'rip'
      dot.style.left = `${e.clientX - box.left}px`
      dot.style.top = `${e.clientY - box.top}px`
      dot.addEventListener('animationend', () => dot.remove())
      button.appendChild(dot)
    }

    const onPointerMove = (e: PointerEvent) => {
      const target = e.target as HTMLElement | null
      const tile = target?.closest?.('.tile') as HTMLElement | null
      if (!tile) return
      const box = tile.getBoundingClientRect()
      tile.style.setProperty('--mx', `${e.clientX - box.left}px`)
      tile.style.setProperty('--my', `${e.clientY - box.top}px`)
    }

    document.addEventListener('keydown', onKeyDown)
    document.addEventListener('pointerdown', onPointerDown)
    document.addEventListener('pointermove', onPointerMove)
    return () => {
      document.removeEventListener('keydown', onKeyDown)
      document.removeEventListener('pointerdown', onPointerDown)
      document.removeEventListener('pointermove', onPointerMove)
    }
  }, [selectedMachineId, setSelectedMachineId])

  const handleLogout = async () => {
    try {
      await api.logout()
    } catch {
      // ignore logout error
    }
    setAuthRequired(true)
    showToast('已退出登录')
  }

  const badAccountCount = accounts.filter(
    (a) => a.status === 'invalid' || Boolean(a.fleetInfo?.error),
  ).length

  const elapsedSec = lastSyncAt ? Math.max(0, Math.floor((nowTick - lastSyncAt) / 1000)) : 0
  const syncText = `${elapsedSec > 1 ? `${elapsedSec} 秒前更新` : '刚刚更新'}${
    badAccountCount > 0 ? ` · ${badAccountCount} 个账号异常` : ''
  }`

  const filterAccountId = route.startsWith('acct-') ? route.slice(5) : undefined

  return (
    <>
      <div className="aurora" aria-hidden="true">
        <i />
        <i />
        <i />
      </div>

      {booting ? (
        <div className={`boot ${bootDoneClass ? 'done' : ''}`} id="boot" aria-hidden="true">
          <div className="boot-mark">
            <svg width="56" height="56" viewBox="0 0 32 32">
              <rect x="4" y="5" width="24" height="6" rx="2" />
              <rect x="4" y="13" width="24" height="6" rx="2" />
              <rect x="4" y="21" width="24" height="6" rx="2" />
              <circle className="l1" cx="9" cy="8" r="1.4" />
              <circle className="l2" cx="9" cy="16" r="1.4" />
              <circle className="l3" cx="9" cy="24" r="1.4" />
            </svg>
          </div>
          <div className="boot-name">InstaDeck</div>
          <div className="boot-bar">
            <i />
          </div>
        </div>
      ) : null}

      <div className={`app ${appReady ? 'ready' : ''}`} id="app">
        <header className="top">
          <span className="brand">
            <LogoSvg size={24} />
            InstaDeck
          </span>
          <button
            type="button"
            className="cmdk"
            aria-label="打开命令面板"
            onClick={() => setPaletteOpen(true)}
          >
            <svg width="14" height="14" viewBox="0 0 16 16" aria-hidden="true">
              <circle
                cx="7"
                cy="7"
                r="4.6"
                fill="none"
                stroke="currentColor"
                strokeWidth="1.6"
              />
              <path
                d="m10.6 10.6 3.4 3.4"
                stroke="currentColor"
                strokeWidth="1.6"
                strokeLinecap="round"
              />
            </svg>
            <span>搜索机器或操作</span>
            <kbd>Ctrl K</kbd>
          </button>
          <span className="spacer" />
          <span className="sync" id="sync">
            <svg
              className="sync-ring"
              width="16"
              height="16"
              viewBox="0 0 16 16"
              aria-hidden="true"
            >
              <circle cx="8" cy="8" r="6" />
              <circle className="p" cx="8" cy="8" r="6" pathLength={100} />
            </svg>
            <span id="syncText">{syncText}</span>
          </span>
          <button
            type="button"
            className="btn icon-only"
            id="themeBtn"
            aria-label="切换明暗主题"
            title="切换明暗主题"
            onClick={() => setThemeOpen(true)}
          >
            <svg width="15" height="15" viewBox="0 0 16 16" aria-hidden="true">
              <path d="M8 1.5a6.5 6.5 0 1 0 0 13z" fill="currentColor" />
              <circle
                cx="8"
                cy="8"
                r="6.5"
                fill="none"
                stroke="currentColor"
                strokeWidth="1.3"
              />
            </svg>
          </button>
          <button type="button" className="btn" onClick={() => void handleLogout()}>
            退出
          </button>
        </header>

        <nav className="side" id="side" aria-label="主导航">
          <div className="nav">
            <a
              href="#machines"
              className={route === 'machines' ? 'on' : ''}
              aria-current={route === 'machines' ? 'page' : undefined}
            >
              <Icon name="grid" />
              全部机器
              <span className="count">{machines.length}</span>
            </a>
          </div>

          <div className="nav">
            <div className="nav-title">账号</div>
            {accounts.map((acct) => {
              const targetRoute = `acct-${acct.credentialId}`
              const isOn = route === targetRoute
              const invalid = acct.status === 'invalid' || Boolean(acct.fleetInfo?.error)
              const countStr = acct.isUnlimitedMachines
                ? `${acct.machineCount}/∞`
                : acct.hasLimits
                  ? `${acct.machineCount}/${acct.maxMachines}`
                  : '—'
              const meterPct = acct.isUnlimitedMachines
                ? Math.min(100, acct.machineCount * 10)
                : acct.maxMachines > 0
                  ? Math.min(100, (acct.machineCount / acct.maxMachines) * 100)
                  : 0

              return (
                <a
                  key={acct.credentialId}
                  href={`#${targetRoute}`}
                  className={`acct ${isOn ? 'on' : ''}`}
                  aria-current={isOn ? 'page' : undefined}
                >
                  <div className="row1">
                    <Swatch color={acct.color} />
                    <span>{acct.name}</span>
                    <span className="count">{countStr}</span>
                  </div>
                  <div
                    className="meter"
                    style={{ '--c': acct.color } as React.CSSProperties}
                  >
                    <i style={{ width: `${meterPct}%` }} />
                  </div>
                  {invalid ? (
                    <div className="acct-alert">
                      <ErrorIcon /> Key 已失效
                    </div>
                  ) : !acct.enabled ? (
                    <div className="acct-note">已暂停同步</div>
                  ) : acct.fleetInfo && !acct.fleetInfo.complete ? (
                    <div className="acct-note">首次同步中…</div>
                  ) : null}
                </a>
              )
            })}
          </div>

          <div className="nav">
            <div className="nav-title">管理</div>
            <a
              href="#accounts"
              className={route === 'accounts' ? 'on' : ''}
              aria-current={route === 'accounts' ? 'page' : undefined}
            >
              <Icon name="key" />
              InstaBox 账号
            </a>
            <a
              href="#activity"
              className={route === 'activity' ? 'on' : ''}
              aria-current={route === 'activity' ? 'page' : undefined}
            >
              <Icon name="list" />
              操作记录
            </a>
          </div>

          <div className="nav">
            <div className="nav-title">面板设置</div>
            <a
              href="#security"
              className={route === 'security' ? 'on' : ''}
              aria-current={route === 'security' ? 'page' : undefined}
            >
              <Icon name="shield" />
              登录与安全
            </a>
            <a
              href="#appearance"
              className={route === 'appearance' ? 'on' : ''}
              aria-current={route === 'appearance' ? 'page' : undefined}
            >
              <Icon name="palette" />
              外观主题
            </a>
          </div>
        </nav>

        <main className="main">
          <div
            ref={pageRef}
            className={`page ${pageEnter ? 'enter' : ''}`}
            id="page"
          >
            {route === 'accounts' ? (
              <AccountsPage accounts={accounts} />
            ) : route === 'activity' ? (
              <ActivityPage accountMap={accountMap} machines={machines} />
            ) : route === 'security' ? (
              <SecurityPage />
            ) : route === 'appearance' ? (
              <AppearancePage />
            ) : (
              <MachinesPage
                accounts={accounts}
                accountMap={accountMap}
                machines={machines}
                fleetAccounts={fleetAccounts}
                filterAccountId={filterAccountId}
                isLoading={isLoading}
              />
            )}
          </div>

          <MachineDrawer machines={machines} accountMap={accountMap} />
          <TerminalDock />
        </main>
      </div>

      <TerminalWindowsLayer />

      <CommandPalette
        open={paletteOpen}
        onClose={() => setPaletteOpen(false)}
        onOpenCreate={() => setCreateFromPaletteOpen(true)}
        machines={machines}
        accounts={accounts}
        accountMap={accountMap}
      />

      <CreateMachineDialog
        open={createFromPaletteOpen}
        accounts={accounts}
        defaultCredentialId={filterAccountId}
        onClose={() => setCreateFromPaletteOpen(false)}
      />

      <ThemeDialog open={themeOpen} onClose={() => setThemeOpen(false)} />
      <ReauthDialog />
      <LoginOverlay />

      {toast ? (
        <div
          className={`toast ${toast.closing ? 'out' : ''}`}
          id="toast"
          role="status"
        >
          {toast.text}
        </div>
      ) : null}
    </>
  )
}

const defaultQueryClient = new QueryClient({
  defaultOptions: {
    queries: {
      refetchOnWindowFocus: false,
      retry: false,
    },
  },
})

export function App({ queryClient = defaultQueryClient }: { queryClient?: QueryClient }) {
  return (
    <QueryClientProvider client={queryClient}>
      <SetupGate><WorkspaceShell /></SetupGate>
    </QueryClientProvider>
  )
}

export default App
