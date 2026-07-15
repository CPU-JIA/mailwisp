import { onBeforeUnmount, ref } from 'vue'

import { APIError, MailWispClient } from './api/client'
import type { Capability, Inbox, MessageDetail, MessageSummary } from './api/types'

export type Phase = 'welcome' | 'creating' | 'issued' | 'opening' | 'inbox' | 'detail' | 'error'

export interface ViewError {
  code: string
  requestID: string
}

export function useMailbox(client = new MailWispClient()) {
  const phase = ref<Phase>('welcome')
  const token = ref('')
  const inbox = ref<Inbox | null>(null)
  const issuedCapability = ref<Capability | null>(null)
  const messages = ref<MessageSummary[]>([])
  const selected = ref<MessageDetail | null>(null)
  const error = ref<ViewError | null>(null)
  const refreshing = ref(false)
  let activeRequest: AbortController | null = null
  let pollTimer: number | null = null

  async function createInbox(): Promise<void> {
    cancelActiveRequest()
    phase.value = 'creating'
    error.value = null
    activeRequest = new AbortController()
    try {
      const created = await client.createInbox('', 0, activeRequest.signal)
      token.value = created.capability.token
      inbox.value = created.inbox
      issuedCapability.value = created.capability
      phase.value = 'issued'
    } catch (cause) {
      handleError(cause)
    }
  }

  async function openInbox(plaintext = token.value): Promise<void> {
    const normalized = plaintext.trim()
    if (!normalized) return
    cancelActiveRequest()
    phase.value = 'opening'
    error.value = null
    activeRequest = new AbortController()
    try {
      const [loadedInbox, loadedMessages] = await Promise.all([
        client.getInbox(normalized, activeRequest.signal),
        client.listMessages(normalized, 100, activeRequest.signal),
      ])
      token.value = normalized
      inbox.value = loadedInbox
      messages.value = loadedMessages
      issuedCapability.value = null
      phase.value = 'inbox'
      schedulePoll()
    } catch (cause) {
      handleError(cause)
    }
  }

  async function refreshMessages(): Promise<void> {
    if (!token.value || refreshing.value) return
    refreshing.value = true
    cancelActiveRequest()
    const controller = new AbortController()
    activeRequest = controller
    try {
      messages.value = await client.listMessages(token.value, 100, controller.signal)
    } catch (cause) {
      if (!(cause instanceof DOMException && cause.name === 'AbortError')) handleError(cause)
    } finally {
      if (activeRequest === controller) activeRequest = null
      refreshing.value = false
      schedulePoll()
    }
  }

  async function openMessage(summary: MessageSummary): Promise<void> {
    cancelActiveRequest()
    activeRequest = new AbortController()
    try {
      selected.value = await client.getMessage(token.value, summary.id, activeRequest.signal)
      phase.value = 'detail'
    } catch (cause) {
      handleError(cause)
    }
  }

  function closeMessage(): void {
    selected.value = null
    phase.value = 'inbox'
    schedulePoll()
  }

  async function deleteMessage(): Promise<void> {
    if (!selected.value) return
    const deletedID = selected.value.id
    try {
      await client.deleteMessage(token.value, deletedID)
      messages.value = messages.value.filter((item) => item.id !== deletedID)
      closeMessage()
    } catch (cause) {
      handleError(cause)
    }
  }

  async function deleteInbox(): Promise<void> {
    try {
      await client.deleteInbox(token.value)
      reset()
    } catch (cause) {
      handleError(cause)
    }
  }

  function reset(): void {
    cancelActiveRequest()
    stopPoll()
    token.value = ''
    inbox.value = null
    issuedCapability.value = null
    messages.value = []
    selected.value = null
    error.value = null
    phase.value = 'welcome'
  }

  function handleError(cause: unknown): void {
    if (cause instanceof DOMException && cause.name === 'AbortError') return
    const apiError = cause instanceof APIError ? cause : new APIError(0)
    error.value = { code: apiError.code, requestID: apiError.requestID }
    phase.value = 'error'
    stopPoll()
  }

  function cancelActiveRequest(): void {
    activeRequest?.abort()
    activeRequest = null
  }

  function stopPoll(): void {
    if (pollTimer !== null) window.clearTimeout(pollTimer)
    pollTimer = null
  }

  function schedulePoll(): void {
    stopPoll()
    if (phase.value !== 'inbox') return
    pollTimer = window.setTimeout(() => {
      if (document.visibilityState === 'visible') void refreshMessages()
      else schedulePoll()
    }, 10_000)
  }

  onBeforeUnmount(() => {
    cancelActiveRequest()
    stopPoll()
  })

  return {
    phase, token, inbox, issuedCapability, messages, selected, error, refreshing,
    createInbox, openInbox, refreshMessages, openMessage, closeMessage, deleteMessage, deleteInbox, reset,
  }
}
