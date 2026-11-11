import { Link, Outlet } from 'react-router'
import { useConfig } from '../api/hooks'
import { useT } from '../i18n'
import { loginHref } from '../lib/safeNext'
import { BrandLockup } from '../ui/Brand'
import { LangToggle } from '../ui/LangToggle'
import { SiteFooter } from '../ui/SiteSlots'
import { useGlobal } from '../store'

/** 未登录精简布局：品牌 + 主题 + 登录入口；可看上传页（是否可传由后端与 UploadPage 提示）。 */
export function GuestLayout() {
  const { t } = useT()
  const theme = useGlobal((s) => s.theme)
  const toggleTheme = useGlobal((s) => s.toggleTheme)
  const { data: config } = useConfig()
  const guestOn = !!config?.guest_upload_enabled
  return (
    <div className="flex min-h-dvh flex-col">
      <header className="sticky top-0 z-10 flex h-14 items-center gap-9 border-b border-border bg-surface px-8 max-md:gap-4 max-md:px-4">
        <span className="flex items-center text-ink" aria-label={config?.site_name?.trim() || undefined}>
          <BrandLockup badge={guestOn ? 'GUEST' : undefined} word={config?.site_name} />
        </span>
        <div className="ml-auto flex items-center gap-3.5">
          <button
            type="button"
            className="flex h-[30px] w-[30px] cursor-pointer items-center justify-center rounded-sm border border-border bg-surface text-sm text-ink hover:bg-soft"
            title={t('nav.toggleTheme')}
            onClick={toggleTheme}
          >
            {theme === 'light' ? '◐' : '◑'}
          </button>
          <LangToggle />
          {config?.plaza_enabled && (
            <Link to="/explore" className="text-sm-plus font-semibold text-muted no-underline hover:text-ink">
              {t('nav.plaza')}
            </Link>
          )}
          <Link
            to={loginHref('/')}
            className="inline-flex h-[30px] items-center rounded-sm bg-btn px-3.5 text-sm-plus font-semibold text-btn-text hover:opacity-90 hover:text-btn-text"
          >
            {guestOn ? t('nav.loginToManage') : t('nav.loginOrRegister')}
          </Link>
        </div>
      </header>
      <main className="box-border w-full flex-[1_0_auto] px-8 pb-12 max-md:px-5">
        <Outlet />
      </main>
      <SiteFooter
        footer={config?.footer}
        siteName={config?.site_name}
        sourceUrl={config?.source_url}
        aboutEnabled={!!config?.about_enabled}
      />
    </div>
  )
}
