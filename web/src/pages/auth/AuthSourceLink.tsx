import { useT } from '../../i18n'

/**
 * AGPL-3.0 §13 source offer for the public auth pages.
 * Rendered only when the operator configured `source_url`.
 */
export function AuthSourceLink({ url }: { url?: string | null }) {
  const { t } = useT()
  const href = (url || '').trim()
  if (!href) return null
  return (
    <div className="px-6 pb-6 text-center font-mono text-[11px] text-muted">
      <a
        href={href}
        rel="noopener noreferrer"
        className="border-b border-transparent text-inherit no-underline transition-colors hover:border-border hover:text-ink"
      >
        {t('common.sourceCode')}
      </a>
    </div>
  )
}
