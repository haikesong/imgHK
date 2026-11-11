import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { App } from '../App'
import type { User } from '../api/types'

const GB = 1024 ** 3
const user: User = {
  id: 1,
  username: 'ling',
  email: 'ling@img.li',
  nickname: '凌',
  is_admin: false,
  email_verified: true,
  created_at: '2026-07-16T00:00:00Z',
  preferences: {
    default_album_id: null,
    default_visibility: '',
    default_policy_id: null,
    auto_copy_format: '',
    watermark: { enabled: false, position: '', opacity: 0, margin: 0 },
  },
  avatar_url: '',
  watermark_set: false,
  public_profile: false,
}

function jsonRes(body: unknown, status = 200): Response {
  return { ok: status < 400, status, json: () => Promise.resolve(body) } as unknown as Response
}
const env = (data: unknown) => ({ status: true, message: 'ok', data })

function mockBackend(
  loggedIn: boolean,
  guestEnabled = false,
  sessionUser: User = user,
  plazaEnabled = false,
) {
  vi.stubGlobal(
    'fetch',
    vi.fn((url: RequestInfo | URL) => {
      const u = String(url)
      if (u.endsWith('/auth/session'))
        return Promise.resolve(
          loggedIn
            ? jsonRes(env(sessionUser))
            : jsonRes({ status: false, message: '未登录', data: { code: 'unauthorized' } }, 401),
        )
      if (u.endsWith('/config'))
        return Promise.resolve(
          jsonRes(env({
            site_name: 'img.li', registration_mode: 'open', guest_upload_enabled: guestEnabled,
            guest: { max_file_size: 5 * 1024 ** 2, allowed_exts: ['png', 'jpg'], per_day: 3 },
            plaza_enabled: plazaEnabled,
          })),
        )
      if (u.endsWith('/user/quota'))
        return Promise.resolve(jsonRes(env({
          used: 2.14 * GB, total: 10 * GB, max_file_size: 20 * 1024 ** 2, allowed_exts: ['png','jpg','gif','webp'],
          bandwidth_used_month: 1 * GB, bandwidth_quota_month: 5 * GB, bandwidth_period: '2026-07',
        })))
      return Promise.resolve(jsonRes({ status: false, message: '', data: { code: 'not_found' } }, 404))
    }),
  )
}

function renderAt(path: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[path]}>
        <App />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

afterEach(() => vi.unstubAllGlobals())

it('已登录：/ 渲染导航与占位页', async () => {
  mockBackend(true)
  renderAt('/')
  expect(await screen.findByRole('link', { name: '首页' })).toBeInTheDocument()
  expect(screen.getByRole('link', { name: '我的图片' })).toHaveAttribute('href', '/images')
  expect(screen.getByRole('link', { name: '相册' })).toBeInTheDocument()
  expect(screen.getByRole('link', { name: '设置' })).toBeInTheDocument()
  // plaza_enabled 默认 false：广场入口不挂 DOM
  expect(screen.queryByRole('link', { name: '广场' })).not.toBeInTheDocument()
  // Nav 紧凑簇 + 上传页并排条
  expect(await screen.findByTestId('nav-quota-cluster')).toBeInTheDocument()
  expect((await screen.findAllByText('2.14 / 10 GB')).length).toBeGreaterThanOrEqual(1)
  expect(screen.getByText('STOR')).toBeInTheDocument()
  expect(screen.getByText('BW')).toBeInTheDocument()
  expect(screen.getByText('上传图片')).toBeInTheDocument()
})

it('已登录 + plaza_enabled：导航出现广场入口', async () => {
  mockBackend(true, false, user, true)
  renderAt('/')
  const plaza = await screen.findByRole('link', { name: '广场' })
  expect(plaza).toHaveAttribute('href', '/explore')
})

it('未登录访问受保护路由跳 /login', async () => {
  mockBackend(false)
  renderAt('/images')
  expect(await screen.findByText('欢迎回来')).toBeInTheDocument()
})

it('站内未知路径渲染 404 页', async () => {
  mockBackend(true)
  renderAt('/nope/deep')
  expect(await screen.findByText('页面不存在')).toBeInTheDocument()
})

it('移动端 4-Tab 存在且指向四页面', async () => {
  mockBackend(true)
  renderAt('/')
  await screen.findByRole('link', { name: '首页' })
  const tabbar = screen.getByTestId('tabbar')
  const links = [...tabbar.querySelectorAll('a')].map((a) => a.getAttribute('href'))
  expect(links).toEqual(['/', '/images', '/albums', '/settings'])
})

it('未登录+游客开关开：/ 渲染游客上传页（无导航/无 TabBar）', async () => {
  mockBackend(false, true)
  renderAt('/')
  expect(await screen.findByText('上传图片')).toBeInTheDocument()
  expect(screen.getByRole('link', { name: '登录以管理图片' })).toHaveAttribute('href', '/login?next=%2F')
  expect(screen.queryByRole('link', { name: '我的图片' })).not.toBeInTheDocument()
  expect(screen.queryByTestId('tabbar')).not.toBeInTheDocument()
  await waitFor(() => {})
})

it('未登录+游客开关关：/ 仍展示上传落地页并提示登录', async () => {
  mockBackend(false, false)
  renderAt('/')
  expect(await screen.findByText('上传图片')).toBeInTheDocument()
  expect(screen.getByTestId('login-gate')).toBeInTheDocument()
  const loginLinks = screen.getAllByRole('link', { name: '登录 / 注册' })
  expect(loginLinks.length).toBeGreaterThanOrEqual(1)
  expect(loginLinks.every((a) => a.getAttribute('href') === '/login?next=%2F')).toBe(true)
  expect(screen.queryByText('欢迎回来')).not.toBeInTheDocument()
})

it('Nav 头像:avatar_url 非空渲染 img', async () => {
  mockBackend(true, false, { ...user, avatar_url: '/avatar/1?v=9' })
  renderAt('/')
  await screen.findByRole('link', { name: '首页' })
  const img = document.querySelector('img[src="/avatar/1?v=9"]')
  expect(img).toBeTruthy()
})

it('Nav 头像:avatar_url 空时显示首字母无 img', async () => {
  mockBackend(true, false, { ...user, avatar_url: '' })
  renderAt('/')
  await screen.findByRole('link', { name: '首页' })
  expect(document.querySelector('header img')).toBeNull()
  // nickname 凌 → 按钮文本为首字母
  expect(screen.getByRole('button', { name: '凌' })).toBeInTheDocument()
})
