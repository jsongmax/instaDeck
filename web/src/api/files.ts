import { useAppStore } from '../store/useAppStore'
import { apiRequest } from './client'
import { ApiError, formatErrorCode } from './errors'
import type { SessionInfo } from './types'

export type FileEntry = {
  name: string
  type: 'dir' | 'file' | 'symlink' | 'other'
  size: number
  mode: string
  modTime: string
  target?: string
}

export type FilesDTO = {
  path: string
  type: 'dir' | 'file' | 'other'
  size: number
  mode: string
  modTime: string
  entries?: FileEntry[]
  content?: string
  binary?: boolean
  truncated?: boolean
}

export const FILE_CHUNK_SIZE = 64 * 1024 * 1024
export const FILE_MAX_UPLOAD_SIZE = 20 * 1024 * 1024 * 1024
export const FILE_PART_MAX_RETRIES = 3

const utf8Encoder = new TextEncoder()

function hasLoneSurrogate(value: string): boolean {
  for (let i = 0; i < value.length; i++) {
    const code = value.charCodeAt(i)
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = i + 1 < value.length ? value.charCodeAt(i + 1) : 0
      if (next < 0xdc00 || next > 0xdfff) return true
      i++
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      return true
    }
  }
  return false
}

function hasControlChar(value: string): boolean {
  for (let i = 0; i < value.length; i++) {
    const code = value.charCodeAt(i)
    if (code <= 0x1f || code === 0x7f) return true
  }
  return false
}

export function validateFilePath(path: string): string | null {
  if (!path || !path.startsWith('/')) {
    return formatErrorCode('file_path_invalid')
  }
  if (hasLoneSurrogate(path) || hasControlChar(path)) {
    return formatErrorCode('file_path_invalid')
  }
  if (utf8Encoder.encode(path).byteLength > 4096) {
    return formatErrorCode('file_path_invalid')
  }
  if (path === '/') return null
  if (path.endsWith('/')) {
    return formatErrorCode('file_path_invalid')
  }
  const segments = path.slice(1).split('/')
  for (const seg of segments) {
    if (!seg || seg === '.' || seg === '..') {
      return formatErrorCode('file_path_invalid')
    }
  }
  return null
}

export function validateFileName(name: string): string | null {
  if (!name || name === '.' || name === '..' || name.includes('/')) {
    return formatErrorCode('file_name_invalid')
  }
  if (hasLoneSurrogate(name) || hasControlChar(name)) {
    return formatErrorCode('file_name_invalid')
  }
  if (utf8Encoder.encode(name).byteLength > 255) {
    return formatErrorCode('file_name_invalid')
  }
  return null
}

export function joinFilePath(dir: string, name: string): string {
  return dir === '/' ? `/${name}` : `${dir}/${name}`
}

export function parentFilePath(path: string): string {
  if (path === '/') return '/'
  const idx = path.lastIndexOf('/')
  return idx <= 0 ? '/' : path.slice(0, idx)
}

export function formatFileSize(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return '—'
  if (bytes < 1024) return `${bytes} B`
  const units = ['KiB', 'MiB', 'GiB', 'TiB']
  let val = bytes / 1024
  let unitIdx = 0
  while (val >= 1024 && unitIdx < units.length - 1) {
    val /= 1024
    unitIdx++
  }
  return `${val.toFixed(val >= 10 ? 1 : 2)} ${units[unitIdx]}`
}

export function buildFileDownloadUrl(
  machineId: string,
  credentialId: string,
  path: string,
): string {
  return `/api/v1/machines/${encodeURIComponent(machineId)}/files/content?credentialId=${encodeURIComponent(credentialId)}&path=${encodeURIComponent(path)}`
}

export const filesApi = {
  listOrPreview: (machineId: string, credentialId: string, path: string) =>
    apiRequest<FilesDTO>(
      `/machines/${encodeURIComponent(machineId)}/files?credentialId=${encodeURIComponent(credentialId)}&path=${encodeURIComponent(path)}`,
    ),

  createFolder: (machineId: string, credentialId: string, path: string) =>
    apiRequest<{ path: string }>(`/machines/${encodeURIComponent(machineId)}/files/folders`, {
      method: 'POST',
      body: { credentialId, path },
    }),

  renameEntry: (machineId: string, credentialId: string, path: string, newName: string) =>
    apiRequest<{ path: string }>(`/machines/${encodeURIComponent(machineId)}/files/rename`, {
      method: 'POST',
      body: { credentialId, path, newName },
    }),

  deleteEntry: (machineId: string, credentialId: string, path: string, confirmPath: string) =>
    apiRequest<{ ok: boolean }>(`/machines/${encodeURIComponent(machineId)}/files/delete`, {
      method: 'POST',
      body: { credentialId, path, confirmPath },
    }),

  completeUpload: (
    machineId: string,
    body: {
      credentialId: string
      path: string
      uploadId: string
      parts: number
      size: number
      overwrite: boolean
    },
  ) =>
    apiRequest<{ path: string; size: number }>(
      `/machines/${encodeURIComponent(machineId)}/files/uploads/complete`,
      {
        method: 'POST',
        body,
      },
    ),

  abortUpload: (machineId: string, credentialId: string, path: string, uploadId: string) =>
    apiRequest<{ ok: boolean }>(
      `/machines/${encodeURIComponent(machineId)}/files/uploads/abort`,
      {
        method: 'POST',
        body: { credentialId, path, uploadId },
      },
    ),
}

export interface XhrLike {
  open: (method: string, url: string, async?: boolean) => void
  setRequestHeader: (name: string, value: string) => void
  send: (body?: Document | XMLHttpRequestBodyInit | null) => void
  abort: () => void
  status: number
  responseText: string
  withCredentials: boolean
  upload: {
    onprogress: ((event: { lengthComputable: boolean; loaded: number; total: number }) => void) | null
  }
  onload: (() => void) | null
  onerror: (() => void) | null
  onabort: (() => void) | null
}

// Helper seam for test environments where jsdom XHR or chunk size needs inspection
export const fileUploadSeam = {
  chunkSize: FILE_CHUNK_SIZE,
  createXhr: (): XhrLike => new XMLHttpRequest() as unknown as XhrLike,
}

function generateUploadId(): string {
  const bytes = new Uint8Array(16)
  crypto.getRandomValues(bytes)
  return Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('')
}

async function readBlobBuffer(blob: Blob): Promise<ArrayBuffer> {
  if (typeof blob.arrayBuffer === 'function') {
    return blob.arrayBuffer()
  }
  return new Promise<ArrayBuffer>((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(reader.result as ArrayBuffer)
    reader.onerror = () => reject(reader.error ?? new Error('读取文件分片失败'))
    reader.readAsArrayBuffer(blob)
  })
}

async function sha256Hex(blob: Blob): Promise<string> {
  const subtle = globalThis.crypto?.subtle
  if (!subtle || typeof subtle.digest !== 'function') {
    throw new ApiError(
      400,
      'crypto_unavailable',
      '当前浏览器环境不支持 SHA-256 校验（需使用 HTTPS 或 localhost），无法进行分片上传',
    )
  }
  const buffer = await readBlobBuffer(blob)
  const digest = await subtle.digest('SHA-256', buffer)
  return Array.from(new Uint8Array(digest), (b) => b.toString(16).padStart(2, '0')).join('')
}

// A part is worth sending again only when the failure may be temporary.
function isTransientUploadError(err: unknown): boolean {
  if (!(err instanceof ApiError)) return true
  return (
    err.status === 0 ||
    err.status >= 500 ||
    err.retryable ||
    err.code === 'timeout' ||
    err.code === 'file_operation_timeout' ||
    err.code === 'unavailable' ||
    err.code === 'rate_limited' ||
    err.code === 'file_upload_incomplete'
  )
}

export class UploadCancelledError extends Error {
  constructor() {
    super('已取消上传')
    this.name = 'UploadCancelledError'
  }
}

async function sendPutBlob<T>(options: {
  url: string
  blob: Blob
  signal: AbortSignal
  onProgress?: (loaded: number) => void
  csrfRetried?: boolean
}): Promise<T> {
  if (options.signal.aborted) {
    throw new UploadCancelledError()
  }

  const csrfToken = useAppStore.getState().session?.csrfToken ?? ''

  const result = await new Promise<{ status: number; responseText: string }>((resolve, reject) => {
    const xhr = fileUploadSeam.createXhr()
    const onAbortSignal = () => {
      xhr.abort()
    }
    options.signal.addEventListener('abort', onAbortSignal, { once: true })

    const cleanup = () => {
      options.signal.removeEventListener('abort', onAbortSignal)
    }

    xhr.open('PUT', options.url, true)
    xhr.withCredentials = true
    xhr.setRequestHeader('Content-Type', 'application/octet-stream')
    if (csrfToken) {
      xhr.setRequestHeader('X-CSRF-Token', csrfToken)
    }

    xhr.upload.onprogress = (event) => {
      if (event.lengthComputable && options.onProgress) {
        options.onProgress(event.loaded)
      }
    }

    xhr.onload = () => {
      cleanup()
      resolve({ status: xhr.status, responseText: xhr.responseText })
    }

    xhr.onerror = () => {
      cleanup()
      reject(
        new ApiError(0, 'unavailable', formatErrorCode('unavailable'), true),
      )
    }

    xhr.onabort = () => {
      cleanup()
      reject(new UploadCancelledError())
    }

    xhr.send(options.blob)
  })

  let payload: unknown = null
  if (result.responseText) {
    try {
      payload = JSON.parse(result.responseText)
    } catch {
      payload = null
    }
  }

  if (result.status >= 200 && result.status < 300) {
    return payload as T
  }

  const errObj =
    payload && typeof payload === 'object' && 'error' in payload
      ? (
          payload as {
            error?: {
              code?: string
              message?: string
              retryable?: boolean
              requestId?: string
            }
          }
        ).error
      : undefined

  const code = errObj?.code ?? `http_${result.status}`
  const retryable = Boolean(errObj?.retryable)
  const requestId = errObj?.requestId
  const message = formatErrorCode(errObj?.code)

  if (result.status === 403 && code === 'csrf_rejected' && !options.csrfRetried) {
    const freshSession = await apiRequest<SessionInfo>('/session', {
      method: 'GET',
      csrfRetried: true,
    })
    useAppStore.getState().setSession(freshSession)
    return sendPutBlob<T>({ ...options, csrfRetried: true })
  }

  if (result.status === 401) {
    useAppStore.getState().setAuthRequired(true)
  }

  throw new ApiError(result.status, code, message, retryable, requestId)
}

export interface UploadFileParams {
  machineId: string
  credentialId: string
  targetPath: string
  file: File
  overwrite: boolean
  signal: AbortSignal
  onProgress: (percent: number | null, phase: 'uploading' | 'merging') => void
}

export async function uploadMachineFile(params: UploadFileParams): Promise<void> {
  const { machineId, credentialId, targetPath, file, overwrite, signal, onProgress } = params

  const pathErr = validateFilePath(targetPath)
  if (pathErr) {
    throw new ApiError(400, 'file_path_invalid', pathErr)
  }

  if (file.size > FILE_MAX_UPLOAD_SIZE) {
    throw new ApiError(400, 'file_too_large', '文件大小超过 20 GiB 上限')
  }

  const chunkSize = fileUploadSeam.chunkSize

  if (file.size <= chunkSize) {
    onProgress(0, 'uploading')
    const url = `/api/v1/machines/${encodeURIComponent(machineId)}/files/content?credentialId=${encodeURIComponent(credentialId)}&path=${encodeURIComponent(targetPath)}&overwrite=${overwrite ? '1' : '0'}`
    await sendPutBlob<{ path: string; size: number; sha256: string; created: boolean }>({
      url,
      blob: file,
      signal,
      onProgress: (loaded) => {
        const pct = file.size > 0 ? Math.min(100, Math.round((loaded / file.size) * 100)) : 100
        onProgress(pct, 'uploading')
      },
    })
    onProgress(100, 'uploading')
    return
  }

  if (!globalThis.crypto?.subtle || typeof globalThis.crypto.subtle.digest !== 'function') {
    throw new ApiError(
      400,
      'crypto_unavailable',
      '当前浏览器环境不支持 SHA-256 校验（需使用 HTTPS 或 localhost），无法进行分片上传',
    )
  }

  // Without this, a name conflict would only surface after sending every part.
  if (!overwrite) {
    let existing: FilesDTO | null = null
    try {
      existing = await filesApi.listOrPreview(machineId, credentialId, targetPath)
    } catch {
      // Not found, or not readable: the merge makes the final atomic check.
    }
    if (existing?.type === 'file') {
      throw new ApiError(409, 'file_exists', formatErrorCode('file_exists'))
    }
    if (existing) {
      throw new ApiError(400, 'file_target_invalid', formatErrorCode('file_target_invalid'))
    }
  }

  const uploadId = generateUploadId()
  const totalParts = Math.ceil(file.size / chunkSize)
  let partsSent = false

  try {
    for (let index = 0; index < totalParts; index++) {
      if (signal.aborted) {
        throw new UploadCancelledError()
      }

      const start = index * chunkSize
      const end = Math.min(file.size, start + chunkSize)
      const partBlob = file.slice(start, end)
      const expectedSha256 = await sha256Hex(partBlob)

      let partAttempt = 0
      while (true) {
        if (signal.aborted) {
          throw new UploadCancelledError()
        }
        partsSent = true
        try {
          const url = `/api/v1/machines/${encodeURIComponent(machineId)}/files/parts?credentialId=${encodeURIComponent(credentialId)}&path=${encodeURIComponent(targetPath)}&uploadId=${encodeURIComponent(uploadId)}&index=${index}`
          const res = await sendPutBlob<{ size: number; sha256: string }>({
            url,
            blob: partBlob,
            signal,
            onProgress: (loaded) => {
              const totalLoaded = start + loaded
              const pct = Math.min(100, Math.round((totalLoaded / file.size) * 100))
              onProgress(pct, 'uploading')
            },
          })

          if (
            !res ||
            typeof res.sha256 !== 'string' ||
            res.sha256.toLowerCase() !== expectedSha256.toLowerCase()
          ) {
            throw new ApiError(
              400,
              'file_upload_incomplete',
              formatErrorCode('file_upload_incomplete'),
            )
          }
          const completedPct = Math.min(100, Math.round((end / file.size) * 100))
          onProgress(completedPct, 'uploading')
          break
        } catch (err) {
          if (err instanceof UploadCancelledError || signal.aborted) {
            throw new UploadCancelledError()
          }
          partAttempt++
          if (partAttempt > FILE_PART_MAX_RETRIES || !isTransientUploadError(err)) {
            throw err
          }
        }
      }
    }

    onProgress(null, 'merging')
    await filesApi.completeUpload(machineId, {
      credentialId,
      path: targetPath,
      uploadId,
      parts: totalParts,
      size: file.size,
      overwrite,
    })
  } catch (err) {
    if (partsSent) {
      try {
        await filesApi.abortUpload(machineId, credentialId, targetPath, uploadId)
      } catch {
        // Ignore abort cleanup error so primary error surfaces
      }
    }
    throw err
  }
}
