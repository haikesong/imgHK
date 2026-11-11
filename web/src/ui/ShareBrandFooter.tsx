import { Fragment, type ReactNode } from 'react'
import { useT } from '../i18n'
import { cn } from '../lib/cn'

type Props = {
  siteName: string
  branding?: 'off' | 'site' | 'links' | string
  helpURL?: string
  upgradeURL?: string
  className?: string
  /** 默认 share-brand-foot；相册页可用 album-share-brand-foot */
  testId?: string
}

/** 分享页 / 公开相册页脚：可选站名/链接（不再展示上游产品署名）。 */
export function ShareBrandFooter({
  siteName,
  branding = 'off',
  helpURL = '',
  upgradeURL = '',
  className,
  testId = 'share-brand-foot',
}: Props) {
  const { t } = useT()
  const showBrand = branding === 'site' || branding === 'links'
  const name = siteName.trim()
  const help = helpURL.trim()
  const upgrade = upgradeURL.trim()

  const items: ReactNode[] = []
  if (showBrand && name) items.push(<span key="site">{t('share.brandVia', { site: name })}</span>)
  if (branding === 'links' && help) {
    items.push(
      <a key="help" href={help} rel="noopener noreferrer">
        {t('share.helpLink')}
      </a>,
    )
  }
  if (branding === 'links' && upgrade) {
    items.push(
      <a key="upgrade" href={upgrade} rel="noopener noreferrer">
        {t('share.upgradeLink')}
      </a>,
    )
  }
  if (items.length === 0) return null

  return (
    <footer
      className={cn(
        'flex flex-wrap items-center gap-1.5 text-xs text-muted [&_a]:text-ink [&_a]:underline [&_a]:underline-offset-2',
        className,
      )}
      data-testid={testId}
    >
      {items.map((node, i) => (
        <Fragment key={i}>
          {i > 0 ? <span className="opacity-45">·</span> : null}
          {node}
        </Fragment>
      ))}
    </footer>
  )
}
