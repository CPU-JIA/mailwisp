import { MailWispClient } from './client'

describe('MailWispClient', () => {
  it('sends capabilities only in the Authorization header', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({ data: [], pagination: { next_cursor: 'next-page' } }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))
    const client = new MailWispClient('https://mail.example')
    const page = await client.listMessages('wisp_cap_secret')
    const [url, init] = fetchMock.mock.calls[0] ?? []
    expect(url).toBe('https://mail.example/api/v1/inboxes/me/messages?limit=100')
    expect(init?.headers).toMatchObject({ Authorization: 'Bearer wisp_cap_secret' })
    expect(String(url)).not.toContain('wisp_cap_secret')
    expect(page).toEqual({ items: [], nextCursor: 'next-page' })
  })

  it('places opaque message cursors only in the pagination query', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({ data: [], pagination: { next_cursor: '' } }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))
    const client = new MailWispClient('https://mail.example')
    await client.listMessages('', 50, 'opaque_cursor')
    const [url] = fetchMock.mock.calls[0] ?? []
    expect(url).toBe('https://mail.example/api/v1/inboxes/me/messages?limit=50&cursor=opaque_cursor')
  })

  it('preserves stable API error evidence', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({
      error: { code: 'unauthenticated', message: 'expired', request_id: 'request-1' },
    }), { status: 401, headers: { 'Content-Type': 'application/json' } }))
    const client = new MailWispClient()
    await expect(client.getInbox('expired')).rejects.toMatchObject({
      code: 'unauthenticated', status: 401, requestID: 'request-1',
    })
  })

  it('downloads attachments with the active capability', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response('attachment', { status: 200, headers: { 'Content-Type': 'text/plain' } }))
    const client = new MailWispClient('https://mail.example')
    const blob = await client.downloadAttachment('wisp_cap_secret', 'message-id', '2.1')
    expect(await blob.text()).toBe('attachment')
    const [url, init] = fetchMock.mock.calls[0] ?? []
    expect(url).toBe('https://mail.example/api/v1/inboxes/me/messages/message-id/attachments/2.1')
    expect(init?.headers).toMatchObject({ Authorization: 'Bearer wisp_cap_secret' })
  })

  it('uses Cookie session CSRF for mutations without exposing a capability', async () => {
    const inbox = { id: '1', address: 'demo@example.com', status: 'active', expires_at: '2026-07-16T00:00:00Z', created_at: '2026-07-15T00:00:00Z' }
    const fetchMock = vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(new Response(JSON.stringify({ data: { inbox, expires_at: inbox.expires_at, csrf_token: 'csrf-proof' } }), { status: 201, headers: { 'Content-Type': 'application/json' } }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
    const client = new MailWispClient('https://mail.example')
    await client.exchangeSession('wisp_cap_secret')
    await client.deleteInbox('')
    const [url, init] = fetchMock.mock.calls[1] ?? []
    expect(url).toBe('https://mail.example/api/v1/inboxes/me')
    expect(init?.headers).toMatchObject({ 'X-MailWisp-CSRF': 'csrf-proof' })
    expect(init?.headers).not.toHaveProperty('Authorization')
  })
})
