import { useEffect, useMemo, useState } from 'react'
import type { HTMLInject, SiteAnnouncement, SiteFooter as SiteFooterConfig } from '../api/types'
import { useT } from '../i18n'
import { localeFingerprint, pickLocale } from '../lib/locale'
import { cn } from '../lib/cn'

const DISMISS_KEY = 'imgli_announcement_dismissed'

function annFingerprint(a: SiteAnnouncement): string {
  return [
    localeFingerprint(a.text),
    a.link_url,
    localeFingerprint(a.link_label),
    a.starts_at,
    a.ends_at,
  ].join('\0')
}

/** 顶栏公告：可关闭（localStorage 记指纹）。视觉与 Nav / Quota 条对齐。 */
export function AnnouncementBar({ announcement }: { announcement?: SiteAnnouncement | null }) {
  const { t, lang } = useT()
  const fp = useMemo(() => (announcement ? annFingerprint(announcement) : ''), [announcement])
  const [hidden, setHidden] = useState(false)

  useEffect(() => {
    if (!announcement?.dismissible) {
      setHidden(false)
      return
    }
    try {
      setHidden(localStorage.getItem(DISMISS_KEY) === fp)
    } catch {
      setHidden(false)
    }
  }, [announcement, fp])

  const text = announcement ? pickLocale(announcement.text, lang) : ''
  const linkLabel = announcement ? pickLocale(announcement.link_label, lang) : ''

  if (!announcement || !text || hidden) return null

  const linkURL = announcement.link_url
  const canDismiss = announcement.dismissible

  const dismiss = () => {
    if (!canDismiss) return
    try {
      localStorage.setItem(DISMISS_KEY, fp)
    } catch {
      /* ignore */
    }
    setHidden(true)
  }

  return (
    <div
      className="relative z-[11] animate-[fadeIn_0.2s_ease] border-b border-border bg-soft text-ink before:absolute before:inset-y-0 before:left-0 before:w-[3px] before:bg-ink before:opacity-85"
      role="region"
      aria-label={t('common.announcementAria')}
    >
      <div className="mx-auto flex max-w-[1100px] flex-wrap items-center gap-x-3.5 gap-y-2.5 px-8 py-2.5 pl-7 max-md:gap-x-2.5 max-md:gap-y-2 max-md:px-4 max-md:pl-[18px]">
        <span className="flex-none rounded-sm border border-border bg-surface px-[7px] py-[3px] font-mono text-2xs font-semibold tracking-[0.12em] text-muted uppercase leading-tight">
          {t('common.announcementKicker')}
        </span>
        <span className="min-w-0 flex-[1_1_12rem] text-[13px] font-medium leading-normal text-ink">{text}</span>
        <div className="ml-auto flex flex-none items-center gap-2 max-md:ml-0 max-md:w-full max-md:justify-start">
          {linkURL && linkLabel ? (
            <a
              className="inline-flex h-7 items-center whitespace-nowrap rounded-sm border border-ink bg-ink px-3 text-xs font-bold text-bg no-underline transition-opacity duration-150 hover:opacity-88 hover:text-bg"
              href={linkURL}
              rel="noopener noreferrer"
            >
              {linkLabel}
            </a>
          ) : null}
          {canDismiss ? (
            <button
              type="button"
              className="inline-flex h-7 w-7 flex-none cursor-pointer items-center justify-center rounded-sm border border-border bg-surface text-base leading-none text-muted transition-colors hover:border-muted hover:bg-bg hover:text-ink"
              onClick={dismiss}
              aria-label={t('common.close')}
              title={t('common.close')}
            >
              ×
            </button>
          ) : null}
        </div>
      </div>
    </div>
  )
}

type SiteFooterProps = {
  footer?: SiteFooterConfig | null
  siteName?: string | null
  sourceUrl?: string | null
  aboutEnabled?: boolean
}

export function SiteFooter({ footer, siteName, sourceUrl, aboutEnabled }: SiteFooterProps) {
  const { t, lang } = useT()
  const groups = footer?.groups?.filter((g) => g.links?.length) ?? []
  const name = (siteName || '').trim()
  const year = new Date().getFullYear()
  const src = (sourceUrl || '').trim()
  const metaQuiet = 'text-muted opacity-80'
  const metaBits = (
    <>
      <span>{`© ${year}${name ? ` ${name}` : ''}`}</span>
      {aboutEnabled ? (
        <a href="/about" className={metaQuiet}>
          {t('common.about')}
        </a>
      ) : null}
      {src ? (
        <a href={src} rel="noopener noreferrer" className={metaQuiet}>
          {t('common.sourceCode')}
        </a>
      ) : null}
    </>
  )

  if (groups.length === 0) {
    return (
      <footer className="mt-auto border-t border-border bg-surface px-8 pt-4 pb-5 max-md:px-4">
        <div className="mx-auto flex max-w-[1100px] flex-col gap-[22px]">
          <div className="flex flex-wrap items-center justify-center gap-x-5 gap-y-1.5 text-center text-[11.5px] leading-snug text-muted">
            {metaBits}
          </div>
        </div>
      </footer>
    )
  }

  return (
    <footer className="mt-auto border-t border-border bg-surface px-8 pt-7 pb-6 max-md:px-4 max-md:pt-[22px] max-md:pb-5">
      <div className="mx-auto flex max-w-[1100px] flex-col gap-[22px]">
        <div className="grid w-full min-w-0 grid-cols-[repeat(auto-fit,minmax(10rem,1fr))] justify-items-stretch gap-x-7 gap-y-6 max-md:grid-cols-2 max-md:gap-x-3 max-md:gap-y-5 max-[420px]:grid-cols-1">
          {groups.map((g, i) => {
            const title = pickLocale(g.title, lang)
            return (
              <div key={i} className="min-w-0 text-center">
                {title ? (
                  <div className="mb-3 font-mono text-2xs font-semibold tracking-[0.12em] text-muted uppercase leading-tight">
                    {title}
                  </div>
                ) : null}
                <ul className="m-0 flex list-none flex-col items-center gap-2 p-0">
                  {g.links.map((l, j) => {
                    const label = pickLocale(l.label, lang)
                    if (!label || !l.url) return null
                    return (
                      <li key={j}>
                        <a
                          href={l.url}
                          rel="noopener noreferrer"
                          className="inline-block border-b border-transparent text-[13px] font-medium leading-snug text-ink no-underline transition-colors hover:border-border hover:text-ink"
                        >
                          {label}
                        </a>
                      </li>
                    )
                  })}
                </ul>
              </div>
            )
          })}
        </div>
        <div className="flex flex-wrap items-center justify-center gap-x-5 gap-y-1.5 border-t border-border pt-4 text-center text-[11.5px] leading-snug text-muted">
          {metaBits}
          <span className={cn(metaQuiet)}>{t('meta.tagline')}</span>
        </div>
      </div>
    </footer>
  )
}

/**
 * 把 template/innerHTML 解析出的节点挂到 target。
 * 浏览器不会执行 via innerHTML/cloneNode 插入的 <script>，统计脚本会「进了 DOM 却不发请求」。
 * 对 script 用 createElement 重建并拷贝属性/正文，外链与内联均可执行。
 */
function appendHTMLFragment(html: string, target: ParentNode): Node[] {
  const tpl = document.createElement('template')
  tpl.innerHTML = html
  const mounted: Node[] = []

  const activateScripts = (root: ParentNode) => {
    // 静态 NodeList：替换时边遍历边改 DOM 安全
    const scripts = Array.from(root.querySelectorAll('script'))
    for (const old of scripts) {
      const s = document.createElement('script')
      for (const attr of Array.from(old.attributes)) {
        s.setAttribute(attr.name, attr.value)
      }
      s.text = old.textContent ?? ''
      old.replaceWith(s)
    }
  }

  Array.from(tpl.content.childNodes).forEach((n) => {
    if (n.nodeName === 'SCRIPT') {
      const old = n as HTMLScriptElement
      const s = document.createElement('script')
      for (const attr of Array.from(old.attributes)) {
        s.setAttribute(attr.name, attr.value)
      }
      s.text = old.textContent ?? ''
      target.appendChild(s)
      mounted.push(s)
      return
    }
    const c = n.cloneNode(true)
    target.appendChild(c)
    if (c instanceof Element) {
      activateScripts(c)
    }
    mounted.push(c)
  })
  return mounted
}

export function HtmlInject({ inject }: { inject?: HTMLInject | null }) {
  useEffect(() => {
    if (!inject) return
    const headNodes: Node[] = []
    const bodyNodes: Node[] = []

    if (inject.head?.trim()) {
      headNodes.push(...appendHTMLFragment(inject.head, document.head))
    }
    if (inject.body_end?.trim()) {
      bodyNodes.push(...appendHTMLFragment(inject.body_end, document.body))
    }

    return () => {
      headNodes.forEach((n) => n.parentNode?.removeChild(n))
      bodyNodes.forEach((n) => n.parentNode?.removeChild(n))
    }
  }, [inject?.head, inject?.body_end])

  return null
}
