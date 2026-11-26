export interface ExternalAuthModule {
  createClient: (url: string, key: string, options: unknown) => {
    auth: {
      signInWithPasskey: () => Promise<{
        data?: { session?: { access_token?: string } }
        error?: Error
      }>
    }
  }
}

export async function loadExternalAuth(): Promise<ExternalAuthModule> {
  const modulePath = '/__shellgate/external-auth.js'
  return import(/* @vite-ignore */ modulePath) as Promise<ExternalAuthModule>
}
