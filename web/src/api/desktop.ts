import type RFB from '@novnc/novnc'
import { apiRequest } from './client'

export type DesktopAction = 'status' | 'install' | 'start'

export type DesktopDTO = {
  installed: boolean
  running: boolean
  installing: boolean
  failed: boolean
  width?: number
  height?: number
}

export type DesktopTicketDTO = {
  ticket: string
}

export type RFBConstructor = typeof RFB

// X is the outline, o the fill.
const ARROW = [
  'X           ',
  'XX          ',
  'XoX         ',
  'XooX        ',
  'XoooX       ',
  'XooooX      ',
  'XoooooX     ',
  'XooooooX    ',
  'XoooooooX   ',
  'XooooooooX  ',
  'XoooooooooX ',
  'XooooooXXXXX',
  'XoooXooX    ',
  'XooXXooX    ',
  'XoX  XooX   ',
  'XX   XooX   ',
  'X     XooX  ',
  '      XooX  ',
  '       XX   ',
]

export function arrowCursor() {
  const w = ARROW[0].length
  const h = ARROW.length
  const rgbaPixels = new Uint8Array(w * h * 4)
  ARROW.forEach((row, y) => {
    for (let x = 0; x < w; x++) {
      const i = (y * w + x) * 4
      const shade = row[x] === 'o' ? 255 : 0
      rgbaPixels[i] = rgbaPixels[i + 1] = rgbaPixels[i + 2] = shade
      rgbaPixels[i + 3] = row[x] === ' ' ? 0 : 255
    }
  })
  return { rgbaPixels, w, h, hotx: 0, hoty: 0 }
}

export const desktopSeam = {
  pollIntervalMs: 3000,
  loadRFB: async (): Promise<RFBConstructor> => {
    const mod = await import('@novnc/novnc')
    // noVNC's fallback for a server that sends no pointer is a dot 3 pixels wide.
    if (mod.default.cursors) mod.default.cursors.dot = arrowCursor()
    return mod.default
  },
}

export function buildDesktopWsUrl(
  loc: { protocol: string; host: string } = typeof window !== 'undefined'
    ? window.location
    : { protocol: 'http:', host: 'localhost' },
): string {
  const wsProto = loc.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${wsProto}//${loc.host}/api/v1/desktop/ws`
}

export function buildDesktopProtocols(ticket: string): string[] {
  return ['instadeck.desktop.v1', `instadeck.ticket.${ticket}`]
}

export const desktopApi = {
  action: (
    machineId: string,
    body: {
      credentialId: string
      action: DesktopAction
      width?: number
      height?: number
    },
  ) =>
    apiRequest<DesktopDTO>(`/machines/${encodeURIComponent(machineId)}/desktop`, {
      method: 'POST',
      body,
    }),

  status: (machineId: string, credentialId: string) =>
    desktopApi.action(machineId, { credentialId, action: 'status' }),

  install: (
    machineId: string,
    credentialId: string,
    size?: { width: number; height: number },
  ) =>
    desktopApi.action(machineId, {
      credentialId,
      action: 'install',
      ...size,
    }),

  start: (
    machineId: string,
    credentialId: string,
    size?: { width: number; height: number },
  ) =>
    desktopApi.action(machineId, {
      credentialId,
      action: 'start',
      ...size,
    }),

  createTicket: (machineId: string, credentialId: string) =>
    apiRequest<DesktopTicketDTO>(
      `/machines/${encodeURIComponent(machineId)}/desktop/tickets`,
      {
        method: 'POST',
        body: { credentialId },
      },
    ),
}
