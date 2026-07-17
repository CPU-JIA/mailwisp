import { randomUUID } from 'node:crypto'

import { expect, test } from '@playwright/test'
import { parsePort, sendSMTPMessage } from '../tests-support/smtp'

const smtpPort = parsePort(process.env.MAILWISP_E2E_SMTP_PORT, 'MAILWISP_E2E_SMTP_PORT')

test('delivers SMTP through the production Compose stack and completes the browser lifecycle', async ({ page, context }) => {
  const pageErrors: string[] = []
  const remoteImageRequests: string[] = []
  page.on('pageerror', (error) => pageErrors.push(error.message))
  page.on('request', (request) => {
    if (new URL(request.url()).hostname === 'tracker.invalid') remoteImageRequests.push(request.url())
  })

  const response = await page.goto('/')
  expect(response?.status()).toBe(200)
  const headers = response?.headers() || {}
  expect(headers['content-security-policy']).toContain("default-src 'self'")
  expect(headers['x-content-type-options']).toBe('nosniff')
  expect(headers['cross-origin-opener-policy']).toBe('same-origin')
  await expect(page).toHaveTitle('MailWisp')

  await page.getByRole('button', { name: '创建临时邮箱' }).click()
  const address = (await page.locator('.credential-row').filter({ hasText: 'Email' }).locator('code').textContent())?.trim() || ''
  const capability = (await page.locator('.credential-row').filter({ hasText: 'Capability' }).locator('code').textContent())?.trim() || ''
  expect(address).toMatch(/^[a-z0-9]+@mailwisp\.test$/)
  expect(capability).toMatch(/^wisp_cap_v1_/)
  expect(page.url()).not.toContain(capability)
  expect(await page.evaluate(() => Object.values(localStorage))).not.toContain(capability)

  await page.getByRole('button', { name: '我已保存，进入收件箱' }).click()
  await expect(page.getByRole('heading', { name: address })).toBeVisible()
  let sessionCookie = (await context.cookies()).find((cookie) => cookie.name === '__Host-mailwisp_session')
  expect(sessionCookie).toMatchObject({ httpOnly: true, secure: true, sameSite: 'Lax', path: '/' })
  expect(await page.evaluate(() => Object.values(localStorage))).not.toContain(capability)

  await page.reload()
  await expect(page.getByRole('heading', { name: address })).toBeVisible()
  sessionCookie = (await context.cookies()).find((cookie) => cookie.name === '__Host-mailwisp_session')
  expect(sessionCookie).toBeDefined()

  const subject = `Production path ${randomUUID()}`
  const attachment = 'MailWisp production attachment\n'
  await sendSMTPMessage({
    port: smtpPort,
    recipient: address,
    subject,
    textBody: 'Production pipeline body.',
    htmlBody: '<p>HTML production body.</p><script>document.body.dataset.executed="true"</script><img src="https://tracker.invalid/pixel">',
    attachmentName: 'proof.txt',
    attachment,
    attachmentContentType: 'text/plain',
  })

  const messageButton = page.getByRole('button', { name: new RegExp(subject) })
  await expect(async () => {
    await page.getByRole('button', { name: '刷新来信' }).click()
    await expect(messageButton).toBeVisible({ timeout: 1_500 })
  }).toPass({ timeout: 30_000, intervals: [250, 500, 1_000] })

  await messageButton.click()
  await expect(page.getByRole('heading', { name: subject })).toBeVisible()
  await expect(page.locator('.plain-content')).toContainText('Production pipeline body.')
  await expect(page.getByText('proof.txt')).toBeVisible()

  await page.getByRole('tab', { name: '安全 HTML' }).click()
  const safeFrame = page.frameLocator('iframe')
  await expect(safeFrame.locator('body')).toContainText('HTML production body.')
  await expect(safeFrame.locator('script')).toHaveCount(0)
  await expect(safeFrame.locator('img[data-mailwisp-blocked="true"]')).toHaveCount(1)
  expect(remoteImageRequests).toEqual([])

  const downloadPromise = page.waitForEvent('download')
  await page.getByRole('button', { name: '下载' }).click()
  const download = await downloadPromise
  expect(download.suggestedFilename()).toBe('proof.txt')
  const stream = await download.createReadStream()
  const chunks: Buffer[] = []
  for await (const chunk of stream) chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk))
  expect(Buffer.concat(chunks).toString('utf8')).toBe(attachment)

  await page.getByRole('button', { name: '删除这封邮件' }).click()
  await expect(page.getByText('风还没有带来新消息')).toBeVisible()
  await page.getByRole('button', { name: '永久删除邮箱' }).click()
  await page.getByRole('button', { name: '再次点击，确认永久删除' }).click()
  await expect(page.getByRole('button', { name: '创建临时邮箱' })).toBeVisible()
  expect((await context.cookies()).some((cookie) => cookie.name === '__Host-mailwisp_session')).toBe(false)
  expect(pageErrors).toEqual([])
})
