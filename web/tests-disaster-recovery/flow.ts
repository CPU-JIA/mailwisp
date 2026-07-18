import path from 'node:path'

import { expect } from '@playwright/test'
import type { Page } from '@playwright/test'

export const sourceSubject = 'MailWisp disaster recovery source'
export const sourceBody = 'Source message survives the verified restore.'
export const sourceHTML = '<p>Source recovery HTML body.</p>'
export const sourceHTMLText = 'Source recovery HTML body.'
export const sourceAttachment = 'MailWisp source recovery attachment\n'
export const restoredSubject = 'MailWisp disaster recovery write proof'
export const restoredBody = 'The restored stack accepts new mail.'
export const restoredHTML = '<p>Post-restore write path is healthy.</p>'
export const restoredHTMLText = 'Post-restore write path is healthy.'
export const restoredAttachment = 'MailWisp post-restore attachment\n'

export function requiredStateRoot(): string {
  const root = process.env.MAILWISP_DR_STATE_ROOT
  if (!root) throw new Error('MAILWISP_DR_STATE_ROOT is required')
  return root
}

export function browserStatePath(): string {
  return path.join(requiredStateRoot(), 'browser-state.json')
}

export function flowStatePath(): string {
  return path.join(requiredStateRoot(), 'flow-state.json')
}

export async function assertDownload(page: Page, fileName: string, expected: string): Promise<void> {
  const downloadPromise = page.waitForEvent('download')
  await page.getByRole('button', { name: '下载', exact: true }).click()
  const download = await downloadPromise
  expect(download.suggestedFilename()).toBe(fileName)
  const stream = await download.createReadStream()
  const chunks: Buffer[] = []
  for await (const chunk of stream) chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk))
  expect(Buffer.concat(chunks).toString('utf8')).toBe(expected)
}
