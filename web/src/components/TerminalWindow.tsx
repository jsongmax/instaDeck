import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'
import { useCallback, useEffect, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { DesktopWindow } from './DesktopWindow'
import { Icon } from './Icons'
import { api } from '../api/client'
import { ApiError, formatErrorCode } from '../api/errors'
import { type TerminalWindowItem, useAppStore } from '../store/useAppStore'
import {
  buildTerminalProtocols,
  buildTerminalWsUrl,
  readXtermThemeFromCss,
  sendTerminalInput,
} from './terminalUtils'

type ConnState = 'connecting' | 'connected' | 'disconnected'

function SingleTerminalWindow({ item }: { item: TerminalWindowItem }) {
  const termSize = useAppStore((s) => s.termSize)

  const hostRef = useRef<HTMLDivElement | null>(null)
  const termRef = useRef<Terminal | null>(null)
  const fitRef = useRef<FitAddon | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const minimizedRef = useRef<boolean>(item.minimized)
  const lastSizeRef = useRef<{ cols: number; rows: number }>({ cols: 0, rows: 0 })
  const resizeTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const unmountedRef = useRef<boolean>(false)

  const [connState, setConnState] = useState<ConnState>('connecting')
  const [disconnectReason, setDisconnectReason] = useState<string>('')

  useEffect(() => {
    minimizedRef.current = item.minimized
  }, [item.minimized])

  const scheduleFitAndResize = useCallback((immediate = false) => {
    if (minimizedRef.current) return
    if (resizeTimerRef.current) {
      clearTimeout(resizeTimerRef.current)
    }
    const run = () => {
      if (minimizedRef.current || unmountedRef.current) return
      const term = termRef.current
      const fit = fitRef.current
      if (!term || !fit) return
      try {
        fit.fit()
      } catch {
        // ignore fit errors in headless test env
      }
      const cols = term.cols
      const rows = term.rows
      if (
        cols > 0 &&
        rows > 0 &&
        (cols !== lastSizeRef.current.cols || rows !== lastSizeRef.current.rows)
      ) {
        const ws = wsRef.current
        if (ws && ws.readyState === 1) {
          lastSizeRef.current = { cols, rows }
          ws.send(JSON.stringify({ type: 'resize', cols, rows }))
        }
      }
    }
    if (immediate) {
      run()
    } else {
      resizeTimerRef.current = setTimeout(run, 100)
    }
  }, [])

  const connectSession = useCallback(
    async (isReconnect: boolean) => {
      if (wsRef.current) {
        try {
          wsRef.current.onclose = null
          wsRef.current.onerror = null
          wsRef.current.onmessage = null
          wsRef.current.close()
        } catch {
          // ignore
        }
        wsRef.current = null
      }

      setConnState('connecting')
      setDisconnectReason('')
      lastSizeRef.current = { cols: 0, rows: 0 }

      try {
        const ticketRes = await api.createTerminalTicket(item.machineId, item.credentialId)
        if (unmountedRef.current) return

        const wsUrl = buildTerminalWsUrl()
        const protocols = buildTerminalProtocols(ticketRes.ticket)
        const ws = new WebSocket(wsUrl, protocols)
        ws.binaryType = 'arraybuffer'
        wsRef.current = ws

        if (isReconnect && termRef.current) {
          termRef.current.writeln('\r\n--- 正在重新连接… ---')
        }

        ws.onopen = () => {
          if (unmountedRef.current) return
          setConnState('connected')
          if (!minimizedRef.current) {
            scheduleFitAndResize(false)
          }
        }

        ws.onmessage = (ev: MessageEvent) => {
          if (unmountedRef.current) return
          const term = termRef.current
          if (ev.data instanceof ArrayBuffer) {
            term?.write(new Uint8Array(ev.data))
            return
          }
          if (ev.data instanceof Uint8Array) {
            term?.write(ev.data)
            return
          }
          if (typeof ev.data === 'string') {
            try {
              const ctrl = JSON.parse(ev.data) as {
                type?: string
                exitCode?: number
                code?: string
              }
              if (ctrl.type === 'ready') {
                setConnState('connected')
                if (!minimizedRef.current) {
                  scheduleFitAndResize(true)
                }
              } else if (ctrl.type === 'exit') {
                setDisconnectReason(
                  typeof ctrl.exitCode === 'number'
                    ? `会话已退出（退出码 ${ctrl.exitCode}）`
                    : '会话已结束',
                )
                setConnState('disconnected')
              } else if (ctrl.type === 'error') {
                const msg = formatErrorCode(ctrl.code)
                setDisconnectReason(msg)
                setConnState('disconnected')
              }
            } catch {
              // ignore malformed control frame
            }
          }
        }

        ws.onerror = () => {
          if (unmountedRef.current) return
          setConnState('disconnected')
        }

        ws.onclose = () => {
          if (unmountedRef.current) return
          setConnState('disconnected')
        }
      } catch (err) {
        if (unmountedRef.current) return
        const msg = err instanceof ApiError ? err.message : formatErrorCode('terminal_rejected')
        setDisconnectReason(msg)
        setConnState('disconnected')
      }
    },
    [item.credentialId, item.machineId, scheduleFitAndResize],
  )

  useEffect(() => {
    unmountedRef.current = false
    const host = hostRef.current
    if (!host) return

    const term = new Terminal({
      cursorBlink: true,
      fontSize: useAppStore.getState().termSize,
      fontFamily:
        '"JetBrains Mono Variable", "JetBrains Mono", "SFMono-Regular", Consolas, "Microsoft YaHei", monospace',
      theme: readXtermThemeFromCss(),
    })
    const fitAddon = new FitAddon()
    term.loadAddon(fitAddon)
    term.open(host)

    termRef.current = term
    fitRef.current = fitAddon

    const dataSub = term.onData((data) => {
      sendTerminalInput(wsRef.current, data)
    })

    if (!minimizedRef.current) {
      try {
        fitAddon.fit()
      } catch {
        // ignore in test env
      }
    }

    void connectSession(false)

    return () => {
      unmountedRef.current = true
      if (resizeTimerRef.current) {
        clearTimeout(resizeTimerRef.current)
      }
      dataSub.dispose()
      if (wsRef.current) {
        try {
          wsRef.current.onclose = null
          wsRef.current.onerror = null
          wsRef.current.onmessage = null
          wsRef.current.close()
        } catch {
          // ignore
        }
        wsRef.current = null
      }
      term.dispose()
      termRef.current = null
      fitRef.current = null
    }
  }, [connectSession])

  // The skin state changes before a View Transition applies the attribute, so
  // follow the attribute itself. The system scheme matters for the auto skin.
  useEffect(() => {
    const applyTheme = () => {
      if (termRef.current) {
        termRef.current.options.theme = readXtermThemeFromCss()
      }
    }
    applyTheme()
    const observer = new MutationObserver(applyTheme)
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['data-skin', 'data-theme'],
    })
    const scheme =
      typeof window.matchMedia === 'function'
        ? window.matchMedia('(prefers-color-scheme: dark)')
        : null
    scheme?.addEventListener('change', applyTheme)
    return () => {
      observer.disconnect()
      scheme?.removeEventListener('change', applyTheme)
    }
  }, [])

  // Update xterm fontSize when appearance setting changes
  useEffect(() => {
    if (termRef.current) {
      termRef.current.options.fontSize = termSize
      if (!item.minimized) {
        scheduleFitAndResize(false)
      }
    }
  }, [termSize, item.minimized, scheduleFitAndResize])

  // Fit and send resize when restored from minimized or resized/maximized
  useEffect(() => {
    if (!item.minimized) {
      scheduleFitAndResize(false)
    }
  }, [item.minimized, item.maximized, item.w, item.h, scheduleFitAndResize])

  return (
    <FloatingWindow item={item} status={<span className={`win-state ${connState}`}>{connState === 'connecting' ? '连接中…' : connState === 'disconnected' ? '已断开' : '已连接'}</span>}>
      <div className="win-body">
        <div ref={hostRef} className="xterm-host" />
      </div>
      {connState === 'disconnected' ? (
        <div className="win-disconnect-bar">
          <span>
            已断开{disconnectReason ? `：${disconnectReason}` : ''}
          </span>
          <button
            type="button"
            className="btn"
            style={{ height: '26px', padding: '0 10px', fontSize: '12px' }}
            onClick={() => void connectSession(true)}
          >
            重连
          </button>
        </div>
      ) : null}
    </FloatingWindow>
  )
}

export function FloatingWindow({ item, children, status }: { item: TerminalWindowItem; children: ReactNode; status?: ReactNode }) {
  const focusTerminal = useAppStore((s) => s.focusTerminal)
  const minimizeTerminal = useAppStore((s) => s.minimizeTerminal)
  const toggleMaximizeTerminal = useAppStore((s) => s.toggleMaximizeTerminal)
  const closeTerminal = useAppStore((s) => s.closeTerminal)
  const updateTerminalGeometry = useAppStore((s) => s.updateTerminalGeometry)
  const winRef = useRef<HTMLElement | null>(null)
  const [animClass, setAnimClass] = useState<string>(item.fresh ? 'win-new' : '')
  const handleMinimize = () => {
    setAnimClass('win-min')
    setTimeout(() => {
      setAnimClass('')
      minimizeTerminal(item.id)
    }, 170)
  }

  const handleClose = () => {
    setAnimClass('win-out')
    setTimeout(() => {
      closeTerminal(item.id)
    }, 150)
  }

  const handleBarPointerDown = (e: React.PointerEvent<HTMLDivElement>) => {
    if ((e.target as HTMLElement).closest('button')) return
    focusTerminal(item.id)
    if (item.maximized) return
    e.preventDefault()
    const startX = e.clientX
    const startY = e.clientY
    const origX = item.x
    const origY = item.y
    const w = item.w

    const onMove = (ev: PointerEvent) => {
      const dx = ev.clientX - startX
      const dy = ev.clientY - startY
      const nx = Math.min(window.innerWidth - 80, Math.max(-w + 120, origX + dx))
      const ny = Math.min(window.innerHeight - 40, Math.max(48, origY + dy))
      updateTerminalGeometry(item.id, { x: nx, y: ny })
    }
    const onUp = () => {
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
    }
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
  }

  const handleResizePointerDown = (corner: 'nw' | 'ne' | 'sw' | 'se') => (
    e: React.PointerEvent<HTMLSpanElement>,
  ) => {
    focusTerminal(item.id)
    if (item.maximized) return
    e.preventDefault()
    e.stopPropagation()
    const startX = e.clientX
    const startY = e.clientY
    const orig = { x: item.x, y: item.y, w: item.w, h: item.h }

    const onMove = (ev: PointerEvent) => {
      const dx = ev.clientX - startX
      const dy = ev.clientY - startY
      let { x, y, w, h } = orig
      if (corner.includes('e')) w = Math.max(340, orig.w + dx)
      if (corner.includes('s')) h = Math.max(180, orig.h + dy)
      if (corner.includes('w')) {
        w = Math.max(340, orig.w - dx)
        x = orig.x + orig.w - w
      }
      if (corner.includes('n')) {
        h = Math.max(180, orig.h - dy)
        y = Math.max(48, orig.y + orig.h - h)
      }
      updateTerminalGeometry(item.id, { x, y, w, h })
    }
    const onUp = () => {
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
    }
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
  }

  const geoStyle: React.CSSProperties = item.maximized
    ? {
        left: '8px',
        top: '64px',
        width: 'calc(100vw - 16px)',
        height: 'calc(100% - 72px)',
        zIndex: item.z,
        '--c': item.accountColor,
        display: item.minimized ? 'none' : 'flex',
      } as React.CSSProperties
    : ({
        left: `${item.x}px`,
        top: `${item.y}px`,
        width: `${item.w}px`,
        height: `${item.h}px`,
        zIndex: item.z,
        '--c': item.accountColor,
        display: item.minimized ? 'none' : 'flex',
      } as React.CSSProperties)

  return (
    <section
      ref={winRef}
      className={`win ${item.kind === 'desktop' ? 'desktop-window' : ''} ${animClass}`}
      id={item.id}
      style={geoStyle}
      hidden={item.minimized}
      aria-label={`${item.accountName} ${item.machineName} ${item.kind === 'desktop' ? '桌面' : '终端'}`}
      onPointerDown={() => focusTerminal(item.id)}
    >
      <div className="win-bar" onPointerDown={handleBarPointerDown}>
        <span className="swatch" />
        <span className="who">
          {item.accountName} / {item.machineName} · {item.kind === 'desktop' ? '桌面' : '终端'}
        </span>
        {status}
        <span className="spacer" />
        <button
          type="button"
          data-act="winMin"
          aria-label="最小化"
          title="最小化"
          onClick={handleMinimize}
        >
          –
        </button>
        <button
          type="button"
          data-act="winMax"
          aria-label={item.maximized ? '还原' : '最大化'}
          title={item.maximized ? '还原' : '最大化'}
          onClick={() => toggleMaximizeTerminal(item.id)}
        >
          {item.maximized ? '❐' : '□'}
        </button>
        <button
          type="button"
          data-act="winClose"
          aria-label="关闭"
          title="关闭"
          onClick={handleClose}
        >
          ×
        </button>
      </div>
      {children}
      {!item.maximized
        ? (['nw', 'ne', 'sw', 'se'] as const).map((corner) => (
            <span
              key={corner}
              className={`h ${corner}`}
              onPointerDown={handleResizePointerDown(corner)}
            />
          ))
        : null}
    </section>
  )
}

export function TerminalWindowsLayer() {
  const terminals = useAppStore((s) => s.terminals)
  return (
    <div id="wins">
      {terminals.map((t) => (
        t.kind === 'desktop' ? <DesktopWindow key={t.id} item={t} /> : <SingleTerminalWindow key={t.id} item={t} />
      ))}
    </div>
  )
}

export function TerminalDock() {
  const terminals = useAppStore((s) => s.terminals)
  const focusTerminal = useAppStore((s) => s.focusTerminal)

  return (
    <div className="dock" id="dock">
      <span className="label num">窗口 {terminals.length}/8</span>
      {terminals.length > 0 ? (
        terminals.map((t) => (
          <button
            key={t.id}
            type="button"
            className="btn"
            aria-pressed={!t.minimized}
            onClick={() => focusTerminal(t.id)}
          >
            <span
              className="swatch"
              style={{ '--c': t.accountColor } as React.CSSProperties}
            />
            <Icon name={t.kind === 'desktop' ? 'desktop' : 'term'} />
            {t.accountName} / {t.machineName} · {t.kind === 'desktop' ? '桌面' : '终端'}
            {t.minimized ? <span className="muted">（已最小化）</span> : null}
          </button>
        ))
      ) : (
        <span className="label">在机器行点“终端”可打开窗口，可同时打开多台。</span>
      )}
    </div>
  )
}
