import {
  BrandMark,
  Button,
  FormField,
  Input,
  LoadingButton,
  Notice,
  Panel,
  Skeleton,
} from '@x0c/ui'
import { KeyRound } from 'lucide-react'
import { useCallback, useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { errorMessage, postBody, requestJSON } from '../lib/api'
import { loadExternalAuth } from '../lib/external-auth'
import { assignLocation, replaceHistory, replaceLocation } from '../lib/navigation'
import { prepareCreationOptions, prepareRequestOptions, serializeCredential } from '../lib/passkeys'
import type { AuthConfig, SignInResult } from '../types'

const SSO_ERRORS: Record<string, string> = {
  not_allowed: 'This account is not allowed. Ask the operator to add your email to the allowlist.',
  unverified_email: 'Your organization has not confirmed your email address. Contact your administrator.',
  expired: 'The sign-in request expired. Please try again.',
  access_denied: 'Sign-in was cancelled at the identity provider.',
  provider_unavailable: 'Could not connect to your sign-in provider. Please try again.',
  login_failed: 'Sign-in failed. Please try again.',
}

function LoginFrame({ children }: { children: ReactNode }) {
  return (
    <main className="login-shell">
      <div className="login-identity">
        <BrandMark size="lg" label="shell-gate" />
        <h1 className="login-wordmark">shell-gate</h1>
        <p className="login-tagline">Your terminal workspace</p>
      </div>
      <Panel flush className="mb-0">
        <div className="p-6">{children}</div>
      </Panel>
    </main>
  )
}

export function LoginPage() {
  const [phase, setPhase] = useState<'loading' | 'ready' | 'error'>('loading')
  const [config, setConfig] = useState<AuthConfig | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [ssoPending, setSSOPending] = useState(false)
  const [mfaRequired, setMFARequired] = useState(false)
  const [recoveryMode, setRecoveryMode] = useState(false)
  const [setupOpen, setSetupOpen] = useState(false)
  const [loginEmail, setLoginEmail] = useState('')
  const [loginPassword, setLoginPassword] = useState('')
  const [email, setEmail] = useState('')
  const [invitation, setInvitation] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [totpCode, setTOTPCode] = useState('')
  const emailInput = useRef<HTMLInputElement>(null)
  const invitationInput = useRef<HTMLInputElement>(null)
  const passkeySupported = window.isSecureContext && Boolean(window.PublicKeyCredential)

  const finishSignIn = (result: SignInResult) => {
    if (result.mfaRequired) {
      setMFARequired(true)
      replaceHistory('/login?step=totp')
    } else {
      replaceLocation('/')
    }
  }

  const load = useCallback(async () => {
    setPhase('loading')
    setError('')
    setSSOPending(false)
    try {
      const query = new URLSearchParams(window.location.search)
      const callbackError = query.get('error')
      if (callbackError) replaceHistory(window.location.pathname)
      const value = await requestJSON<AuthConfig>('/api/config', {
        credentials: 'same-origin',
        signal: AbortSignal.timeout(10000),
      })
      const pending = await requestJSON<{ required?: boolean }>('/api/totp/pending', {
        credentials: 'same-origin',
        signal: AbortSignal.timeout(10000),
      })
      setConfig(value)
      if (pending.required) {
        setMFARequired(true)
        replaceHistory('/login?step=totp')
      } else {
        setMFARequired(false)
        if (query.get('step') === 'totp') {
          setError('The verification request expired. Sign in again.')
          replaceHistory('/login')
        } else if (callbackError) {
          setError(SSO_ERRORS[callbackError] || SSO_ERRORS.login_failed)
        }
      }
      setPhase('ready')
    } catch {
      setPhase('error')
      setError('Could not load sign-in options. Check your connection and retry.')
    }
  }, [])

  useEffect(() => {
    void load()
    const resetSSO = () => setSSOPending(false)
    window.addEventListener('pageshow', resetSSO)
    return () => window.removeEventListener('pageshow', resetSSO)
  }, [load])

  const runAuth = async (action: () => Promise<void>) => {
    setBusy(true)
    setError('')
    try {
      await action()
    } catch (cause) {
      if (cause instanceof Error && cause.name === 'NotAllowedError') {
        setError('The passkey request was cancelled or timed out. Try again when ready.')
      } else {
        setError(errorMessage(cause, 'Sign-in failed.'))
      }
    } finally {
      setBusy(false)
    }
  }

  const startSSO = () => {
    setError('')
    setSSOPending(true)
    assignLocation('/api/sso/login')
  }

  const passwordSignIn = (event: FormEvent) => {
    event.preventDefault()
    void runAuth(async () => {
      const result = await requestJSON<SignInResult>('/api/password/login', postBody({ email: loginEmail, password: loginPassword }))
      setLoginPassword('')
      finishSignIn(result)
    })
  }

  const passwordRegister = (event: FormEvent) => {
    event.preventDefault()
    void runAuth(async () => {
      const result = await requestJSON<SignInResult>('/api/password/register', postBody({ email, token: invitation, password: newPassword }))
      setNewPassword('')
      setInvitation('')
      finishSignIn(result)
    })
  }

  const localPasskey = () => runAuth(async () => {
    const options = await requestJSON<Parameters<typeof prepareRequestOptions>[0]>('/api/passkeys/login/begin', postBody({}))
    const credential = await navigator.credentials.get(prepareRequestOptions(options)) as PublicKeyCredential | null
    if (!credential) throw new Error('No passkey was selected.')
    finishSignIn(await requestJSON<SignInResult>('/api/passkeys/login/finish', postBody(serializeCredential(credential))))
  })

  const registerPasskey = () => {
    if (!emailInput.current?.reportValidity() || !invitationInput.current?.reportValidity()) return
    void runAuth(async () => {
      const options = await requestJSON<Parameters<typeof prepareCreationOptions>[0]>('/api/passkeys/register/begin', postBody({ email, token: invitation }))
      const credential = await navigator.credentials.create({ publicKey: prepareCreationOptions(options) }) as PublicKeyCredential | null
      if (!credential) throw new Error('The passkey was not created.')
      const result = await requestJSON<SignInResult>('/api/passkeys/register/finish', postBody(serializeCredential(credential)))
      setInvitation('')
      finishSignIn(result)
    })
  }

  const supabasePasskey = () => runAuth(async () => {
    if (!config) return
    const { createClient } = await loadExternalAuth()
    const supabase = createClient(config.supabaseUrl || '', config.anonKey || '', {
      auth: { experimental: { passkey: true }, persistSession: false, autoRefreshToken: false },
    })
    const { data, error: providerError } = await supabase.auth.signInWithPasskey()
    if (providerError) throw providerError
    if (!data?.session?.access_token) throw new Error('The provider did not return a session.')
    finishSignIn(await requestJSON<SignInResult>('/api/session', postBody({ access_token: data.session.access_token })))
  })

  const verifyTOTP = (event: FormEvent) => {
    event.preventDefault()
    void runAuth(async () => {
      await requestJSON('/api/totp/verify', postBody({ code: totpCode }))
      setTOTPCode('')
      replaceLocation('/')
    })
  }

  if (phase === 'loading') {
    return (
      <LoginFrame>
        <div className="grid gap-4" id="loading" aria-busy="true">
          <span className="sr-only" role="status">Loading sign-in options…</span>
          <Skeleton className="h-4 w-20" />
          <Skeleton className="h-12 w-full" />
          <Skeleton className="h-4 w-24" />
          <Skeleton className="h-12 w-full" />
          <Skeleton className="h-12 w-full" />
        </div>
      </LoginFrame>
    )
  }

  if (phase === 'error') {
    return (
      <LoginFrame>
        <div className="grid justify-items-start gap-4">
          <Notice tone="error">{error}</Notice>
          <Button id="retry" type="button" variant="outline" onClick={() => void load()}>Retry</Button>
        </div>
      </LoginFrame>
    )
  }

  if (mfaRequired) {
    return (
      <LoginFrame>
        <section className="grid gap-5" id="totpAuth" aria-labelledby="totpTitle">
          <h2 className="m-0 text-xl font-medium" id="totpTitle">Two-factor authentication</h2>
          <form className="login-form" id="totpLogin" onSubmit={verifyTOTP}>
            <FormField label={recoveryMode ? 'Recovery code' : 'Authenticator code'} htmlFor="totpCode">
              <Input
                className="login-input"
                id="totpCode"
                value={totpCode}
                onChange={(event) => setTOTPCode(event.target.value)}
                type="text"
                inputMode={recoveryMode ? 'text' : 'numeric'}
                autoComplete="one-time-code"
                pattern={recoveryMode ? undefined : '[0-9 ]{6,8}'}
                maxLength={recoveryMode ? 64 : 8}
                required
              />
            </FormField>
            <LoadingButton className="login-action" id="verifyTOTP" type="submit" pending={busy}>Verify</LoadingButton>
          </form>
          {error && <Notice tone="error">{error}</Notice>}
          <Button
            id="useRecovery"
            type="button"
            variant="outline"
            disabled={busy}
            onClick={() => {
              setRecoveryMode((value) => !value)
              setTOTPCode('')
              setError('')
            }}
          >
            {recoveryMode ? 'Use an authenticator code' : 'Use a recovery code'}
          </Button>
          <a className="justify-self-start text-sm text-muted-foreground underline-offset-4 hover:underline" href="/logout">Back to sign in</a>
        </section>
      </LoginFrame>
    )
  }

  const ssoOnly = config?.authMode === 'sso' || config?.authMode === 'oidc'
  const local = config?.authMode === 'local'
  const supabase = config?.authMode === 'supabase'
  const additiveSSO = Boolean(config?.ssoEnabled && !ssoOnly)
  let provider = ''
  if (config?.ssoIssuer) {
    try { provider = new URL(config.ssoIssuer).hostname } catch { provider = '' }
  }

  return (
    <LoginFrame>
      <section className="grid gap-5" aria-label="Sign in" id="primaryAuth">
        {ssoOnly && (
          <div>
            <h2 className="m-0 text-xl font-medium" id="signinTitle">Sign in</h2>
            {provider && <p className="mb-0 mt-2 text-xs text-muted-foreground" id="provider">{provider}</p>}
          </div>
        )}
        {!setupOpen && (
          <p className="m-0 text-xs leading-5 text-muted-foreground" id="accessNote">
            {ssoOnly ? 'Use your organization account.' : 'Access is limited to invited accounts.'}
          </p>
        )}
        {error && <div id="msg"><Notice tone="error">{error}</Notice></div>}

        {ssoOnly ? (
          <LoadingButton className="login-action" id="btn" type="button" pending={ssoPending} onClick={startSSO}>
            {ssoPending ? 'Connecting…' : 'Sign in with SSO'}
          </LoadingButton>
        ) : (
          <>
            {!setupOpen && local && (
              <form className="login-form" id="passwordLogin" onSubmit={passwordSignIn}>
                <FormField label="Email" htmlFor="loginEmail">
                  <Input className="login-input" id="loginEmail" type="email" value={loginEmail} onChange={(event) => setLoginEmail(event.target.value)} autoComplete="username" required />
                </FormField>
                <FormField label="Password" htmlFor="loginPassword">
                  <Input className="login-input" id="loginPassword" type="password" value={loginPassword} onChange={(event) => setLoginPassword(event.target.value)} autoComplete="current-password" required />
                </FormField>
                <LoadingButton className="login-action" type="submit" pending={busy}>Sign in</LoadingButton>
              </form>
            )}
            {!setupOpen && (local || supabase) && passkeySupported && (
              <Button className="login-action" id="btn" type="button" variant="outline" disabled={busy || ssoPending} onClick={() => void (local ? localPasskey() : supabasePasskey())}>
                <KeyRound />
                Sign in with a passkey
              </Button>
            )}
            {!setupOpen && additiveSSO && (
              <LoadingButton className="login-action" id="btnSSO" type="button" variant="outline" pending={ssoPending} disabled={busy} onClick={startSSO}>
                {ssoPending ? 'Connecting…' : 'Sign in with SSO'}
              </LoadingButton>
            )}
            {local && (
              <details
                className="login-disclosure"
                id="enroll"
                open={setupOpen}
                onToggle={(event) => setSetupOpen(event.currentTarget.open)}
              >
                <summary>{setupOpen ? 'Back to sign in' : 'Set up or recover an account'}</summary>
                <form className="login-form mt-4" id="register" onSubmit={passwordRegister}>
                  <FormField label="Email" htmlFor="email">
                    <Input ref={emailInput} className="login-input" id="email" type="email" value={email} onChange={(event) => setEmail(event.target.value)} autoComplete="email" required />
                  </FormField>
                  <FormField label="Invitation code" htmlFor="invitation">
                    <Input ref={invitationInput} className="login-input" id="invitation" type="password" value={invitation} onChange={(event) => setInvitation(event.target.value)} autoComplete="off" required />
                  </FormField>
                  <FormField label="New password" htmlFor="newPassword">
                    <Input className="login-input" id="newPassword" type="password" value={newPassword} onChange={(event) => setNewPassword(event.target.value)} autoComplete="new-password" minLength={12} maxLength={128} placeholder="12 or more characters" required />
                  </FormField>
                  <LoadingButton className="login-action" type="submit" pending={busy}>Set password and sign in</LoadingButton>
                  {passkeySupported && (
                    <Button className="login-action" id="registerPasskey" type="button" variant="outline" disabled={busy} onClick={registerPasskey}>
                      Use a passkey instead · no password needed
                    </Button>
                  )}
                </form>
              </details>
            )}
          </>
        )}
        {!ssoOnly && !local && !supabase && <Notice tone="error">Sign-in is not configured. Contact your administrator.</Notice>}
        <noscript>Enable JavaScript to sign in to ShellGate.</noscript>
      </section>
    </LoginFrame>
  )
}
