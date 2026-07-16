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

test('does not let a late session restore overwrite a user-created inbox', async ({ page }) => {
  const staleInbox = { id: '018f26e5-8f04-7b44-8ba2-4a8f434dcb10', address: 'stale@example.com', status: 'active', expires_at: '2026-07-17T00:00:00Z', created_at: '2026-07-16T00:00:00Z' }
  const createdInbox = { id: '018f26e5-8f04-7b44-8ba2-4a8f434dcb11', address: 'created@example.com', status: 'active', expires_at: '2026-07-17T00:00:00Z', created_at: '2026-07-16T00:00:00Z' }
  await page.route('**/api/v1/session', async (route) => {
    await new Promise((resolve) => setTimeout(resolve, 2_000))
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: { inbox: staleInbox, expires_at: staleInbox.expires_at, csrf_token: 'stale-csrf' } }) }).catch(() => undefined)
  })
  await page.route('**/api/v1/inboxes', async (route) => {
    await route.fulfill({ status: 201, contentType: 'application/json', body: JSON.stringify({ data: { inbox: createdInbox, capability: { token: 'wisp_cap_v1_created', expires_at: createdInbox.expires_at, scopes: ['inbox:read'] } } }) })
  })
  await page.route('**/api/v1/inboxes/me/messages?*', async (route) => {
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: [] }) })
  })
  await page.goto('/')
  await page.getByRole('button', { name: '创建临时邮箱' }).click()
  await expect(page.getByText('created@example.com')).toBeVisible()
  await page.waitForTimeout(2_200)
  await expect(page.getByText('created@example.com')).toBeVisible()
  await expect(page.getByText('stale@example.com')).toHaveCount(0)
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

test('loads every cursor page without replacing newer messages', async ({ page }) => {
  await page.clock.install()
  let listRequests = 0
  const inbox = { id: '018f26e5-8f04-7b44-8ba2-4a8f434dcb12', address: 'pages@example.com', status: 'active', expires_at: '2026-07-17T00:00:00Z', created_at: '2026-07-16T00:00:00Z' }
  const recent = { id: '018f26e5-8f04-7b44-8ba2-4a8f434dcb13', envelope_sender: 'new@example.com', subject: 'Newest page', preview: 'new', received_at: '2026-07-16T02:00:00Z', parse_status: 'parsed', size_bytes: 128, has_attachments: false, seen: false }
  const older = { id: '018f26e5-8f04-7b44-8ba2-4a8f434dcb14', envelope_sender: 'old@example.com', subject: 'Earlier page', preview: 'old', received_at: '2026-07-16T01:00:00Z', parse_status: 'parsed', size_bytes: 128, has_attachments: false, seen: false }
  const arrived = { id: '018f26e5-8f04-7b44-8ba2-4a8f434dcb15', envelope_sender: 'live@example.com', subject: 'Arrived while browsing', preview: 'live', received_at: '2026-07-16T03:00:00Z', parse_status: 'parsed', size_bytes: 128, has_attachments: false, seen: false }
  await page.route('**/api/v1/session', async (route) => {
    if (route.request().method() === 'POST') {
      await route.fulfill({ status: 201, contentType: 'application/json', body: JSON.stringify({ data: { inbox, expires_at: inbox.expires_at, csrf_token: 'csrf' } }) })
      return
    }
    await route.fulfill({ status: 401, contentType: 'application/json', body: JSON.stringify({ error: { code: 'unauthenticated', message: 'no session', request_id: 'e2e' } }) })
  })
  await page.route('**/api/v1/inboxes/me/messages?*', async (route) => {
    listRequests++
    const cursor = new URL(route.request().url()).searchParams.get('cursor')
    const body = cursor === 'older-page'
      ? { data: [recent, older], pagination: { next_cursor: '' } }
      : listRequests >= 3
        ? { data: [arrived, recent], pagination: { next_cursor: 'older-page' } }
        : { data: [recent], pagination: { next_cursor: 'older-page' } }
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) })
  })
  await page.goto('/')
  await page.locator('#capability-token').fill('wisp_cap_v1_test')
  await page.getByRole('button', { name: '打开收件箱' }).click()
  await expect(page.getByRole('button', { name: /Newest page/ })).toBeVisible()
  await page.getByRole('button', { name: '加载更早来信' }).click()
  await expect(page.getByRole('button', { name: /Newest page/ })).toHaveCount(1)
  await expect(page.getByRole('button', { name: /Earlier page/ })).toBeVisible()
  await expect(page.getByRole('button', { name: '加载更早来信' })).toHaveCount(0)
  await page.clock.fastForward(11_000)
  await expect.poll(() => listRequests).toBe(3)
  await expect(page.getByRole('button', { name: /Arrived while browsing/ })).toBeVisible()
  await expect(page.getByRole('button', { name: /Earlier page/ })).toBeVisible()
  await expect(page.getByRole('button', { name: /Newest page/ })).toHaveCount(1)
})

test('keeps cursor pagination retryable after a transient failure', async ({ page }) => {
  let loadMoreAttempts = 0
  const inbox = { id: '018f26e5-8f04-7b44-8ba2-4a8f434dcb12', address: 'retry@example.com', status: 'active', expires_at: '2026-07-17T00:00:00Z', created_at: '2026-07-16T00:00:00Z' }
  const recent = { id: '018f26e5-8f04-7b44-8ba2-4a8f434dcb13', envelope_sender: 'new@example.com', subject: 'Recent message', preview: 'new', received_at: '2026-07-16T02:00:00Z', parse_status: 'parsed', size_bytes: 128, has_attachments: false, seen: false }
  const older = { id: '018f26e5-8f04-7b44-8ba2-4a8f434dcb14', envelope_sender: 'old@example.com', subject: 'Recovered earlier message', preview: 'old', received_at: '2026-07-16T01:00:00Z', parse_status: 'parsed', size_bytes: 128, has_attachments: false, seen: false }
  await page.route('**/api/v1/session', async (route) => {
    if (route.request().method() === 'POST') {
      await route.fulfill({ status: 201, contentType: 'application/json', body: JSON.stringify({ data: { inbox, expires_at: inbox.expires_at, csrf_token: 'csrf' } }) })
      return
    }
    await route.fulfill({ status: 401, contentType: 'application/json', body: JSON.stringify({ error: { code: 'unauthenticated', message: 'no session', request_id: 'e2e' } }) })
  })
  await page.route('**/api/v1/inboxes/me/messages?*', async (route) => {
    const cursor = new URL(route.request().url()).searchParams.get('cursor')
    if (!cursor) {
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: [recent], pagination: { next_cursor: 'older-page' } }) })
      return
    }
    loadMoreAttempts++
    if (loadMoreAttempts === 1) {
      await route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: { code: 'temporarily_unavailable', message: 'retry', request_id: 'e2e-retry' } }) })
      return
    }
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: [older], pagination: { next_cursor: '' } }) })
  })
  await page.goto('/')
  await page.locator('#capability-token').fill('wisp_cap_v1_test')
  await page.getByRole('button', { name: '打开收件箱' }).click()
  await page.getByRole('button', { name: '加载更早来信' }).click()
  await expect(page.getByText('更早的来信暂时没有加载成功。')).toBeVisible()
  await page.getByRole('button', { name: '加载更早来信' }).click()
  await expect(page.getByRole('button', { name: /Recovered earlier message/ })).toBeVisible()
  await expect(page.getByText('更早的来信暂时没有加载成功。')).toHaveCount(0)
  expect(loadMoreAttempts).toBe(2)
})

test('does not restart inbox polling after load-more is aborted by message detail', async ({ page }) => {
  await page.clock.install()
  let listRequests = 0
  const inbox = { id: '018f26e5-8f04-7b44-8ba2-4a8f434dcb12', address: 'race@example.com', status: 'active', expires_at: '2026-07-17T00:00:00Z', created_at: '2026-07-16T00:00:00Z' }
  const summary = { id: '018f26e5-8f04-7b44-8ba2-4a8f434dcb13', envelope_sender: 'sender@example.com', subject: 'Open during pagination', preview: 'body', received_at: '2026-07-16T02:00:00Z', parse_status: 'parsed', size_bytes: 128, has_attachments: false, seen: false }
  await page.route('**/api/v1/session', async (route) => {
    if (route.request().method() === 'POST') {
      await route.fulfill({ status: 201, contentType: 'application/json', body: JSON.stringify({ data: { inbox, expires_at: inbox.expires_at, csrf_token: 'csrf' } }) })
      return
    }
    await route.fulfill({ status: 401, contentType: 'application/json', body: JSON.stringify({ error: { code: 'unauthenticated', message: 'no session', request_id: 'e2e' } }) })
  })
  await page.route('**/api/v1/inboxes/me/messages?*', async (route) => {
    listRequests++
    const cursor = new URL(route.request().url()).searchParams.get('cursor')
    if (cursor) {
      await new Promise((resolve) => setTimeout(resolve, 750))
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: [], pagination: { next_cursor: '' } }) }).catch(() => undefined)
      return
    }
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: [summary], pagination: { next_cursor: 'older-page' } }) })
  })
  await page.route(`**/api/v1/inboxes/me/messages/${summary.id}`, async (route) => {
    await new Promise((resolve) => setTimeout(resolve, 150))
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: { ...summary, header_message_id: '', from: [], to: [], cc: [], sent_at: null, text: 'body', html_source: '', attachments: [], warnings: [] } }) })
  })
  await page.goto('/')
  await page.locator('#capability-token').fill('wisp_cap_v1_test')
  await page.getByRole('button', { name: '打开收件箱' }).click()
  await page.getByRole('button', { name: '加载更早来信' }).click()
  await expect.poll(() => listRequests).toBe(2)
  await page.getByRole('button', { name: /Open during pagination/ }).click()
  await expect(page.getByRole('heading', { name: 'Open during pagination' })).toBeVisible()
  await page.clock.fastForward(11_000)
  expect(listRequests).toBe(2)
})
