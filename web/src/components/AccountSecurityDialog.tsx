import {
  Button,
  Dialog,
  DialogContent,
  DialogTitle,
  FormField,
  Input,
  LoadingButton,
  Notice,
  Skeleton,
} from '@x0c/ui'
import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react'
import { errorMessage, postBody, requestJSON } from '../lib/api'
import { focusActiveTerminal, isCoarseWorkspace } from '../lib/viewport'
import type { TOTPRecovery, TOTPSetup, TOTPStatus } from '../types'

type Stage = 'loading' | 'error' | 'status' | 'setup' | 'recovery'

async function securityRequest<T>(path: string, body?: unknown): Promise<T> {
  return requestJSON<T>(path, {
    ...(body === undefined ? {} : postBody(body)),
    signal: AbortSignal.timeout(15000),
  })
}

export function AccountSecurityDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const [stage, setStage] = useState<Stage>('loading')
  const [status, setStatus] = useState<TOTPStatus | null>(null)
  const [setup, setSetup] = useState<TOTPSetup | null>(null)
  const [setupCode, setSetupCode] = useState('')
  const [proof, setProof] = useState('')
  const [recoveryCodes, setRecoveryCodes] = useState<string[]>([])
  const [message, setMessage] = useState('')
  const [error, setError] = useState('')
  const [reauthenticate, setReauthenticate] = useState(false)
  const [pending, setPending] = useState(false)
  const generation = useRef(0)
  const openRef = useRef(open)
  const setupInput = useRef<HTMLInputElement>(null)

  const clearSecrets = useCallback(() => {
    setSetup(null)
    setSetupCode('')
    setProof('')
    setRecoveryCodes([])
  }, [])

  const perform = useCallback(async (work: (active: () => boolean) => Promise<void>) => {
    const current = generation.current
    setPending(true)
    setError('')
    setReauthenticate(false)
    const active = () => openRef.current && generation.current === current
    try {
      await work(active)
    } catch (cause) {
      if (active()) {
        setError(errorMessage(cause, 'Could not complete this request. Try again.'))
        setReauthenticate(cause instanceof Error && 'reauthenticate' in cause && Boolean(cause.reauthenticate))
        setStage((currentStage) => currentStage === 'loading' ? 'error' : currentStage)
      }
    } finally {
      if (active()) setPending(false)
    }
  }, [])

  const loadStatus = useCallback(async () => {
    await perform(async (active) => {
      const state = await securityRequest<TOTPStatus>('/api/totp')
      if (!active()) return
      clearSecrets()
      setStatus(state)
      setMessage('')
      setStage('status')
    })
  }, [clearSecrets, perform])

  useEffect(() => {
    openRef.current = open
    if (open) {
      generation.current += 1
      clearSecrets()
      setStatus(null)
      setMessage('')
      setError('')
      setReauthenticate(false)
      setStage('loading')
      void loadStatus()
    } else {
      generation.current += 1
      clearSecrets()
      setPending(false)
      setMessage('')
      setError('')
    }
  }, [open, clearSecrets, loadStatus])

  const changeOpen = (next: boolean) => {
    if (!next) {
      openRef.current = false
      generation.current += 1
      clearSecrets()
    }
    onOpenChange(next)
  }

  const showRecovery = (codes: string[]) => {
    setSetup(null)
    setSetupCode('')
    setProof('')
    setRecoveryCodes(codes)
    setStage('recovery')
    setMessage('Two-factor authentication is on.')
  }

  const beginSetup = () => perform(async (active) => {
    const value = await securityRequest<TOTPSetup>('/api/totp/setup', {})
    if (!active()) return
    setSetup(value)
    setStage('setup')
    requestAnimationFrame(() => setupInput.current?.focus())
  })

  const enable = (event: FormEvent) => {
    event.preventDefault()
    void perform(async (active) => {
      if (!setup) return
      const result = await securityRequest<TOTPRecovery>('/api/totp/enable', { setupId: setup.setupId, code: setupCode })
      if (active()) showRecovery(result.recoveryCodes)
    })
  }

  const changeFactor = (path: '/api/totp/disable' | '/api/totp/recovery') => {
    if (!proof.trim()) {
      setError('Enter an authenticator or recovery code.')
      return
    }
    void perform(async (active) => {
      const result = await securityRequest<TOTPRecovery>(path, { code: proof })
      if (!active()) return
      if (result.enabled) {
        showRecovery(result.recoveryCodes)
      } else {
        await loadStatus()
        if (active()) setMessage('Two-factor authentication is off.')
      }
    })
  }

  const copy = async (value: string, confirmation: string) => {
    await perform(async (active) => {
      await navigator.clipboard.writeText(value)
      if (active()) setMessage(confirmation)
    })
  }

  const downloadRecovery = () => {
    const file = new Blob(['ShellGate recovery codes\n\n', recoveryCodes.join('\n'), '\n'], { type: 'text/plain' })
    const url = URL.createObjectURL(file)
    const link = document.createElement('a')
    link.href = url
    link.download = 'shellgate-recovery-codes.txt'
    link.click()
    window.setTimeout(() => URL.revokeObjectURL(url), 1000)
  }

  return (
    <Dialog open={open} onOpenChange={changeOpen}>
      <DialogContent
        showCloseButton={false}
        className="max-h-[calc(var(--app-height)-2rem)] max-w-xl overflow-y-auto p-0"
        aria-describedby={undefined}
        onCloseAutoFocus={(event) => {
          if (isCoarseWorkspace()) {
            event.preventDefault()
            focusActiveTerminal()
          }
        }}
      >
        <header className="security-heading">
          <DialogTitle>Account security</DialogTitle>
          <Button type="button" variant="ghost" size="sm" onClick={() => changeOpen(false)}>Close</Button>
        </header>
        <div className="security-content" aria-busy={pending || undefined}>
          {status?.email && <p className="m-0 text-xs text-muted-foreground [overflow-wrap:anywhere]">{status.email}</p>}
          {message && <p className="m-0 text-sm" role="status" aria-live="polite">{message}</p>}
          {error && <Notice tone="error">{error}</Notice>}
          {reauthenticate && (
            <Button asChild variant="outline" className="justify-self-start">
              <a href="/logout">Sign in again</a>
            </Button>
          )}

          {stage === 'loading' && (
            <div className="grid gap-3" aria-busy="true">
              <span className="sr-only" role="status">Loading account security…</span>
              <Skeleton className="h-4 w-2/3" />
              <Skeleton className="h-20 w-full" />
              <Skeleton className="h-9 w-40" />
            </div>
          )}

          {stage === 'error' && !reauthenticate && (
            <Button type="button" variant="outline" className="justify-self-start" disabled={pending} onClick={() => void loadStatus()}>
              Retry
            </Button>
          )}

          {stage === 'status' && status && (
            <section className="grid gap-5" aria-label="Two-factor authentication">
              <div className="flex items-center justify-between gap-4">
                <h3 className="m-0 text-sm font-semibold">Two-factor authentication</h3>
                <span className="text-sm text-muted-foreground">{status.enabled ? 'On' : 'Off'}</span>
              </div>
              {!status.enabled ? (
                <LoadingButton type="button" className="justify-self-start" pending={pending} onClick={() => void beginSetup()}>
                  Set up authenticator
                </LoadingButton>
              ) : (
                <div className="grid gap-4">
                  <p className="m-0 text-sm text-muted-foreground">{status.recoveryCodesRemaining} recovery codes remaining</p>
                  <FormField label="Authenticator or recovery code" htmlFor="securityProof">
                    <Input
                      id="securityProof"
                      value={proof}
                      onChange={(event) => setProof(event.target.value)}
                      autoComplete="one-time-code"
                      maxLength={64}
                      required
                    />
                  </FormField>
                  <div className="flex flex-wrap justify-end gap-2">
                    <Button type="button" variant="outline" disabled={pending} onClick={() => changeFactor('/api/totp/recovery')}>
                      New recovery codes
                    </Button>
                    <Button type="button" variant="outline" disabled={pending} onClick={() => changeFactor('/api/totp/disable')}>
                      Turn off 2FA
                    </Button>
                  </div>
                </div>
              )}
            </section>
          )}

          {stage === 'setup' && setup && (
            <section className="grid gap-5" aria-label="Set up authenticator">
              <p className="m-0 text-sm text-muted-foreground">Scan this code with your authenticator app, or enter the setup key.</p>
              <img className="security-qr" src={setup.qr} alt="Authenticator setup QR code" width="240" height="240" />
              <div className="grid justify-items-start gap-3">
                <code className="text-sm [overflow-wrap:anywhere] [user-select:all]">{setup.secret}</code>
                <Button type="button" variant="outline" size="sm" disabled={pending} onClick={() => void copy(setup.secret, 'Setup key copied.')}>
                  Copy key
                </Button>
              </div>
              <form className="grid gap-4" onSubmit={enable}>
                <FormField label="Authenticator code" htmlFor="securitySetupCode">
                  <Input
                    ref={setupInput}
                    id="securitySetupCode"
                    value={setupCode}
                    onChange={(event) => setSetupCode(event.target.value)}
                    type="text"
                    inputMode="numeric"
                    autoComplete="one-time-code"
                    pattern="[0-9 ]{6,8}"
                    maxLength={8}
                    required
                  />
                </FormField>
                <div className="flex flex-wrap justify-end gap-2">
                  <Button type="button" variant="outline" disabled={pending} onClick={() => void loadStatus()}>Back</Button>
                  <LoadingButton type="submit" pending={pending}>Enable 2FA</LoadingButton>
                </div>
              </form>
            </section>
          )}

          {stage === 'recovery' && (
            <section className="grid gap-5" aria-label="Save recovery codes">
              <h3 className="m-0 text-sm font-semibold">Save your recovery codes</h3>
              <p className="m-0 text-sm text-muted-foreground">Each code works once. Save them somewhere safe; they will not be shown again.</p>
              <ol className="security-codes">
                {recoveryCodes.map((code) => <li key={code}>{code}</li>)}
              </ol>
              <div className="flex flex-wrap justify-end gap-2">
                <Button type="button" variant="outline" disabled={pending} onClick={() => void copy(recoveryCodes.join('\n'), 'Recovery codes copied.')}>Copy codes</Button>
                <Button type="button" variant="outline" disabled={pending} onClick={downloadRecovery}>Download</Button>
                <LoadingButton type="button" pending={pending} onClick={() => void loadStatus()}>Done</LoadingButton>
              </div>
            </section>
          )}
        </div>
      </DialogContent>
    </Dialog>
  )
}
