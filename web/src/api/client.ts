import type { CreatedInbox, ErrorEnvelope, Inbox, MessageDetail, MessageSummary } from './types'

export class APIError extends Error {
  readonly code: string
  readonly status: number
  readonly requestID: string

  constructor(status: number, envelope?: ErrorEnvelope) {
    super(envelope?.error.message || `MailWisp API returned HTTP ${status}`)
    this.name = 'APIError'
    this.code = envelope?.error.code || 'unexpected_response'
    this.status = status
    this.requestID = envelope?.error.request_id || ''
  }
}
interface DataEnvelope<T> {
  data: T
}

export class MailWispClient {
  readonly #baseURL: string

  constructor(baseURL = '') {
    this.#baseURL = baseURL.replace(/\/$/, '')
  }

  createInbox(domain = '', ttlSeconds = 0, signal?: AbortSignal): Promise<CreatedInbox> {
    return this.#request<CreatedInbox>('/api/v1/inboxes', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ domain, ttl_seconds: ttlSeconds }),
      signal,
    })
  }

  getInbox(token: string, signal?: AbortSignal): Promise<Inbox> {
    return this.#request<Inbox>('/api/v1/inboxes/me', this.#authenticated(token, signal))
  }

  listMessages(token: string, limit = 100, signal?: AbortSignal): Promise<MessageSummary[]> {
    return this.#request<MessageSummary[]>(`/api/v1/inboxes/me/messages?limit=${limit}`, this.#authenticated(token, signal))
  }

  getMessage(token: string, messageID: string, signal?: AbortSignal): Promise<MessageDetail> {
    return this.#request<MessageDetail>(`/api/v1/inboxes/me/messages/${encodeURIComponent(messageID)}`, this.#authenticated(token, signal))
  }

  async deleteMessage(token: string, messageID: string, signal?: AbortSignal): Promise<void> {
    await this.#request<void>(`/api/v1/inboxes/me/messages/${encodeURIComponent(messageID)}`, {
      ...this.#authenticated(token, signal),
      method: 'DELETE',
    })
  }

  async deleteInbox(token: string, signal?: AbortSignal): Promise<void> {
    await this.#request<void>('/api/v1/inboxes/me', { ...this.#authenticated(token, signal), method: 'DELETE' })
  }

  #authenticated(token: string, signal?: AbortSignal): RequestInit {
    return { headers: { Authorization: `Bearer ${token}` }, signal }
  }

  async #request<T>(path: string, init: RequestInit): Promise<T> {
    let response: Response
    try {
      response = await fetch(this.#baseURL + path, {
        ...init,
        credentials: 'same-origin',
        headers: { Accept: 'application/json', ...init.headers },
      })
    } catch (error) {
      if (error instanceof DOMException && error.name === 'AbortError') {
        throw error
      }
      throw new APIError(0, { error: { code: 'network_error', message: 'Network request failed', request_id: '' } })
    }
    if (response.status === 204) {
      return undefined as T
    }
    const payload = await response.json().catch(() => undefined) as DataEnvelope<T> | ErrorEnvelope | undefined
    if (!response.ok) {
      throw new APIError(response.status, payload as ErrorEnvelope | undefined)
    }
    if (!payload || !('data' in payload)) {
      throw new APIError(response.status)
    }
    return payload.data
  }
}
