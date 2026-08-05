import { existsSync } from 'node:fs'

import { defineConfig, devices } from '@playwright/test'

if (!existsSync(new URL('./dist/index.html', import.meta.url))) {
  throw new Error('Web build not found. Run `make web-e2e` from the repository root.')
}

const baseURL = process.env.OMNARA_WEB_E2E_BASE_URL
if (!baseURL) {
  throw new Error('Real API URL not found. Run `make web-e2e` from the repository root.')
}

export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 2 : 0,
  workers: process.env.CI ? 1 : undefined,
  reporter: process.env.CI ? [['github'], ['html', { open: 'never' }]] : 'line',
  use: {
    baseURL,
    trace: 'on-first-retry',
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
})
