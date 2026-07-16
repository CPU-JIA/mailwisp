import { defineConfig, devices } from '@playwright/test'

const baseURL = process.env.MAILWISP_E2E_BASE_URL
if (!baseURL) throw new Error('MAILWISP_E2E_BASE_URL is required')

export default defineConfig({
  testDir: './tests-production',
  timeout: 60_000,
  expect: { timeout: 15_000 },
  fullyParallel: false,
  workers: 1,
  retries: 0,
  outputDir: 'test-results-production',
  use: {
    baseURL,
    ignoreHTTPSErrors: true,
    ...devices['Desktop Chrome'],
    screenshot: 'only-on-failure',
    trace: 'retain-on-failure',
    video: 'retain-on-failure',
  },
  reporter: [
    ['list'],
    ['html', { outputFolder: 'playwright-production-report', open: 'never' }],
  ],
})
