import type { ReactNode } from 'react'
import { useConfig } from '../../api/hooks'
import { useT } from '../../i18n'
import { BrandLockup } from '../../ui/Brand'
import { AuthSourceLink } from './AuthSourceLink'
import { LangToggle } from '../../ui/LangToggle'
import { useGlobal } from '../../store'

const themeBtn =
  'flex h-[30px] w-[30px] cursor-pointer items-center justify-center rounded-sm border border-border bg-surface text-sm text-ink hover:bg-soft'

/** 登录/注册与邮件流公开页共用的品牌壳(左 brand aside + 右表单 pane)。 */
export function AuthShell({ children }: { children: ReactNode }) {
  const { t } = useT()
  const theme = useGlobal((s) => s.theme)
  const toggleTheme = useGlobal((s) => s.toggleTheme)
  const { data: config } = useConfig()
  const siteName = (config?.site_name || '').trim()
  const sourceUrl = (config?.source_url || '').trim()
  return (
    <div className="flex min-h-screen">
      <aside className="relative box-border flex flex-1 flex-col justify-between overflow-hidden bg-btn px-12 py-11 text-btn-text max-[900px]:hidden">
        <div className="relative z-[1] flex items-center gap-[9px]">
          <BrandLockup beta invert word={config?.site_name} />
        </div>
        <div>
          <div className="relative z-[1] mb-4 font-mono text-[11px] tracking-[0.14em] opacity-55">
            {t('meta.slogan')}
          </div>
          <div className="relative z-[1] max-w-[420px] text-[34px] font-extrabold leading-snug tracking-[-0.02em]">
            {t('auth.headlineLine1')}
            <br />
            {t('auth.headlineLine2')}
          </div>
        </div>
        <div className="relative z-[1] font-mono text-xs-plus opacity-45">
          {t('auth.copyright', { year: new Date().getFullYear(), site: siteName })}
        </div>
        <div
          className="absolute -right-[60px] -bottom-[60px] size-[340px] border border-[rgba(128,128,128,0.3)]"
          style={{
            background:
              'repeating-linear-gradient(45deg, transparent, transparent 7px, rgba(128, 128, 128, 0.25) 7px, rgba(128, 128, 128, 0.25) 8px)',
          }}
        />
      </aside>
      <main className="relative flex flex-1 flex-col bg-bg">
        <div className="absolute top-5 right-6 z-[1] flex items-center gap-2">
          <button type="button" className={themeBtn} title={t('nav.toggleTheme')} onClick={toggleTheme}>
            {theme === 'light' ? '◐' : '◑'}
          </button>
          <LangToggle />
        </div>
        <div className="mx-auto my-auto w-[360px] max-w-full animate-[rise_0.3s_both] px-6 py-12">{children}</div>
        <AuthSourceLink url={sourceUrl} />
      </main>
    </div>
  )
}
