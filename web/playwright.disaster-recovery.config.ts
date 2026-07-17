import path from 'node:path'

import { defineConfig, devices } from '@playwright/test'

const baseURL = process.env.MAILWISP_DR_BASE_URL
const stateRoot = process.env.MAILWISP_DR_STATE_ROOT
if (!baseURL) throw new Error('MAILWISP_DR_BASE_URL is required')
if (!stateRoot) throw new Error('MAILWISP_DR_STATE_ROOT is required')

export default defineConfig({
  testDir: './tests-disaster-recovery',
  timeout: 60_000,
  expect: { timeout: 15_000 },
  fullyParallel: false,
  workers: 1,
  retries: 0,
  outputDir: path.join(stateRoot, 'playwright-output'),
  use: {
    baseURL,
    ignoreHTTPSErrors: true,
    ...devices['Desktop Chrome'],
    screenshot: 'off',
    trace: 'off',
    video: 'off',
  },
  reporter: [['list']],
})
