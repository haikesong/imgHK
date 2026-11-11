import { useState, useEffect, type FormEvent } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { ApiError } from '../../api/client'
import { useLogin, useRegister, useConfig } from '../../api/hooks'
import { useT } from '../../i18n'
import { errorText } from '../../i18n/errorText'
import { pickLocale } from '../../lib/locale'
import { STRONG_RE } from '../../lib/password'
import { safeNext } from '../../lib/safeNext'
import { useGlobal } from '../../store'
import { BrandLockup } from '../../ui/Brand'
import { AuthSourceLink } from './AuthSourceLink'
import { Button } from '../../ui/Button'
import { Input } from '../../ui/Input'
import { LangToggle } from '../../ui/LangToggle'
import { Segmented } from '../../ui/Segmented'

type Mode = 'login' | 'reg'

const EMAIL_RE = /^\S+@\S+\.\S+$/

const themeBtn =
  'flex h-[30px] w-[30px] cursor-pointer items-center justify-center rounded-sm border border-border bg-surface text-sm text-ink hover:bg-soft'

export function AuthPage() {
  const { t, lang } = useT()
  const [mode, setMode] = useState<Mode>('login')
  const [username, setUsername] = useState('')
  const [account, setAccount] = useState('')
  const [email, setEmail] = useState('')
  const [pwd, setPwd] = useState('')
  const [invite, setInvite] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [done, setDone] = useState(false)
  const theme = useGlobal((s) => s.theme)
  const toggleTheme = useGlobal((s) => s.toggleTheme)
  const login = useLogin()
  const register = useRegister()
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const afterAuth = safeNext(searchParams.get('next'), '/')
  const busy = login.isPending || register.isPending
  const isLogin = mode === 'login'
  const config = useConfig()
  const regClosed = config.data?.registration_mode === 'closed'
  const regInvite = config.data?.registration_mode === 'invite'
  const siteName = (config.data?.site_name || '').trim()
  const sourceUrl = (config.data?.source_url || '').trim()
  const registerNotice = pickLocale(config.data?.register_notice, lang)
  // 浅场景文案：?from=picgo|blog|private 或 utm_campaign（非多触点 funnel）
  const scenarioCopy = (() => {
    try {
      const q = new URLSearchParams(window.location.search)
      const key = (q.get('from') || q.get('utm_campaign') || '').toLowerCase()
      if (key === 'picgo' || key === 'sharex' || key === 'upic') return t('auth.scenarioPicgo')
      if (key === 'blog' || key === 'markdown' || key === 'typora') return t('auth.scenarioBlog')
      if (key === 'private' || key === 'team') return t('auth.scenarioPrivate')
    } catch {
      /* ignore */
    }
    return ''
  })()
  const helpURL = (config.data?.help_url || '').trim()
  const upgradeURL = (config.data?.upgrade_url || '').trim()
  // 注册关闭时强制回登录模式(含直接停在 reg 态的场景)
  useEffect(() => {
    if (regClosed && mode === 'reg') setMode('login')
  }, [regClosed, mode])

  function submit(e: FormEvent) {
    e.preventDefault()
    if (busy || done) return
    if (!isLogin && !username.trim()) return setError(t('auth.errUsernameRequired'))
    if (isLogin) {
      if (!account.trim()) return setError(t('auth.errAccountRequired'))
      if (!pwd) return setError(t('auth.errPasswordRequired'))
    } else {
      if (!EMAIL_RE.test(email)) return setError(t('auth.errEmailInvalid'))
      if (!STRONG_RE.test(pwd)) return setError(t('auth.errPasswordWeak'))
      if (regInvite && !invite.trim()) return setError(t('auth.errInviteRequired'))
    }
    setError(null)
    const onSuccess = () => {
      setDone(true)
      setTimeout(() => navigate(afterAuth), 400)
    }
    const onError = (err: unknown) =>
      setError(
        err instanceof ApiError
          ? errorText(err.code, err.message)
          : t('auth.requestFailed'),
      )
    if (isLogin) login.mutate({ account: account.trim(), password: pwd }, { onSuccess, onError })
    else {
      const utm = {
        utm_source: searchParams.get('utm_source') || undefined,
        utm_medium: searchParams.get('utm_medium') || undefined,
        utm_campaign: searchParams.get('utm_campaign') || undefined,
      }
      let referer_host: string | undefined
      try {
        if (document.referrer) referer_host = new URL(document.referrer).hostname || undefined
      } catch {
        /* ignore */
      }
      register.mutate(
        {
          username: username.trim(),
          email: email.trim(),
          password: pwd,
          ...(regInvite ? { invite_code: invite.trim().toUpperCase() } : {}),
          ...utm,
          ...(referer_host ? { referer_host } : {}),
        },
        { onSuccess, onError },
      )
    }
  }

  const submitLabel = done
    ? t('auth.successRedirect')
    : busy
      ? t('auth.pleaseWait')
      : isLogin
        ? t('auth.submitLogin')
        : t('auth.submitRegister')

  return (
    <div className="flex min-h-screen">
      <aside className="relative box-border flex flex-1 flex-col justify-between overflow-hidden bg-btn px-12 py-11 text-btn-text max-[900px]:hidden">
        <div className="relative z-[1] flex items-center gap-[9px]">
          <BrandLockup beta invert word={config.data?.site_name} />
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
        <div className="mx-auto my-auto w-[360px] max-w-full animate-[rise_0.3s_both] px-6 py-12">
          <div className="mb-2.5 font-mono text-[11px] tracking-[0.14em] text-muted">
            {isLogin ? t('auth.signInKicker') : t('auth.createAccountKicker')}
          </div>
          <h1 className="mb-6 mt-0 text-2xl font-bold tracking-[-0.015em]">
            {isLogin ? t('auth.welcomeBack') : t('auth.createAccount')}
          </h1>
          {!isLogin && !regClosed && (scenarioCopy || registerNotice) && (
            <p className="mb-3.5 mt-0 text-sm-plus leading-[1.55] text-muted" data-testid="reg-trial-note">
              {scenarioCopy || registerNotice}
              {(helpURL || upgradeURL) && (
                <>
                  {' '}
                  {helpURL && (
                    <a href={helpURL} rel="noopener noreferrer" className="text-ink underline underline-offset-2">
                      {t('auth.helpLink')}
                    </a>
                  )}
                  {helpURL && upgradeURL ? ' · ' : null}
                  {upgradeURL && (
                    <a href={upgradeURL} rel="noopener noreferrer" className="text-ink underline underline-offset-2">
                      {t('auth.upgradeLink')}
                    </a>
                  )}
                </>
              )}
            </p>
          )}

          <div className="mb-6 [&_button]:py-[9px] [&_button]:text-sm-plus [&_button]:font-bold">
            {regClosed ? (
              <div className="border border-dashed border-border px-3 py-2 text-xs text-muted">
                {t('auth.regClosed')}
              </div>
            ) : (
              <Segmented<Mode>
                options={[
                  { value: 'login', label: t('auth.login') },
                  { value: 'reg', label: t('auth.register') },
                ]}
                value={mode}
                onChange={(m) => {
                  setMode(m)
                  setError(null)
                }}
              />
            )}
          </div>

          <form className="flex flex-col gap-3.5" onSubmit={submit} noValidate>
            {!isLogin && (
              <Input
                label={t('auth.username')}
                placeholder={t('auth.usernamePlaceholder')}
                value={username}
                onChange={(e) => setUsername(e.target.value)}
              />
            )}
            {isLogin ? (
              <Input
                label={t('auth.account')}
                placeholder={t('auth.accountPlaceholder')}
                value={account}
                onChange={(e) => setAccount(e.target.value)}
              />
            ) : (
              <Input
                label={t('auth.email')}
                type="email"
                placeholder={t('auth.emailPlaceholder')}
                value={email}
                onChange={(e) => setEmail(e.target.value)}
              />
            )}
            <Input
              label={t('auth.password')}
              type="password"
              placeholder={isLogin ? t('auth.passwordPlaceholderLogin') : t('auth.passwordPlaceholderReg')}
              value={pwd}
              onChange={(e) => setPwd(e.target.value)}
            />
            {isLogin && (
              <div className="-mt-1.5 text-right">
                <Link to="/forgot-password" className="text-xs text-muted hover:text-ink">
                  {t('auth.forgotPassword')}
                </Link>
              </div>
            )}
            {!isLogin && regInvite && (
              <Input
                label={t('auth.inviteCode')}
                placeholder={t('auth.inviteCodePlaceholder')}
                value={invite}
                onChange={(e) => setInvite(e.target.value)}
              />
            )}
            {error && <div className="animate-[fadeIn_0.15s] text-xs text-err">{error}</div>}
            <Button
              variant="primary"
              type="submit"
              data-testid="auth-submit"
              className="mt-1 py-3 text-[13.5px]"
              disabled={busy}
            >
              {submitLabel}
            </Button>
            {isLogin && config.data?.oidc_enabled && (
              <a
                href="/api/v1/auth/oidc/start"
                className="mt-2.5 block py-3 text-center text-[13.5px]"
              >
                <Button variant="secondary" type="button" className="w-full">
                  {t('auth.oidcLogin')}
                </Button>
              </a>
            )}
          </form>
        </div>
        <AuthSourceLink url={sourceUrl} />
      </main>
    </div>
  )
}
