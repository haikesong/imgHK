import type { UploadResult } from '../api/types'
import type { QueueOpts } from './queue'
import {
  CHUNK_CONCURRENCY,
  CHUNK_SIZE,
  CHUNK_THRESHOLD,
  uploadFile,
} from './uploader'

// 分片上传测试：用一个可编排的 XHR 假实现控制每个请求的响应时机，
// 覆盖 3 路并发、乱序完成、断点续传、complete 409 补传、取消与非重试失败。

const RESULT: UploadResult = {
  key: 'aB3xK9mQ2wZp',
  name: 'big.png',
  size: 34 << 20,
  instant: false,
  links: {
    url: 'http://localhost:8686/i/aB3xK9mQ2wZp.png',
    markdown: '![big.png](http://localhost:8686/i/aB3xK9mQ2wZp.png)',
    html: '<img src="..." alt="big.png">',
    bbcode: '[img]...[/img]',
    thumbnail_url: 'http://localhost:8686/t/aB3xK9mQ2wZp.jpg',
  },
}

const OPTS: QueueOpts = { visibility: 'private', albumId: null, policyId: null, expiresIn: 0, maxViews: 0 }
const SID = '0123456789abcdef0123456789abcdef'
const SESSION = { session_id: SID, chunk_size: CHUNK_SIZE, chunks_total: 5, size: 34 << 20 }

class FakeXHR {
  static instances: FakeXHR[] = []
  method = ''
  url = ''
  headers: Record<string, string> = {}
  upload = { onprogress: null as null | ((e: { lengthComputable: boolean; loaded: number; total: number }) => void) }
  status = 0
  responseText = ''
  body: unknown = null
  aborted = false
  onload: null | (() => void) = null
  onerror: null | (() => void) = null
  onabort: null | (() => void) = null
  open(m: string, u: string) {
    this.method = m
    this.url = u
  }
  setRequestHeader(k: string, v: string) {
    this.headers[k] = v
  }
  send(body: unknown) {
    this.body = body
    FakeXHR.instances.push(this)
    queueMicrotask(() => {
      void responder(this)
    })
  }
  abort() {
    this.aborted = true
    this.onabort?.()
  }
}

type Responder = (x: FakeXHR) => void
let responder: Responder = () => {
  throw new Error('responder 未设置')
}

function json(x: FakeXHR, status: number, data: unknown, message = '') {
  x.status = status
  x.responseText = JSON.stringify({ status: status < 300, message, data })
  x.onload?.()
}

function fail(x: FakeXHR, status: number, code: string) {
  json(x, status, { code }, code)
}

async function flush() {
  await new Promise((r) => setTimeout(r, 0))
}

function bigFile(name = 'big.png', size = 34 << 20) {
  return new File([new Uint8Array(size)], name, { type: 'image/png', lastModified: 1700000000000 })
}

function resumeKey(name: string, size: number) {
  return `imgli:upload-session:${name}:${size}:1700000000000`
}

function puts(xs = FakeXHR.instances) {
  return xs.filter((x) => x.method === 'PUT')
}

function putIndex(x: FakeXHR): number {
  return Number(x.url.split('/').pop())
}

beforeEach(() => {
  FakeXHR.instances = []
  localStorage.clear()
  vi.stubGlobal('XMLHttpRequest', FakeXHR as unknown as typeof XMLHttpRequest)
})
afterEach(() => vi.unstubAllGlobals())

it('≥32MB 走分片：会话 → 3 路并发乱序传片 → complete，小文件仍走单请求', async () => {
  expect(CHUNK_THRESHOLD).toBe(32 << 20)
  expect(CHUNK_SIZE).toBe(8 << 20)
  expect(CHUNK_CONCURRENCY).toBe(3)

  const pendingPuts: FakeXHR[] = []
  responder = (x) => {
    if (x.method === 'POST' && x.url === '/api/v1/upload/session') return json(x, 200, SESSION)
    if (x.method === 'PUT') {
      expect(x.headers['Content-Type']).toBeUndefined()
      pendingPuts.push(x)
      return
    }
    if (x.method === 'POST' && x.url.endsWith('/complete')) return json(x, 200, RESULT)
    throw new Error(`unexpected ${x.method} ${x.url}`)
  }

  const onProgress = vi.fn()
  const h = uploadFile(bigFile(), OPTS, onProgress)
  await flush()

  // 会话请求带 JSON 头与全部选项。
  const create = FakeXHR.instances.find((x) => x.url === '/api/v1/upload/session')!
  expect(create.headers['Content-Type']).toBe('application/json')
  expect(JSON.parse(create.body as string)).toMatchObject({
    name: 'big.png',
    size: 34 << 20,
    visibility: 'private',
    album_id: 0,
  })

  // 同时在途恰好 3 片。
  expect(pendingPuts).toHaveLength(CHUNK_CONCURRENCY)
  // 分片按下标切片：前 4 片 8MB，最后一片 2MB。
  const sizes = pendingPuts.map((x) => (x.body as Blob).size)
  expect(sizes.every((n) => n === CHUNK_SIZE || n === (34 << 20) - 4 * CHUNK_SIZE)).toBe(true)

  // 倒序完成：模拟乱序到达，服务端按 index 拼装。
  while (pendingPuts.length) {
    const x = pendingPuts.pop()!
    json(x, 200, { index: putIndex(x), received_bytes: 1, missing: 0 })
    await flush()
  }
  await expect(h.promise).resolves.toEqual(RESULT)

  const all = puts()
  expect(all.map(putIndex).sort((a, b) => a - b)).toEqual([0, 1, 2, 3, 4])
  expect(localStorage.getItem(resumeKey('big.png', 34 << 20))).toBeNull()
  expect(onProgress).toHaveBeenCalledWith(100)
})

it('31MB 仍走单请求 POST /api/v1/upload', async () => {
  responder = (x) => {
    expect(x.method).toBe('POST')
    expect(x.url).toBe('/api/v1/upload')
    json(x, 200, RESULT)
  }
  const h = uploadFile(bigFile('small.png', (32 << 20) - 1), OPTS, vi.fn())
  await expect(h.promise).resolves.toEqual(RESULT)
  expect(FakeXHR.instances).toHaveLength(1)
})

it('断点续传：localStorage 里的会话只补缺失分片', async () => {
  localStorage.setItem(resumeKey('big.png', 34 << 20), JSON.stringify({ id: SID, ts: Date.now() }))
  const pendingPuts: FakeXHR[] = []
  responder = (x) => {
    if (x.method === 'GET' && x.url === `/api/v1/upload/session/${SID}`) {
      return json(x, 200, {
        ...SESSION,
        received: [0, 1],
        missing: [2, 3, 4],
        state: 'open',
      })
    }
    if (x.method === 'PUT') return void pendingPuts.push(x)
    if (x.method === 'POST' && x.url.endsWith('/complete')) return json(x, 200, RESULT)
    throw new Error(`unexpected ${x.method} ${x.url}`)
  }
  const h = uploadFile(bigFile(), OPTS, vi.fn())
  await flush()
  expect(FakeXHR.instances.some((x) => x.method === 'POST' && x.url === '/api/v1/upload/session')).toBe(false)

  while (pendingPuts.length) {
    json(pendingPuts.shift()!, 200, {})
    await flush()
  }
  await expect(h.promise).resolves.toEqual(RESULT)
  expect(puts().map(putIndex).sort((a, b) => a - b)).toEqual([2, 3, 4])
})

it('续传记录过期/会话已清理时重建会话', async () => {
  localStorage.setItem(resumeKey('big.png', 34 << 20), JSON.stringify({ id: SID, ts: Date.now() }))
  const pendingPuts: FakeXHR[] = []
  responder = (x) => {
    if (x.method === 'GET') return fail(x, 404, 'not_found')
    if (x.method === 'POST' && x.url === '/api/v1/upload/session') return json(x, 200, SESSION)
    if (x.method === 'PUT') return void pendingPuts.push(x)
    if (x.method === 'POST' && x.url.endsWith('/complete')) return json(x, 200, RESULT)
    throw new Error(`unexpected ${x.method} ${x.url}`)
  }
  const h = uploadFile(bigFile(), OPTS, vi.fn())
  await flush()
  while (pendingPuts.length) {
    json(pendingPuts.shift()!, 200, {})
    await flush()
  }
  await expect(h.promise).resolves.toEqual(RESULT)
  expect(puts()).toHaveLength(5)
})

it('续传状态查询遇到网络错误：保留续传记录，不新建会话', async () => {
  localStorage.setItem(resumeKey('big.png', 34 << 20), JSON.stringify({ id: SID, ts: Date.now() }))
  responder = (x) => {
    expect(x.method).toBe('GET')
    x.onerror?.()
  }
  const h = uploadFile(bigFile(), OPTS, vi.fn())
  await expect(h.promise).rejects.toMatchObject({ code: 'network_error' })
  expect(localStorage.getItem(resumeKey('big.png', 34 << 20))).not.toBeNull()
  expect(FakeXHR.instances.some((x) => x.method === 'POST')).toBe(false)
})

it('complete 返回 upload_incomplete 时查状态补传后再合并', async () => {
  let completeCalls = 0
  const pendingPuts: FakeXHR[] = []
  responder = (x) => {
    if (x.method === 'POST' && x.url === '/api/v1/upload/session') return json(x, 200, SESSION)
    if (x.method === 'PUT') return void pendingPuts.push(x)
    if (x.method === 'GET') {
      return json(x, 200, { ...SESSION, received: [0, 1, 2, 3], missing: [4], state: 'open' })
    }
    if (x.method === 'POST' && x.url.endsWith('/complete')) {
      completeCalls++
      if (completeCalls === 1) return fail(x, 409, 'upload_incomplete')
      return json(x, 200, RESULT)
    }
    throw new Error(`unexpected ${x.method} ${x.url}`)
  }
  const h = uploadFile(bigFile(), OPTS, vi.fn())
  await flush()
  while (pendingPuts.length) {
    json(pendingPuts.shift()!, 200, {})
    await flush()
  }
  await expect(h.promise).resolves.toEqual(RESULT)
  expect(completeCalls).toBe(2)
  // 第一次 5 片 + 补传 1 片。
  expect(puts()).toHaveLength(6)
})

it('complete 时会话已不存在：清续传记录，按可重试失败上报', async () => {
  const pendingPuts: FakeXHR[] = []
  responder = (x) => {
    if (x.method === 'POST' && x.url === '/api/v1/upload/session') return json(x, 200, SESSION)
    if (x.method === 'PUT') return void pendingPuts.push(x)
    if (x.method === 'POST' && x.url.endsWith('/complete')) return fail(x, 404, 'not_found')
    throw new Error(`unexpected ${x.method} ${x.url}`)
  }
  const h = uploadFile(bigFile(), OPTS, vi.fn())
  const caught = h.promise.catch((e) => e) // 先挂上处理器，避免测试中途出现 unhandled rejection
  await flush()
  while (pendingPuts.length) {
    json(pendingPuts.shift()!, 200, {})
    await flush()
  }
  expect(await caught).toMatchObject({ code: 'upload_incomplete' })
  expect(localStorage.getItem(resumeKey('big.png', 34 << 20))).toBeNull()
})

it('单片网络错误自动重试，重试成功后照常合并', async () => {
  let failedOnce = false
  const pendingPuts: FakeXHR[] = []
  responder = (x) => {
    if (x.method === 'POST' && x.url === '/api/v1/upload/session') return json(x, 200, SESSION)
    if (x.method === 'PUT') {
      if (!failedOnce) {
        failedOnce = true
        x.onerror?.()
        return
      }
      return void pendingPuts.push(x)
    }
    if (x.method === 'POST' && x.url.endsWith('/complete')) return json(x, 200, RESULT)
    throw new Error(`unexpected ${x.method} ${x.url}`)
  }
  const h = uploadFile(bigFile(), OPTS, vi.fn())
  // 第一次失败后等待 500ms 再重试。
  await new Promise((r) => setTimeout(r, 600))
  while (pendingPuts.length) {
    json(pendingPuts.shift()!, 200, {})
    await flush()
  }
  await expect(h.promise).resolves.toEqual(RESULT)
  expect(puts().length).toBe(6) // 5 片 + 1 次重试
})

it('不可重试的分片错误直接失败，并删除会话与续传记录', async () => {
  responder = (x) => {
    if (x.method === 'POST' && x.url === '/api/v1/upload/session') return json(x, 200, SESSION)
    if (x.method === 'PUT') return fail(x, 400, 'invalid_request')
    if (x.method === 'DELETE') return json(x, 200, null)
    throw new Error(`unexpected ${x.method} ${x.url}`)
  }
  const h = uploadFile(bigFile(), OPTS, vi.fn())
  await expect(h.promise).rejects.toMatchObject({ code: 'invalid_request' })
  expect(FakeXHR.instances.some((x) => x.method === 'DELETE' && x.url.endsWith(SID))).toBe(true)
  expect(localStorage.getItem(resumeKey('big.png', 34 << 20))).toBeNull()
})

it('abort：中止在途分片、删除会话、清掉续传记录', async () => {
  const pendingPuts: FakeXHR[] = []
  responder = (x) => {
    if (x.method === 'POST' && x.url === '/api/v1/upload/session') return json(x, 200, SESSION)
    if (x.method === 'PUT') return void pendingPuts.push(x)
    if (x.method === 'DELETE') return json(x, 200, null)
    throw new Error(`unexpected ${x.method} ${x.url}`)
  }
  const h = uploadFile(bigFile(), OPTS, vi.fn())
  await flush()
  expect(pendingPuts).toHaveLength(CHUNK_CONCURRENCY)
  h.abort()
  await expect(h.promise).rejects.toMatchObject({ code: 'aborted' })
  expect(pendingPuts.every((x) => x.aborted)).toBe(true)
  expect(localStorage.getItem(resumeKey('big.png', 34 << 20))).toBeNull()
  await flush()
  expect(FakeXHR.instances.some((x) => x.method === 'DELETE' && x.url.endsWith(SID))).toBe(true)
})
