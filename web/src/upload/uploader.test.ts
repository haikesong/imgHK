import { ApiError } from '../api/client'
import type { UploadResult } from '../api/types'
import { t } from '../i18n'
import type { QueueOpts } from './queue'
import { uploadFile, uploadFromURL } from './uploader'

// 可编程的 XMLHttpRequest 假实现
class FakeXHR {
  static last: FakeXHR | null = null
  method = ''
  url = ''
  status = 0
  responseText = ''
  upload = { onprogress: null as null | ((e: { lengthComputable: boolean; loaded: number; total: number }) => void) }
  onload: null | (() => void) = null
  onerror: null | (() => void) = null
  onabort: null | (() => void) = null
  sent: unknown = null
  aborted = false
  open(m: string, u: string) {
    this.method = m
    this.url = u
  }
  send(body: unknown) {
    this.sent = body
    FakeXHR.last = this
  }
  abort() {
    this.aborted = true
    this.onabort?.()
  }
}

const RESULT: UploadResult = {
  key: 'aB3xK9mQ2wZp',
  name: 'shot.png',
  size: 1048576,
  instant: false,
  links: {
    url: 'http://localhost:8686/i/aB3xK9mQ2wZp.png',
    markdown: '![shot.png](http://localhost:8686/i/aB3xK9mQ2wZp.png)',
    html: '<img src="..." alt="shot.png">',
    bbcode: '[img]...[/img]',
    thumbnail_url: 'http://localhost:8686/t/aB3xK9mQ2wZp.jpg',
  },
}

const OPTS_PRIVATE: QueueOpts = { visibility: 'private', albumId: null, policyId: null, expiresIn: 0, maxViews: 0 }
const OPTS_PUBLIC: QueueOpts = { visibility: 'public', albumId: null, policyId: null, expiresIn: 0, maxViews: 0 }

beforeEach(() => {
  FakeXHR.last = null
  vi.stubGlobal('XMLHttpRequest', FakeXHR as unknown as typeof XMLHttpRequest)
})
afterEach(() => vi.unstubAllGlobals())

function makeFile(name = 'shot.png') {
  return new File([new Uint8Array(10)], name, { type: 'image/png' })
}

it('uploadFile 走 POST /api/v1/upload，带 file+visibility+album_id，进度回调整数', async () => {
  const onProgress = vi.fn()
  const h = uploadFile(makeFile(), OPTS_PRIVATE, onProgress)
  const xhr = FakeXHR.last!
  expect(xhr.method).toBe('POST')
  expect(xhr.url).toBe('/api/v1/upload')
  const fd = xhr.sent as FormData
  expect((fd.get('file') as File).name).toBe('shot.png')
  expect(fd.get('visibility')).toBe('private')
  expect(fd.get('album_id')).toBe('0')
  expect(fd.get('policy_id')).toBeNull()

  xhr.upload.onprogress?.({ lengthComputable: true, loaded: 34, total: 100 })
  expect(onProgress).toHaveBeenCalledWith(34)

  xhr.status = 200
  xhr.responseText = JSON.stringify({ status: true, message: 'ok', data: RESULT })
  xhr.onload?.()
  await expect(h.promise).resolves.toEqual(RESULT)
})

it('uploadFile 显式 album/policy 写入 FormData', async () => {
  uploadFile(makeFile(), { visibility: 'public', albumId: 7, policyId: 3, expiresIn: 0, maxViews: 0 }, vi.fn())
  const fd = FakeXHR.last!.sent as FormData
  expect(fd.get('album_id')).toBe('7')
  expect(fd.get('policy_id')).toBe('3')
})

it('uploadFile expiresIn>0 写入 expires_in，永久不带', async () => {
  uploadFile(makeFile(), { visibility: 'public', albumId: null, policyId: null, expiresIn: 604800, maxViews: 0 }, vi.fn())
  expect((FakeXHR.last!.sent as FormData).get('expires_in')).toBe('604800')

  uploadFile(makeFile(), { visibility: 'public', albumId: null, policyId: null, expiresIn: 0, maxViews: 0 }, vi.fn())
  expect((FakeXHR.last!.sent as FormData).get('expires_in')).toBeNull()
})

it('业务失败抛 ApiError（码取 data.code）', async () => {
  const h = uploadFile(makeFile(), OPTS_PUBLIC, vi.fn())
  const xhr = FakeXHR.last!
  xhr.status = 413
  xhr.responseText = JSON.stringify({ status: false, message: '存储配额不足', data: { code: 'quota_exceeded' } })
  xhr.onload?.()
  const err = (await h.promise.catch((e) => e)) as ApiError
  expect(err).toBeInstanceOf(ApiError)
  expect(err.code).toBe('quota_exceeded')
  expect(err.httpStatus).toBe(413)
  expect(err.message).toBe('存储配额不足')
})

it('网络错误与 abort 分别归一', async () => {
  const h1 = uploadFile(makeFile(), OPTS_PUBLIC, vi.fn())
  FakeXHR.last!.onerror?.()
  await expect(h1.promise).rejects.toMatchObject({ code: 'network_error', httpStatus: 0 })

  const h2 = uploadFile(makeFile(), OPTS_PUBLIC, vi.fn())
  h2.abort()
  expect(FakeXHR.last!.aborted).toBe(true)
  await expect(h2.promise).rejects.toMatchObject({ code: 'aborted' })
})

it('uploadFromURL 转调 post /upload/url 并带 album_id', async () => {
  const f = vi.fn().mockResolvedValue({
    ok: true,
    status: 200,
    json: () => Promise.resolve({ status: true, message: 'ok', data: RESULT }),
  } as unknown as Response)
  vi.stubGlobal('fetch', f)
  await expect(uploadFromURL('https://x.com/a.png', OPTS_PUBLIC)).resolves.toEqual(RESULT)
  const [url, init] = f.mock.calls[0]
  expect(url).toBe('/api/v1/upload/url')
  expect(JSON.parse(init.body)).toEqual({
    url: 'https://x.com/a.png',
    visibility: 'public',
    album_id: 0,
  })
})

it('uploadFromURL 含 policyId 时写入 body', async () => {
  const f = vi.fn().mockResolvedValue({
    ok: true,
    status: 200,
    json: () => Promise.resolve({ status: true, message: 'ok', data: RESULT }),
  } as unknown as Response)
  vi.stubGlobal('fetch', f)
  await uploadFromURL('https://x.com/a.png', { visibility: 'private', albumId: 2, policyId: 9, expiresIn: 0, maxViews: 0 })
  expect(JSON.parse(f.mock.calls[0][1].body)).toEqual({
    url: 'https://x.com/a.png',
    visibility: 'private',
    album_id: 2,
    policy_id: 9,
  })
})

it('uploadFromURL expiresIn>0 写入 body.expires_in，永久省略', async () => {
  const f = vi.fn().mockResolvedValue({
    ok: true,
    status: 200,
    json: () => Promise.resolve({ status: true, message: 'ok', data: RESULT }),
  } as unknown as Response)
  vi.stubGlobal('fetch', f)
  await uploadFromURL('https://x.com/a.png', { visibility: 'public', albumId: null, policyId: null, expiresIn: 86400, maxViews: 0 })
  expect(JSON.parse(f.mock.calls[0][1].body)).toEqual({
    url: 'https://x.com/a.png',
    visibility: 'public',
    album_id: 0,
    expires_in: 86400,
  })
  await uploadFromURL('https://x.com/a.png', { visibility: 'public', albumId: null, policyId: null, expiresIn: 0, maxViews: 0 })
  expect(JSON.parse(f.mock.calls[1][1].body)).toEqual({
    url: 'https://x.com/a.png',
    visibility: 'public',
    album_id: 0,
  })
})

it('响应非 JSON 时归一为 internal_error', async () => {
  const h = uploadFile(makeFile(), OPTS_PUBLIC, vi.fn())
  const xhr = FakeXHR.last!
  xhr.status = 502
  xhr.responseText = '<html>Bad Gateway</html>'
  xhr.onload?.()
  await expect(h.promise).rejects.toMatchObject({ code: 'internal_error', httpStatus: 502 })
})

it('524/413 给出可操作的网关错误文案，而不是「响应格式错误」', async () => {
  for (const [status, key] of [
    [524, 'errGatewayTimeout'],
    [413, 'errGatewayTooLarge'],
  ] as const) {
    const h = uploadFile(makeFile(), OPTS_PUBLIC, vi.fn())
    const xhr = FakeXHR.last!
    xhr.status = status
    xhr.responseText = '<html>cloudflare error</html>'
    xhr.onload?.()
    const err = (await h.promise.catch((e) => e)) as ApiError
    expect(err.httpStatus).toBe(status)
    expect(err.message).toBe(t(`upload.${key}`))
    expect(err.message).not.toBe(t('upload.errResponseFormat'))
  }
})

it('lengthComputable=false 的进度事件不回调', () => {
  const onProgress = vi.fn()
  uploadFile(makeFile(), OPTS_PUBLIC, onProgress)
  FakeXHR.last!.upload.onprogress?.({ lengthComputable: false, loaded: 0, total: 0 })
  expect(onProgress).not.toHaveBeenCalled()
})

it('401 触发全局 onUnauthorized（与 fetch 通道对齐）', async () => {
  const { setOnUnauthorized } = await import('../api/client')
  const cb = vi.fn()
  setOnUnauthorized(cb)
  const h = uploadFile(makeFile(), OPTS_PUBLIC, vi.fn())
  const xhr = FakeXHR.last!
  xhr.status = 401
  xhr.responseText = JSON.stringify({ status: false, message: '请先登录', data: { code: 'unauthorized' } })
  xhr.onload?.()
  await h.promise.catch(() => {})
  expect(cb).toHaveBeenCalledOnce()
  setOnUnauthorized(null)
})
