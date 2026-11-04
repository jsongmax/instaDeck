import { useMemo, useState } from 'react'
import { selectPreferredSource } from '../api/errors'
import { type FleetMachine, SKIN_META } from '../api/types'
import type { EnrichedAccount } from '../hooks/useFleetData'
import { useAppStore } from '../store/useAppStore'
import { ModalDialog } from './Dialogs'
import { Swatch } from './Icons'

interface PaletteItem {
  key: string
  label: string
  account?: EnrichedAccount
  run: () => void
}

function CommandPaletteBody({
  onClose,
  onOpenCreate,
  machines,
  accounts,
  accountMap,
}: {
  onClose: () => void
  onOpenCreate: () => void
  machines: FleetMachine[]
  accounts: EnrichedAccount[]
  accountMap: Map<string, EnrichedAccount>
}) {
  const setSelectedMachineId = useAppStore((s) => s.setSelectedMachineId)
  const openTerminal = useAppStore((s) => s.openTerminal)
  const setSkin = useAppStore((s) => s.setSkin)

  const [query, setQuery] = useState('')
  const [selectedIndex, setSelectedIndex] = useState(0)

  const allItems = useMemo<PaletteItem[]>(() => {
    const items: PaletteItem[] = []
    machines.forEach((m) => {
      if (m.state === 'deleted') return
      const pref = selectPreferredSource(m)
      const acct = pref ? accountMap.get(pref.credentialId) : undefined
      const invalid =
        !acct || acct.status === 'invalid' || Boolean(acct.fleetInfo?.error) || !acct.enabled
      if (m.state === 'running' && !invalid && acct) {
        items.push({
          key: `term-${m.id}`,
          label: `打开终端：${m.name}`,
          account: acct,
          run: () =>
            openTerminal({
              machineId: m.id,
              machineName: m.name,
              credentialId: acct.credentialId,
              accountName: acct.name,
              accountColor: acct.color,
            }),
        })
      }
      items.push({
        key: `view-${m.id}`,
        label: `查看机器：${m.name}`,
        account: acct,
        run: () => {
          window.location.hash = 'machines'
          setSelectedMachineId(m.id)
        },
      })
    })

    accounts.forEach((a) => {
      items.push({
        key: `acct-${a.credentialId}`,
        label: `筛选账号：${a.name}`,
        account: a,
        run: () => {
          window.location.hash = `acct-${a.credentialId}`
        },
      })
    })

    SKIN_META.forEach((s) => {
      items.push({
        key: `skin-${s.id}`,
        label: `切换主题：${s.name}`,
        run: () => setSkin(s.id, true),
      })
    })

    items.push(
      {
        key: 'nav-create',
        label: '新建机器',
        run: () => onOpenCreate(),
      },
      {
        key: 'nav-accounts',
        label: 'InstaBox 账号',
        run: () => {
          window.location.hash = 'accounts'
        },
      },
      {
        key: 'nav-activity',
        label: '查看操作记录',
        run: () => {
          window.location.hash = 'activity'
        },
      },
      {
        key: 'nav-security',
        label: '修改密码',
        run: () => {
          window.location.hash = 'security'
        },
      },
    )

    return items
  }, [accounts, accountMap, machines, onOpenCreate, openTerminal, setSelectedMachineId, setSkin])

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase()
    return allItems
      .filter((item) => !q || item.label.toLowerCase().includes(q))
      .slice(0, 12)
  }, [allItems, query])

  const activeIndex = Math.min(selectedIndex, Math.max(0, filtered.length - 1))

  const handleKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setSelectedIndex((prev) => Math.min(filtered.length - 1, prev + 1))
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setSelectedIndex((prev) => Math.max(0, prev - 1))
    } else if (e.key === 'Enter' && filtered[activeIndex]) {
      e.preventDefault()
      const action = filtered[activeIndex].run
      onClose()
      action()
    }
  }

  return (
    <>
      <input
        id="pq"
        placeholder="搜索机器或操作，例如 dev-1"
        aria-label="命令面板搜索"
        autoComplete="off"
        value={query}
        onChange={(e) => {
          setQuery(e.target.value)
          setSelectedIndex(0)
        }}
        onKeyDown={handleKeyDown}
        autoFocus
      />
      <div className="pal-list" id="pl">
        {filtered.length > 0 ? (
          filtered.map((item, idx) => (
            <button
              key={item.key}
              type="button"
              className={idx === activeIndex ? 'on' : ''}
              onClick={() => {
                onClose()
                item.run()
              }}
            >
              {item.account ? (
                <Swatch color={item.account.color} />
              ) : (
                <span className="swatch" style={{ visibility: 'hidden' }} />
              )}
              {item.label}
              {item.account ? (
                <span className="end">{item.account.name}</span>
              ) : null}
            </button>
          ))
        ) : (
          <div className="muted" style={{ padding: '10px' }}>
            没有匹配结果
          </div>
        )}
      </div>
    </>
  )
}

export function CommandPalette({
  open,
  onClose,
  onOpenCreate,
  machines,
  accounts,
  accountMap,
}: {
  open: boolean
  onClose: () => void
  onOpenCreate: () => void
  machines: FleetMachine[]
  accounts: EnrichedAccount[]
  accountMap: Map<string, EnrichedAccount>
}) {
  return (
    <ModalDialog open={open} onClose={onClose} className="palette">
      {open ? (
        <CommandPaletteBody
          onClose={onClose}
          onOpenCreate={onOpenCreate}
          machines={machines}
          accounts={accounts}
          accountMap={accountMap}
        />
      ) : null}
    </ModalDialog>
  )
}
