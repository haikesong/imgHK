import { Outlet } from 'react-router'
import { useConfig, useQuota, useSession } from '../api/hooks'
import { QuotaAlertBar } from '../ui/QuotaAlertBar'
import { SiteFooter } from '../ui/SiteSlots'
import { Nav } from './Nav'
import { TabBar } from './TabBar'

export function AppLayout() {
  const { data: user } = useSession()
  const { data: config } = useConfig()
  const quota = useQuota()
  if (!user) return null
  return (
    <div className="flex min-h-dvh flex-col">
      <Nav user={user} />
      {quota.data && (
        <QuotaAlertBar used={quota.data.used} total={quota.data.total} upgradeUrl={config?.upgrade_url} />
      )}
      <main className="box-border w-full flex-[1_0_auto] px-8 pb-12 max-md:px-5 max-md:pb-[100px]">
        <Outlet />
      </main>
      <SiteFooter
        footer={config?.footer}
        siteName={config?.site_name}
        sourceUrl={config?.source_url}
        aboutEnabled={!!config?.about_enabled}
      />
      <TabBar />
    </div>
  )
}
