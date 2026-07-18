import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'

const wcagTags = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa']

async function expectNoAccessibilityViolations(page: Page): Promise<void> {
  const result = await new AxeBuilder({ page }).withTags(wcagTags).analyze()
  expect(result.violations).toEqual([])
}

test('keeps the welcome workspace accessible across themes and mobile', async ({ page }) => {
  await page.route('**/api/v1/session', async (route) => {
    await route.fulfill({ status: 401, contentType: 'application/json', body: JSON.stringify({ error: { code: 'unauthenticated', message: 'no session', request_id: 'axe' } }) })
  })
  await page.goto('/')
  await expectNoAccessibilityViolations(page)
  await page.locator('select[aria-label="主题"]').selectOption('dark')
  await expectNoAccessibilityViolations(page)
  await page.setViewportSize({ width: 390, height: 844 })
  await expectNoAccessibilityViolations(page)
})

test('keeps inbox and message detail accessible in master-detail and mobile views', async ({ page }) => {
  const inbox = { id: '018f26e5-8f04-7b44-8ba2-4a8f434dcb90', address: 'accessibility@example.com', status: 'active', expires_at: '2026-07-20T00:00:00Z', created_at: '2026-07-19T00:00:00Z' }
  const summary = { id: '018f26e5-8f04-7b44-8ba2-4a8f434dcb91', envelope_sender: 'sender@example.com', subject: 'Accessible message detail', preview: 'Readable preview', received_at: '2026-07-19T01:00:00Z', parse_status: 'parsed', size_bytes: 128, has_attachments: false, seen: false }
  await page.route('**/api/v1/session', async (route) => {
    if (route.request().method() === 'POST') {
      await route.fulfill({ status: 201, contentType: 'application/json', body: JSON.stringify({ data: { inbox, expires_at: inbox.expires_at, csrf_token: 'axe-csrf' } }) })
      return
    }
    await route.fulfill({ status: 401, contentType: 'application/json', body: JSON.stringify({ error: { code: 'unauthenticated', message: 'no session', request_id: 'axe' } }) })
  })
  await page.route('**/api/v1/inboxes/me/messages?*', async (route) => {
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: [summary], pagination: { next_cursor: '' } }) })
  })
  await page.route(`**/api/v1/inboxes/me/messages/${summary.id}`, async (route) => {
    if (route.request().method() === 'PATCH') {
      await route.fulfill({ status: 204 })
      return
    }
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: { ...summary, header_message_id: '', from: [{ name: '', address: 'sender@example.com' }], to: [{ name: '', address: inbox.address }], cc: [], sent_at: null, text: 'Accessible body', html_source: '', attachments: [], warnings: [] } }) })
  })

  await page.goto('/')
  await page.getByRole('tab', { name: '访问令牌' }).click()
  await page.locator('#capability-token').fill('wisp_cap_v1_accessibility')
  await page.getByRole('button', { name: '打开收件箱' }).click()
  await expectNoAccessibilityViolations(page)
  await page.getByRole('button', { name: /Accessible message detail/ }).click()
  await expectNoAccessibilityViolations(page)
  await page.setViewportSize({ width: 390, height: 844 })
  await expectNoAccessibilityViolations(page)
})
