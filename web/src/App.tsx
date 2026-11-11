import { lazy, Suspense, useEffect, useRef } from 'react'
import { Route, Routes, useNavigate } from 'react-router'
import { setOnForbidden, setOnUnauthorized } from './api/client'
import { useConfig, useSession } from './api/hooks'
import { t, useT } from './i18n'
import { setHtmlLang } from './i18n/lang'
import { useGlobal } from './store'
import { AlbumDetailPage } from './pages/albums/AlbumDetailPage'
import { AlbumsPage } from './pages/albums/AlbumsPage'
import { PublicAlbumPage } from './pages/albums/PublicAlbumPage'
import { AuthPage } from './pages/auth/AuthPage'
import { ForgotPasswordPage } from './pages/auth/ForgotPasswordPage'
import { ResetPasswordPage } from './pages/auth/ResetPasswordPage'
import { ConfirmEmailPage } from './pages/auth/ConfirmEmailPage'
import { VerifyEmailPage } from './pages/auth/VerifyEmailPage'
import { DiscoverLayout } from './pages/discover/DiscoverLayout'
import { ExplorePage } from './pages/discover/ExplorePage'
import { UserPublicPage } from './pages/discover/UserPublicPage'
import { SharePage } from './pages/share/SharePage'
import { AboutPage } from './pages/about/AboutPage'
import { ImagesPage } from './pages/images/ImagesPage'
import { SettingsPage } from './pages/settings/SettingsPage'
import { TrashPage } from './pages/trash/TrashPage'
import { UploadPage } from './pages/upload/UploadPage'
import { AppLayout } from './shell/AppLayout'
import { NotFoundPage } from './shell/NotFoundPage'
import { RequireAuth } from './shell/RequireAuth'
import { RequireAdmin } from './shell/RequireAdmin'
import { RequireAuthOrGuest } from './shell/RequireAuthOrGuest'
import { applySiteTheme } from './lib/siteTheme'
import { InstallPrompt } from './ui/InstallPrompt'
import { AnnouncementBar, HtmlInject } from './ui/SiteSlots'
import { Skeleton } from './ui/Skeleton'
import { Toasts } from './ui/Toasts'

const AdminApp = lazy(() => import('./pages/admin/AdminApp'))

export function App() {
  const navigate = useNavigate()
  const config = useConfig()
  const { data: session } = useSession()
  const { lang, setLang } = useT()
  useEffect(() => {
    setHtmlLang(lang)
  }, [lang])
  // 登录态跨设备同步:仅在服务端语言偏好实际变化时应用(登录/换设备),不依赖本地 lang,
  // 否则本地切换会触发 effect 用旧会话缓存值覆盖刚选的语言(codex 基建评审 F2 竞态)。
  const lastServerLang = useRef<string | undefined>(undefined)
  useEffect(() => {
    const l = session?.preferences?.lang
    if ((l === 'zh' || l === 'en') && l !== lastServerLang.current) {
      lastServerLang.current = l
      setLang(l)
    }
  }, [session?.preferences?.lang, setLang])
  useEffect(() => {
    // 标题只用后台配置的站点名；配置未到时保留服务端写好的静态标题，避免闪回旧品牌。
    const name = config.data?.site_name?.trim()
    if (name) document.title = name
  }, [config.data?.site_name])
  useEffect(() => {
    const href = (config.data?.favicon_url || '').trim()
    const sel = 'link[data-imgli-favicon="1"]'
    document.querySelectorAll(sel).forEach((n) => n.remove())
    if (!href) return
    const link = document.createElement('link')
    link.rel = 'icon'
    link.href = href
    link.setAttribute('data-imgli-favicon', '1')
    document.head.appendChild(link)
    return () => {
      document.querySelectorAll(sel).forEach((n) => n.remove())
    }
  }, [config.data?.favicon_url])
  useEffect(() => {
    applySiteTheme(config.data ?? null)
  }, [
    config.data?.theme_accent,
    config.data?.theme_bg_color,
    config.data?.theme_bg_image_url,
    config.data?.theme_bg_dim,
    config.data?.theme_glass,
  ])
  useEffect(() => {
    setOnUnauthorized(() => {
      if (window.location.pathname === '/login') return
      const next = encodeURIComponent(`${window.location.pathname}${window.location.search}`)
      navigate(`/login?next=${next}`)
    })
    setOnForbidden(() => {
      if (window.location.pathname.startsWith('/admin')) {
        useGlobal.getState().pushToast(t('errors.adminRevoked'))
        navigate('/')
      }
    })
    return () => {
      setOnUnauthorized(null)
      setOnForbidden(null)
    }
  }, [navigate])

  return (
    <>
      <HtmlInject inject={config.data?.html_inject} />
      <AnnouncementBar announcement={config.data?.announcement} />
      <Routes>
        <Route path="/login" element={<AuthPage />} />
        <Route path="/about" element={<AboutPage />} />
        <Route path="/forgot-password" element={<ForgotPasswordPage />} />
        <Route path="/reset-password" element={<ResetPasswordPage />} />
        <Route path="/verify-email" element={<VerifyEmailPage />} />
        <Route path="/confirm-email" element={<ConfirmEmailPage />} />
        <Route element={<DiscoverLayout />}>
          <Route path="/explore" element={<ExplorePage />} />
          <Route path="/u/:username" element={<UserPublicPage />} />
          <Route path="/a/:id" element={<PublicAlbumPage />} />
        </Route>
        <Route path="/s/:key" element={<SharePage />} />
        <Route
          path="/admin/*"
          element={
            <RequireAdmin>
              <Suspense
                fallback={
                  <div style={{ padding: 24 }}>
                    <Skeleton height={220} />
                  </div>
                }
              >
                <AdminApp />
              </Suspense>
            </RequireAdmin>
          }
        />
        <Route element={<RequireAuthOrGuest />}>
          <Route path="/" element={<UploadPage />} />
        </Route>
        <Route
          element={
            <RequireAuth>
              <AppLayout />
            </RequireAuth>
          }
        >
          <Route path="/images" element={<ImagesPage />} />
          <Route path="/albums" element={<AlbumsPage />} />
          <Route path="/albums/:id" element={<AlbumDetailPage />} />
          <Route path="/trash" element={<TrashPage />} />
          <Route path="/settings/:tab?" element={<SettingsPage />} />
          <Route path="*" element={<NotFoundPage />} />
        </Route>
      </Routes>
      <Toasts />
      <InstallPrompt />
    </>
  )
}
