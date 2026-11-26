import { test, expect, type FrameLocator, type Page } from '@playwright/test'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import { createHmac } from 'node:crypto'

const fixturePath = path.resolve(import.meta.dirname, '../test-results/e2e-fixture.json')
const screenshots = path.resolve(import.meta.dirname, '../test-results/screenshots')

async function fixture() {
  return JSON.parse(await readFile(fixturePath, 'utf8')) as { baseURL: string; email: string; password: string; invitation: string }
}

async function capture(page: Page, name: string) {
  await page.screenshot({ path: path.resolve(screenshots, `${name}.png`) })
}

async function login(page: Page, enroll = false) {
  const config = await fixture()
  await page.goto(`${config.baseURL}/login`)
  await expect(page.getByRole('heading', { name: 'shell-gate' })).toBeVisible()
  if (enroll) {
    await capture(page, 'desktop-login')
    await page.getByText('Set up or recover an account').click()
    await page.locator('#email').fill(config.email)
    await page.locator('#invitation').fill(config.invitation)
    await page.locator('#newPassword').fill(config.password)
    await page.getByRole('button', { name: 'Set password and sign in' }).click()
  } else {
    await page.locator('#loginEmail').fill(config.email)
    await page.locator('#loginPassword').fill(config.password)
    await page.getByRole('button', { name: 'Sign in', exact: true }).click()
  }
  if (await page.locator('#totpAuth').isVisible()) return
  await expect(page).toHaveURL(`${config.baseURL}/`)
}

async function activeTerminal(page: Page): Promise<FrameLocator> {
  const frame = page.frameLocator('iframe.terminal-pane[data-active="true"]')
  await expect(frame.locator('.xterm-helper-textarea')).toBeVisible({ timeout: 30_000 })
  return frame
}

async function terminalText(page: Page): Promise<string> {
  const handle = await page.locator('iframe.terminal-pane[data-active="true"]').elementHandle()
  const frame = await handle!.contentFrame()
  return frame!.evaluate(() => {
    const terminal = (window as unknown as {
      term?: { buffer: { active: { length: number; getLine: (index: number) => { translateToString: (trim: boolean) => string } | undefined } } }
    }).term
    if (!terminal) return ''
    const buffer = terminal.buffer.active
    const lines: string[] = []
    for (let index = 0; index < buffer.length; index += 1) lines.push(buffer.getLine(index)?.translateToString(true) ?? '')
    return lines.join('\n')
  })
}

async function typeCommand(page: Page, command: string, expected: string, clickTerminal = true) {
  const frame = await activeTerminal(page)
  if (clickTerminal) await frame.locator('.xterm-helper-textarea').click()
  await page.keyboard.type(command)
  await page.keyboard.press('Enter')
  await expect.poll(() => terminalText(page), { timeout: 20_000 }).toContain(expected)
}

function currentTOTP(secret: string): string {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'
  const clean = secret.replace(/[\s-]/g, '').toUpperCase()
  const bytes: number[] = []
  let bits = 0
  let value = 0
  for (const char of clean) {
    const index = alphabet.indexOf(char)
    if (index < 0) continue
    value = (value << 5) | index
    bits += 5
    if (bits >= 8) {
      bytes.push((value >>> (bits - 8)) & 255)
      bits -= 8
    }
  }
  const key = Buffer.from(bytes)
  const counter = Math.floor(Date.now() / 30_000)
  const message = Buffer.alloc(8)
  message.writeBigUInt64BE(BigInt(counter))
  const digest = createHmac('sha1', key).update(message).digest()
  const offset = digest[digest.length - 1] & 15
  const code = (digest.readUInt32BE(offset) & 0x7fffffff) % 1_000_000
  return String(code).padStart(6, '0')
}

test('desktop login, terminal, themes, diagnostics and presets', async ({ page }) => {
  const config = await fixture()
  await login(page, true)
  await activeTerminal(page)
  await typeCommand(page, "printf 'SHELLGATE_E2E_OK\\n'", 'SHELLGATE_E2E_OK')
  await capture(page, 'desktop-dark')

  const initialURL = await page.locator('iframe.terminal-pane[data-active="true"]').getAttribute('src')
  await page.locator('#btnTheme').click()
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light')
  await expect(page.locator('iframe.terminal-pane[data-active="true"]')).toHaveAttribute('src', initialURL!)
  await capture(page, 'desktop-light')

  await page.locator('#btnNet').click()
  await expect(page.getByRole('complementary', { name: 'Network diagnostics' })).toBeVisible()
  await typeCommand(page, "printf 'NETWORK_STAYS_OPEN\\n'", 'NETWORK_STAYS_OPEN')
  await capture(page, 'desktop-network-light')
  await page.getByRole('button', { name: 'Close network diagnostics' }).click()

  const topHeight = (await page.locator('.workspace-top').boundingBox())!.height
  await page.locator('#btnPresets').click()
  await expect(page.getByRole('dialog')).toBeVisible()
  for (const [name, command] of [['alpha', "printf 'ALPHA_OK\\n'"], ['a much longer preset name', "printf 'BETA_OK\\n'"], ['htop', 'htop']]) {
    await page.locator('#presetName').fill(name)
    await page.locator('#presetCmd').fill(command)
    await page.getByRole('button', { name: 'Add', exact: true }).click()
    await expect(page.locator('.preset-row').filter({ hasText: name })).toBeVisible()
  }
  const heights = await page.locator('.preset-row').evaluateAll((rows) => rows.map((row) => row.getBoundingClientRect().height))
  expect(new Set(heights).size).toBe(1)
  await capture(page, 'desktop-presets-light')
  await page.getByRole('button', { name: 'Delete alpha' }).click()
  await expect(page.getByText('Preset deleted')).toBeVisible()
  await page.getByRole('button', { name: 'Undo' }).click()
  await expect(page.locator('.preset-row').filter({ hasText: 'alpha' })).toBeVisible()
  await page.getByRole('button', { name: 'Close', exact: true }).click()

  await page.getByRole('button', { name: 'alpha', exact: true }).click()
  await expect(page.getByRole('tab', { name: 'alpha' })).toBeVisible()
  await typeCommand(page, "printf 'ALPHA_STILL_INTERACTIVE\\n'", 'ALPHA_STILL_INTERACTIVE')
  expect(await page.locator('.workspace-top').evaluate((element) => element.getBoundingClientRect().height)).toBe(topHeight)
  const tabWidths = await page.locator('.terminal-tab').evaluateAll((tabs) => tabs.map((tab) => tab.getBoundingClientRect().width))
  expect(new Set(tabWidths).size).toBe(1)
  await capture(page, 'desktop-tabs-light')
  await page.getByRole('button', { name: 'Close alpha', exact: true }).click()
  await expect(page.getByRole('tab', { name: 'alpha' })).toHaveCount(0)
  expect(config.baseURL).toBeTruthy()
})

test('390px and 320px mobile menus keep the terminal usable without overflow', async ({ browser }) => {
  const context = await browser.newContext({ viewport: { width: 390, height: 844}, deviceScaleFactor: 2, isMobile: true, hasTouch: true, colorScheme: 'dark' })
  const page = await context.newPage()
  await login(page)
  await activeTerminal(page)
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390)
  await expect(page.locator('#btnLaunchMenu')).toBeVisible()
  await expect(page.locator('#launchers')).toBeHidden()
  await page.locator('#btnLaunchMenu').click()
  await expect(page.locator('[data-slot="dropdown-menu-content"][data-state="open"]')).toBeVisible()
  await expect(page.locator('[data-slot="dropdown-menu-content"][data-state="open"]')).toHaveCSS('opacity', '1')
  await expect(page.getByText('Launch', { exact: true })).toBeVisible()
  await capture(page, 'mobile-390-launch-menu')
  await page.locator('.menu-scrim').click({ position: { x: 20, y: 700 } })
  await page.locator('#btnMoreMenu').click()
  await expect(page.getByText('More', { exact: true })).toBeVisible()
  await expect(page.locator('[data-slot="dropdown-menu-content"][data-state="open"]')).toHaveCSS('opacity', '1')
  await page.getByText('Network diagnostics', { exact: true }).click()
  await expect(page.locator('[data-slot="dropdown-menu-content"]')).toHaveCount(0)
  await expect(page.getByRole('complementary', { name: 'Network diagnostics' })).toBeVisible()
  await typeCommand(page, "printf 'MOBILE_INPUT_OK\\n'", 'MOBILE_INPUT_OK', false)
  await capture(page, 'mobile-390-network')
  await page.getByRole('button', { name: 'Close network diagnostics' }).click()
  await page.setViewportSize({ width: 320, height: 568 })
  await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))))
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(320)
  await capture(page, 'mobile-320-workspace')
  await context.close()
})

test('account security enrollment, secret cleanup and second-factor sign-in', async ({ page }) => {
  const config = await fixture()
  await login(page)
  await activeTerminal(page)
  await page.locator('#btnSecurity').click()
  await expect(page.getByRole('dialog')).toBeVisible()
  await page.getByRole('button', { name: 'Set up authenticator' }).click()
  const secret = (await page.locator('code').filter({ hasText: /^[A-Z2-7\s-]+$/ }).first().textContent())!.replace(/\s/g, '')
  await page.getByLabel('Authenticator code').fill(currentTOTP(secret))
  await page.getByRole('button', { name: 'Enable 2FA' }).click()
  await expect(page.getByText('Save your recovery codes')).toBeVisible()
  await expect(page.locator('.security-codes li')).toHaveCount(10)
  const recoveryCode = (await page.locator('.security-codes li').first().textContent())!
  await page.getByRole('button', { name: 'Close', exact: true }).click()
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await page.locator('#btnSecurity').click()
  await expect(page.getByText('On', { exact: true })).toBeVisible()
  await expect(page.locator('.security-codes')).toHaveCount(0)
  await page.getByRole('button', { name: 'Close', exact: true }).click()

  await page.goto(`${config.baseURL}/logout`)
  await page.locator('#loginEmail').fill(config.email)
  await page.locator('#loginPassword').fill(config.password)
  await page.getByRole('button', { name: 'Sign in', exact: true }).click()
  await expect(page.locator('#totpAuth')).toBeVisible()
  await page.getByRole('button', { name: 'Use a recovery code' }).click()
  await page.locator('#totpCode').fill(recoveryCode)
  await page.locator('#verifyTOTP').click()
  await expect(page).toHaveURL(`${config.baseURL}/`)
  await activeTerminal(page)
  await capture(page, 'desktop-after-totp')
})
