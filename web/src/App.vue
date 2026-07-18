<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import SafeMessageFrame from './components/SafeMessageFrame.vue'
import type { Locale } from './i18n'
import { theme } from './theme'
import { useMailbox } from './useMailbox'

const { t, locale } = useI18n()
const mailbox = useMailbox()
const tokenInput = ref('')
const copied = ref('')
const copyFailed = ref('')
const deletingInbox = ref(false)
const deletingMessage = ref(false)
const contentMode = ref<'text' | 'html'>('text')
let copyTimer: number | undefined
let inboxDeleteTimer: number | undefined
let messageDeleteTimer: number | undefined

const language = computed({
  get: () => locale.value as Locale,
  set: (value: Locale) => { locale.value = value },
})

watch(language, (value) => {
  localStorage.setItem('mailwisp.locale', value)
  document.documentElement.lang = value
}, { immediate: true })

watch(() => mailbox.selected.value, () => {
  contentMode.value = 'text'
  deletingMessage.value = false
  if (messageDeleteTimer !== undefined) window.clearTimeout(messageDeleteTimer)
})

const formattedExpiry = computed(() => mailbox.inbox.value ? formatDate(mailbox.inbox.value.expires_at) : '')
const copyAnnouncement = computed(() => copied.value
  ? t('issued.copied')
  : copyFailed.value
    ? t('issued.copyFailed')
    : '')

function formatDate(value: string): string {
  return new Intl.DateTimeFormat(language.value, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value))
}

function formatRelative(value: string): string {
  const deltaMinutes = Math.round((new Date(value).getTime() - Date.now()) / 60_000)
  const formatter = new Intl.RelativeTimeFormat(language.value, { numeric: 'auto' })
  if (Math.abs(deltaMinutes) < 60) return formatter.format(deltaMinutes, 'minute')
  return formatter.format(Math.round(deltaMinutes / 60), 'hour')
}

function formatBytes(bytes: number): string {
  return new Intl.NumberFormat(language.value, { style: 'unit', unit: bytes >= 1_048_576 ? 'megabyte' : 'kilobyte', maximumFractionDigits: 1 }).format(bytes / (bytes >= 1_048_576 ? 1_048_576 : 1024))
}

function senderLabel(sender: string): string {
  return sender || t('inbox.unknownSender')
}

async function copyText(value: string, marker: string): Promise<void> {
  if (copyTimer !== undefined) window.clearTimeout(copyTimer)
  copyFailed.value = ''
  try {
    await navigator.clipboard.writeText(value)
    copied.value = marker
  } catch {
    copied.value = ''
    copyFailed.value = marker
  }
  copyTimer = window.setTimeout(() => {
    if (copied.value === marker) copied.value = ''
    if (copyFailed.value === marker) copyFailed.value = ''
  }, 2400)
}

async function confirmDeleteInbox(): Promise<void> {
  if (!deletingInbox.value) {
    deletingInbox.value = true
    if (inboxDeleteTimer !== undefined) window.clearTimeout(inboxDeleteTimer)
    inboxDeleteTimer = window.setTimeout(() => { deletingInbox.value = false }, 4000)
    return
  }
  deletingInbox.value = false
  await mailbox.deleteInbox()
}

async function confirmDeleteMessage(): Promise<void> {
  if (!deletingMessage.value) {
    deletingMessage.value = true
    if (messageDeleteTimer !== undefined) window.clearTimeout(messageDeleteTimer)
    messageDeleteTimer = window.setTimeout(() => { deletingMessage.value = false }, 4000)
    return
  }
  deletingMessage.value = false
  await mailbox.deleteMessage()
}

function openFromToken(): void {
  void mailbox.openInbox(tokenInput.value)
}

function errorMessage(code: string): string {
  const known = ['network_error', 'unauthenticated', 'csrf_failed', 'rate_limited', 'invalid_request', 'not_found', 'internal_error']
  return t(`error.${known.includes(code) ? code : 'generic'}`)
}

function handleContentTabKeydown(event: KeyboardEvent): void {
  const order: Array<'text' | 'html'> = ['text', 'html']
  const current = order.indexOf(contentMode.value)
  let next = current
  if (event.key === 'ArrowRight') next = (current + 1) % order.length
  else if (event.key === 'ArrowLeft') next = (current - 1 + order.length) % order.length
  else if (event.key === 'Home') next = 0
  else if (event.key === 'End') next = order.length - 1
  else return
  event.preventDefault()
  const mode = order[next]
  if (!mode) return
  contentMode.value = mode
  document.getElementById(`message-tab-${mode}`)?.focus()
}

onBeforeUnmount(() => {
  if (copyTimer !== undefined) window.clearTimeout(copyTimer)
  if (inboxDeleteTimer !== undefined) window.clearTimeout(inboxDeleteTimer)
  if (messageDeleteTimer !== undefined) window.clearTimeout(messageDeleteTimer)
})
</script>

<template>
  <div class="page-shell">
    <header class="site-header">
      <button class="wordmark" type="button" aria-label="MailWisp" :disabled="mailbox.leaving.value" @click="mailbox.leaveInbox">
        <span class="wordmark-mark" aria-hidden="true">M</span>
        <span><strong>MailWisp</strong><small>{{ t('brand.tagline') }}</small></span>
      </button>
      <nav class="preferences" :aria-label="t('nav.preferences')">
        <label class="select-control">
          <span>{{ t('nav.language') }}</span>
          <select v-model="language">
            <option value="zh-CN">中文</option>
            <option value="en-US">English</option>
          </select>
        </label>
        <label class="select-control">
          <span>{{ t('nav.theme') }}</span>
          <select v-model="theme">
            <option value="system">{{ t('theme.system') }}</option>
            <option value="light">{{ t('theme.light') }}</option>
            <option value="dark">{{ t('theme.dark') }}</option>
            <option value="mist">{{ t('theme.mist') }}</option>
          </select>
        </label>
      </nav>
      <p class="sr-only" role="status" aria-live="polite">{{ copyAnnouncement }}</p>
    </header>

    <main>
      <section v-if="mailbox.phase.value === 'welcome' || mailbox.phase.value === 'creating' || mailbox.phase.value === 'opening'" class="welcome-grid">
        <div class="hero-copy reveal" style="--delay: 0ms">
          <p class="eyebrow">{{ t('welcome.eyebrow') }}</p>
          <h1>{{ t('welcome.title') }}</h1>
          <p class="hero-description">{{ t('welcome.description') }}</p>
          <button class="button primary-button" type="button" :disabled="mailbox.phase.value !== 'welcome'" @click="mailbox.createInbox">
            <span class="button-mark" aria-hidden="true">↗</span>
            {{ mailbox.phase.value === 'creating' ? t('welcome.creating') : t('welcome.create') }}
          </button>
        </div>

        <aside class="token-entry reveal" style="--delay: 90ms">
          <p class="section-number">02</p>
          <h2>{{ t('welcome.existing') }}</h2>
          <form @submit.prevent="openFromToken">
            <label for="capability-token">{{ t('welcome.tokenLabel') }}</label>
            <textarea id="capability-token" v-model="tokenInput" rows="4" spellcheck="false" autocomplete="off" :placeholder="t('welcome.tokenPlaceholder')" required />
            <button class="button secondary-button" type="submit" :disabled="mailbox.phase.value !== 'welcome' || !tokenInput.trim()">
              {{ mailbox.phase.value === 'opening' ? t('welcome.opening') : t('welcome.open') }}
            </button>
          </form>
          <p class="privacy-note"><span aria-hidden="true">◌</span>{{ t('welcome.privacy') }}</p>
        </aside>
        <div class="wisp-lines" aria-hidden="true"><i /><i /><i /></div>
      </section>

      <section v-else-if="mailbox.phase.value === 'issued' && mailbox.inbox.value && mailbox.issuedCapability.value" class="issued-layout reveal">
        <div>
          <p class="eyebrow">{{ t('issued.eyebrow') }}</p>
          <h1>{{ t('issued.title') }}</h1>
          <p class="hero-description">{{ t('issued.description') }}</p>
        </div>
        <div class="credential-sheet">
          <div class="credential-row">
            <span>Email</span><code>{{ mailbox.inbox.value.address }}</code>
            <button class="text-button" type="button" @click="copyText(mailbox.inbox.value.address, 'address')">{{ copied === 'address' ? t('issued.copied') : copyFailed === 'address' ? t('issued.copyFailed') : t('issued.copyAddress') }}</button>
          </div>
          <div class="credential-row token-row">
            <span>Capability</span><code>{{ mailbox.issuedCapability.value.token }}</code>
            <button class="text-button" type="button" @click="copyText(mailbox.issuedCapability.value.token, 'token')">{{ copied === 'token' ? t('issued.copied') : copyFailed === 'token' ? t('issued.copyFailed') : t('issued.copyToken') }}</button>
          </div>
          <p class="warning-note">{{ t('issued.warning') }}</p>
          <button class="button primary-button" type="button" @click="mailbox.openInbox()">{{ t('issued.continue') }}</button>
        </div>
      </section>

      <section v-else-if="mailbox.phase.value === 'error' && mailbox.error.value" class="error-state reveal" role="alert">
        <span class="error-glyph" aria-hidden="true">×</span>
        <div><p class="eyebrow">{{ t('error.title') }}</p><h1>{{ errorMessage(mailbox.error.value.code) }}</h1>
          <p v-if="mailbox.error.value.requestID" class="request-id">{{ t('error.requestID', { id: mailbox.error.value.requestID }) }}</p>
          <button class="button secondary-button" type="button" :disabled="mailbox.leaving.value" @click="mailbox.leaveInbox">{{ mailbox.leaving.value ? t('inbox.leaving') : t('error.retry') }}</button>
        </div>
      </section>

      <template v-else-if="mailbox.inbox.value">
        <section v-if="mailbox.phase.value === 'inbox'" class="inbox-layout">
          <aside class="inbox-identity reveal" style="--delay: 0ms">
            <p class="eyebrow">{{ t('inbox.label') }}</p>
            <h1>{{ mailbox.inbox.value.address }}</h1>
            <button class="text-button" type="button" @click="copyText(mailbox.inbox.value.address, 'inbox-address')">{{ copied === 'inbox-address' ? t('issued.copied') : copyFailed === 'inbox-address' ? t('issued.copyFailed') : t('inbox.copy') }}</button>
            <dl><div><dt>{{ t('inbox.expires') }}</dt><dd>{{ formattedExpiry }}</dd></div></dl>
            <div class="inbox-actions">
              <button class="text-button" type="button" :disabled="mailbox.leaving.value" @click="mailbox.leaveInbox">{{ mailbox.leaving.value ? t('inbox.leaving') : t('inbox.signOut') }}</button>
              <button class="text-button danger" type="button" @click="confirmDeleteInbox">{{ deletingInbox ? t('inbox.deleteConfirm') : t('inbox.delete') }}</button>
            </div>
          </aside>

          <div class="message-column reveal" style="--delay: 80ms">
            <div class="section-heading">
              <div><p class="section-number">01</p><h2>{{ t('inbox.messages') }}</h2></div>
              <button class="icon-button" type="button" :aria-label="t('inbox.refresh')" :disabled="mailbox.refreshing.value || mailbox.loadingMore.value" @click="mailbox.refreshMessages">
                <span :class="{ spinning: mailbox.refreshing.value }" aria-hidden="true">↻</span>
              </button>
            </div>
            <div v-if="mailbox.messages.value.length" class="message-list">
              <button v-for="item in mailbox.messages.value" :key="item.id" class="message-row" :class="{ unread: !item.seen }" type="button" @click="mailbox.openMessage(item)">
                <span class="message-sender">{{ senderLabel(item.envelope_sender) }}</span>
                <span class="message-subject">{{ item.subject || t('inbox.noSubject') }}</span>
                <span class="message-preview">{{ item.preview }}</span>
                <span class="message-time">{{ formatRelative(item.received_at) }}</span>
                <span v-if="item.parse_status !== 'parsed'" class="status-chip">{{ item.parse_status === 'failed' ? t('inbox.failed') : t('inbox.pending') }}</span>
              </button>
              <div v-if="mailbox.nextCursor.value || mailbox.loadMoreError.value" class="message-pagination">
                <p v-if="mailbox.loadMoreError.value" role="status">{{ t('inbox.loadMoreError') }}</p>
                <button class="button secondary-button" type="button" :disabled="mailbox.loadingMore.value || mailbox.refreshing.value" @click="mailbox.loadMoreMessages">
                  {{ mailbox.loadingMore.value ? t('inbox.loadingMore') : t('inbox.loadMore') }}
                </button>
              </div>
            </div>
            <div v-else class="empty-state"><span aria-hidden="true">⌁</span><h3>{{ t('inbox.emptyTitle') }}</h3><p>{{ t('inbox.emptyBody') }}</p></div>
          </div>
        </section>

        <article v-else-if="mailbox.phase.value === 'detail' && mailbox.selected.value" class="message-detail reveal">
          <div class="detail-toolbar">
            <button class="text-button" type="button" @click="mailbox.closeMessage">← {{ t('message.back') }}</button>
            <div class="detail-actions">
              <button class="text-button" type="button" @click="mailbox.downloadSource">{{ t('message.downloadSource') }}</button>
              <button class="text-button danger" type="button" @click="confirmDeleteMessage">{{ deletingMessage ? t('message.deleteConfirm') : t('message.delete') }}</button>
            </div>
          </div>
          <header class="message-header">
            <p class="eyebrow">{{ senderLabel(mailbox.selected.value.envelope_sender) }}</p>
            <h1>{{ mailbox.selected.value.subject || t('inbox.noSubject') }}</h1>
            <div class="message-facts"><span>{{ formatDate(mailbox.selected.value.received_at) }}</span><span>{{ formatBytes(mailbox.selected.value.size_bytes) }}</span></div>
          </header>
          <div v-if="mailbox.selected.value.parse_status === 'failed'" class="parse-notice" role="status">{{ t('message.parseFailed') }}</div>
          <div v-else-if="mailbox.selected.value.parse_status !== 'parsed'" class="parse-notice" role="status">{{ t('message.parsePending') }}</div>
          <template v-else>
            <div class="content-tabs" role="tablist" :aria-label="t('message.contentViews')" @keydown="handleContentTabKeydown">
              <button id="message-tab-text" type="button" role="tab" aria-controls="message-panel-text" :aria-selected="contentMode === 'text'" :tabindex="contentMode === 'text' ? 0 : -1" @click="contentMode = 'text'">{{ t('message.text') }}</button>
              <button id="message-tab-html" type="button" role="tab" aria-controls="message-panel-html" :aria-selected="contentMode === 'html'" :tabindex="contentMode === 'html' ? 0 : -1" @click="contentMode = 'html'">{{ t('message.html') }}</button>
            </div>
            <pre v-if="contentMode === 'text'" id="message-panel-text" class="plain-content" role="tabpanel" aria-labelledby="message-tab-text" tabindex="0">{{ mailbox.selected.value.text || mailbox.selected.value.preview }}</pre>
            <div v-else id="message-panel-html" class="html-content" role="tabpanel" aria-labelledby="message-tab-html" tabindex="0"><p class="privacy-note">◌ {{ t('message.privacy') }}</p><SafeMessageFrame v-if="mailbox.selected.value.html_source" :html="mailbox.selected.value.html_source" :title="t('message.htmlFrameTitle', { subject: mailbox.selected.value.subject || t('inbox.noSubject') })" :cid-sources="mailbox.inlineImages.value" /><p v-else>{{ t('message.htmlUnavailable') }}</p></div>
            <section v-if="mailbox.selected.value.attachments.length" class="attachments"><h2>{{ t('message.attachments') }}</h2><ul><li v-for="attachment in mailbox.selected.value.attachments" :key="attachment.part_path"><span>{{ attachment.file_name || attachment.content_type }}</span><span class="attachment-actions"><small>{{ formatBytes(attachment.size_bytes) }}</small><button class="text-button" type="button" @click="mailbox.downloadAttachment(attachment)">{{ t('message.download') }}</button></span></li></ul></section>
          </template>
        </article>
      </template>
    </main>

    <footer><span>MailWisp</span><span>Fast mail. Zero trace.</span></footer>
  </div>
</template>
