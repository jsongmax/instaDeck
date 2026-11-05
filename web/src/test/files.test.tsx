import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse, http } from 'msw'
import { setupServer } from 'msw/node'
import type { ReactNode } from 'react'
import { afterAll, afterEach, beforeAll, expect, it } from 'vitest'
import { FILE_CHUNK_SIZE, fileUploadSeam } from '../api/files'
import { FilesDialog } from '../components/FilesDialog'
import { useAppStore } from '../store/useAppStore'

const server = setupServer()

beforeAll(() => server.listen())
afterEach(() => {
  server.resetHandlers()
  fileUploadSeam.chunkSize = FILE_CHUNK_SIZE
})
afterAll(() => server.close())

function show(node: ReactNode) {
  useAppStore.setState({
    session: {
      administratorId: 'admin',
      csrfToken: 'csrf-token-1',
      expiresAt: new Date(Date.now() + 3600_000).toISOString(),
      recentlyAuthenticated: true,
    },
  })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={client}>{node}</QueryClientProvider>)
}

it('目录导航、上级跳转与前端路径校验', async () => {
  const user = userEvent.setup()
  const requestedPaths: string[] = []

  server.use(
    http.get('/api/v1/machines/m/files', ({ request }) => {
      const url = new URL(request.url)
      const p = url.searchParams.get('path') ?? ''
      requestedPaths.push(p)
      if (p === '/data/home') {
        return HttpResponse.json({
          path: '/data/home',
          type: 'dir',
          size: 0,
          mode: 'drwxr-xr-x',
          modTime: '2026-01-01T00:00:00Z',
          entries: [
            {
              name: 'sub',
              type: 'dir',
              size: 0,
              mode: 'drwxr-xr-x',
              modTime: '2026-01-01T00:00:00Z',
            },
          ],
        })
      }
      return HttpResponse.json({
        path: p,
        type: 'dir',
        size: 0,
        mode: 'drwxr-xr-x',
        modTime: '2026-01-01T00:00:00Z',
        entries: [],
      })
    }),
  )

  show(
    <FilesDialog
      open
      machineId="m"
      machineName="box"
      credentialId="a"
      onClose={() => {}}
    />,
  )

  expect(await screen.findByRole('button', { name: 'sub/' })).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'sub/' }))
  await waitFor(() => expect(requestedPaths).toContain('/data/home/sub'))

  await user.click(screen.getByRole('button', { name: '上级' }))
  await waitFor(() =>
    expect(requestedPaths[requestedPaths.length - 1]).toBe('/data/home'),
  )

  const pathInput = screen.getByLabelText('当前路径')
  await user.clear(pathInput)
  await user.type(pathInput, '/data/home/../etc{Enter}')
  expect(await screen.findByText('文件路径格式无效')).toBeInTheDocument()
  expect(requestedPaths).not.toContain('/data/home/../etc')
})

it('目录截断提示、文件截断提示与 HTML 内容原样文本预览（不解析为 DOM）', async () => {
  const user = userEvent.setup()

  server.use(
    http.get('/api/v1/machines/m/files', ({ request }) => {
      const p = new URL(request.url).searchParams.get('path')
      if (p === '/data/home') {
        return HttpResponse.json({
          path: '/data/home',
          type: 'dir',
          size: 0,
          mode: 'drwxr-xr-x',
          modTime: '2026-01-01T00:00:00Z',
          truncated: true,
          entries: [
            {
              name: 'index.html',
              type: 'file',
              size: 300000,
              mode: '-rw-r--r--',
              modTime: '2026-01-01T00:00:00Z',
            },
          ],
        })
      }
      return HttpResponse.json({
        path: '/data/home/index.html',
        type: 'file',
        size: 300000,
        mode: '-rw-r--r--',
        modTime: '2026-01-01T00:00:00Z',
        truncated: true,
        content: '<img src=x onerror=alert(1)><script>window.evil=true</script>',
      })
    }),
  )

  show(
    <FilesDialog
      open
      machineId="m"
      machineName="box"
      credentialId="a"
      onClose={() => {}}
    />,
  )

  expect(await screen.findByText(/仅显示前 5000 项/)).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'index.html' }))

  expect(await screen.findByText(/仅预览前 256 KiB 内容/)).toBeInTheDocument()
  expect(
    screen.getByText('<img src=x onerror=alert(1)><script>window.evil=true</script>'),
  ).toBeInTheDocument()
  expect(document.querySelector('script')).toBeNull()
  expect(document.querySelector('img')).toBeNull()
})

it('删除文件按钮在完整输入目标路径前保持禁用', async () => {
  const user = userEvent.setup()
  let deletedBody: unknown = null

  server.use(
    http.get('/api/v1/machines/m/files', () =>
      HttpResponse.json({
        path: '/data/home',
        type: 'dir',
        size: 0,
        mode: 'drwxr-xr-x',
        modTime: '2026-01-01T00:00:00Z',
        entries: [
          {
            name: 'secret.txt',
            type: 'file',
            size: 12,
            mode: '-rw-r--r--',
            modTime: '2026-01-01T00:00:00Z',
          },
        ],
      }),
    ),
    http.post('/api/v1/machines/m/files/delete', async ({ request }) => {
      deletedBody = await request.json()
      return HttpResponse.json({ ok: true })
    }),
  )

  show(
    <FilesDialog
      open
      machineId="m"
      machineName="box"
      credentialId="a"
      onClose={() => {}}
    />,
  )

  const row = (await screen.findByText('secret.txt')).closest('tr')!
  await user.click(within(row).getByRole('button', { name: '删除' }))

  const confirmBtn = screen.getByRole('button', { name: '确认删除' })
  expect(confirmBtn).toBeDisabled()

  const input = screen.getByLabelText('输入完整路径确认')
  await user.type(input, '/data/home/secret')
  expect(confirmBtn).toBeDisabled()

  await user.type(input, '.txt')
  expect(confirmBtn).toBeEnabled()
  await user.click(confirmBtn)

  await waitFor(() =>
    expect(deletedBody).toEqual({
      credentialId: 'a',
      path: '/data/home/secret.txt',
      confirmPath: '/data/home/secret.txt',
    }),
  )
})

it('下载链接对含空格和 # 的路径正确使用 encodeURIComponent 编码', async () => {
  server.use(
    http.get('/api/v1/machines/m/files', () =>
      HttpResponse.json({
        path: '/data/home',
        type: 'dir',
        size: 0,
        mode: 'drwxr-xr-x',
        modTime: '2026-01-01T00:00:00Z',
        entries: [
          {
            name: 'my file#1.txt',
            type: 'file',
            size: 42,
            mode: '-rw-r--r--',
            modTime: '2026-01-01T00:00:00Z',
          },
        ],
      }),
    ),
  )

  show(
    <FilesDialog
      open
      machineId="m"
      machineName="box"
      credentialId="a"
      onClose={() => {}}
    />,
  )

  const row = (await screen.findByText('my file#1.txt')).closest('tr')!
  const link = within(row).getByRole('link', { name: '下载' })
  expect(link).toHaveAttribute(
    'href',
    '/api/v1/machines/m/files/content?credentialId=a&path=%2Fdata%2Fhome%2Fmy%20file%231.txt',
  )
  expect(link).toHaveAttribute('download')
})

it('上传遇到 file_exists 时在该行显示确认覆盖，且队列中的下一个文件继续上传', async () => {
  const user = userEvent.setup()
  const uploaded: Array<{ path: string; overwrite: string }> = []

  server.use(
    http.get('/api/v1/machines/m/files', () =>
      HttpResponse.json({
        path: '/data/home',
        type: 'dir',
        size: 0,
        mode: 'drwxr-xr-x',
        modTime: '2026-01-01T00:00:00Z',
        entries: [],
      }),
    ),
    http.put('/api/v1/machines/m/files/content', ({ request }) => {
      const url = new URL(request.url)
      const p = url.searchParams.get('path') ?? ''
      const overwrite = url.searchParams.get('overwrite') ?? '0'
      uploaded.push({ path: p, overwrite })
      if (p.endsWith('/first.txt') && overwrite === '0') {
        return HttpResponse.json(
          { error: { code: 'file_exists', message: 'exists', retryable: false } },
          { status: 409 },
        )
      }
      return HttpResponse.json({
        path: p,
        size: 4,
        sha256: '00',
        created: true,
      })
    }),
  )

  show(
    <FilesDialog
      open
      machineId="m"
      machineName="box"
      credentialId="a"
      onClose={() => {}}
    />,
  )

  await screen.findByText('此目录为空')
  const fileInput = screen.getByLabelText('选择上传文件')
  const file1 = new File(['aaaa'], 'first.txt', { type: 'text/plain' })
  const file2 = new File(['bbbb'], 'second.txt', { type: 'text/plain' })

  await user.upload(fileInput, [file1, file2])

  expect(await screen.findByRole('button', { name: '确认覆盖' })).toBeInTheDocument()
  await waitFor(() =>
    expect(uploaded).toEqual([
      { path: '/data/home/first.txt', overwrite: '0' },
      { path: '/data/home/second.txt', overwrite: '0' },
    ]),
  )

  await user.click(screen.getByRole('button', { name: '确认覆盖' }))
  const dialogs = screen.getAllByRole('dialog')
  const confirmDialog = dialogs[dialogs.length - 1]
  expect(within(confirmDialog).getByText('/data/home/first.txt')).toBeInTheDocument()
  await user.click(within(confirmDialog).getByRole('button', { name: '确认覆盖' }))

  await waitFor(() =>
    expect(uploaded).toContainEqual({
      path: '/data/home/first.txt',
      overwrite: '1',
    }),
  )
})

it('分片上传按序发送正确的分片索引并调用 complete', async () => {
  const user = userEvent.setup()
  fileUploadSeam.chunkSize = 5 // 5 bytes per chunk for test speed

  const partIndices: number[] = []
  let completeBody: unknown = null

  server.use(
    http.get('/api/v1/machines/m/files', ({ request }) =>
      new URL(request.url).searchParams.get('path') === '/data/home'
        ? HttpResponse.json({
            path: '/data/home',
            type: 'dir',
            size: 0,
            mode: 'drwxr-xr-x',
            modTime: '2026-01-01T00:00:00Z',
            entries: [],
          })
        : HttpResponse.json({ error: { code: 'file_not_found' } }, { status: 400 }),
    ),
    http.put('/api/v1/machines/m/files/parts', async ({ request }) => {
      const url = new URL(request.url)
      partIndices.push(Number(url.searchParams.get('index')))
      const buf = await request.arrayBuffer()
      const digest = await crypto.subtle.digest('SHA-256', buf)
      const sha256 = Array.from(new Uint8Array(digest), (b) =>
        b.toString(16).padStart(2, '0'),
      ).join('')
      return HttpResponse.json({ size: buf.byteLength, sha256 })
    }),
    http.post('/api/v1/machines/m/files/uploads/complete', async ({ request }) => {
      completeBody = await request.json()
      return HttpResponse.json({ path: '/data/home/big.bin', size: 12 })
    }),
  )

  show(
    <FilesDialog
      open
      machineId="m"
      machineName="box"
      credentialId="a"
      onClose={() => {}}
    />,
  )

  await screen.findByText('此目录为空')
  const fileInput = screen.getByLabelText('选择上传文件')
  const bigFile = new File(['0123456789ab'], 'big.bin', {
    type: 'application/octet-stream',
  })
  await user.upload(fileInput, [bigFile])

  await waitFor(() => expect(completeBody).not.toBeNull())
  expect(partIndices).toEqual([0, 1, 2])
  expect(completeBody).toMatchObject({
    credentialId: 'a',
    path: '/data/home/big.bin',
    parts: 3,
    size: 12,
    overwrite: false,
  })
  expect(
    (completeBody as { uploadId: string }).uploadId,
  ).toMatch(/^[0-9a-f]{32}$/)
})

it('分片 SHA-256 不一致时重试该分片最多 3 次，随后调用 abort', async () => {
  const user = userEvent.setup()
  fileUploadSeam.chunkSize = 5

  let partCalls = 0
  let abortBody: unknown = null

  server.use(
    http.get('/api/v1/machines/m/files', ({ request }) =>
      new URL(request.url).searchParams.get('path') === '/data/home'
        ? HttpResponse.json({
            path: '/data/home',
            type: 'dir',
            size: 0,
            mode: 'drwxr-xr-x',
            modTime: '2026-01-01T00:00:00Z',
            entries: [],
          })
        : HttpResponse.json({ error: { code: 'file_not_found' } }, { status: 400 }),
    ),
    http.put('/api/v1/machines/m/files/parts', () => {
      partCalls++
      return HttpResponse.json({
        size: 5,
        sha256: '0000000000000000000000000000000000000000000000000000000000000000',
      })
    }),
    http.post('/api/v1/machines/m/files/uploads/abort', async ({ request }) => {
      abortBody = await request.json()
      return HttpResponse.json({ ok: true })
    }),
  )

  show(
    <FilesDialog
      open
      machineId="m"
      machineName="box"
      credentialId="a"
      onClose={() => {}}
    />,
  )

  await screen.findByText('此目录为空')
  const fileInput = screen.getByLabelText('选择上传文件')
  const badFile = new File(['0123456789'], 'corrupt.bin', {
    type: 'application/octet-stream',
  })
  await user.upload(fileInput, [badFile])

  await waitFor(() => expect(abortBody).not.toBeNull())
  // 1 initial attempt + 3 retries = 4 calls for part 0
  expect(partCalls).toBe(4)
  expect(abortBody).toMatchObject({
    credentialId: 'a',
    path: '/data/home/corrupt.bin',
  })
  expect(await screen.findByText(/上传的分片不完整或校验失败/)).toBeInTheDocument()
})

it('上传完一个文件后继续下一个，大文件同名时不发送分片', async () => {
  const user = userEvent.setup()
  fileUploadSeam.chunkSize = 5
  const uploaded: string[] = []
  let partCalls = 0
  server.use(
    http.get('/api/v1/machines/m/files', ({ request }) => {
      const path = new URL(request.url).searchParams.get('path')
      if (path === '/data/home/taken.bin') {
        return HttpResponse.json({
          path,
          type: 'file',
          size: 1,
          mode: '-rw-r--r--',
          modTime: '2026-01-01T00:00:00Z',
        })
      }
      if (path === '/data/home') {
        return HttpResponse.json({
          path,
          type: 'dir',
          size: 0,
          mode: 'drwxr-xr-x',
          modTime: '2026-01-01T00:00:00Z',
          entries: [],
        })
      }
      return HttpResponse.json({ error: { code: 'file_not_found' } }, { status: 400 })
    }),
    http.put('/api/v1/machines/m/files/content', ({ request }) => {
      uploaded.push(new URL(request.url).searchParams.get('path') ?? '')
      return HttpResponse.json({ path: 'x', size: 1, sha256: 'x', created: true })
    }),
    http.put('/api/v1/machines/m/files/parts', () => {
      partCalls++
      return HttpResponse.json({ size: 5, sha256: 'x' })
    }),
  )
  show(<FilesDialog open machineId="m" machineName="box" credentialId="a" onClose={() => {}} />)
  await screen.findByText('此目录为空')
  await user.upload(screen.getByLabelText('选择上传文件'), [
    new File(['1'], 'a.txt'),
    new File(['1'], 'b.txt'),
    new File(['1234567890'], 'taken.bin'),
    new File(['1'], 'c.txt'),
  ])
  await waitFor(() =>
    expect(uploaded).toEqual(['/data/home/a.txt', '/data/home/b.txt', '/data/home/c.txt']),
  )
  expect(await screen.findByRole('button', { name: '确认覆盖' })).toBeInTheDocument()
  expect(partCalls).toBe(0)
})

it('关闭对话框（Esc）时有上传进行中要先确认', async () => {
  const user = userEvent.setup()
  let release: () => void = () => {}
  const held = new Promise<void>((resolve) => {
    release = resolve
  })
  let closed = false
  server.use(
    http.get('/api/v1/machines/m/files', () =>
      HttpResponse.json({
        path: '/data/home',
        type: 'dir',
        size: 0,
        mode: 'drwxr-xr-x',
        modTime: '2026-01-01T00:00:00Z',
        entries: [],
      }),
    ),
    http.put('/api/v1/machines/m/files/content', async () => {
      await held
      return HttpResponse.json({ path: 'x', size: 1, sha256: 'x', created: true })
    }),
  )
  show(
    <FilesDialog
      open
      machineId="m"
      machineName="box"
      credentialId="a"
      onClose={() => {
        closed = true
      }}
    />,
  )
  await screen.findByText('此目录为空')
  await user.upload(screen.getByLabelText('选择上传文件'), [new File(['1'], 'a.txt')])
  await screen.findByText('正在上传')
  const dialog = document.querySelector('dialog') as HTMLDialogElement
  dialog.dispatchEvent(new Event('cancel', { cancelable: true }))
  expect(await screen.findByText('正在上传文件')).toBeInTheDocument()
  expect(closed).toBe(false)
  release()
})

const emptyHome = () =>
  http.get('/api/v1/machines/m/files', ({ request }) => {
    const path = new URL(request.url).searchParams.get('path')
    if (path === '/data/home') {
      return HttpResponse.json({
        path,
        type: 'dir',
        size: 0,
        mode: 'drwxr-xr-x',
        modTime: '2026-01-01T00:00:00Z',
        entries: [
          { name: 'backup', type: 'dir', size: 0, mode: 'd', modTime: '2026-01-01T00:00:00Z' },
          {
            name: 'dead',
            type: 'symlink',
            size: 0,
            mode: 'l',
            modTime: '2026-01-01T00:00:00Z',
            target: '/gone',
          },
        ],
      })
    }
    if (path === '/data/home/backup') {
      return HttpResponse.json({ path, type: 'dir', size: 0, mode: 'd', modTime: 'x', entries: [] })
    }
    return HttpResponse.json({ error: { code: 'file_not_found' } }, { status: 400 })
  })

it('在子对话框按 Esc 只关闭子对话框', async () => {
  const user = userEvent.setup()
  let closed = false
  server.use(emptyHome())
  show(
    <FilesDialog
      open
      machineId="m"
      machineName="box"
      credentialId="a"
      onClose={() => {
        closed = true
      }}
    />,
  )
  await screen.findByRole('button', { name: 'backup/' })
  await user.click(screen.getByRole('button', { name: '新建文件夹' }))
  const child = screen.getByLabelText('文件夹名称').closest('dialog') as HTMLDialogElement
  child.dispatchEvent(new Event('cancel', { cancelable: true }))
  await waitFor(() => expect(screen.queryByLabelText('文件夹名称')).toBeNull())
  expect(closed).toBe(false)
})

it('会话过期时让出对话框，登录后恢复', async () => {
  server.use(emptyHome())
  show(<FilesDialog open machineId="m" machineName="box" credentialId="a" onClose={() => {}} />)
  await screen.findByRole('button', { name: 'backup/' })
  const dialog = document.querySelector('dialog') as HTMLDialogElement
  expect(dialog.open).toBe(true)
  act(() => useAppStore.getState().setAuthRequired(true))
  expect(dialog.open).toBe(false)
  act(() => useAppStore.getState().setAuthRequired(false))
  expect(dialog.open).toBe(true)
  expect(screen.getByRole('button', { name: 'backup/', hidden: true })).toBeInTheDocument()
})

it('大文件遇到同名文件夹立即报错，不提供覆盖也不发送分片', async () => {
  const user = userEvent.setup()
  fileUploadSeam.chunkSize = 5
  let partCalls = 0
  server.use(
    emptyHome(),
    http.put('/api/v1/machines/m/files/parts', () => {
      partCalls++
      return HttpResponse.json({ size: 5, sha256: 'x' })
    }),
  )
  show(<FilesDialog open machineId="m" machineName="box" credentialId="a" onClose={() => {}} />)
  await screen.findByRole('button', { name: 'backup/' })
  await user.upload(screen.getByLabelText('选择上传文件'), [new File(['1234567890'], 'backup')])
  expect(await screen.findByText(/目标不是普通文件/)).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: '确认覆盖' })).toBeNull()
  expect(partCalls).toBe(0)
})

it('点击失效链接时留在当前目录并显示原因', async () => {
  const user = userEvent.setup()
  server.use(emptyHome())
  show(<FilesDialog open machineId="m" machineName="box" credentialId="a" onClose={() => {}} />)
  await user.click(await screen.findByRole('button', { name: 'dead' }))
  expect(await screen.findByText(/找不到这个文件或文件夹/)).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'backup/' })).toBeInTheDocument()
  expect(screen.getByLabelText('当前路径')).toHaveValue('/data/home')
})

