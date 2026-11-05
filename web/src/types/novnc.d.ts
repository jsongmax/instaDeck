declare module '@novnc/novnc' {
  export interface RFBOptions {
    shared?: boolean
    credentials?: {
      username?: string
      password?: string
      target?: string
    }
    repeaterID?: string
    wsProtocols?: string[]
  }

  export interface RFBDisconnectDetail {
    clean: boolean
  }

  export interface RFBSecurityFailureDetail {
    status: number
    reason?: string
  }

  export interface RFBEventMap {
    connect: CustomEvent<undefined>
    disconnect: CustomEvent<RFBDisconnectDetail>
    securityfailure: CustomEvent<RFBSecurityFailureDetail>
  }

  export default class RFB extends EventTarget {
    static cursors?: { dot: { rgbaPixels: Uint8Array; w: number; h: number; hotx: number; hoty: number } }
    constructor(
      target: HTMLElement,
      urlOrChannel: string | WebSocket | RTCDataChannel,
      options?: RFBOptions,
    )

    scaleViewport: boolean
    showDotCursor: boolean
    resizeSession: boolean
    viewOnly: boolean
    background: string
    clipViewport: boolean
    dragViewport: boolean
    focusOnClick: boolean
    qualityLevel: number
    compressionLevel: number

    disconnect(): void
    sendCtrlAltDel(): void
    focus(options?: FocusOptions): void
    blur(): void

    addEventListener<K extends keyof RFBEventMap>(
      type: K,
      listener: (event: RFBEventMap[K]) => void,
      options?: boolean | AddEventListenerOptions,
    ): void
    addEventListener(
      type: string,
      listener: EventListenerOrEventListenerObject | null,
      options?: boolean | AddEventListenerOptions,
    ): void
    removeEventListener<K extends keyof RFBEventMap>(
      type: K,
      listener: (event: RFBEventMap[K]) => void,
      options?: boolean | EventListenerOptions,
    ): void
    removeEventListener(
      type: string,
      listener: EventListenerOrEventListenerObject | null,
      options?: boolean | EventListenerOptions,
    ): void
  }
}
