import { ApiError, notifyUnauthorized, post } from '../api/client'
import type { UploadResult } from '../api/types'
import { t } from '../i18n'
import type { QueueOpts } from './queue'

export interface UploadHandle {
  promise: Promise<UploadResult>
  abort(): void
}

/** 单个 XHR 请求的句柄（分片上传内部按片持有，便于并发中止）。 */
interface RawHandle<T> {
  promise: Promise<T>
  abort(): void
}

interface Envelope {
  status: boolean
  message: string
  data: unknown
}

/**
 * 大文件分片上传参数。与服务端 internal/handler/upload_session.go 对齐：
 * 32MB 起走会话 + 分片（8MB/片，3 路并发，任意顺序），小文件仍是单请求 POST。
 * 分片上传绕开 Cloudflare 约 100 秒的源站读取超时——每片都是独立的短请求。
 */
export const CHUNK_THRESHOLD = 32 << 20
export const CHUNK_SIZE = 8 << 20
export const CHUNK_CONCURRENCY = 3
/** 单片刻录失败后的重试次数（网络抖动/限速）。 */
const CHUNK_RETRIES = 2
/** 续传记录有效期：与服务端会话 TTL（2 小时）一致，避免拿着已清理的 id 空跑。 */
const RESUME_TTL_MS = 2 * 60 * 60 * 1000
const RESUME_PREFIX = 'imgli:upload-session:'

interface SessionInfo {
  session_id: string
  chunk_size: number
  chunks_total: number
  size: number
}

interface SessionState {
  chunk_size: number
  chunks_total: number
  size: number
  received: number[]
  missing: number[]
  state: 'open' | 'completing'
}

/**
 * 响应不是 JSON 时说明请求没走到应用：多半是 Cloudflare/Caddy 的 HTML 错误页。
 * 413 = 请求体超过边缘上限；524/522/504 = 源站处理超过 Cloudflare 100 秒。
 * 把这些状态翻译成人能看懂的原因，而不是笼统的「响应格式错误」。
 */
function gatewayMessage(status: number): string {
  if (status === 413) return t('upload.errGatewayTooLarge')
  if (status === 524 || status === 522 || status === 504) return t('upload.errGatewayTimeout')
  if (status === 502 || status === 520 || status === 521 || status === 523) {
    return t('upload.errGatewayUnavailable')
  }
  return t('upload.errResponseFormat')
}

/** 解析响应信封；失败时抛 ApiError（码取 data.code），与 api/client 语义一致。 */
function parseEnvelope(xhr: XMLHttpRequest): unknown {
  let env: Envelope
  try {
    env = JSON.parse(xhr.responseText) as Envelope
  } catch {
    throw new ApiError(xhr.status, 'internal_error', gatewayMessage(xhr.status))
  }
  if (xhr.status >= 200 && xhr.status < 300 && env.status) return env.data
  const code = (env.data as { code?: string } | null)?.code ?? 'internal_error'
  if (xhr.status === 401 && code !== 'invalid_credentials') notifyUnauthorized()
  let retryAfter: number | undefined
  try {
    const ra = xhr.getResponseHeader('Retry-After')
    if (ra) {
      const n = Number(ra)
      if (Number.isFinite(n) && n > 0) retryAfter = Math.ceil(n)
    }
  } catch {
    /* mock XHR may omit getResponseHeader */
  }
  throw new ApiError(xhr.status, code, env.message || t('upload.errUploadFailed'), retryAfter)
}

/**
 * 底层 XHR 请求（fetch 没有上传进度事件）。
 * onUp 只在有请求体时回调；json=true 时带 Content-Type。
 */
function send<T>(
  method: string,
  url: string,
  body: XMLHttpRequestBodyInit | null,
  onUp?: (loaded: number, total: number) => void,
  json = false,
): RawHandle<T> {
  const xhr = new XMLHttpRequest()
  const promise = new Promise<T>((resolve, reject) => {
    xhr.open(method, url)
    if (json) xhr.setRequestHeader('Content-Type', 'application/json')
    if (onUp) {
      xhr.upload.onprogress = (e) => {
        if (e.lengthComputable && e.total > 0) onUp(e.loaded, e.total)
      }
    }
    xhr.onload = () => {
      try {
        resolve(parseEnvelope(xhr) as T)
      } catch (e) {
        reject(e)
      }
    }
    xhr.onerror = () => reject(new ApiError(0, 'network_error', t('upload.errNetwork')))
    xhr.onabort = () => reject(new ApiError(0, 'aborted', t('upload.errAborted')))
    xhr.send(body)
  })
  return { promise, abort: () => xhr.abort() }
}

function sessionUrl(id: string): string {
  return `/api/v1/upload/session/${id}`
}

// ---- 断点续传记录：file 指纹 → 会话 id（localStorage，刷新页面也能续） ----

function resumeKey(file: File): string {
  return `${RESUME_PREFIX}${file.name}:${file.size}:${file.lastModified}`
}

function loadResume(file: File): string | null {
  try {
    const raw = localStorage.getItem(resumeKey(file))
    if (!raw) return null
    const v = JSON.parse(raw) as { id?: string; ts?: number }
    if (!v.id || !v.ts || Date.now() - v.ts > RESUME_TTL_MS) {
      localStorage.removeItem(resumeKey(file))
      return null
    }
    return v.id
  } catch {
    return null
  }
}

function saveResume(file: File, id: string) {
  try {
    localStorage.setItem(resumeKey(file), JSON.stringify({ id, ts: Date.now() }))
  } catch {
    /* 隐私模式/配额满：续传降级为不记忆，不影响本次上传 */
  }
}

function clearResume(file: File) {
  try {
    localStorage.removeItem(resumeKey(file))
  } catch {
    /* ignore */
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms))
}

function retryableChunkError(e: unknown): boolean {
  if (!(e instanceof ApiError)) return false
  return (
    e.code === 'network_error' ||
    e.code === 'rate_limited' ||
    e.code === 'internal_error' ||
    // 缺片/正在合并：会话仍可用，保留续传记录，重试只补缺失分片
    e.code === 'upload_incomplete'
  )
}

/**
 * 分片上传：会话 → 并发 3 路乱序传片 → complete。
 * - 失败（网络/限速）时保留会话与续传记录，用户点重试只补缺失分片；
 * - 用户取消时删除会话并清掉续传记录；
 * - 服务端已存在同内容对象时，complete 内部走秒传，不会重复存储。
 */
function uploadChunked(
  file: File,
  opts: QueueOpts,
  onProgress: (pct: number) => void,
): UploadHandle {
  const active = new Set<RawHandle<unknown>>()
  let aborted = false
  let sessionId: string | null = null

  const track = <T>(h: RawHandle<T>): Promise<T> => {
    active.add(h)
    const done = () => active.delete(h)
    h.promise.then(done, done)
    return h.promise
  }

  /** 中止在途请求但不算「用户取消」（失败重试时要保留会话）。 */
  const abortActive = () => {
    for (const h of active) h.abort()
    active.clear()
  }

  const abortAll = () => {
    aborted = true
    abortActive()
  }

  const dropSession = () => {
    const id = sessionId
    sessionId = null
    clearResume(file)
    if (!id) return
    // 尽力而为：卡片已被移除，不需要等结果
    try {
      track(send('DELETE', sessionUrl(id), null)).catch(() => {})
    } catch {
      /* ignore */
    }
  }

  const promise = (async (): Promise<UploadResult> => {
    try {
      let session: SessionInfo | null = null
      let received = new Set<number>()

      const resumed = loadResume(file)
      if (resumed) {
        try {
          const st = await track(send<SessionState>('GET', sessionUrl(resumed), null))
          if (st.size === file.size && st.chunk_size > 0) {
            session = {
              session_id: resumed,
              chunk_size: st.chunk_size,
              chunks_total: st.chunks_total,
              size: st.size,
            }
            received = new Set(st.received.filter((i) => i >= 0 && i < st.chunks_total))
          }
        } catch (e) {
          // 网络/限速是暂时的：保留续传记录，交给队列重试。
          if (e instanceof ApiError && retryableChunkError(e)) throw e
          clearResume(file) // 会话已过期/被清理：重建
        }
      }
      if (aborted) throw new ApiError(0, 'aborted', t('upload.errAborted'))

      if (!session) {
        session = await track(
          send<SessionInfo>(
            'POST',
            '/api/v1/upload/session',
            JSON.stringify({
              name: file.name,
              size: file.size,
              visibility: opts.visibility,
              album_id: opts.albumId ?? 0, // 0=明确不归档（与单请求路径一致）
              ...(opts.policyId != null ? { policy_id: opts.policyId } : {}),
              ...(opts.expiresIn > 0 ? { expires_in: opts.expiresIn } : {}),
              ...(opts.maxViews > 0 ? { max_views: opts.maxViews } : {}),
            }),
            undefined,
            true,
          ),
        )
        saveResume(file, session.session_id)
      }
      sessionId = session.session_id
      const info = session
      const chunkSize = info.chunk_size

      // 进度 = 所有分片的已上传字节 / 总大小（续传时从已收到的分片起算）。
      const loaded = new Map<number, number>()
      for (const idx of received) {
        const start = idx * chunkSize
        loaded.set(idx, Math.min(chunkSize, file.size - start))
      }
      const baseBytes = () => {
        let n = 0
        for (const v of loaded.values()) n += v
        return n
      }
      const report = () => {
        if (file.size <= 0) return
        const pct = Math.min(99, Math.floor((baseBytes() / file.size) * 100))
        onProgress(pct)
      }
      report()

      const putOne = async (idx: number): Promise<void> => {
        const start = idx * chunkSize
        const end = Math.min(start + chunkSize, file.size)
        const blob = file.slice(start, end)
        for (let attempt = 0; ; attempt++) {
          if (aborted) throw new ApiError(0, 'aborted', t('upload.errAborted'))
          try {
            await track(
              send(
                'PUT',
                `${sessionUrl(info.session_id)}/${idx}`,
                blob,
                (n) => {
                  loaded.set(idx, Math.min(n, end - start))
                  report()
                },
              ),
            )
            loaded.set(idx, end - start)
            report()
            return
          } catch (e) {
            if (aborted) throw new ApiError(0, 'aborted', t('upload.errAborted'))
            if (attempt >= CHUNK_RETRIES || !retryableChunkError(e)) throw e
            const err = e as ApiError
            const wait =
              err.code === 'rate_limited' && err.retryAfterSec
                ? err.retryAfterSec * 1000
                : 500 * 2 ** attempt
            loaded.set(idx, 0)
            report()
            await sleep(wait)
          }
        }
      }

      const uploadMissing = async (missing: number[]): Promise<void> => {
        const queue = [...missing]
        let next = 0
        const worker = async (): Promise<void> => {
          for (;;) {
            if (aborted) throw new ApiError(0, 'aborted', t('upload.errAborted'))
            const at = next++
            if (at >= queue.length) return
            await putOne(queue[at])
          }
        }
        const n = Math.min(CHUNK_CONCURRENCY, queue.length)
        await Promise.all(Array.from({ length: n }, worker))
      }

      const missing = Array.from({ length: info.chunks_total }, (_, i) => i).filter(
        (i) => !received.has(i),
      )
      await uploadMissing(missing)
      onProgress(100)

      // complete 偶发 409（分片在服务端被判缺失）：查一次状态补传后再试。
      for (let round = 0; ; round++) {
        try {
          const res = await track(
            send<UploadResult>('POST', `${sessionUrl(info.session_id)}/complete`, null),
          )
          sessionId = null
          clearResume(file)
          return res
        } catch (e) {
          const err = e as ApiError
          if (aborted) throw new ApiError(0, 'aborted', t('upload.errAborted'))
          if (round === 0 && err.code === 'upload_incomplete') {
            const st = await track(send<SessionState>('GET', sessionUrl(info.session_id), null))
            if (st.state === 'completing') {
              // 另一次 complete 仍在合并（例如上一轮请求被网关掐断但源站没停）：
              // 等它结束；成功的话会话会被删掉，下一轮按 404 处理。
              await sleep(3000)
            } else {
              await uploadMissing(st.missing)
              onProgress(100)
            }
            continue
          }
          if (err.code === 'not_found') {
            // 会话已被另一次 complete 消费或被 TTL 清理：清掉续传记录，
            // 按可重试失败上报，用户重试时会新建会话重传（落库仍有 SHA-256 秒传兜底）。
            clearResume(file)
            sessionId = null
            throw new ApiError(409, 'upload_incomplete', t('upload.errUploadFailed'))
          }
          // 永久失败服务端已删会话；可重试的保留，供用户重试时续传。
          if (!retryableChunkError(err)) dropSession()
          throw err
        }
      }
    } catch (e) {
      abortActive()
      // 用户取消、或重试也没用的校验失败（类型/配额/文件名）：删掉会话与续传记录。
      if (aborted || (e instanceof ApiError && !retryableChunkError(e))) dropSession()
      throw e
    }
  })()

  return {
    promise,
    abort() {
      abortAll()
      dropSession()
    },
  }
}

/** 上传文件：≥32MB 走分片会话，小文件仍是单请求（行为与之前完全一致）。 */
export function uploadFile(
  file: File,
  opts: QueueOpts,
  onProgress: (pct: number) => void,
): UploadHandle {
  if (file.size >= CHUNK_THRESHOLD) return uploadChunked(file, opts, onProgress)

  const fd = new FormData()
  fd.append('file', file)
  fd.append('visibility', opts.visibility)
  fd.append('album_id', String(opts.albumId ?? 0)) // 0=明确不归档(三态契约,Web 恒显式)
  if (opts.policyId != null) fd.append('policy_id', String(opts.policyId))
  if (opts.expiresIn > 0) fd.append('expires_in', String(opts.expiresIn))
  if (opts.maxViews > 0) fd.append('max_views', String(opts.maxViews))
  return send<UploadResult>('POST', '/api/v1/upload', fd, (loaded, total) => {
    onProgress(Math.round((loaded / total) * 100))
  })
}

/** URL 远程抓取（服务端拉取，无进度语义）。 */
export function uploadFromURL(url: string, opts: QueueOpts): Promise<UploadResult> {
  return post<UploadResult>('/upload/url', {
    url,
    visibility: opts.visibility,
    album_id: opts.albumId ?? 0,
    ...(opts.policyId != null ? { policy_id: opts.policyId } : {}),
    ...(opts.expiresIn > 0 ? { expires_in: opts.expiresIn } : {}),
    ...(opts.maxViews > 0 ? { max_views: opts.maxViews } : {}),
  })
}
