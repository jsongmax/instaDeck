import type { ITheme } from '@xterm/xterm'

export const MAX_TERMINAL_FRAME_BYTES = 16 * 1024

const SKIN_TERM_VARS: Record<
  string,
  {
    bg: string
    ink: string
    dim: string
    green: string
    blue: string
    yellow: string
    red: string
    cyan: string
    magenta: string
  }
> = {
  light: {
    bg: '#f7f9fc',
    ink: '#1b2438',
    dim: '#68748c',
    green: '#0b7f50',
    blue: '#1f5fd6',
    yellow: '#8f5a00',
    red: '#cf2e2e',
    cyan: '#06a6c9',
    magenta: '#c02e8f',
  },
  graphite: {
    bg: '#161618',
    ink: '#e4e4e7',
    dim: '#8b8b93',
    green: '#6fd6a3',
    blue: '#93b7f7',
    yellow: '#e5c778',
    red: '#f4827b',
    cyan: '#5ccbd6',
    magenta: '#ec89c6',
  },
  nord: {
    bg: '#272c36',
    ink: '#e5e9f0',
    dim: '#8f99ab',
    green: '#a3be8c',
    blue: '#81a1c1',
    yellow: '#ebcb8b',
    red: '#e88d96',
    cyan: '#88c0d0',
    magenta: '#b9a2d8',
  },
  forest: {
    bg: '#121a16',
    ink: '#dfe9e2',
    dim: '#87968c',
    green: '#7fdba9',
    blue: '#8cc4d8',
    yellow: '#e3c47f',
    red: '#ef8476',
    cyan: '#68c4cc',
    magenta: '#e692bd',
  },
  plum: {
    bg: '#19131f',
    ink: '#ebe2f0',
    dim: '#92859e',
    green: '#79dcaf',
    blue: '#b2adff',
    yellow: '#eec978',
    red: '#ff8585',
    cyan: '#5fc9d4',
    magenta: '#e79be0',
  },
  sky: {
    bg: '#f7f9fc',
    ink: '#1b2438',
    dim: '#68748c',
    green: '#0b7f50',
    blue: '#1f5fd6',
    yellow: '#8f5a00',
    red: '#cf2e2e',
    cyan: '#06a6c9',
    magenta: '#c02e8f',
  },
  paper: {
    bg: '#fbf8f1',
    ink: '#2f281f',
    dim: '#857a6b',
    green: '#2c7f4f',
    blue: '#23669e',
    yellow: '#96600e',
    red: '#bb3a2c',
    cyan: '#0f7f8a',
    magenta: '#a93a86',
  },
}

export function chunkTerminalInput(input: string | Uint8Array): Uint8Array<ArrayBuffer>[] {
  const bytes = typeof input === 'string' ? new TextEncoder().encode(input) : input
  if (bytes.byteLength === 0) return []
  const chunks: Uint8Array<ArrayBuffer>[] = []
  for (let offset = 0; offset < bytes.byteLength; offset += MAX_TERMINAL_FRAME_BYTES) {
    const slice = bytes.slice(offset, Math.min(bytes.byteLength, offset + MAX_TERMINAL_FRAME_BYTES))
    chunks.push(slice)
  }
  return chunks
}

export function sendTerminalInput(
  ws: { readyState: number; send: (data: Uint8Array<ArrayBuffer>) => void } | null,
  input: string | Uint8Array,
): number {
  if (!ws || ws.readyState !== 1) return 0
  const chunks = chunkTerminalInput(input)
  for (const chunk of chunks) {
    ws.send(chunk)
  }
  return chunks.length
}

export function buildTerminalWsUrl(
  loc: { protocol: string; host: string } = typeof window !== 'undefined'
    ? window.location
    : { protocol: 'http:', host: 'localhost' },
): string {
  const wsProto = loc.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${wsProto}//${loc.host}/api/v1/terminal/ws`
}

export function buildTerminalProtocols(ticket: string): string[] {
  return ['instadeck.terminal.v1', `instadeck.ticket.${ticket}`]
}

export function readXtermThemeFromCss(
  root: HTMLElement = document.documentElement,
): ITheme {
  const style = getComputedStyle(root)
  const skinKey = root.dataset.skin || root.dataset.theme || 'light'
  const fallback = SKIN_TERM_VARS[skinKey] ?? SKIN_TERM_VARS.light

  const getVar = (name: string, fb: string) => {
    const val = style.getPropertyValue(name).trim()
    return val || fb
  }

  const bg = getVar('--term-bg', fallback.bg)
  const ink = getVar('--term-ink', fallback.ink)
  const dim = getVar('--term-dim', fallback.dim)
  const green = getVar('--term-green', fallback.green)
  const blue = getVar('--term-blue', fallback.blue)
  const yellow = getVar('--term-yellow', fallback.yellow)
  const red = getVar('--bad', fallback.red)
  const cyan = getVar('--a2', fallback.cyan)
  const magenta = getVar('--a4', fallback.magenta)

  return {
    background: bg,
    foreground: ink,
    cursor: green,
    cursorAccent: bg,
    selectionBackground: 'rgba(128, 150, 200, 0.28)',
    black: bg,
    red,
    green,
    yellow,
    blue,
    magenta,
    cyan,
    white: ink,
    brightBlack: dim,
    brightRed: red,
    brightGreen: green,
    brightYellow: yellow,
    brightBlue: blue,
    brightMagenta: magenta,
    brightCyan: cyan,
    brightWhite: ink,
  }
}
