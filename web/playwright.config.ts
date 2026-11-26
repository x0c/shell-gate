import { defineConfig } from '@playwright/test'
import { existsSync } from 'node:fs'

const localChrome = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'
const executablePath = process.env.CHROME_PATH || (existsSync(localChrome) ? localChrome : undefined)

export default defineConfig({
  testDir: './e2e',
  globalSetup: './e2e/global-setup.ts',
  outputDir: './test-results/playwright',
  fullyParallel: false,
  workers: 1,
  timeout: 120_000,
  expect: { timeout: 15_000 },
  reporter: 'list',
  use: {
    headless: true,
    viewport: { width: 1440, height: 900 },
    deviceScaleFactor: 2,
    colorScheme: 'dark',
    actionTimeout: 15_000,
    screenshot: 'only-on-failure',
    launchOptions: {
      executablePath,
      args: ['--disable-dev-shm-usage'],
    },
  },
})
