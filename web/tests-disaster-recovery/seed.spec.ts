import { rename, writeFile } from 'node:fs/promises'

import { expect, test } from '@playwright/test'
import { parsePort, sendSMTPMessage } from '../tests-support/smtp'
import { assertDownload, browserStatePath, flowStatePath, sourceAttachment, sourceBody, sourceHTML, sourceHTMLText, sourceSubject } from './flow'

const smtpPort = parsePort(process.env.MAILWISP_DR_SMTP_PORT, 'MAILWISP_DR_SMTP_PORT')

test('creates durable source state before the offline backup', async ({ page, context }) => {
  await page.goto('/')
  await page.getByRole('button', { name: '创建临时邮箱' }).click()
  const address = (await page.locator('.credential-row').filter({ hasText: 'Email' }).locator('code').textContent())?.trim() || ''
  expect(address).toMatch(/^[a-z0-9]+@mailwisp\.test$/)
  const redactionState = flowStatePath()
  const pendingRedactionState = `${redactionState}.pending`
  await writeFile(pendingRedactionState, JSON.stringify({ address }), { encoding: 'utf8', mode: 0o600, flag: 'wx' })
  await rename(pendingRedactionState, redactionState)
  await page.getByRole('button', { name: '我已保存，进入收件箱' }).click()
  await expect(page.getByRole('heading', { name: address })).toBeVisible()

  await sendSMTPMessage({
    port: smtpPort,
    recipient: address,
    subject: sourceSubject,
    textBody: sourceBody,
    htmlBody: sourceHTML,
    attachmentName: 'source-proof.txt',
    attachment: sourceAttachment,
    attachmentContentType: 'text/plain',
  })
  const messageButton = page.getByRole('button', { name: new RegExp(sourceSubject) })
  await expect(async () => {
    await page.getByRole('button', { name: '刷新来信' }).click()
    await expect(messageButton).toBeVisible({ timeout: 1_500 })
  }).toPass({ timeout: 30_000, intervals: [250, 500, 1_000] })
  await messageButton.click()
  await expect(page.locator('.plain-content')).toContainText(sourceBody)
  await page.getByRole('tab', { name: '安全 HTML' }).click()
  await expect(page.frameLocator('iframe').locator('body')).toContainText(sourceHTMLText)
  await assertDownload(page, 'source-proof.txt', sourceAttachment)

  await context.storageState({ path: browserStatePath() })
})
