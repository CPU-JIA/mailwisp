import { randomUUID } from 'node:crypto'
import { once } from 'node:events'
import { createConnection } from 'node:net'
import { createInterface } from 'node:readline'

export interface SMTPMessage {
  port: number
  recipient: string
  subject: string
  textBody: string
  htmlBody: string
  attachmentName: string
  attachment: string | Buffer
  attachmentContentType?: string
}

export async function sendSMTPMessage(message: SMTPMessage): Promise<void> {
  for (const value of [message.recipient, message.subject, message.attachmentName]) {
    if (value.includes('\r') || value.includes('\n')) throw new Error('SMTP fixture headers must not contain CR or LF')
  }
  const socket = createConnection({ host: '127.0.0.1', port: message.port })
  socket.setTimeout(10_000, () => socket.destroy(new Error('SMTP session timed out')))
  await once(socket, 'connect')
  const lines = createInterface({ input: socket, crlfDelay: Number.POSITIVE_INFINITY })
  const iterator = lines[Symbol.asyncIterator]()
  const mixedBoundary = `mailwisp-mixed-${randomUUID()}`
  const alternativeBoundary = `mailwisp-alt-${randomUUID()}`
  const raw = [
    'From: MailWisp Verification <sender@example.net>',
    `To: ${message.recipient}`,
    `Subject: ${message.subject}`,
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
    'Content-Transfer-Encoding: 8bit',
    '',
    message.textBody,
    `--${alternativeBoundary}`,
    'Content-Type: text/html; charset=UTF-8',
    'Content-Transfer-Encoding: 8bit',
    '',
    message.htmlBody,
    `--${alternativeBoundary}--`,
    `--${mixedBoundary}`,
    `Content-Type: ${message.attachmentContentType || 'application/octet-stream'}; name="${message.attachmentName}"`,
    `Content-Disposition: attachment; filename="${message.attachmentName}"`,
    'Content-Transfer-Encoding: base64',
    '',
    Buffer.from(message.attachment).toString('base64'),
    `--${mixedBoundary}--`,
  ].join('\r\n')

  try {
    await readReply(iterator, 220)
    await writeLine(socket, 'EHLO verification.mailwisp.test')
    await readReply(iterator, 250)
    await writeLine(socket, 'MAIL FROM:<sender@example.net>')
    await readReply(iterator, 250)
    await writeLine(socket, `RCPT TO:<${message.recipient}>`)
    await readReply(iterator, 250)
    await writeLine(socket, 'DATA')
    await readReply(iterator, 354)
    const dotStuffed = raw.split('\r\n').map((line) => line.startsWith('.') ? `.${line}` : line).join('\r\n')
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

export function parsePort(raw: string | undefined, name: string): number {
  const port = Number.parseInt(raw || '', 10)
  if (!Number.isInteger(port) || port < 1 || port > 65_535) throw new Error(`${name} must be a valid TCP port`)
  return port
}
