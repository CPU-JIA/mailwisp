import { MailWispClient } from './client'

describe('MailWispClient', () => {
  it('sends capabilities only in the Authorization header', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({ data: [] }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))
    const client = new MailWispClient('https://mail.example')
    await client.listMessages('wisp_cap_secret')
    const [url, init] = fetchMock.mock.calls[0] ?? []
    expect(url).toBe('https://mail.example/api/v1/inboxes/me/messages?limit=100')
    expect(init?.headers).toMatchObject({ Authorization: 'Bearer wisp_cap_secret' })
    expect(String(url)).not.toContain('wisp_cap_secret')
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
})
