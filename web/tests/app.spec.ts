import { expect, test } from '@playwright/test'

test('renders the Chinese welcome screen and switches language/theme', async ({ page }) => {
  await page.route('**/api/v1/session', async (route) => {
    await route.fulfill({ status: 401, contentType: 'application/json', body: JSON.stringify({ error: { code: 'unauthenticated', message: 'no session', request_id: 'e2e' } }) })
  })
  await page.goto('/')
  await expect(page).toHaveTitle('MailWisp')
  await expect(page.getByRole('heading', { level: 1 })).toContainText('给下一封陌生来信')
  await page.locator('select').first().selectOption('en-US')
  await expect(page.getByRole('heading', { level: 1 })).toContainText('Give the next unknown message')
  await page.locator('select').nth(1).selectOption('mist')
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'mist')
  await expect(page.locator('body')).not.toContainText('undefined')
})

test('downloads an owned attachment from message detail', async ({ page }) => {
  const inbox = { id: '018f26e5-8f04-7b44-8ba2-4a8f434dcb12', address: 'demo@example.com', status: 'active', expires_at: '2026-07-16T00:00:00Z', created_at: '2026-07-15T00:00:00Z' }
  const summary = { id: '018f26e5-8f04-7b44-8ba2-4a8f434dcb13', envelope_sender: 'sender@example.com', subject: 'Attachment', preview: 'See file', received_at: '2026-07-15T00:00:00Z', parse_status: 'parsed', size_bytes: 128, has_attachments: true, seen: false }
  await page.route('**/api/v1/session', async (route) => {
    if (route.request().method() === 'POST') {
      await route.fulfill({ status: 201, contentType: 'application/json', body: JSON.stringify({ data: { inbox, expires_at: inbox.expires_at, csrf_token: 'csrf' } }) })
      return
    }
    await route.fulfill({ status: 401, contentType: 'application/json', body: JSON.stringify({ error: { code: 'unauthenticated', message: 'no session', request_id: 'e2e' } }) })
  })
  await page.route('**/api/v1/inboxes/me/messages?*', async (route) => {
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: [summary] }) })
  })
  await page.route(`**/api/v1/inboxes/me/messages/${summary.id}`, async (route) => {
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: { ...summary, header_message_id: '', from: [], to: [], cc: [], sent_at: null, text: 'See file', html_source: '<img src="cid:logo@example.com">', attachments: [{ part_path: '2', file_name: 'report.txt', content_type: 'text/plain', disposition: 'attachment', content_id: '', size_bytes: 10 }, { part_path: '3', file_name: 'logo.png', content_type: 'image/png', disposition: 'inline', content_id: 'logo@example.com', size_bytes: 4 }], warnings: [] } }) })
  })
  await page.route(`**/api/v1/inboxes/me/messages/${summary.id}/attachments/2`, async (route) => {
    await route.fulfill({ status: 200, contentType: 'text/plain', headers: { 'Content-Disposition': 'attachment; filename=report.txt' }, body: 'attachment' })
  })
  await page.route(`**/api/v1/inboxes/me/messages/${summary.id}/attachments/3`, async (route) => {
    await route.fulfill({ status: 200, contentType: 'image/png', body: Buffer.from([137, 80, 78, 71]) })
  })
  await page.goto('/')
  await page.locator('#capability-token').fill('wisp_cap_v1_test')
  await page.getByRole('button', { name: '打开收件箱' }).click()
  await page.getByRole('button', { name: /Attachment/ }).click()
  await page.getByRole('tab', { name: '安全 HTML' }).click()
  await expect.poll(async () => page.locator('iframe').getAttribute('srcdoc')).toContain('data:image/png;base64,')
  const downloadPromise = page.waitForEvent('download')
  await page.getByRole('button', { name: '下载' }).first().click()
  const download = await downloadPromise
  expect(download.suggestedFilename()).toBe('report.txt')
})
