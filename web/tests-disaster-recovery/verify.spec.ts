import { readFile } from 'node:fs/promises'

import { expect, test } from '@playwright/test'
import { parsePort, sendSMTPMessage } from '../tests-support/smtp'
import { assertDownload, browserStatePath, flowStatePath, restoredAttachment, restoredBody, restoredHTML, restoredHTMLText, restoredSubject, sourceAttachment, sourceBody, sourceHTMLText, sourceSubject } from './flow'

const smtpPort = parsePort(process.env.MAILWISP_DR_SMTP_PORT, 'MAILWISP_DR_SMTP_PORT')
test.use({ storageState: browserStatePath() })

test('restores the browser session and accepts new mail', async ({ page, context }) => {
  const state = JSON.parse(await readFile(flowStatePath(), 'utf8')) as { address: string }
  expect(state.address).toMatch(/^[a-z0-9]+@mailwisp\.test$/)
  await page.goto('/')
  await expect(page.getByRole('heading', { name: state.address })).toBeVisible()
  expect((await context.cookies()).some((cookie) => cookie.name === '__Host-mailwisp_session')).toBe(true)

  const sourceMessage = page.getByRole('button', { name: new RegExp(sourceSubject) })
  await expect(sourceMessage).toBeVisible()
  await sourceMessage.click()
  await expect(page.locator('.plain-content')).toContainText(sourceBody)
  await page.getByRole('tab', { name: '安全 HTML' }).click()
  await expect(page.frameLocator('iframe').locator('body')).toContainText(sourceHTMLText)
  await assertDownload(page, 'source-proof.txt', sourceAttachment)
  await page.getByRole('button', { name: '返回来信列表' }).click()

  await sendSMTPMessage({
    port: smtpPort,
    recipient: state.address,
    subject: restoredSubject,
    textBody: restoredBody,
    htmlBody: restoredHTML,
    attachmentName: 'restored-proof.txt',
    attachment: restoredAttachment,
    attachmentContentType: 'text/plain',
  })
  const restoredMessage = page.getByRole('button', { name: new RegExp(restoredSubject) })
  await expect(async () => {
    await page.getByRole('button', { name: '刷新来信' }).click()
    await expect(restoredMessage).toBeVisible({ timeout: 1_500 })
  }).toPass({ timeout: 30_000, intervals: [250, 500, 1_000] })
  await restoredMessage.click()
  await expect(page.locator('.plain-content')).toContainText(restoredBody)
  await page.getByRole('tab', { name: '安全 HTML' }).click()
  await expect(page.frameLocator('iframe').locator('body')).toContainText(restoredHTMLText)
  await assertDownload(page, 'restored-proof.txt', restoredAttachment)
})
