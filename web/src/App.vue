<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import SafeMessageFrame from './components/SafeMessageFrame.vue'
import type { Locale } from './i18n'
import { theme } from './theme'
import { useMailbox } from './useMailbox'

const { t, locale } = useI18n()
const mailbox = useMailbox()
const tokenInput = ref('')
const copied = ref('')
const deletingInbox = ref(false)
const contentMode = ref<'text' | 'html'>('text')

const language = computed({
  get: () => locale.value as Locale,
  set: (value: Locale) => { locale.value = value },
})

watch(language, (value) => {
  localStorage.setItem('mailwisp.locale', value)
  document.documentElement.lang = value
}, { immediate: true })

watch(() => mailbox.selected.value, () => { contentMode.value = 'text' })

const formattedExpiry = computed(() => mailbox.inbox.value ? formatDate(mailbox.inbox.value.expires_at) : '')

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
  await navigator.clipboard.writeText(value)
  copied.value = marker
  window.setTimeout(() => { if (copied.value === marker) copied.value = '' }, 1600)
}

async function confirmDeleteInbox(): Promise<void> {
  if (!deletingInbox.value) {
    deletingInbox.value = true
    window.setTimeout(() => { deletingInbox.value = false }, 4000)
    return
  }
  deletingInbox.value = false
  await mailbox.deleteInbox()
}

function openFromToken(): void {
  void mailbox.openInbox(tokenInput.value)
}

function errorMessage(code: string): string {
  const known = ['network_error', 'unauthenticated', 'csrf_failed', 'rate_limited', 'invalid_request', 'not_found', 'internal_error']
  return t(`error.${known.includes(code) ? code : 'generic'}`)
}
</script>

<template>
  <div class="page-shell">
    <header class="site-header">
      <button class="wordmark" type="button" aria-label="MailWisp" @click="mailbox.reset">
        <span class="wordmark-mark" aria-hidden="true">M</span>
        <span><strong>MailWisp</strong><small>{{ t('brand.tagline') }}</small></span>
      </button>
      <nav class="preferences" :aria-label="t('nav.language')">
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
            <button class="text-button" type="button" @click="copyText(mailbox.inbox.value.address, 'address')">{{ copied === 'address' ? t('issued.copied') : t('issued.copyAddress') }}</button>
          </div>
          <div class="credential-row token-row">
            <span>Capability</span><code>{{ mailbox.issuedCapability.value.token }}</code>
            <button class="text-button" type="button" @click="copyText(mailbox.issuedCapability.value.token, 'token')">{{ copied === 'token' ? t('issued.copied') : t('issued.copyToken') }}</button>
          </div>
          <p class="warning-note">{{ t('issued.warning') }}</p>
          <button class="button primary-button" type="button" @click="mailbox.openInbox()">{{ t('issued.continue') }}</button>
        </div>
      </section>

      <section v-else-if="mailbox.phase.value === 'error' && mailbox.error.value" class="error-state reveal" role="alert">
        <span class="error-glyph" aria-hidden="true">×</span>
        <div><p class="eyebrow">{{ t('error.title') }}</p><h1>{{ errorMessage(mailbox.error.value.code) }}</h1>
          <p v-if="mailbox.error.value.requestID" class="request-id">{{ t('error.requestID', { id: mailbox.error.value.requestID }) }}</p>
          <button class="button secondary-button" type="button" @click="mailbox.reset">{{ t('error.retry') }}</button>
        </div>
      </section>

      <template v-else-if="mailbox.inbox.value">
        <section v-if="mailbox.phase.value === 'inbox'" class="inbox-layout">
          <aside class="inbox-identity reveal" style="--delay: 0ms">
            <p class="eyebrow">{{ t('inbox.label') }}</p>
            <h1>{{ mailbox.inbox.value.address }}</h1>
            <button class="text-button" type="button" @click="copyText(mailbox.inbox.value.address, 'inbox-address')">{{ copied === 'inbox-address' ? t('issued.copied') : t('inbox.copy') }}</button>
            <dl><div><dt>{{ t('inbox.expires') }}</dt><dd>{{ formattedExpiry }}</dd></div></dl>
            <div class="inbox-actions">
              <button class="text-button" type="button" @click="mailbox.reset">{{ t('inbox.signOut') }}</button>
              <button class="text-button danger" type="button" @click="confirmDeleteInbox">{{ deletingInbox ? t('inbox.deleteConfirm') : t('inbox.delete') }}</button>
            </div>
          </aside>

          <div class="message-column reveal" style="--delay: 80ms">
            <div class="section-heading">
              <div><p class="section-number">01</p><h2>{{ t('inbox.messages') }}</h2></div>
              <button class="icon-button" type="button" :aria-label="t('inbox.refresh')" :disabled="mailbox.refreshing.value" @click="mailbox.refreshMessages">
                <span :class="{ spinning: mailbox.refreshing.value }" aria-hidden="true">↻</span>
              </button>
            </div>
            <div v-if="mailbox.messages.value.length" class="message-list">
              <button v-for="item in mailbox.messages.value" :key="item.id" class="message-row" type="button" @click="mailbox.openMessage(item)">
                <span class="message-sender">{{ senderLabel(item.envelope_sender) }}</span>
                <span class="message-subject">{{ item.subject || t('inbox.noSubject') }}</span>
                <span class="message-preview">{{ item.preview }}</span>
                <span class="message-time">{{ formatRelative(item.received_at) }}</span>
                <span v-if="item.parse_status !== 'parsed'" class="status-chip">{{ item.parse_status === 'failed' ? t('inbox.failed') : t('inbox.pending') }}</span>
              </button>
            </div>
            <div v-else class="empty-state"><span aria-hidden="true">⌁</span><h3>{{ t('inbox.emptyTitle') }}</h3><p>{{ t('inbox.emptyBody') }}</p></div>
          </div>
        </section>

        <article v-else-if="mailbox.phase.value === 'detail' && mailbox.selected.value" class="message-detail reveal">
          <div class="detail-toolbar">
            <button class="text-button" type="button" @click="mailbox.closeMessage">← {{ t('message.back') }}</button>
            <button class="text-button danger" type="button" @click="mailbox.deleteMessage">{{ t('message.delete') }}</button>
          </div>
          <header class="message-header">
            <p class="eyebrow">{{ senderLabel(mailbox.selected.value.envelope_sender) }}</p>
            <h1>{{ mailbox.selected.value.subject || t('inbox.noSubject') }}</h1>
            <div class="message-facts"><span>{{ formatDate(mailbox.selected.value.received_at) }}</span><span>{{ formatBytes(mailbox.selected.value.size_bytes) }}</span></div>
          </header>
          <div v-if="mailbox.selected.value.parse_status !== 'parsed'" class="parse-notice">{{ t('message.parsePending') }}</div>
          <template v-else>
            <div class="content-tabs" role="tablist">
              <button type="button" role="tab" :aria-selected="contentMode === 'text'" @click="contentMode = 'text'">{{ t('message.text') }}</button>
              <button type="button" role="tab" :aria-selected="contentMode === 'html'" @click="contentMode = 'html'">{{ t('message.html') }}</button>
            </div>
            <pre v-if="contentMode === 'text'" class="plain-content">{{ mailbox.selected.value.text || mailbox.selected.value.preview }}</pre>
            <div v-else class="html-content"><p class="privacy-note">◌ {{ t('message.privacy') }}</p><SafeMessageFrame v-if="mailbox.selected.value.html_source" :html="mailbox.selected.value.html_source" :title="mailbox.selected.value.subject" /><p v-else>{{ t('message.htmlUnavailable') }}</p></div>
            <section v-if="mailbox.selected.value.attachments.length" class="attachments"><h2>{{ t('message.attachments') }}</h2><ul><li v-for="attachment in mailbox.selected.value.attachments" :key="attachment.part_path"><span>{{ attachment.file_name || attachment.content_type }}</span><small>{{ formatBytes(attachment.size_bytes) }}</small></li></ul></section>
          </template>
        </article>
      </template>
    </main>

    <footer><span>MailWisp</span><span>Fast mail. Zero trace.</span></footer>
  </div>
</template>
