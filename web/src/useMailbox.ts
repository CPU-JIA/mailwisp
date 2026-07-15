import { onBeforeUnmount, ref } from 'vue'

import { APIError, MailWispClient } from './api/client'
import type { Attachment, Capability, Inbox, MessageDetail, MessageSummary } from './api/types'
import { normalizeCID } from './messageHTML'

export type Phase = 'welcome' | 'creating' | 'issued' | 'opening' | 'inbox' | 'detail' | 'error'

export interface ViewError {
  code: string
  requestID: string
}

export function useMailbox(client = new MailWispClient()) {
  const phase = ref<Phase>('welcome')
  const token = ref('')
	const sessionActive = ref(false)
  const inbox = ref<Inbox | null>(null)
  const issuedCapability = ref<Capability | null>(null)
  const messages = ref<MessageSummary[]>([])
  const selected = ref<MessageDetail | null>(null)
  const inlineImages = ref<Record<string, string>>({})
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
	  let credential = normalized
	  let loadedInbox: Inbox
	  try {
		const session = await client.exchangeSession(normalized, activeRequest.signal)
		loadedInbox = session.inbox
		sessionActive.value = true
		credential = ''
	  } catch (cause) {
		if (!(cause instanceof APIError) || cause.code !== 'not_configured') throw cause
		loadedInbox = await client.getInbox(normalized, activeRequest.signal)
		sessionActive.value = false
	  }
	  const loadedMessages = await client.listMessages(credential, 100, activeRequest.signal)
	  token.value = credential
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
	if ((!token.value && !sessionActive.value) || refreshing.value) return
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
      inlineImages.value = {}
      phase.value = 'detail'
      void loadInlineImages(selected.value, activeRequest.signal)
    } catch (cause) {
      handleError(cause)
    }
  }

  function closeMessage(): void {
    cancelActiveRequest()
    selected.value = null
    inlineImages.value = {}
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

  async function downloadAttachment(attachment: Attachment): Promise<void> {
    if (!selected.value) return
    try {
      const blob = await client.downloadAttachment(token.value, selected.value.id, attachment.part_path)
      const url = URL.createObjectURL(blob)
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = attachment.file_name || 'attachment'
      anchor.click()
      URL.revokeObjectURL(url)
    } catch (cause) {
      handleError(cause)
    }
  }

  async function loadInlineImages(detail: MessageDetail, signal: AbortSignal): Promise<void> {
    const candidates: Attachment[] = []
    let totalBytes = 0
    for (const attachment of detail.attachments) {
      if (!attachment.content_id || !attachment.content_type.startsWith('image/') || attachment.size_bytes <= 0 || attachment.size_bytes > 2 * 1024 * 1024) continue
      if (candidates.length >= 16 || totalBytes + attachment.size_bytes > 8 * 1024 * 1024) break
      candidates.push(attachment)
      totalBytes += attachment.size_bytes
    }
    const sources: Record<string, string> = {}
    let next = 0
    const workers = Array.from({ length: Math.min(4, candidates.length) }, async () => {
      while (next < candidates.length && !signal.aborted) {
        const attachment = candidates[next++]
        if (!attachment) return
        try {
          const blob = await client.downloadAttachment(token.value, detail.id, attachment.part_path, signal)
          if (!blob.type.startsWith('image/') || blob.size > 2 * 1024 * 1024) continue
          sources[normalizeCID(attachment.content_id)] = await blobToDataURL(blob)
        } catch (cause) {
          if (cause instanceof DOMException && cause.name === 'AbortError') return
        }
      }
    })
    await Promise.all(workers)
    if (!signal.aborted && selected.value?.id === detail.id) inlineImages.value = sources
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
	if (sessionActive.value) void client.deleteSession().catch(() => undefined)
	sessionActive.value = false
	token.value = ''
    inbox.value = null
    issuedCapability.value = null
    messages.value = []
    selected.value = null
    inlineImages.value = {}
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

	void restoreSession()

	async function restoreSession(): Promise<void> {
	  try {
		const session = await client.getSession()
		const loadedMessages = await client.listMessages('', 100)
		sessionActive.value = true
		inbox.value = session.inbox
		messages.value = loadedMessages
		phase.value = 'inbox'
		schedulePoll()
	  } catch (cause) {
		if (cause instanceof APIError && (cause.code === 'unauthenticated' || cause.code === 'not_configured' || cause.code === 'network_error' || cause.code === 'unexpected_response')) return
		handleError(cause)
	  }
	}

  return {
	phase, token, sessionActive, inbox, issuedCapability, messages, selected, inlineImages, error, refreshing,
    createInbox, openInbox, refreshMessages, openMessage, closeMessage, deleteMessage, downloadAttachment, deleteInbox, reset,
  }
}

function blobToDataURL(blob: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.addEventListener('load', () => resolve(String(reader.result)))
    reader.addEventListener('error', () => reject(reader.error))
    reader.readAsDataURL(blob)
  })
}
