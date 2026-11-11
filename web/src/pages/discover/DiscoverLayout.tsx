import { Link, Outlet } from 'react-router'
import { useConfig, useSession } from '../../api/hooks'
import { useT } from '../../i18n'
import { BrandLockup } from '../../ui/Brand'
import { LangToggle } from '../../ui/LangToggle'
import { useGlobal } from '../../store'

/** 发现面公开壳：品牌 + 主题 + 登录态入口，子路由经 Outlet 渲染。 */
export function DiscoverLayout() {
  const { t } = useT()
  const theme = useGlobal((s) => s.theme)
  const toggleTheme = useGlobal((s) => s.toggleTheme)
  const { data: user } = useSession()
  const { data: config } = useConfig()

  return (
    <>
      <header className="sticky top-0 z-10 flex h-14 items-center gap-9 border-b border-border bg-surface px-8 max-md:gap-4 max-md:px-4">
        <Link to="/explore" className="flex items-center text-ink no-underline hover:text-ink" aria-label={config?.site_name?.trim() || undefined}>
          <BrandLockup word={config?.site_name} />
        </Link>
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
          {user ? (
            <Link
              to="/"
              className="inline-flex h-[30px] items-center rounded-sm bg-btn px-3.5 text-sm-plus font-semibold text-btn-text no-underline hover:opacity-90 hover:text-btn-text"
            >
              {t('nav.backToUpload')}
            </Link>
          ) : (
            <>
              <Link to="/login" className="text-sm-plus text-muted no-underline hover:text-ink">
                {t('nav.login')}
              </Link>
              <Link
                to="/login"
                className="inline-flex h-[30px] items-center rounded-sm bg-btn px-3.5 text-sm-plus font-semibold text-btn-text no-underline hover:opacity-90 hover:text-btn-text"
              >
                {t('nav.register')}
              </Link>
            </>
          )}
        </div>
      </header>
      <main className="box-border mx-auto w-full max-w-[1100px] px-8 pt-6 pb-20 max-md:px-5 max-md:pt-4 max-md:pb-16">
        <Outlet />
      </main>
    </>
  )
}
