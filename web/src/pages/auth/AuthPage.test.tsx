import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router'
import { AuthPage } from './AuthPage'

function jsonRes(body: unknown, status = 200): Response {
  return { ok: status < 400, status, json: () => Promise.resolve(body) } as unknown as Response
}

function renderPage(entry = '/login') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[entry]}>
        <Routes>
          <Route path="/login" element={<AuthPage />} />
          <Route path="/" element={<div>HOME</div>} />
          <Route path="/images" element={<div>IMAGES</div>} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

afterEach(() => vi.unstubAllGlobals())

// user-event + vitest 假定时器组合在此环境下会挂起（advanceTimers 绑定/内部调度问题），
// 改用 fireEvent（同步）+ act 包裹，语义保持不变：仍验证请求体、成功文案、400ms 后跳转。
it('登录成功后跳转首页', async () => {
  vi.useFakeTimers()
  const f = vi.fn((url: RequestInfo | URL, init?: RequestInit) => {
    void init
    const u = String(url)
    if (u.endsWith('/config'))
      return Promise.resolve(jsonRes({ status: true, message: 'ok', data: { site_name: 'img.li', registration_mode: 'open', guest_upload_enabled: false, guest: null } }))
    return Promise.resolve(jsonRes({ status: true, message: 'ok', data: { id: 1, username: 'ling' } }))
  })
  vi.stubGlobal('fetch', f)
  renderPage()

  act(() => {
    fireEvent.change(screen.getByLabelText('账号'), { target: { value: 'ling' } })
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'passw0rd' } })
  })
  await act(async () => {
    fireEvent.click(screen.getByTestId('auth-submit'))
    await vi.advanceTimersByTimeAsync(0)
  })

  const loginCall = f.mock.calls.find((call) => String(call[0]).endsWith('/auth/login'))
  expect(loginCall).toBeDefined()
  if (loginCall) {
    const [url, init] = loginCall
    expect(url).toBe('/api/v1/auth/login')
    expect(JSON.parse(init?.body as string)).toEqual({ account: 'ling', password: 'passw0rd' })
  }
  expect(screen.getByText(/成功，正在跳转/)).toBeInTheDocument()
  await act(async () => {
    await vi.advanceTimersByTimeAsync(500)
  })
  expect(screen.getByText('HOME')).toBeInTheDocument()
  vi.useRealTimers()
})

it('登录成功后跳转 next 指定路径', async () => {
  vi.useFakeTimers()
  const f = vi.fn((url: RequestInfo | URL, init?: RequestInit) => {
    void init
    const u = String(url)
    if (u.endsWith('/config'))
      return Promise.resolve(jsonRes({ status: true, message: 'ok', data: { site_name: 'img.li', registration_mode: 'open', guest_upload_enabled: false, guest: null } }))
    return Promise.resolve(jsonRes({ status: true, message: 'ok', data: { id: 1, username: 'ling' } }))
  })
  vi.stubGlobal('fetch', f)
  renderPage('/login?next=%2Fimages')

  act(() => {
    fireEvent.change(screen.getByLabelText('账号'), { target: { value: 'ling' } })
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'passw0rd' } })
  })
  await act(async () => {
    fireEvent.click(screen.getByTestId('auth-submit'))
    await vi.advanceTimersByTimeAsync(0)
  })
  await act(async () => {
    await vi.advanceTimersByTimeAsync(500)
  })
  expect(screen.getByText('IMAGES')).toBeInTheDocument()
  vi.useRealTimers()
})

it('注册模式校验用户名与密码强度', async () => {
  const user = userEvent.setup()
  vi.stubGlobal('fetch', vi.fn((url: RequestInfo | URL) => {
    const u = String(url)
    if (u.endsWith('/config'))
      return Promise.resolve(jsonRes({ status: true, message: 'ok', data: { site_name: 'img.li', registration_mode: 'open', guest_upload_enabled: false, guest: null } }))
    return Promise.resolve(jsonRes({ status: false, message: '', data: { code: 'not_found' } }, 404))
  }))
  renderPage()
  await waitFor(() => expect(screen.queryByRole('button', { name: '注册' })).toBeInTheDocument())
  await user.click(screen.getByRole('button', { name: '注册' }))

  await user.type(screen.getByLabelText('邮箱'), 'a@b.co')
  await user.type(screen.getByLabelText('密码'), 'short')
  await user.click(screen.getByTestId('auth-submit'))
  expect(screen.getByText('请输入用户名')).toBeInTheDocument()

  await user.type(screen.getByLabelText('用户名'), 'ling')
  await user.click(screen.getByTestId('auth-submit'))
  expect(screen.getByText('密码至少 8 位且包含字母和数字')).toBeInTheDocument()
  // 校验失败不发 register（允许 LangToggle 的 /session 等旁路请求）
  expect((fetch as any).mock.calls.filter((c: unknown[]) => String(c[0]).endsWith('/auth/register'))).toHaveLength(0)
})

it('邮箱格式非法行内报错', async () => {
  const user = userEvent.setup()
  vi.stubGlobal('fetch', vi.fn((url: RequestInfo | URL) => {
    const u = String(url)
    if (u.endsWith('/config'))
      return Promise.resolve(jsonRes({ status: true, message: 'ok', data: { site_name: 'img.li', registration_mode: 'open', guest_upload_enabled: false, guest: null } }))
    return Promise.resolve(jsonRes({ status: false, message: '', data: { code: 'not_found' } }, 404))
  }))
  renderPage()
  await waitFor(() => expect(screen.queryByRole('button', { name: '注册' })).toBeInTheDocument())
  await user.click(screen.getByRole('button', { name: '注册' }))
  await user.type(screen.getByLabelText('用户名'), 'ling')
  await user.type(screen.getByLabelText('邮箱'), 'not-an-email')
  await user.type(screen.getByLabelText('密码'), 'passw0rd1')
  await user.click(screen.getByTestId('auth-submit'))
  expect(screen.getByText('请输入有效的邮箱地址')).toBeInTheDocument()
  // 校验失败不发 register（允许 LangToggle 的 /session 等旁路请求）
  expect((fetch as any).mock.calls.filter((c: unknown[]) => String(c[0]).endsWith('/auth/register'))).toHaveLength(0)
})

it('服务端错误显示后端 message', async () => {
  const user = userEvent.setup()
  vi.stubGlobal(
    'fetch',
    vi.fn((url: RequestInfo | URL) => {
      const u = String(url)
      if (u.endsWith('/config'))
        return Promise.resolve(jsonRes({ status: true, message: 'ok', data: { site_name: 'img.li', registration_mode: 'open', guest_upload_enabled: false, guest: null } }))
      return Promise.resolve(jsonRes({ status: false, message: '账号或密码错误', data: { code: 'invalid_credentials' } }, 401))
    }),
  )
  renderPage()
  await waitFor(() => expect(screen.queryByLabelText('账号')).toBeInTheDocument())
  await user.type(screen.getByLabelText('账号'), 'ling')
  await user.type(screen.getByLabelText('密码'), 'wrongpass1')
  await user.click(screen.getByTestId('auth-submit'))
  expect(await screen.findByText('账号或密码错误')).toBeInTheDocument()
})

it('登录态渲染忘记密码链接；不渲染邀请码/OAuth', () => {
  vi.stubGlobal('fetch', vi.fn((url: RequestInfo | URL) => {
    const u = String(url)
    if (u.endsWith('/config'))
      return Promise.resolve(jsonRes({ status: true, message: 'ok', data: { site_name: 'img.li', registration_mode: 'open', guest_upload_enabled: false, guest: null } }))
    return Promise.resolve(jsonRes({ status: false, message: '', data: { code: 'not_found' } }, 404))
  }))
  renderPage()
  const forgot = screen.getByRole('link', { name: '忘记密码?' })
  expect(forgot).toBeInTheDocument()
  expect(forgot).toHaveAttribute('href', '/forgot-password')
  expect(screen.queryByText(/邀请码/)).not.toBeInTheDocument()
  expect(screen.queryByText(/GitHub/)).not.toBeInTheDocument()
})

function mockConfigFetch(mode: string) {
  vi.stubGlobal('fetch', vi.fn((url: RequestInfo | URL) => {
    const u = String(url)
    if (u.endsWith('/config'))
      return Promise.resolve(jsonRes({ status: true, message: 'ok', data: { site_name: 'img.li', registration_mode: mode, guest_upload_enabled: false, guest: null } }))
    return Promise.resolve(jsonRes({ status: false, message: '', data: { code: 'not_found' } }, 404))
  }))
}

it('注册关闭：隐藏注册入口并提示', async () => {
  mockConfigFetch('closed')
  renderPage()
  await waitFor(() => expect(screen.queryByText(/已关闭注册/)).toBeInTheDocument())
  expect(screen.queryByRole('button', { name: '注册' })).not.toBeInTheDocument()
})

it('注册开放：显示登录/注册切换', async () => {
  mockConfigFetch('open')
  renderPage()
  await waitFor(() => expect(screen.queryByRole('button', { name: '注册' })).toBeInTheDocument())
})

it('邀请模式：注册表单含邀请码框,提交体带规整后的码', async () => {
  const calls: Array<{ url: string; body: unknown }> = []
  vi.stubGlobal('fetch', vi.fn((url: RequestInfo | URL, init?: RequestInit) => {
    const u = String(url)
    if (u.endsWith('/config'))
      return Promise.resolve(jsonRes({ status: true, message: 'ok', data: { site_name: 'img.li', registration_mode: 'invite', guest_upload_enabled: false, guest: null } }))
    if (u.endsWith('/auth/register')) {
      calls.push({ url: u, body: JSON.parse(String(init?.body)) })
      return Promise.resolve(jsonRes({ status: true, message: 'ok', data: { id: 2, username: 'ivy' } }))
    }
    return Promise.resolve(jsonRes({ status: false, message: '', data: { code: 'not_found' } }, 404))
  }))
  renderPage()
  const user = userEvent.setup()
  await user.click(await screen.findByRole('button', { name: '注册' }))
  expect(screen.getByLabelText('邀请码')).toBeInTheDocument()
  await user.type(screen.getByLabelText('用户名'), 'ivy')
  await user.type(screen.getByLabelText('邮箱'), 'ivy@img.li')
  await user.type(screen.getByLabelText('密码'), 'passw0rd-1')
  await user.type(screen.getByLabelText('邀请码'), ' il-g88d-g88d ')
  await user.click(screen.getByTestId('auth-submit'))
  await waitFor(() => expect(calls).toHaveLength(1))
  expect((calls[0].body as { invite_code: string }).invite_code).toBe('IL-G88D-G88D')
})

it('邀请模式：邀请码留空提交给行内错误', async () => {
  mockConfigFetch('invite')
  renderPage()
  const user = userEvent.setup()
  await user.click(await screen.findByRole('button', { name: '注册' }))
  await user.type(screen.getByLabelText('用户名'), 'ivy')
  await user.type(screen.getByLabelText('邮箱'), 'ivy@img.li')
  await user.type(screen.getByLabelText('密码'), 'passw0rd-1')
  await user.click(screen.getByTestId('auth-submit'))
  expect(screen.getByText('请输入邀请码')).toBeInTheDocument()
})

it('开放模式：注册表单无邀请码框', async () => {
  mockConfigFetch('open')
  renderPage()
  const user = userEvent.setup()
  await user.click(await screen.findByRole('button', { name: '注册' }))
  expect(screen.queryByLabelText('邀请码')).not.toBeInTheDocument()
})

it('登录页页脚版权行只显示 © 年份 站点名，并提供 AGPL 源码入口', async () => {
  vi.stubGlobal('fetch', vi.fn((url: RequestInfo | URL) => {
    const u = String(url)
    if (u.endsWith('/config'))
      return Promise.resolve(jsonRes({ status: true, message: 'ok', data: {
        site_name: 'HKSimg',
        registration_mode: 'open',
        guest_upload_enabled: false,
        guest: null,
        source_url: 'https://github.com/haikesong/imgHK',
      } }))
    return Promise.resolve(jsonRes({ status: false, message: '', data: { code: 'not_found' } }, 404))
  }))
  renderPage()
  // 精确匹配整行：页脚版权行只允许 `© 年份 站点名`。
  // 上游以后若在尾部追加任何署名/品牌文案，这条断言会直接失败。
  const year = new Date().getFullYear()
  expect(await screen.findByText(`© ${year} HKSimg`)).toBeInTheDocument()
  expect(screen.queryByText(/开源项目 imgli/)).not.toBeInTheDocument()
  expect(screen.getByRole('link', { name: '源码' })).toHaveAttribute(
    'href',
    'https://github.com/haikesong/imgHK',
  )
  // 切到英文后版权行同样只允许 `© 年份 站点名`
  fireEvent.click(screen.getByRole('button', { name: 'language' }))
  expect(await screen.findByText(`© ${year} HKSimg`)).toBeInTheDocument()
})
