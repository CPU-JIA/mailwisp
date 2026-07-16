import { once } from 'node:events'
import { createConnection } from 'node:net'
import { createInterface } from 'node:readline'
import { randomUUID } from 'node:crypto'

import { expect, test } from '@playwright/test'

const smtpPort = parsePort(process.env.MAILWISP_E2E_SMTP_PORT)

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
  await sendSMTPMessage(address, subject, attachment)

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

async function sendSMTPMessage(recipient: string, subject: string, attachment: string): Promise<void> {
  const socket = createConnection({ host: '127.0.0.1', port: smtpPort })
  socket.setTimeout(10_000, () => socket.destroy(new Error('SMTP session timed out')))
  await once(socket, 'connect')
  const lines = createInterface({ input: socket, crlfDelay: Number.POSITIVE_INFINITY })
  const iterator = lines[Symbol.asyncIterator]()
  const mixedBoundary = `mailwisp-mixed-${randomUUID()}`
  const alternativeBoundary = `mailwisp-alt-${randomUUID()}`
  const message = [
    'From: Production Sender <sender@example.net>',
    `To: ${recipient}`,
    `Subject: ${subject}`,
    `Date: ${new Date().toUTCString()}`,
    `Message-ID: <${randomUUID()}@mailwisp.test>`,
    'MIME-Version: 1.0',
    `Content-Type: multipart/mixed; boundary="${mixedBoundary}"`,
    '',
    `--${mixedBoundary}`,
    `Content-Type: multipart/alternative; boundary="${alternativeBoundary}"`,
    '',
    `--${alternativeBoundary}`,
    'Content-Type: text/plain; charset=UTF-8',
    'Content-Transfer-Encoding: 7bit',
    '',
    'Production pipeline body.',
    `--${alternativeBoundary}`,
    'Content-Type: text/html; charset=UTF-8',
    'Content-Transfer-Encoding: 7bit',
    '',
    '<p>HTML production body.</p><script>document.body.dataset.executed="true"</script><img src="https://tracker.invalid/pixel">',
    `--${alternativeBoundary}--`,
    `--${mixedBoundary}`,
    'Content-Type: text/plain; name="proof.txt"',
    'Content-Disposition: attachment; filename="proof.txt"',
    'Content-Transfer-Encoding: base64',
    '',
    Buffer.from(attachment).toString('base64'),
    `--${mixedBoundary}--`,
  ].join('\r\n')

  try {
    await readReply(iterator, 220)
    await writeLine(socket, 'EHLO production-e2e.mailwisp.test')
    await readReply(iterator, 250)
    await writeLine(socket, 'MAIL FROM:<sender@example.net>')
    await readReply(iterator, 250)
    await writeLine(socket, `RCPT TO:<${recipient}>`)
    await readReply(iterator, 250)
    await writeLine(socket, 'DATA')
    await readReply(iterator, 354)
    const dotStuffed = message.split('\r\n').map((line) => line.startsWith('.') ? `.${line}` : line).join('\r\n')
    await writeLine(socket, `${dotStuffed}\r\n.`)
    await readReply(iterator, 250)
    await writeLine(socket, 'QUIT')
    await readReply(iterator, 221)
  } finally {
    lines.close()
    socket.destroy()
  }
}

async function readReply(iterator: AsyncIterableIterator<string>, expectedCode: number): Promise<void> {
  for (;;) {
    const next = await iterator.next()
    if (next.done || next.value.length < 4) throw new Error(`SMTP closed while waiting for ${expectedCode}`)
    const actualCode = Number.parseInt(next.value.slice(0, 3), 10)
    if (actualCode !== expectedCode) throw new Error(`SMTP returned ${next.value}; expected ${expectedCode}`)
    if (next.value[3] === ' ') return
    if (next.value[3] !== '-') throw new Error(`SMTP returned an invalid reply: ${next.value}`)
  }
}

function writeLine(socket: ReturnType<typeof createConnection>, line: string): Promise<void> {
  return new Promise((resolve, reject) => {
    socket.write(`${line}\r\n`, (error) => error ? reject(error) : resolve())
  })
}

function parsePort(raw: string | undefined): number {
  const port = Number.parseInt(raw || '', 10)
  if (!Number.isInteger(port) || port < 1 || port > 65_535) throw new Error('MAILWISP_E2E_SMTP_PORT must be a valid TCP port')
  return port
}
