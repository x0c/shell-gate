import { execFileSync, spawn, type ChildProcess } from 'node:child_process'
import { existsSync } from 'node:fs'
import { mkdir, mkdtemp, readFile, rm, unlink, writeFile } from 'node:fs/promises'
import { createServer } from 'node:net'
import { get as httpGet } from 'node:http'
import { tmpdir } from 'node:os'
import path from 'node:path'

const root = path.resolve(import.meta.dirname, '../..')
const output = path.resolve(root, 'web/test-results')
const fixturePath = path.resolve(output, 'e2e-fixture.json')

async function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const server = createServer()
    server.unref()
    server.on('error', reject)
    server.listen(0, '127.0.0.1', () => {
      const address = server.address()
      if (!address || typeof address === 'string') {
        server.close()
        reject(new Error('Could not allocate a test port'))
        return
      }
      const port = address.port
      server.close(() => resolve(port))
    })
  })
}

async function waitForHTTP(url: string, child: ChildProcess, ready: (status: number) => boolean = (status) => status >= 200 && status < 400, timeoutMs = 30_000) {
  const deadline = Date.now() + timeoutMs
  let lastError = ''
  while (Date.now() < deadline) {
    if (child.exitCode !== null) throw new Error(`Test process exited before ${url} became ready`)
    try {
      // nodejs/undici#2896: fetch rejects some 407 responses; readiness needs the status itself.
      const status = await new Promise<number>((resolve, reject) => {
        const request = httpGet(url, (response) => {
          response.resume()
          resolve(response.statusCode ?? 0)
        })
        request.on('error', reject)
        request.setTimeout(3000, () => request.destroy(new Error('HTTP readiness timeout')))
      })
      if (ready(status)) return
      lastError = `HTTP ${status}`
    } catch (error) {
      lastError = error instanceof Error ? error.message : String(error)
    }
    await new Promise((resolve) => setTimeout(resolve, 150))
  }
  throw new Error(`Timed out waiting for ${url}: ${lastError}`)
}

export default async function globalSetup() {
  await mkdir(output, { recursive: true })
  await rm(path.resolve(output, 'screenshots'), { recursive: true, force: true })
  await mkdir(path.resolve(output, 'screenshots'), { recursive: true })
  const data = await mkdtemp(path.resolve(tmpdir(), 'shell-gate-browser-'))
  const ttydPort = await freePort()
  const gatewayPort = await freePort()
  const binary = path.resolve(output, process.platform === 'win32' ? 'shell-gate-e2e.exe' : 'shell-gate-e2e')
  execFileSync('npm', ['run', 'build'], { cwd: path.resolve(root, 'web'), stdio: 'pipe' })
  execFileSync('go', ['build', '-o', binary, './cmd/shell-gate'], { cwd: root, stdio: 'pipe' })

  const terminalHome = path.resolve(data, 'home')
  await mkdir(terminalHome, { recursive: true })
  await writeFile(path.resolve(terminalHome, '.zshrc'), "export PS1='shell> '\n")
  await writeFile(path.resolve(terminalHome, '.bash_profile'), "export PS1='shell> '\n")
  const ttydBinary = process.env.TTYD_BIN || (existsSync('/opt/homebrew/bin/ttyd') ? '/opt/homebrew/bin/ttyd' : 'ttyd')
  const terminalScript = process.platform === 'darwin'
    ? path.resolve(root, 'deploy/mac/terminal-shell')
    : path.resolve(root, 'deploy/container/terminal-shell')
  const ttydLog = path.resolve(data, 'ttyd.log')
  const gateLog = path.resolve(data, 'gateway.log')
  const { open } = await import('node:fs/promises')
  const ttydOut = await open(ttydLog, 'w')
  const gateOut = await open(gateLog, 'w')
  const ttyd = spawn(ttydBinary, [
    '-p', String(ttydPort), '-i', '127.0.0.1', '-W', '-a', '-H', 'X-Webauth-User', terminalScript,
  ], {
    cwd: root,
    env: {
      ...process.env,
      HOME: terminalHome,
      ZDOTDIR: terminalHome,
      SHELL: process.platform === 'darwin' ? '/bin/zsh' : '/bin/bash',
      TERM: 'xterm-256color',
      COLORTERM: 'truecolor',
    },
    stdio: ['ignore', ttydOut.fd, ttydOut.fd],
  })
  const env = {
    ...process.env,
    SHELL_ALLOWED_EMAILS: 'e2e@example.com',
    SHELL_PUBLIC_URL: `http://localhost:${gatewayPort}`,
    SHELL_LISTEN: `127.0.0.1:${gatewayPort}`,
    SHELL_TTYD_URL: `http://127.0.0.1:${ttydPort}`,
    SHELL_DATA_DIR: data,
    SHELL_NETMON_INTERVAL: '0',
    SHELL_SESSION_TTL: '1h',
    SHELL_DEFAULT_PRESETS: '[]',
  }

  let gateway: ChildProcess | undefined
  try {
    await waitForHTTP(`http://127.0.0.1:${ttydPort}/`, ttyd, (status) => status === 200 || status === 407)
    gateway = spawn(binary, ['serve'], { cwd: root, env, stdio: ['ignore', gateOut.fd, gateOut.fd] })
    await waitForHTTP(`http://127.0.0.1:${gatewayPort}/healthz`, gateway)
    const invitationOutput = execFileSync(binary, ['invite', '--email', 'e2e@example.com', '--json'], {
      cwd: root,
      env,
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'pipe'],
    })
    const invitation = JSON.parse(invitationOutput) as { ok: boolean; data?: { token?: string } }
    if (!invitation.ok || !invitation.data?.token) throw new Error('The test invitation could not be created')
    await writeFile(fixturePath, JSON.stringify({
      baseURL: `http://localhost:${gatewayPort}`,
      email: 'e2e@example.com',
      password: 'shell-gate-e2e-password',
      invitation: invitation.data.token,
    }), { mode: 0o600 })
  } catch (error) {
    const ttydDiagnostics = await readFile(ttydLog, 'utf8').catch(() => '')
    const gateDiagnostics = await readFile(gateLog, 'utf8').catch(() => '')
    gateway?.kill('SIGTERM')
    ttyd.kill('SIGTERM')
    await rm(data, { recursive: true, force: true })
    throw new Error(`${error instanceof Error ? error.message : String(error)}\nttyd log:\n${ttydDiagnostics}\ngateway log:\n${gateDiagnostics}`)
  } finally {
    await ttydOut.close()
    await gateOut.close()
  }

  return async () => {
    gateway?.kill('SIGTERM')
    ttyd.kill('SIGTERM')
    await new Promise((resolve) => setTimeout(resolve, 500))
    if (gateway?.exitCode === null) gateway.kill('SIGKILL')
    if (ttyd.exitCode === null) ttyd.kill('SIGKILL')
    await rm(data, { recursive: true, force: true })
    await unlink(fixturePath).catch(() => undefined)
  }
}

export async function readFixture() {
  return JSON.parse(await readFile(fixturePath, 'utf8')) as {
    baseURL: string
    email: string
    password: string
    invitation: string
  }
}
