import { act, fireEvent, render, screen } from '@testing-library/react'
import { InstallPrompt } from './InstallPrompt'

const DISMISS_KEY = 'imgli-pwa-dismissed'

afterEach(() => {
  localStorage.removeItem(DISMISS_KEY)
})

function dispatchBeforeInstallPrompt(prompt = vi.fn().mockResolvedValue(undefined)) {
  const evt = new Event('beforeinstallprompt') as Event & {
    prompt: () => Promise<void>
    userChoice: Promise<{ outcome: string }>
  }
  evt.prompt = prompt
  evt.userChoice = Promise.resolve({ outcome: 'accepted' })
  Object.defineProperty(evt, 'prompt', { value: prompt, writable: true })
  act(() => {
    window.dispatchEvent(evt)
  })
  return { evt, prompt }
}

it('初始无 beforeinstallprompt 时不渲染', () => {
  render(<InstallPrompt />)
  expect(screen.queryByText('安装到主屏')).not.toBeInTheDocument()
})

it('beforeinstallprompt 后显示安装文案与按钮', () => {
  render(<InstallPrompt />)
  dispatchBeforeInstallPrompt()
  expect(screen.getByText('把 HKS图床 装到主屏，像应用一样打开')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: '安装到主屏' })).toBeInTheDocument()
  expect(screen.getByRole('button', { name: '不再提示' })).toBeInTheDocument()
})

it('点击安装按钮调用 evt.prompt()', () => {
  render(<InstallPrompt />)
  const { prompt } = dispatchBeforeInstallPrompt()
  fireEvent.click(screen.getByRole('button', { name: '安装到主屏' }))
  expect(prompt).toHaveBeenCalledOnce()
})

it('点击 dismiss 写入 localStorage 且不再渲染', () => {
  render(<InstallPrompt />)
  dispatchBeforeInstallPrompt()
  fireEvent.click(screen.getByRole('button', { name: '不再提示' }))
  expect(localStorage.getItem(DISMISS_KEY)).toBe('1')
  expect(screen.queryByText('安装到主屏')).not.toBeInTheDocument()
})
