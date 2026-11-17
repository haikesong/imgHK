import { create } from 'zustand'
import type { UploadResult } from '../api/types'
import { t } from '../i18n'
import { errorText } from '../i18n/errorText'
import { formatBytes } from '../lib/format'
import { uploadFile, uploadFromURL, type UploadHandle } from './uploader'

export type UploadStatus = 'queued' | 'uploading' | 'processing' | 'success' | 'instant' | 'failed'

export interface QueueOpts {
  visibility: 'public' | 'private'
  albumId: number | null
  policyId: number | null
  /** 有效期秒数;0=永久 */
  expiresIn: number
  /** 0=不限；1=阅后即焚 */
  maxViews: number
}

export interface Limits {
  maxFileSize: number
  allowedExts: string[]
}

export interface QueueItem {
  id: number
  kind: 'file' | 'url'
  name: string
  size: number
  ext: string
  pct: number
  status: UploadStatus
  thumb: string | null
  reason: string | null
  retryable: boolean
  result: UploadResult | null
  opts: QueueOpts
  file?: File
  url?: string
}

export const CONCURRENCY = 3

/** 失败码 → 是否可重试（不可重试 = 重试也必败）。 */
export function retryableCode(code: string): boolean {
  return (
    code === 'network_error' ||
    code === 'rate_limited' ||
    code === 'internal_error' ||
    // 分片会话缺片/正在合并：服务端保留会话，重试只补缺失分片
    code === 'upload_incomplete'
  )
}

export function extLabel(allowedExts: string[] | null | undefined): string {
  const seen = new Set<string>()
  for (const e of allowedExts ?? []) seen.add(e.toLowerCase() === 'jpeg' ? 'JPG' : e.toUpperCase())
  return [...seen].join(' · ')
}

interface QueueState {
  items: QueueItem[]
  addFiles(files: File[], opts: QueueOpts, limits: Limits): void
  addUrl(url: string, opts: QueueOpts): void
  retry(id: number): void
  remove(id: number): void
  clearDone(): void
}

let seq = 0
// XHR 句柄不进 store state（不可序列化、与渲染无关）
const handles = new Map<number, UploadHandle>()

export const useUploadQueue = create<QueueState>((set, get) => {
  function update(id: number, ch: Partial<QueueItem>) {
    set((s) => ({ items: s.items.map((i) => (i.id === id ? { ...i, ...ch } : i)) }))
  }

  function finish(id: number, result: UploadResult) {
    const item = get().items.find((i) => i.id === id)
    update(id, {
      status: result.instant ? 'instant' : 'success',
      pct: 100,
      result,
      // URL 抓取项无本地预览，成功后用缩略图直链
      thumb: item?.thumb ?? result.links.thumbnail_url,
    })
  }

  function fail(id: number, code: string, message: string, retryAfterSec?: number) {
    if (code === 'aborted') return // remove() 已删卡
    const secs = retryAfterSec && retryAfterSec > 0 ? Math.ceil(retryAfterSec) : 0
    if (code === 'rate_limited' && secs > 0) {
      update(id, {
        status: 'failed',
        reason: message + (secs ? ` (${secs}s)` : ''),
        retryable: true,
      })
      // 按 Retry-After 自动重试一次
      window.setTimeout(() => {
        const cur = get().items.find((i) => i.id === id)
        if (cur?.status === 'failed' && cur.retryable) get().retry(id)
      }, secs * 1000)
      return
    }
    update(id, { status: 'failed', reason: message, retryable: retryableCode(code) })
  }

  function start(item: QueueItem) {
    update(item.id, { status: 'uploading', pct: 0, reason: null })
    if (item.kind === 'url') {
      // 服务端拉取无进度，直接视为处理中
      update(item.id, { status: 'processing' })
      uploadFromURL(item.url!, item.opts)
        .then((r) => finish(item.id, r))
        .catch((e) => {
          const code = e?.code ?? 'internal_error'
          if (code === 'aborted') return fail(item.id, code, '')
          fail(
            item.id,
            code,
            errorText(code, e?.message ?? t('upload.errFetchFailed')),
            e?.retryAfterSec,
          )
        })
        .finally(pump)
      return
    }
    const h = uploadFile(item.file!, item.opts, (pct) => {
      update(item.id, pct >= 100 ? { pct: 100, status: 'processing' } : { pct })
    })
    handles.set(item.id, h)
    h.promise
      .then((r) => finish(item.id, r))
      .catch((e) => {
        const code = e?.code ?? 'internal_error'
        if (code === 'aborted') return fail(item.id, code, '')
        fail(
          item.id,
          code,
          errorText(code, e?.message ?? t('upload.errUploadFailed')),
          e?.retryAfterSec,
        )
      })
      .finally(() => {
        handles.delete(item.id)
        pump()
      })
  }

  /** 调度器：在途（uploading/processing）不足 CONCURRENCY 时启动队首 queued 项。 */
  function pump() {
    const items = get().items
    let active = items.filter((i) => i.status === 'uploading' || i.status === 'processing').length
    for (const item of items) {
      if (active >= CONCURRENCY) break
      if (item.status !== 'queued') continue
      active++
      start(item)
    }
  }

  return {
    items: [],

    addFiles(files, opts, limits) {
      const label = extLabel(limits.allowedExts)
      const allowed = new Set(limits.allowedExts.map((e) => e.toLowerCase()))
      const fresh: QueueItem[] = files.map((file) => {
        const name = file.name || `pasted-${Date.now().toString(36)}.png`
        const ext = (name.split('.').pop() ?? '').toLowerCase()
        const base: QueueItem = {
          id: ++seq, kind: 'file', name, size: file.size, ext,
          pct: 0, status: 'queued', thumb: URL.createObjectURL(file),
          reason: null, retryable: false, result: null, opts, file,
        }
        if (limits.maxFileSize > 0 && file.size > limits.maxFileSize) {
          return {
            ...base,
            status: 'failed',
            reason: t('upload.errFileTooLarge', { max: formatBytes(limits.maxFileSize) }),
          }
        }
        if (!allowed.has(ext)) {
          return {
            ...base,
            status: 'failed',
            reason: t('upload.errExtNotAllowed', { formats: label }),
          }
        }
        return base
      })
      set((s) => ({ items: [...fresh, ...s.items] }))
      pump()
    },

    addUrl(url, opts) {
      const raw = (url.split('/').pop() ?? '').split('?')[0].split('#')[0]
      const name = raw || 'remote-image'
      const ext = (name.split('.').pop() ?? 'png').toLowerCase()
      const item: QueueItem = {
        id: ++seq, kind: 'url', name, size: 0, ext,
        pct: 0, status: 'queued', thumb: null,
        reason: null, retryable: false, result: null, opts, url,
      }
      set((s) => ({ items: [item, ...s.items] }))
      pump()
    },

    retry(id) {
      const item = get().items.find((i) => i.id === id)
      if (!item || item.status !== 'failed' || !item.retryable) return
      update(id, { status: 'queued', pct: 0, reason: null })
      pump()
    },

    remove(id) {
      const item = get().items.find((i) => i.id === id)
      if (!item) return
      handles.get(id)?.abort()
      handles.delete(id)
      if (item.kind === 'file' && item.thumb) URL.revokeObjectURL(item.thumb)
      set((s) => ({ items: s.items.filter((i) => i.id !== id) }))
      pump()
    },

    clearDone() {
      set((s) => {
        for (const i of s.items) {
          if ((i.status === 'success' || i.status === 'instant') && i.kind === 'file' && i.thumb) {
            URL.revokeObjectURL(i.thumb)
          }
        }
        return { items: s.items.filter((i) => i.status !== 'success' && i.status !== 'instant') }
      })
    },
  }
})
