import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router'
import { useGlobal } from '../../store'
import { PublicAlbumPage } from './PublicAlbumPage'

function jsonRes(body: unknown, status = 200): Response {
  return { ok: status < 400, status, json: () => Promise.resolve(body) } as unknown as Response
}
const env = (data: unknown) => ({ status: true, message: 'ok', data })

function mockBackend(opts: {
  notFound?: boolean
  empty?: boolean
  plazaBrand?: 'off' | 'site' | 'links'
  publicProfile?: boolean
  /** undefined/true → click opens immersive; false → share link */
  clickToImmersive?: boolean
} = {}) {
  const branding = opts.plazaBrand ?? 'site'
  vi.stubGlobal(
    'fetch',
    vi.fn((url: RequestInfo | URL) => {
      const u = String(url)
      if (u.includes('/config'))
        return Promise.resolve(
          jsonRes(
            env({
              site_name: 'img.li',
              registration_mode: 'open',
              guest_upload_enabled: false,
              plaza_enabled: true,
              share_branding: branding,
              help_url: branding === 'links' ? 'https://help.example' : '',
              upgrade_url: '',
              guest: null,
            }),
          ),
        )
      if (u.includes('/a/') && u.includes('/images')) {
        if (opts.empty) return Promise.resolve(jsonRes(env({ items: [], next_cursor: '' })))
        return Promise.resolve(
          jsonRes(
            env({
              items: [
                {
                  key: 'k1',
                  name: 'one.png',
                  ext: 'png',
                  width: 800,
                  height: 600,
                  size: 100,
                  thumbnail_url: '/t/k1.jpg',
                  url: '/i/k1.png',
                  share_path: '/s/k1',
                },
                {
                  key: 'k2',
                  name: 'two.png',
                  ext: 'png',
                  width: 400,
                  height: 900,
                  size: 120,
                  thumbnail_url: '/t/k2.jpg',
                  url: '/i/k2.png',
                  share_path: '/s/k2',
                },
              ],
              next_cursor: '',
            }),
          ),
        )
      }
      if (u.includes('/a/') && !u.includes('/images')) {
        if (opts.notFound)
          return Promise.resolve(jsonRes({ status: false, message: 'no', data: { code: 'not_found' } }, 404))
        return Promise.resolve(
          jsonRes(
            env({
              id: 7,
              name: '旅行',
              visibility: 'public',
              image_count: opts.empty ? 0 : 2,
              cover_key: opts.empty ? '' : 'k1',
              default_view: 'gallery',
              click_to_immersive: opts.clickToImmersive !== false,
              owner: {
                username: 'alice',
                nickname: 'Alice',
                public_profile: opts.publicProfile !== false,
              },
            }),
          ),
        )
      }
      return Promise.resolve(jsonRes({ status: false, message: '', data: { code: 'not_found' } }, 404))
    }),
  )
}

function renderPage(path = '/a/7') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/a/:id" element={<PublicAlbumPage />} />
          <Route path="/s/:key" element={<div>SHARE</div>} />
          <Route path="/u/:username" element={<div>PROFILE</div>} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

class FakeIO {
  constructor(public cb: IntersectionObserverCallback) {}
  observe() {}
  disconnect() {}
  unobserve() {}
}

beforeEach(() => {
  useGlobal.setState({ toasts: [], lang: 'zh' })
  vi.stubGlobal('IntersectionObserver', FakeIO as unknown as typeof IntersectionObserver)
  mockBackend()
})
afterEach(() => vi.unstubAllGlobals())

it('渲染相册名、作者链、复制相册链接与页脚 branding', async () => {
  const user = userEvent.setup()
  const writeText = vi.fn().mockResolvedValue(undefined)
  Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })

  renderPage()
  expect(await screen.findByRole('heading', { name: '旅行' })).toBeInTheDocument()
  expect(screen.getByText(/2 张公开图片/)).toBeInTheDocument()
  expect(await screen.findByTestId('album-hero')).toBeInTheDocument()
  expect(await screen.findByTestId('album-masonry')).toBeInTheDocument()
  await waitFor(() => {
    expect(document.title).toMatch(/旅行/)
    expect(document.title).toMatch(/img\.li/)
  })
  const author = screen.getByRole('link', { name: /作者 Alice/ })
  expect(author).toHaveAttribute('href', '/u/alice')
  expect(screen.getByTestId('album-share-brand-foot')).toHaveTextContent(/本站 img\.li/)
  expect(screen.getByTestId('album-share-brand-foot')).not.toHaveTextContent(/开源图床|图鲤|imgli/)

  await user.click(screen.getAllByRole('button', { name: '复制相册链接' })[0]!)
  await waitFor(() => {
    expect(writeText).toHaveBeenCalledWith(`${window.location.origin}/a/7`)
  })
  expect(useGlobal.getState().toasts.some((t) => t.message.includes('访客链接'))).toBe(true)
})

it('空态说明双重可见性', async () => {
  mockBackend({ empty: true })
  renderPage()
  expect(await screen.findByText('相册里还没有可展示的图片')).toBeInTheDocument()
  expect(screen.getByText(/相册公开不等于/)).toBeInTheDocument()
})

it('点图进入沉浸：胶片条、切换下一张、请求全屏', async () => {
  const user = userEvent.setup()
  const writeText = vi.fn().mockResolvedValue(undefined)
  Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
  const requestFullscreen = vi.fn().mockResolvedValue(undefined)
  HTMLElement.prototype.requestFullscreen = requestFullscreen

  renderPage()
  expect(await screen.findByTestId('album-scroll-sentinel')).toBeInTheDocument()
  const thumb = await screen.findByRole('button', { name: 'one.png' })
  await user.click(thumb)
  const shell = await screen.findByTestId('album-immersive')
  expect(shell).toHaveAttribute('role', 'dialog')
  await waitFor(() => expect(requestFullscreen).toHaveBeenCalled())
  expect(within(shell).getByRole('link', { name: '打开分享页 →' })).toHaveAttribute('href', '/s/k1')
  expect(within(shell).getByText('1 / 2')).toBeInTheDocument()
  expect(within(shell).getByTestId('album-filmstrip')).toBeInTheDocument()
  expect(within(shell).getByTestId('album-filmstrip-track')).toBeInTheDocument()

  await user.click(within(shell).getByRole('button', { name: '复制分享页' }))
  await waitFor(() => {
    expect(writeText).toHaveBeenCalledWith(`${window.location.origin}/s/k1`)
  })

  // 胶片条点第二张
  await user.click(within(shell).getByRole('button', { name: 'two.png' }))
  expect(await within(shell).findByText('2 / 2')).toBeInTheDocument()
  expect(within(shell).getByRole('link', { name: '打开分享页 →' })).toHaveAttribute('href', '/s/k2')
})

it('click_to_immersive=false 时缩略图为分享页链接且不进沉浸', async () => {
  mockBackend({ clickToImmersive: false })
  renderPage()
  await screen.findByTestId('album-masonry')
  const thumb = await screen.findByRole('link', { name: 'one.png' })
  expect(thumb).toHaveAttribute('href', '/s/k1')
  expect(screen.queryByRole('button', { name: 'one.png' })).not.toBeInTheDocument()
  expect(screen.queryByTestId('album-immersive')).not.toBeInTheDocument()
})

it('深链 ?view=immersive&i=2 直接进沉浸第二张', async () => {
  renderPage('/a/7?view=immersive&i=2')
  const shell = await screen.findByTestId('album-immersive')
  expect(within(shell).getByText('2 / 2')).toBeInTheDocument()
  expect(within(shell).getByRole('link', { name: '打开分享页 →' })).toHaveAttribute('href', '/s/k2')
})

it('页头切换：画廊 ↔ 沉浸', async () => {
  const user = userEvent.setup()
  renderPage()
  await screen.findByTestId('album-masonry')
  await user.click(screen.getByRole('button', { name: '沉浸' }))
  expect(await screen.findByTestId('album-immersive')).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: '画廊' }))
  await waitFor(() => {
    expect(screen.queryByTestId('album-immersive')).not.toBeInTheDocument()
  })
})

it('404 公开相册', async () => {
  mockBackend({ notFound: true })
  renderPage()
  expect(await screen.findByText('公开相册不存在或未公开')).toBeInTheDocument()
})

it('owner 未开公开主页时不链 /u', async () => {
  mockBackend({ publicProfile: false })
  renderPage()
  expect(await screen.findByTestId('album-hero')).toBeInTheDocument()
  expect(screen.getByText(/作者 Alice/)).toBeInTheDocument()
  expect(screen.queryByRole('link', { name: /作者 Alice/ })).not.toBeInTheDocument()
})
