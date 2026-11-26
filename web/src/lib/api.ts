export class APIError extends Error {
  readonly status: number
  readonly code: string
  readonly reauthenticate: boolean

  constructor(message: string, status: number, code = '') {
    super(message)
    this.name = 'APIError'
    this.status = status
    this.code = code
    this.reauthenticate = code === 'reauth_required' || code === 'signin_required'
  }
}

interface ErrorPayload {
  message?: string
  code?: string
}

export async function requestJSON<T>(path: string, options: RequestInit = {}): Promise<T> {
  const headers = new Headers(options.headers)
  if (options.body && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json')
  const response = await fetch(path, {
    credentials: 'same-origin',
    ...options,
    headers,
  })
  const body = (await response.json().catch(() => ({}))) as ErrorPayload & T
  if (!response.ok) {
    throw new APIError(body.message || 'Request failed. Please try again.', response.status, body.code)
  }
  return body
}

export async function workspaceAPI<T>(path: string, options: RequestInit = {}): Promise<T> {
  try {
    return await requestJSON<T>(path, options)
  } catch (error) {
    if (error instanceof APIError && error.status === 401) {
      window.location.replace('/login')
    }
    throw error
  }
}

export function postBody(value: unknown): RequestInit {
  return { method: 'POST', body: JSON.stringify(value) }
}

export function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error && error.message ? error.message : fallback
}
