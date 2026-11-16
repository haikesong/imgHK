import { ApiError, notifyUnauthorized, post } from '../api/client'
import type { UploadResult } from '../api/types'
import { t } from '../i18n'
import type { QueueOpts } from './queue'

export interface UploadHandle {
  promise: Promise<UploadResult>
  abort(): void
}

interface Envelope {
  status: boolean
  message: string
  data: unknown
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

/** XHR 上传（fetch 无上传进度事件）。信封语义与 api/client 一致。 */
export function uploadFile(
  file: File,
  opts: QueueOpts,
  onProgress: (pct: number) => void,
): UploadHandle {
  const xhr = new XMLHttpRequest()
  const promise = new Promise<UploadResult>((resolve, reject) => {
    xhr.open('POST', '/api/v1/upload')
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable && e.total > 0) onProgress(Math.round((e.loaded / e.total) * 100))
    }
    xhr.onload = () => {
      let env: Envelope
      try {
        env = JSON.parse(xhr.responseText) as Envelope
      } catch {
        reject(new ApiError(xhr.status, 'internal_error', gatewayMessage(xhr.status)))
        return
      }
      if (xhr.status >= 200 && xhr.status < 300 && env.status) {
        resolve(env.data as UploadResult)
        return
      }
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
      reject(new ApiError(xhr.status, code, env.message || t('upload.errUploadFailed'), retryAfter))
    }
    xhr.onerror = () => reject(new ApiError(0, 'network_error', t('upload.errNetwork')))
    xhr.onabort = () => reject(new ApiError(0, 'aborted', t('upload.errAborted')))
    const fd = new FormData()
    fd.append('file', file)
    fd.append('visibility', opts.visibility)
    fd.append('album_id', String(opts.albumId ?? 0)) // 0=明确不归档(三态契约,Web 恒显式)
    if (opts.policyId != null) fd.append('policy_id', String(opts.policyId))
    if (opts.expiresIn > 0) fd.append('expires_in', String(opts.expiresIn))
    if (opts.maxViews > 0) fd.append('max_views', String(opts.maxViews))
    xhr.send(fd)
  })
  return { promise, abort: () => xhr.abort() }
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
