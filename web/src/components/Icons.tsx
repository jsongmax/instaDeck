import type { ReactNode } from 'react'
import { getMachineStateMeta } from '../api/errors'

export type IconName =
  | 'play'
  | 'pause'
  | 'stop'
  | 'term'
  | 'grid'
  | 'key'
  | 'list'
  | 'plus'
  | 'shield'
  | 'palette'

export function Icon({ name }: { name: IconName }) {
  let content: ReactNode = null
  switch (name) {
    case 'play':
      content = <path d="M4.5 2.8v10.4L13 8z" fill="currentColor" />
      break
    case 'pause':
      content = (
        <>
          <rect x="3.5" y="2.8" width="3.2" height="10.4" rx="1" fill="currentColor" />
          <rect x="9.3" y="2.8" width="3.2" height="10.4" rx="1" fill="currentColor" />
        </>
      )
      break
    case 'stop':
      content = <rect x="3.2" y="3.2" width="9.6" height="9.6" rx="2" fill="currentColor" />
      break
    case 'term':
      content = (
        <>
          <rect
            x="1.5"
            y="2.5"
            width="13"
            height="11"
            rx="2.2"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.4"
          />
          <path
            d="m4.4 6 2.2 2-2.2 2M8.2 10.4h3.2"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.4"
            strokeLinecap="round"
            strokeLinejoin="round"
          />
        </>
      )
      break
    case 'grid':
      content = (
        <>
          <rect x="2" y="2" width="5" height="5" rx="1.3" fill="currentColor" />
          <rect x="9" y="2" width="5" height="5" rx="1.3" fill="currentColor" />
          <rect x="2" y="9" width="5" height="5" rx="1.3" fill="currentColor" />
          <rect x="9" y="9" width="5" height="5" rx="1.3" fill="currentColor" />
        </>
      )
      break
    case 'key':
      content = (
        <>
          <circle
            cx="5.5"
            cy="10.5"
            r="3.2"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.4"
          />
          <path
            d="m8 8 5.5-5.5M11 5l2 2"
            stroke="currentColor"
            strokeWidth="1.4"
            strokeLinecap="round"
          />
        </>
      )
      break
    case 'list':
      content = (
        <>
          <path
            d="M5.5 4h8.5M5.5 8h8.5M5.5 12h8.5"
            stroke="currentColor"
            strokeWidth="1.4"
            strokeLinecap="round"
          />
          <circle cx="2.4" cy="4" r="1" fill="currentColor" />
          <circle cx="2.4" cy="8" r="1" fill="currentColor" />
          <circle cx="2.4" cy="12" r="1" fill="currentColor" />
        </>
      )
      break
    case 'plus':
      content = (
        <path
          d="M8 3v10M3 8h10"
          stroke="currentColor"
          strokeWidth="1.7"
          strokeLinecap="round"
        />
      )
      break
    case 'shield':
      content = (
        <>
          <path
            d="M8 1.8 13.2 3.6v4c0 3.2-2.1 5.4-5.2 6.6-3.1-1.2-5.2-3.4-5.2-6.6v-4z"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.4"
            strokeLinejoin="round"
          />
          <path
            d="m5.8 8 1.6 1.6 2.9-3"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.4"
            strokeLinecap="round"
            strokeLinejoin="round"
          />
        </>
      )
      break
    case 'palette':
      content = (
        <>
          <path
            d="M8 1.8a6.2 6.2 0 1 0 0 12.4c1 0 1.5-.7 1.5-1.4 0-.9-.7-1.2-.7-2 0-.8.6-1.3 1.5-1.3h1.6c1.4 0 2.3-1 2.3-2.4 0-2.8-2.8-5.3-6.2-5.3z"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.4"
            strokeLinejoin="round"
          />
          <circle cx="5" cy="7" r="1" fill="currentColor" />
          <circle cx="8" cy="5" r="1" fill="currentColor" />
          <circle cx="11" cy="6.6" r="1" fill="currentColor" />
        </>
      )
      break
  }

  return (
    <svg className="ic" width="15" height="15" viewBox="0 0 16 16" aria-hidden="true">
      {content}
    </svg>
  )
}

export function ErrorIcon() {
  return (
    <svg width="12" height="11" viewBox="0 0 12 11" aria-hidden="true">
      <path d="M6 .5 11.6 10.5H.4Z" fill="currentColor" />
    </svg>
  )
}

export function BusySpinnerIcon() {
  return (
    <svg className="spin" width="12" height="12" viewBox="0 0 12 12" aria-hidden="true">
      <circle
        cx="6"
        cy="6"
        r="4.6"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.8"
        strokeDasharray="18 10"
      />
    </svg>
  )
}

export function RunningCircleIcon() {
  return (
    <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true">
      <circle cx="5" cy="5" r="4.5" fill="currentColor" />
    </svg>
  )
}

export function StoppedCircleIcon() {
  return (
    <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true">
      <circle cx="5" cy="5" r="4" fill="none" stroke="currentColor" strokeWidth="1.6" />
    </svg>
  )
}

export function SuspendedBarsIcon() {
  return (
    <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true">
      <rect x="1.5" y="1" width="2.5" height="8" fill="currentColor" />
      <rect x="6" y="1" width="2.5" height="8" fill="currentColor" />
    </svg>
  )
}

export function LogoSvg({ size = 24 }: { size?: number }) {
  return (
    <svg className="logo" width={size} height={size} viewBox="0 0 32 32" aria-hidden="true">
      <rect x="4" y="5" width="24" height="6" rx="2" />
      <rect x="4" y="13" width="24" height="6" rx="2" />
      <rect x="4" y="21" width="24" height="6" rx="2" />
      <circle cx="9" cy="8" r="1.4" />
      <circle cx="9" cy="16" r="1.4" />
      <circle cx="9" cy="24" r="1.4" />
    </svg>
  )
}

export function Swatch({ color }: { color: string }) {
  return <span className="swatch" style={{ '--c': color } as React.CSSProperties} />
}

export function MachineStateBadge({
  state,
  phase,
  forceBusy = false,
}: {
  state: string
  phase?: string
  forceBusy?: boolean
}) {
  const meta = getMachineStateMeta(state)
  const isBusy = forceBusy || meta.isBusy
  const color = isBusy ? 'var(--busy)' : meta.color
  const icon = isBusy ? 'busy' : meta.icon

  return (
    <>
      <span className="state" style={{ '--c': color } as React.CSSProperties}>
        {icon === 'busy' ? (
          <BusySpinnerIcon />
        ) : (
          <span className={`led led-${icon}`}>
            {icon === 'error' ? (
              <ErrorIcon />
            ) : icon === 'suspended' ? (
              <SuspendedBarsIcon />
            ) : null}
          </span>
        )}
        {meta.label}
      </span>
      {isBusy && phase ? <span className="phase">{phase}</span> : null}
    </>
  )
}

