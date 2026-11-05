import type RFB from '@novnc/novnc'
import { type RefObject, useCallback, useEffect, useRef, useState } from 'react'
import {
  type DesktopDTO,
  buildDesktopProtocols,
  buildDesktopWsUrl,
  desktopApi,
  desktopSeam,
} from '../api/desktop'
import { ApiError, formatErrorCode } from '../api/errors'
import { useAppStore } from '../store/useAppStore'
import type { TerminalWindowItem } from '../store/useAppStore'
import { FloatingWindow } from './TerminalWindow'
import { BusySpinnerIcon, Icon } from './Icons'

type ConnState = 'idle' | 'connecting' | 'connected' | 'disconnected'

function isFullscreenSupported(el: HTMLElement | null): boolean {
  if (typeof document === 'undefined') return false
  if (document.fullscreenEnabled === false) return false
  const req =
    el?.requestFullscreen ??
    (typeof HTMLElement !== 'undefined' ? HTMLElement.prototype.requestFullscreen : undefined)
  return typeof req === 'function'
}

export function DesktopWindow({ item }: { item: TerminalWindowItem }) {
  const { machineId, machineName, credentialId } = item
  const closeTerminal = useAppStore((s) => s.closeTerminal)
  const rootRef = useRef<HTMLDivElement | null>(null)
  return (
    <FloatingWindow item={item}>
      <DesktopContent
        item={item}
        machineId={machineId}
        machineName={machineName}
        credentialId={credentialId}
        rootRef={rootRef}
        onClose={() => closeTerminal(item.id)}
      />
    </FloatingWindow>
  )
}

export function DesktopContent({ machineId, machineName, credentialId, onClose, item, rootRef }: {
  machineId: string
  machineName: string
  credentialId: string
  onClose: () => void
  item?: TerminalWindowItem
  rootRef?: RefObject<HTMLDivElement | null>
}) {
  const authRequired = useAppStore((s) => s.authRequired)

  const localRootRef = useRef<HTMLDivElement | null>(null)
  const contentRef = rootRef ?? localRootRef
  const screenRef = useRef<HTMLDivElement | null>(null)
  const rfbRef = useRef<RFB | null>(null)
  const minimizedRef = useRef(item?.minimized ?? false)
  useEffect(() => { minimizedRef.current = item?.minimized ?? false }, [item?.minimized])
  const closingRfbRef = useRef<RFB | null>(null)
  const unmountedRef = useRef(false)
  const userClosingRef = useRef(false)
  const inFlightRef = useRef(false)
  const actionBusyRef = useRef(false)
  const connSeqRef = useRef(0)

  const [loadingStatus, setLoadingStatus] = useState(true)
  const [desktop, setDesktop] = useState<DesktopDTO | null>(null)
  const [statusError, setStatusError] = useState('')
  const [actionBusy, setActionBusy] = useState<'install' | 'start' | null>(null)
  const [actionError, setActionError] = useState('')
  const [connState, setConnState] = useState<ConnState>('idle')
  const [hasConnectedOnce, setHasConnectedOnce] = useState(false)
  const [disconnectInfo, setDisconnectInfo] = useState<{
    message: string
    isError: boolean
  } | null>(null)
  const [fullscreenAvailable, setFullscreenAvailable] = useState(() =>
    isFullscreenSupported(null),
  )
  const [isFullscreen, setIsFullscreen] = useState(false)

  const cleanupRfb = useCallback(() => {
    connSeqRef.current += 1
    inFlightRef.current = false
    const current = rfbRef.current
    if (current) {
      closingRfbRef.current = current
      rfbRef.current = null
      try {
        current.disconnect()
      } catch {
        // ignore disconnect errors during teardown
      }
    }
  }, [])

  const connectVnc = useCallback(async () => {
    if (inFlightRef.current || unmountedRef.current || userClosingRef.current) {
      return
    }
    inFlightRef.current = true
    const seq = ++connSeqRef.current

    if (rfbRef.current) {
      const prev = rfbRef.current
      closingRfbRef.current = prev
      rfbRef.current = null
      try {
        prev.disconnect()
      } catch {
        // ignore
      }
    }

    setConnState('connecting')
    setDisconnectInfo(null)
    setActionError('')

    try {
      // Loaded first: a ticket lasts 30 seconds, and the first download could take longer.
      const RFBClass = await desktopSeam.loadRFB()
      if (unmountedRef.current || userClosingRef.current || seq !== connSeqRef.current) {
        inFlightRef.current = false
        return
      }
      const ticketRes = await desktopApi.createTicket(machineId, credentialId)

      if (
        unmountedRef.current ||
        userClosingRef.current ||
        seq !== connSeqRef.current ||
        !screenRef.current
      ) {
        inFlightRef.current = false
        return
      }

      const wsUrl = buildDesktopWsUrl()
      const wsProtocols = buildDesktopProtocols(ticketRes.ticket)
      const rfb = new RFBClass(screenRef.current, wsUrl, { wsProtocols })
      rfb.scaleViewport = true
      // noVNC hides the local pointer and draws the server's. A server that sends none, or a
      // transparent one, would leave no pointer at all, so fall back to a dot.
      rfb.showDotCursor = true
      rfb.resizeSession = false
      rfbRef.current = rfb

      const security = { reason: '' }

      rfb.addEventListener('connect', () => {
        if (unmountedRef.current || userClosingRef.current || rfbRef.current !== rfb) {
          return
        }
        inFlightRef.current = false
        setHasConnectedOnce(true)
        setConnState('connected')
        setDisconnectInfo(null)
        try {
          if (!minimizedRef.current && !useAppStore.getState().authRequired) rfb.focus()
        } catch {
          // ignore focus errors in test environments
        }
      })

      rfb.addEventListener('securityfailure', (ev) => {
        if (unmountedRef.current || userClosingRef.current || rfbRef.current !== rfb) {
          return
        }
        security.reason = ev.detail?.reason
          ? '安全握手失败，连接已拒绝'
          : '安全验证失败，连接已断开'
      })

      rfb.addEventListener('disconnect', (ev) => {
        if (rfbRef.current === rfb) {
          rfbRef.current = null
        }
        inFlightRef.current = false
        if (
          unmountedRef.current ||
          userClosingRef.current ||
          closingRfbRef.current === rfb
        ) {
          if (closingRfbRef.current === rfb) {
            closingRfbRef.current = null
          }
          return
        }
        setConnState('disconnected')
        setDisconnectInfo({
          message:
            security.reason ||
            (ev.detail?.clean
              ? '连接已断开，可能是桌面已停止或网络中断。'
              : '无法连接到远程桌面。'),
          isError: true,
        })
        // The desktop may have stopped: then the way back is starting it, not reconnecting.
        void desktopApi
          .status(machineId, credentialId)
          .then((next) => {
            if (!unmountedRef.current && !userClosingRef.current) setDesktop(next)
          })
          .catch(() => {})
      })
    } catch (err) {
      inFlightRef.current = false
      if (unmountedRef.current || userClosingRef.current || seq !== connSeqRef.current) {
        return
      }
      const message =
        err instanceof ApiError ? err.message : formatErrorCode('desktop_unavailable')
      setConnState('disconnected')
      setDisconnectInfo({ message, isError: true })
      void desktopApi
        .status(machineId, credentialId)
        .then((next) => {
          if (!unmountedRef.current && !userClosingRef.current) setDesktop(next)
        })
        .catch(() => {})
    }
  }, [credentialId, machineId])

  const fetchInitialStatus = useCallback(async () => {
    setLoadingStatus(true)
    setStatusError('')
    setActionError('')
    try {
      const dto = await desktopApi.status(machineId, credentialId)
      if (unmountedRef.current || userClosingRef.current) return
      setDesktop(dto)
      setLoadingStatus(false)
      if (dto.running) {
        void connectVnc()
      }
    } catch (err) {
      if (unmountedRef.current || userClosingRef.current) return
      setLoadingStatus(false)
      setStatusError(
        err instanceof ApiError ? err.message : formatErrorCode('desktop_unavailable'),
      )
    }
  }, [connectVnc, credentialId, machineId])

  useEffect(() => {
    unmountedRef.current = false
    userClosingRef.current = false
    setFullscreenAvailable(isFullscreenSupported(contentRef.current))
    void fetchInitialStatus()
    const fullscreenTarget = contentRef.current?.closest('.win') ?? contentRef.current

    return () => {
      unmountedRef.current = true
      userClosingRef.current = true
      cleanupRfb()
      if (document.fullscreenElement === fullscreenTarget) void document.exitFullscreen?.().catch(() => {})
    }
  }, [cleanupRfb, fetchInitialStatus, contentRef])

  // Poll status every 3s while installing; stop on success, failure, error, authRequired, or unmount.
  useEffect(() => {
    if (!desktop?.installing || statusError || authRequired) return
    const poll = { cancelled: false }
    const timer = setTimeout(() => {
      void (async () => {
        try {
          const next = await desktopApi.status(machineId, credentialId)
          if (poll.cancelled || unmountedRef.current || userClosingRef.current) return
          setDesktop(next)
          if (next.running) {
            void connectVnc()
          }
        } catch (err) {
          if (poll.cancelled || unmountedRef.current || userClosingRef.current) return
          setStatusError(
            err instanceof ApiError ? err.message : formatErrorCode('desktop_unavailable'),
          )
        }
      })()
    }, desktopSeam.pollIntervalMs)

    return () => {
      poll.cancelled = true
      clearTimeout(timer)
    }
  }, [authRequired, connectVnc, credentialId, desktop, machineId, statusError])

  useEffect(() => {
    const onFullscreenChange = () => {
      setIsFullscreen(document.fullscreenElement === (contentRef.current?.closest('.win') ?? contentRef.current))
    }
    document.addEventListener('fullscreenchange', onFullscreenChange)
    return () => {
      document.removeEventListener('fullscreenchange', onFullscreenChange)
    }
  }, [contentRef])

  useEffect(() => {
    if (authRequired && document.fullscreenElement === contentRef.current?.closest('.win')) {
      void document.exitFullscreen?.().catch(() => {})
    }
  }, [authRequired, contentRef])

  const handleExplicitClose = useCallback(() => {
    userClosingRef.current = true
    cleanupRfb()
    if (typeof document !== 'undefined' && document.fullscreenElement) {
      void document.exitFullscreen?.().catch(() => {})
    }
    onClose()
  }, [cleanupRfb, onClose])

  useEffect(() => {
    if (item?.minimized || authRequired) return
    const frame = requestAnimationFrame(() => {
      const rfb = rfbRef.current
      if (!rfb) return
      rfb.scaleViewport = false
      rfb.scaleViewport = true
      rfb.focus()
    })
    return () => cancelAnimationFrame(frame)
  }, [item?.minimized, item?.maximized, item?.w, item?.h, item?.z, authRequired])

  const handleInstall = async () => {
    if (actionBusyRef.current) return
    actionBusyRef.current = true
    setActionBusy('install')
    setActionError('')
    setStatusError('')
    try {
      const next = await desktopApi.install(machineId, credentialId)
      if (unmountedRef.current || userClosingRef.current) return
      setDesktop(next)
      if (next.running) {
        void connectVnc()
      }
    } catch (err) {
      if (unmountedRef.current || userClosingRef.current) return
      setActionError(
        err instanceof ApiError ? err.message : formatErrorCode('desktop_unavailable'),
      )
    } finally {
      actionBusyRef.current = false
      if (!unmountedRef.current) setActionBusy(null)
    }
  }

  const handleStart = async () => {
    if (actionBusyRef.current || inFlightRef.current) return
    actionBusyRef.current = true
    setActionBusy('start')
    setActionError('')
    setStatusError('')
    try {
      const next = await desktopApi.start(machineId, credentialId)
      if (unmountedRef.current || userClosingRef.current) return
      setDesktop(next)
      if (next.running) {
        void connectVnc()
      }
    } catch (err) {
      if (unmountedRef.current || userClosingRef.current) return
      setActionError(
        err instanceof ApiError ? err.message : formatErrorCode('desktop_unavailable'),
      )
    } finally {
      actionBusyRef.current = false
      if (!unmountedRef.current) setActionBusy(null)
    }
  }

  const handleSendCtrlAltDel = () => {
    if (connState !== 'connected' || !rfbRef.current) return
    rfbRef.current.sendCtrlAltDel()
    try {
      rfbRef.current.focus()
    } catch {
      // ignore
    }
  }

  const handleToggleFullscreen = async () => {
    const target = contentRef.current?.closest<HTMLElement>('.win') ?? contentRef.current
    if (!target || !isFullscreenSupported(target)) return
    try {
      if (document.fullscreenElement) {
        await document.exitFullscreen?.()
      } else {
        await target.requestFullscreen()
      }
    } catch {
      // Ignore fullscreen rejection from browser permissions
    }
    // The click left focus on this button, where keys would not reach the desktop.
    try {
      rfbRef.current?.focus()
    } catch {
      // ignore
    }
  }

  let badgeLabel = '查询状态中…'
  let badgeTone = 'busy'
  if (loadingStatus) {
    badgeLabel = '正在查询状态…'
    badgeTone = 'busy'
  } else if (statusError) {
    badgeLabel = '状态查询失败'
    badgeTone = 'bad'
  } else if (desktop?.installing) {
    badgeLabel = '正在安装…'
    badgeTone = 'busy'
  } else if (connState === 'connected') {
    badgeLabel = '已连接'
    badgeTone = 'ok'
  } else if (connState === 'connecting') {
    badgeLabel = '连接中…'
    badgeTone = 'busy'
  } else if (connState === 'disconnected') {
    badgeLabel = '已断开'
    badgeTone = disconnectInfo?.isError ? 'bad' : ''
  } else if (desktop?.failed) {
    badgeLabel = '桌面异常'
    badgeTone = 'bad'
  } else if (desktop && !desktop.installed) {
    badgeLabel = '未安装'
    badgeTone = ''
  } else if (desktop && !desktop.running) {
    badgeLabel = '未运行'
    badgeTone = ''
  }

  const canConnect =
    Boolean(desktop?.running) &&
    connState !== 'connecting' &&
    connState !== 'connected'
  const connectBtnLabel =
    hasConnectedOnce || connState === 'disconnected' ? '重新连接' : '连接'
  const showOverlay = connState !== 'connected'

  return (
    <div ref={contentRef} className="desktop-content" onClick={() => rfbRef.current?.focus()}>
      <div className="desktop-head">
        <div className="desktop-title">
          <h2>
            <Icon name="desktop" /> 远程桌面 · {machineName}
          </h2>
          <span className={`desktop-badge ${badgeTone}`}>{badgeLabel}</span>
        </div>

        <div className="desktop-toolbar">
          <button
            type="button"
            className="btn"
            disabled={!canConnect}
            onClick={() => void connectVnc()}
          >
            {connectBtnLabel}
          </button>
          <button
            type="button"
            className="btn"
            disabled={connState !== 'connected'}
            onClick={handleSendCtrlAltDel}
          >
            发送 Ctrl+Alt+Del
          </button>
          {fullscreenAvailable ? (
            <button
              type="button"
              className="btn"
              onClick={() => void handleToggleFullscreen()}
            >
              {isFullscreen ? '退出全屏' : '全屏'}
            </button>
          ) : null}
          <button type="button" className="btn" onClick={handleExplicitClose}>
            关闭
          </button>
        </div>
      </div>

      <div className="desktop-stage">
        <div
          ref={screenRef}
          className="desktop-screen"
          data-testid="desktop-screen"
          tabIndex={-1}
          onClick={() => {
            try {
              rfbRef.current?.focus()
            } catch {
              // ignore
            }
          }}
        />

        {showOverlay ? (
          <div className="desktop-overlay">
            <div className="desktop-card">
              {loadingStatus ? (
                <>
                  <h3>
                    <BusySpinnerIcon /> 正在查询桌面状态…
                  </h3>
                  <p className="muted">正在检查远程桌面环境是否已安装并运行。</p>
                </>
              ) : statusError ? (
                <>
                  <h3>无法获取桌面状态</h3>
                  <p className="errline">{statusError}</p>
                  <div className="btn-row">
                    <button
                      type="button"
                      className="btn primary"
                      onClick={() => void fetchInitialStatus()}
                    >
                      重试
                    </button>
                  </div>
                </>
              ) : desktop?.installing ? (
                <>
                  <h3>
                    <BusySpinnerIcon /> 正在安装桌面环境…
                  </h3>
                  <p className="muted">
                    后台安装约需半分钟，占用根磁盘约 1.4 GiB 空间，每 3 秒自动刷新进度。
                  </p>
                </>
              ) : desktop && !desktop.installed ? (
                <>
                  <h3>尚未安装远程桌面</h3>
                  <p className="muted">
                    首次安装约需半分钟，将占用根磁盘约 1.4 GiB 空间。安装完成后可随时启动桌面。
                  </p>
                  {desktop.failed ? (
                    <p className="errline">上次安装远程桌面失败，请重试。</p>
                  ) : null}
                  {actionError ? <p className="errline">{actionError}</p> : null}
                  <div className="btn-row">
                    <button
                      type="button"
                      className={`btn primary ${actionBusy === 'install' ? 'loading' : ''}`}
                      disabled={actionBusy !== null}
                      onClick={() => void handleInstall()}
                    >
                      安装桌面
                    </button>
                  </div>
                </>
              ) : desktop && !desktop.running ? (
                <>
                  <h3>远程桌面未运行</h3>
                  <p className="muted">桌面环境已安装就绪，点击下方按钮启动并连接。</p>
                  {desktop.failed ? (
                    <p className="errline">远程桌面启动或运行失败，请重试。</p>
                  ) : null}
                  {actionError ? <p className="errline">{actionError}</p> : null}
                  <div className="btn-row">
                    <button
                      type="button"
                      className={`btn primary ${actionBusy === 'start' ? 'loading' : ''}`}
                      disabled={actionBusy !== null}
                      onClick={() => void handleStart()}
                    >
                      启动桌面
                    </button>
                    {desktop.failed || actionError ? (
                      <button
                        type="button"
                        className="btn"
                        disabled={actionBusy !== null}
                        onClick={() => void handleInstall()}
                      >
                        重新安装
                      </button>
                    ) : null}
                  </div>
                </>
              ) : connState === 'connecting' ? (
                <>
                  <h3>
                    <BusySpinnerIcon /> 正在连接远程桌面…
                  </h3>
                  <p className="muted">正在建立连接，请稍候。</p>
                </>
              ) : connState === 'disconnected' ? (
                <>
                  <h3>远程桌面已断开</h3>
                  {disconnectInfo ? (
                    <p className={disconnectInfo.isError ? 'errline' : 'muted'}>
                      {disconnectInfo.message}
                    </p>
                  ) : null}
                  <div className="btn-row">
                    <button
                      type="button"
                      className="btn primary"
                      onClick={() => void connectVnc()}
                    >
                      重新连接
                    </button>
                    <button
                      type="button"
                      className={`btn ${actionBusy === 'start' ? 'loading' : ''}`}
                      disabled={actionBusy !== null}
                      onClick={() => void handleStart()}
                    >
                      启动桌面
                    </button>
                    <button
                      type="button"
                      className="btn"
                      disabled={actionBusy !== null}
                      onClick={() => void handleInstall()}
                    >
                      重新安装
                    </button>
                  </div>
                  {actionError ? <p className="errline">{actionError}</p> : null}
                </>
              ) : (
                <>
                  <h3>远程桌面已就绪</h3>
                  <div className="btn-row">
                    <button
                      type="button"
                      className="btn primary"
                      onClick={() => void connectVnc()}
                    >
                      连接
                    </button>
                  </div>
                </>
              )}
            </div>
          </div>
        ) : null}
      </div>
    </div>
  )
}
