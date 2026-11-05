import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback, useEffect, useRef, useState } from 'react'
import { ApiError } from '../api/errors'
import {
  type FileEntry,
  type FilesDTO,
  UploadCancelledError,
  buildFileDownloadUrl,
  filesApi,
  formatFileSize,
  joinFilePath,
  parentFilePath,
  uploadMachineFile,
  validateFileName,
  validateFilePath,
} from '../api/files'
import { ModalDialog } from './Dialogs'
import { Icon } from './Icons'

const INITIAL_PATH = '/data/home'

interface UploadRowItem {
  id: string
  name: string
  targetPath: string
  percent: number | null
  state: 'queued' | 'uploading' | 'merging' | 'done' | 'exists' | 'failed' | 'cancelled'
  error?: string
  file?: File
  overwrite?: boolean
  controller?: AbortController
}

export function FilesDialog({
  open,
  machineId,
  machineName,
  credentialId,
  onClose,
}: {
  open: boolean
  machineId: string
  machineName: string
  credentialId: string
  onClose: () => void
}) {
  // Esc closes through the dialog itself, and must meet the same upload guard as the button.
  const closeRequest = useRef<() => void>(onClose)
  const registerClose = useCallback((close: () => void) => {
    closeRequest.current = close
  }, [])
  return (
    <ModalDialog open={open} onClose={() => closeRequest.current()} wide>
      {open ? (
        <FilesManagerContent
          key={`${machineId}:${credentialId}`}
          machineId={machineId}
          machineName={machineName}
          credentialId={credentialId}
          registerClose={registerClose}
          onClose={onClose}
        />
      ) : null}
    </ModalDialog>
  )
}

function FilesManagerContent({
  machineId,
  machineName,
  credentialId,
  registerClose,
  onClose,
}: {
  machineId: string
  machineName: string
  credentialId: string
  registerClose: (close: () => void) => void
  onClose: () => void
}) {
  const queryClient = useQueryClient()
  const [currentDir, setCurrentDir] = useState(INITIAL_PATH)
  const [pathInput, setPathInput] = useState(INITIAL_PATH)
  const [pathError, setPathError] = useState('')
  const [previewPath, setPreviewPath] = useState<string | null>(null)
  const [actionError, setActionError] = useState('')

  const [mkdirOpen, setMkdirOpen] = useState(false)
  const [renameTarget, setRenameTarget] = useState<FileEntry | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<FileEntry | null>(null)
  const [overwriteTarget, setOverwriteTarget] = useState<UploadRowItem | null>(null)
  const [confirmCloseOpen, setConfirmCloseOpen] = useState(false)

  const [uploads, setUploads] = useState<UploadRowItem[]>([])
  const fileInputRef = useRef<HTMLInputElement | null>(null)
  const [activeUploadId, setActiveUploadId] = useState<string | null>(null)
  const unmountedRef = useRef(false)
  const activeControllerRef = useRef<AbortController | null>(null)

  useEffect(() => {
    unmountedRef.current = false
    return () => {
      unmountedRef.current = true
      activeControllerRef.current?.abort()
    }
  }, [])

  const dirQueryKey = ['machineFiles', machineId, credentialId, currentDir]
  const dirQuery = useQuery({
    queryKey: dirQueryKey,
    queryFn: () => filesApi.listOrPreview(machineId, credentialId, currentDir),
    retry: false,
  })

  const previewQuery = useQuery({
    queryKey: ['machineFilePreview', machineId, credentialId, previewPath],
    queryFn: () => filesApi.listOrPreview(machineId, credentialId, previewPath!),
    enabled: Boolean(previewPath),
    retry: false,
    // File contents are not kept in memory after the preview closes.
    gcTime: 0,
  })

  const hasActiveUploads = uploads.some(
    (u) => u.state === 'queued' || u.state === 'uploading' || u.state === 'merging',
  )

  // One upload at a time: the next starts when activeUploadId clears.
  useEffect(() => {
    if (activeUploadId) return
    const nextItem = uploads.find((u) => u.state === 'queued' && u.file)
    if (!nextItem?.file) return

    const controller = new AbortController()
    const fileToUpload = nextItem.file
    const overwrite = Boolean(nextItem.overwrite)
    activeControllerRef.current = controller

    const update = (change: Partial<UploadRowItem>) => {
      if (unmountedRef.current) return
      setUploads((prev) => prev.map((u) => (u.id === nextItem.id ? { ...u, ...change } : u)))
    }

    // eslint-disable-next-line react-hooks/set-state-in-effect -- starts the external upload
    setActiveUploadId(nextItem.id)
    update({ state: 'uploading', percent: 0, error: undefined, controller })

    void (async () => {
      try {
        await uploadMachineFile({
          machineId,
          credentialId,
          targetPath: nextItem.targetPath,
          file: fileToUpload,
          overwrite,
          signal: controller.signal,
          onProgress: (percent, phase) =>
            update({ percent, state: phase === 'merging' ? 'merging' : 'uploading' }),
        })
        update({ state: 'done', percent: 100, file: undefined, controller: undefined })
        void queryClient.invalidateQueries({
          queryKey: ['machineFiles', machineId, credentialId],
        })
      } catch (err) {
        if (err instanceof UploadCancelledError || controller.signal.aborted) {
          update({ state: 'cancelled', percent: null, file: undefined, controller: undefined })
        } else if (err instanceof ApiError && err.code === 'file_exists' && !overwrite) {
          // The file is kept only while waiting for the overwrite decision.
          update({ state: 'exists', percent: null, error: err.message, controller: undefined })
        } else {
          update({
            state: 'failed',
            percent: null,
            error: err instanceof Error ? err.message : '上传失败',
            file: undefined,
            controller: undefined,
          })
        }
      } finally {
        activeControllerRef.current = null
        if (!unmountedRef.current) setActiveUploadId(null)
      }
    })()
  }, [uploads, activeUploadId, machineId, credentialId, queryClient])

  const confirmOverwrite = (item: UploadRowItem) => {
    setOverwriteTarget(null)
    setUploads((prev) =>
      prev.map((u) =>
        u.id === item.id
          ? { ...u, state: 'queued', percent: 0, overwrite: true, error: undefined }
          : u,
      ),
    )
  }

  const navigateTo = (nextPath: string) => {
    const trimmed = nextPath.trim()
    const err = validateFilePath(trimmed)
    if (err) {
      setPathError(err)
      return
    }
    setPathError('')
    setActionError('')
    setPreviewPath(null)
    setCurrentDir(trimmed)
    setPathInput(trimmed)
  }

  const handlePathSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    navigateTo(pathInput)
  }

  const handleGoUp = () => {
    const parent = parentFilePath(currentDir)
    navigateTo(parent)
  }

  const handleRefresh = () => {
    setPathError('')
    setActionError('')
    void dirQuery.refetch()
    if (previewPath) {
      void previewQuery.refetch()
    }
  }

  const handleSelectFiles = (e: React.ChangeEvent<HTMLInputElement>) => {
    const picked = e.target.files ? Array.from(e.target.files) : []
    e.target.value = ''
    if (picked.length === 0) return

    const newItems: UploadRowItem[] = picked.map((file) => {
      const nameErr = validateFileName(file.name)
      const targetPath = joinFilePath(currentDir, file.name)
      if (nameErr) {
        return {
          id: crypto.randomUUID(),
          name: file.name,
          targetPath,
          percent: 0,
          state: 'failed',
          error: nameErr,
        }
      }
      return {
        id: crypto.randomUUID(),
        name: file.name,
        targetPath,
        percent: 0,
        state: 'queued',
        file,
      }
    })

    setUploads((prev) => [...prev, ...newItems])
  }

  const cancelUploadItem = (id: string) => {
    if (id === activeUploadId) activeControllerRef.current?.abort()
    setUploads((prev) =>
      prev.map((u) =>
        u.id === id
          ? { ...u, state: 'cancelled', percent: null, file: undefined, controller: undefined }
          : u,
      ),
    )
  }

  const cancelAllUploadsAndClose = () => {
    // A merge in progress keeps running on the machine: its request takes no signal.
    uploads.forEach((u) => u.controller?.abort())
    setUploads([])
    setConfirmCloseOpen(false)
    onClose()
  }

  const requestDialogClose = () => {
    if (hasActiveUploads) {
      setConfirmCloseOpen(true)
      return
    }
    setUploads([])
    onClose()
  }

  useEffect(() => registerClose(requestDialogClose))

  const handleEntryClick = async (entry: FileEntry) => {
    const fullPath = joinFilePath(currentDir, entry.name)
    setActionError('')
    if (entry.type === 'symlink') {
      // Stay in this directory unless the link leads to another one.
      try {
        const target = await filesApi.listOrPreview(machineId, credentialId, fullPath)
        if (target.type === 'dir') navigateTo(fullPath)
        else setPreviewPath(fullPath)
      } catch (err) {
        setActionError(err instanceof Error ? err.message : '无法打开这个链接')
      }
    } else if (entry.type === 'dir') {
      navigateTo(fullPath)
    } else if (entry.type === 'file') {
      setActionError('')
      setPreviewPath(fullPath)
    }
  }

  // If user navigated via path input directly to a file, switch to previewing that file
  const dirData: FilesDTO | undefined = dirQuery.data
  const entries = dirData?.type === 'dir' ? (dirData.entries ?? []) : []
  const canWriteHere = dirData?.type === 'dir' && !dirQuery.isFetching

  return (
    <div className="files-dlg">
      <div className="files-head">
        <h2>
          <Icon name="folder" /> 文件管理 · {machineName}
        </h2>
        <button type="button" className="btn" onClick={requestDialogClose}>
          关闭
        </button>
      </div>

      <form className="files-bar" onSubmit={handlePathSubmit}>
        <input
          className="field mono files-path-input"
          aria-label="当前路径"
          value={pathInput}
          onChange={(e) => {
            setPathInput(e.target.value)
            if (pathError) setPathError('')
          }}
        />
        <button
          type="button"
          className="btn"
          disabled={currentDir === '/'}
          onClick={handleGoUp}
        >
          上级
        </button>
        <button type="button" className="btn" onClick={handleRefresh}>
          刷新
        </button>
        <button
          type="button"
          className="btn"
          disabled={!canWriteHere}
          onClick={() => {
            setActionError('')
            setMkdirOpen(true)
          }}
        >
          新建文件夹
        </button>
        <button
          type="button"
          className="btn primary"
          disabled={!canWriteHere}
          onClick={() => fileInputRef.current?.click()}
        >
          上传
        </button>
      </form>
      <input
        ref={fileInputRef}
        type="file"
        multiple
        hidden
        aria-label="选择上传文件"
        onChange={handleSelectFiles}
      />

      {pathError ? <p className="errline">{pathError}</p> : null}
      {actionError ? <p className="errline">{actionError}</p> : null}
      {dirQuery.error ? (
        <p className="errline">
          {dirQuery.error instanceof Error ? dirQuery.error.message : '加载目录失败'}
        </p>
      ) : null}

      {dirData?.type === 'dir' && dirData.truncated ? (
        <div className="notice warn">目录项过多，仅显示前 5000 项。</div>
      ) : null}

      {uploads.length > 0 ? (
        <div className="files-uploads" aria-label="上传列表">
          {uploads.map((item) => (
            <div className="files-upload-row" key={item.id}>
              <span className="mono files-upload-name">{item.name}</span>
              <span className="num muted files-upload-pct">
                {item.state === 'merging'
                  ? ''
                  : item.percent !== null
                    ? `${item.percent}%`
                    : ''}
              </span>
              <span className="files-upload-state">
                {item.state === 'queued' && '排队中'}
                {item.state === 'uploading' && '正在上传'}
                {item.state === 'merging' && '服务器正在合并'}
                {item.state === 'done' && '已完成'}
                {item.state === 'cancelled' && '已取消'}
                {item.state === 'exists' && (
                  <span className="errline">{item.error || '同名文件已存在'}</span>
                )}
                {item.state === 'failed' && (
                  <span className="errline">{item.error || '上传失败'}</span>
                )}
              </span>
              <div className="btn-row">
                {item.state === 'exists' ? (
                  <button
                    type="button"
                    className="btn"
                    onClick={() => setOverwriteTarget(item)}
                  >
                    确认覆盖
                  </button>
                ) : null}
                {item.state === 'queued' ||
                item.state === 'uploading' ||
                item.state === 'exists' ? (
                  <button
                    type="button"
                    className="btn"
                    onClick={() => cancelUploadItem(item.id)}
                  >
                    取消
                  </button>
                ) : null}
              </div>
            </div>
          ))}
        </div>
      ) : null}

      {dirData?.type === 'file' ? (
        <FilePreviewPanel
          dto={dirData}
          machineId={machineId}
          credentialId={credentialId}
          onClose={() => navigateTo(parentFilePath(dirData.path))}
        />
      ) : (
        <div className="table-wrap files-table-wrap">
          <table className="files-table">
            <thead>
              <tr>
                <th>名称</th>
                <th>大小</th>
                <th>权限</th>
                <th>修改时间</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {dirQuery.isLoading ? (
                <tr>
                  <td colSpan={5} className="muted">
                    正在加载文件列表…
                  </td>
                </tr>
              ) : dirQuery.error ? null : entries.length === 0 ? (
                <tr>
                  <td colSpan={5} className="muted">
                    此目录为空
                  </td>
                </tr>
              ) : (
                entries.map((entry) => {
                  const fullPath = joinFilePath(currentDir, entry.name)
                  const downloadHref = buildFileDownloadUrl(
                    machineId,
                    credentialId,
                    fullPath,
                  )
                  const modTimeText = entry.modTime
                    ? new Date(entry.modTime).toLocaleString('zh-CN')
                    : '—'

                  return (
                    <tr key={entry.name}>
                      <td className="files-cell-name">
                        {entry.type !== 'other' ? (
                          <button
                            type="button"
                            className="linkish mono files-name-btn"
                            onClick={() => void handleEntryClick(entry)}
                          >
                            {entry.type === 'dir' ? `${entry.name}/` : entry.name}
                          </button>
                        ) : (
                          <span className="mono files-name-text">{entry.name}</span>
                        )}
                        {entry.type === 'symlink' && entry.target ? (
                          <span className="mono muted files-symlink-target">
                            {' -> '}
                            {entry.target}
                          </span>
                        ) : null}
                      </td>
                      <td className="num muted">
                        {entry.type === 'dir' ? '—' : formatFileSize(entry.size)}
                      </td>
                      <td className="mono muted">{entry.mode}</td>
                      <td className="num muted">{modTimeText}</td>
                      <td>
                        <div className="btn-row">
                          {entry.type !== 'dir' ? (
                            <a className="btn" href={downloadHref} download>
                              下载
                            </a>
                          ) : null}
                          <button
                            type="button"
                            className="btn"
                            onClick={() => {
                              setActionError('')
                              setRenameTarget(entry)
                            }}
                          >
                            重命名
                          </button>
                          <button
                            type="button"
                            className="btn danger"
                            onClick={() => {
                              setActionError('')
                              setDeleteTarget(entry)
                            }}
                          >
                            删除
                          </button>
                        </div>
                      </td>
                    </tr>
                  )
                })
              )}
            </tbody>
          </table>
        </div>
      )}

      {previewPath ? (
        <div className="files-preview-box">
          {previewQuery.isLoading ? (
            <p className="muted">正在加载文件预览…</p>
          ) : previewQuery.error ? (
            <p className="errline">
              {previewQuery.error instanceof Error
                ? previewQuery.error.message
                : '加载文件预览失败'}
            </p>
          ) : previewQuery.data ? (
            <FilePreviewPanel
              dto={previewQuery.data}
              machineId={machineId}
              credentialId={credentialId}
              onClose={() => setPreviewPath(null)}
            />
          ) : null}
        </div>
      ) : null}

      <ModalDialog open={mkdirOpen} onClose={() => setMkdirOpen(false)}>
        {mkdirOpen ? (
          <CreateFolderForm
            machineId={machineId}
            credentialId={credentialId}
            currentDir={currentDir}
            onCreated={() => {
              setMkdirOpen(false)
              void queryClient.invalidateQueries({ queryKey: dirQueryKey })
            }}
            onClose={() => setMkdirOpen(false)}
          />
        ) : null}
      </ModalDialog>

      <ModalDialog open={Boolean(renameTarget)} onClose={() => setRenameTarget(null)}>
        {renameTarget ? (
          <RenameEntryForm
            machineId={machineId}
            credentialId={credentialId}
            currentDir={currentDir}
            entry={renameTarget}
            onRenamed={(oldPath) => {
              if (previewPath === oldPath) setPreviewPath(null)
              setRenameTarget(null)
              void queryClient.invalidateQueries({ queryKey: dirQueryKey })
            }}
            onClose={() => setRenameTarget(null)}
          />
        ) : null}
      </ModalDialog>

      <ModalDialog open={Boolean(deleteTarget)} onClose={() => setDeleteTarget(null)}>
        {deleteTarget ? (
          <DeleteEntryForm
            machineId={machineId}
            credentialId={credentialId}
            currentDir={currentDir}
            entry={deleteTarget}
            onDeleted={(deletedPath) => {
              if (previewPath === deletedPath) setPreviewPath(null)
              setDeleteTarget(null)
              void queryClient.invalidateQueries({ queryKey: dirQueryKey })
            }}
            onClose={() => setDeleteTarget(null)}
          />
        ) : null}
      </ModalDialog>

      <ModalDialog
        open={Boolean(overwriteTarget)}
        onClose={() => setOverwriteTarget(null)}
      >
        {overwriteTarget ? (
          <div style={{ display: 'contents' }}>
            <h2>确认覆盖文件</h2>
            <p className="notice warn">
              目标路径 <span className="mono">{overwriteTarget.targetPath}</span>{' '}
              已存在，覆盖后原文件内容将无法恢复。
            </p>
            <div className="dlg-foot">
              <button
                type="button"
                className="btn"
                onClick={() => setOverwriteTarget(null)}
              >
                取消
              </button>
              <button
                type="button"
                className="btn danger"
                onClick={() => confirmOverwrite(overwriteTarget)}
              >
                确认覆盖
              </button>
            </div>
          </div>
        ) : null}
      </ModalDialog>

      <ModalDialog open={confirmCloseOpen} onClose={() => setConfirmCloseOpen(false)}>
        {confirmCloseOpen ? (
          <div style={{ display: 'contents' }}>
            <h2>正在上传文件</h2>
            <p className="notice warn">
              关闭窗口会取消还在排队和正在上传的文件。已进入“服务器正在合并”的文件无法取消，会继续完成。
            </p>
            <div className="dlg-foot">
              <button
                type="button"
                className="btn"
                onClick={() => setConfirmCloseOpen(false)}
              >
                继续上传
              </button>
              <button
                type="button"
                className="btn danger"
                onClick={cancelAllUploadsAndClose}
              >
                取消上传并关闭
              </button>
            </div>
          </div>
        ) : null}
      </ModalDialog>
    </div>
  )
}

function FilePreviewPanel({
  dto,
  machineId,
  credentialId,
  onClose,
}: {
  dto: FilesDTO
  machineId: string
  credentialId: string
  onClose: () => void
}) {
  const downloadHref = buildFileDownloadUrl(machineId, credentialId, dto.path)

  return (
    <div className="card files-preview-card">
      <div className="files-preview-head">
        <strong className="mono files-preview-title">{dto.path}</strong>
        <span className="muted num">{formatFileSize(dto.size)}</span>
        <div className="btn-row">
          <a className="btn" href={downloadHref} download>
            下载
          </a>
          <button type="button" className="btn" onClick={onClose}>
            关闭预览
          </button>
        </div>
      </div>
      {dto.truncated ? (
        <div className="notice warn">文件较大，仅预览前 256 KiB 内容。</div>
      ) : null}
      {dto.binary ? (
        <div className="notice">
          <p style={{ margin: '0 0 8px' }}>无法预览二进制文件</p>
          <a className="btn" href={downloadHref} download>
            下载文件
          </a>
        </div>
      ) : (
        <pre className="files-preview-pre mono">{dto.content ?? ''}</pre>
      )}
    </div>
  )
}

function CreateFolderForm({
  machineId,
  credentialId,
  currentDir,
  onCreated,
  onClose,
}: {
  machineId: string
  credentialId: string
  currentDir: string
  onCreated: () => void
  onClose: () => void
}) {
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    const trimmed = name.trim()
    const nameErr = validateFileName(trimmed)
    if (nameErr) {
      setError(nameErr)
      return
    }
    const targetPath = joinFilePath(currentDir, trimmed)
    const pathErr = validateFilePath(targetPath)
    if (pathErr) {
      setError(pathErr)
      return
    }

    setBusy(true)
    setError('')
    try {
      await filesApi.createFolder(machineId, credentialId, targetPath)
      onCreated()
    } catch (err) {
      setError(err instanceof Error ? err.message : '新建文件夹失败')
    } finally {
      setBusy(false)
    }
  }

  return (
    <form style={{ display: 'contents' }} onSubmit={(e) => void handleSubmit(e)}>
      <h2>新建文件夹</h2>
      <label className="lbl">
        文件夹名称
        <input
          className="field wide mono"
          value={name}
          onChange={(e) => setName(e.target.value)}
          maxLength={255}
          required
          autoFocus
        />
      </label>
      {error ? <p className="errline">{error}</p> : null}
      <div className="dlg-foot">
        <button type="button" className="btn" onClick={onClose}>
          取消
        </button>
        <button type="submit" className="btn primary" disabled={busy || !name.trim()}>
          创建
        </button>
      </div>
    </form>
  )
}

function RenameEntryForm({
  machineId,
  credentialId,
  currentDir,
  entry,
  onRenamed,
  onClose,
}: {
  machineId: string
  credentialId: string
  currentDir: string
  entry: FileEntry
  onRenamed: (oldPath: string) => void
  onClose: () => void
}) {
  const [newName, setNewName] = useState(entry.name)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const oldPath = joinFilePath(currentDir, entry.name)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (busy || newName === entry.name) return
    const nameErr = validateFileName(newName)
    if (nameErr) {
      setError(nameErr)
      return
    }

    setBusy(true)
    setError('')
    try {
      await filesApi.renameEntry(machineId, credentialId, oldPath, newName)
      onRenamed(oldPath)
    } catch (err) {
      setError(err instanceof Error ? err.message : '重命名失败')
    } finally {
      setBusy(false)
    }
  }

  return (
    <form style={{ display: 'contents' }} onSubmit={(e) => void handleSubmit(e)}>
      <h2>重命名</h2>
      <label className="lbl">
        新名称
        <input
          className="field wide mono"
          value={newName}
          onChange={(e) => setNewName(e.target.value)}
          maxLength={255}
          required
          autoFocus
        />
      </label>
      {error ? <p className="errline">{error}</p> : null}
      <div className="dlg-foot">
        <button type="button" className="btn" onClick={onClose}>
          取消
        </button>
        <button
          type="submit"
          className="btn primary"
          disabled={busy || !newName || newName === entry.name}
        >
          保存
        </button>
      </div>
    </form>
  )
}

function DeleteEntryForm({
  machineId,
  credentialId,
  currentDir,
  entry,
  onDeleted,
  onClose,
}: {
  machineId: string
  credentialId: string
  currentDir: string
  entry: FileEntry
  onDeleted: (deletedPath: string) => void
  onClose: () => void
}) {
  const fullPath = joinFilePath(currentDir, entry.name)
  const [confirmPath, setConfirmPath] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const canConfirm = confirmPath === fullPath && !busy

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!canConfirm) return
    setBusy(true)
    setError('')
    try {
      await filesApi.deleteEntry(machineId, credentialId, fullPath, confirmPath)
      onDeleted(fullPath)
    } catch (err) {
      setError(err instanceof Error ? err.message : '删除失败')
    } finally {
      setBusy(false)
    }
  }

  return (
    <form style={{ display: 'contents' }} onSubmit={(e) => void handleSubmit(e)}>
      <h2>删除{entry.type === 'dir' ? '文件夹' : entry.type === 'symlink' ? '链接' : '文件'}</h2>
      <div className="notice warn">
        {entry.type === 'symlink'
          ? '只删除这个链接，不影响它指向的内容。'
          : entry.type === 'dir'
            ? '连同其中的内容一起删除，无法恢复。删除操作没有回收站。'
            : '删除操作没有回收站，删除后无法恢复。'}
      </div>
      <p className="mono" style={{ margin: 0, overflowWrap: 'anywhere' }}>
        {fullPath}
      </p>
      <label className="lbl">
        输入完整路径确认
        <input
          className="field wide mono"
          value={confirmPath}
          placeholder={fullPath}
          onChange={(e) => setConfirmPath(e.target.value)}
          autoComplete="off"
          autoFocus
        />
      </label>
      {error ? <p className="errline">{error}</p> : null}
      <div className="dlg-foot">
        <button type="button" className="btn" onClick={onClose}>
          取消
        </button>
        <button type="submit" className="btn danger" disabled={!canConfirm}>
          确认删除
        </button>
      </div>
    </form>
  )
}
