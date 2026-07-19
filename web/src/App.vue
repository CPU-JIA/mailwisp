<script setup lang="ts">
import {
  AlertTriangle,
  ArrowLeft,
  ArrowRight,
  Check,
  ChevronDown,
  Clock3,
  Copy,
  Download,
  FileText,
  Inbox,
  KeyRound,
  Languages,
  LoaderCircle,
  LogOut,
  Mail,
  MailOpen,
  Palette,
  Paperclip,
  RefreshCw,
  RotateCcw,
  ShieldCheck,
  Trash2,
  Wind,
} from '@lucide/vue'
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import SafeMessageFrame from './components/SafeMessageFrame.vue'
import type { MailAddress } from './api/types'
import type { Locale } from './i18n'
import { theme } from './theme'
import { useMailbox } from './useMailbox'

type WelcomeMode = 'create' | 'restore'

const { t, locale } = useI18n()
const mailbox = useMailbox()
const welcomeMode = ref<WelcomeMode>('create')
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
const formattedExpiry = computed(() => mailbox.inbox.value ? formatDate(mailbox.inbox.value.expires_at) : '')
const copyAnnouncement = computed(() => copied.value
  ? t('issued.copied')
  : copyFailed.value
    ? t('issued.copyFailed')
    : '')

watch(language, (value) => {
  localStorage.setItem('mailwisp.locale', value)
  document.documentElement.lang = value
}, { immediate: true })

watch(() => mailbox.selected.value, () => {
  contentMode.value = 'text'
  deletingMessage.value = false
  if (messageDeleteTimer !== undefined) window.clearTimeout(messageDeleteTimer)
})

watch(() => mailbox.phase.value, (phase, previous) => {
  if (phase === 'welcome' && previous !== 'welcome') welcomeMode.value = 'create'
})

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
  const megabytes = bytes >= 1_048_576
  return new Intl.NumberFormat(language.value, {
    style: 'unit',
    unit: megabytes ? 'megabyte' : 'kilobyte',
    maximumFractionDigits: 1,
  }).format(bytes / (megabytes ? 1_048_576 : 1024))
}

function senderLabel(sender: string): string {
  return sender || t('inbox.unknownSender')
}

function addressParts(address: string): { local: string, domain: string } {
  const separator = address.lastIndexOf('@')
  if (separator <= 0) return { local: address, domain: '' }
  return { local: address.slice(0, separator), domain: address.slice(separator) }
}

function formatAddresses(addresses: MailAddress[]): string {
  if (addresses.length === 0) return '-'
  return addresses.map((address) => address.name ? `${address.name} <${address.address}>` : address.address).join(', ')
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

function handleBrandClick(): void {
  if (mailbox.inbox.value) void mailbox.leaveInbox()
}

function errorMessage(code: string): string {
  const known = ['network_error', 'unauthenticated', 'csrf_failed', 'rate_limited', 'invalid_request', 'not_found', 'internal_error']
  return t(`error.${known.includes(code) ? code : 'generic'}`)
}

function handleWelcomeTabKeydown(event: KeyboardEvent): void {
  const order: WelcomeMode[] = ['create', 'restore']
  const current = order.indexOf(welcomeMode.value)
  let next = current
  if (event.key === 'ArrowRight') next = (current + 1) % order.length
  else if (event.key === 'ArrowLeft') next = (current - 1 + order.length) % order.length
  else if (event.key === 'Home') next = 0
  else if (event.key === 'End') next = order.length - 1
  else return
  event.preventDefault()
  const mode = order[next]
  if (!mode) return
  welcomeMode.value = mode
  document.getElementById(`welcome-tab-${mode}`)?.focus()
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
  <div class="app-shell" :class="{ 'has-inbox': Boolean(mailbox.inbox.value) }">
    <a class="skip-link" href="#main-content">{{ t('nav.skip') }}</a>

    <header class="app-header">
      <button class="brand" type="button" aria-label="MailWisp" :disabled="mailbox.leaving.value" @click="handleBrandClick">
        <span class="brand-mark" aria-hidden="true"><Wind :size="21" :stroke-width="2.2" /></span>
        <span class="brand-copy"><strong>MailWisp</strong><small>{{ t('brand.tagline') }}</small></span>
      </button>

      <nav class="preferences" :aria-label="t('nav.preferences')">
        <label class="utility-select">
          <Languages :size="18" aria-hidden="true" />
          <span class="sr-only">{{ t('nav.language') }}</span>
          <span class="utility-value" aria-hidden="true">{{ language === 'zh-CN' ? '中文' : 'English' }}</span>
          <select v-model="language" :aria-label="t('nav.language')">
            <option value="zh-CN">中文</option>
            <option value="en-US">English</option>
          </select>
          <ChevronDown :size="14" aria-hidden="true" />
        </label>
        <label class="utility-select theme-select">
          <Palette :size="18" aria-hidden="true" />
          <span class="sr-only">{{ t('nav.theme') }}</span>
          <span class="utility-value" aria-hidden="true">{{ t(`theme.${theme}`) }}</span>
          <select v-model="theme" :aria-label="t('nav.theme')">
            <option value="system">{{ t('theme.system') }}</option>
            <option value="light">{{ t('theme.light') }}</option>
            <option value="dark">{{ t('theme.dark') }}</option>
            <option value="mist">{{ t('theme.mist') }}</option>
          </select>
          <ChevronDown :size="14" aria-hidden="true" />
        </label>
      </nav>
      <p class="sr-only" role="status" aria-live="polite">{{ copyAnnouncement }}</p>
    </header>

    <main id="main-content" class="app-main" :class="{ 'workspace-main': Boolean(mailbox.inbox.value) }">
      <section v-if="mailbox.phase.value === 'welcome' || mailbox.phase.value === 'creating' || mailbox.phase.value === 'opening'" class="entry-view view-enter">
        <div class="entry-intro">
          <p class="entry-overline"><Wind :size="18" aria-hidden="true" />{{ t('welcome.overline') }}</p>
          <h1>{{ t('welcome.title') }}</h1>
          <p class="entry-tagline">{{ t('brand.tagline') }}</p>
          <p class="entry-description">{{ t('welcome.description') }}</p>
          <div class="entry-signature" aria-hidden="true"><span /><strong>MW</strong></div>
        </div>

        <div class="entry-workbench">
          <header class="workbench-heading">
            <span class="entry-symbol" aria-hidden="true"><Mail :size="22" /></span>
            <div><p class="section-label">{{ t('welcome.workspaceLabel') }}</p><h2>{{ t('welcome.workspaceTitle') }}</h2></div>
          </header>

          <div class="entry-tool">
            <div class="segmented-control" role="tablist" :aria-label="t('welcome.modes')" @keydown="handleWelcomeTabKeydown">
              <button id="welcome-tab-create" type="button" role="tab" aria-controls="welcome-panel-create" :aria-selected="welcomeMode === 'create'" :tabindex="welcomeMode === 'create' ? 0 : -1" @click="welcomeMode = 'create'">
                <Inbox :size="17" aria-hidden="true" />{{ t('welcome.createTab') }}
              </button>
              <button id="welcome-tab-restore" type="button" role="tab" aria-controls="welcome-panel-restore" :aria-selected="welcomeMode === 'restore'" :tabindex="welcomeMode === 'restore' ? 0 : -1" @click="welcomeMode = 'restore'">
                <KeyRound :size="17" aria-hidden="true" />{{ t('welcome.restoreTab') }}
              </button>
            </div>

            <div v-if="welcomeMode === 'create'" id="welcome-panel-create" class="entry-panel create-panel" role="tabpanel" aria-labelledby="welcome-tab-create">
              <dl class="create-summary">
                <div><dt><Mail :size="17" aria-hidden="true" />{{ t('welcome.addressSetting') }}</dt><dd>{{ t('welcome.addressValue') }}</dd></div>
                <div><dt><Clock3 :size="17" aria-hidden="true" />{{ t('welcome.lifetimeSetting') }}</dt><dd>{{ t('welcome.lifetimeValue') }}</dd></div>
                <div><dt><KeyRound :size="17" aria-hidden="true" />{{ t('welcome.accessSetting') }}</dt><dd>Capability</dd></div>
              </dl>
              <button class="button primary-button create-button" type="button" :disabled="mailbox.phase.value !== 'welcome'" @click="mailbox.createInbox">
                <LoaderCircle v-if="mailbox.phase.value === 'creating'" class="spinning" :size="19" aria-hidden="true" />
                <Inbox v-else :size="19" aria-hidden="true" />
                {{ mailbox.phase.value === 'creating' ? t('welcome.creating') : t('welcome.create') }}
                <ArrowRight v-if="mailbox.phase.value !== 'creating'" :size="18" aria-hidden="true" />
              </button>
            </div>

            <form v-else id="welcome-panel-restore" class="entry-panel token-form" role="tabpanel" aria-labelledby="welcome-tab-restore" @submit.prevent="openFromToken">
              <div class="token-heading"><KeyRound :size="20" aria-hidden="true" /><div><h3>{{ t('welcome.restoreTitle') }}</h3><p>{{ t('welcome.restoreMeta') }}</p></div></div>
              <label for="capability-token">{{ t('welcome.tokenLabel') }}</label>
              <textarea id="capability-token" v-model="tokenInput" rows="3" spellcheck="false" autocomplete="off" :placeholder="t('welcome.tokenPlaceholder')" required />
              <button class="button primary-button" type="submit" :disabled="mailbox.phase.value !== 'welcome' || !tokenInput.trim()">
                <LoaderCircle v-if="mailbox.phase.value === 'opening'" class="spinning" :size="19" aria-hidden="true" />
                <KeyRound v-else :size="19" aria-hidden="true" />
                {{ mailbox.phase.value === 'opening' ? t('welcome.opening') : t('welcome.open') }}
              </button>
            </form>

            <p class="entry-note"><ShieldCheck :size="17" aria-hidden="true" />{{ t('welcome.privacy') }}</p>
          </div>
        </div>
      </section>

      <section v-else-if="mailbox.phase.value === 'issued' && mailbox.inbox.value && mailbox.issuedCapability.value" class="issued-view view-enter">
        <header class="issued-heading">
          <span class="success-symbol" aria-hidden="true"><Check :size="25" /></span>
          <div><p class="section-label">{{ t('issued.eyebrow') }}</p><h1>{{ t('issued.title') }}</h1><p>{{ t('issued.description') }}</p></div>
        </header>

        <div class="credential-tool">
          <div class="credential-row">
            <span class="credential-icon" aria-hidden="true"><Mail :size="18" /></span>
            <div><span>{{ t('issued.addressLabel') }}</span><code>{{ mailbox.inbox.value.address }}</code></div>
            <button class="icon-text-button" type="button" @click="copyText(mailbox.inbox.value.address, 'address')">
              <Check v-if="copied === 'address'" :size="18" aria-hidden="true" /><Copy v-else :size="18" aria-hidden="true" />
              {{ copied === 'address' ? t('issued.copied') : copyFailed === 'address' ? t('issued.copyFailed') : t('issued.copyAddress') }}
            </button>
          </div>
          <div class="credential-row token-row">
            <span class="credential-icon" aria-hidden="true"><KeyRound :size="18" /></span>
            <div><span>{{ t('issued.tokenLabel') }}</span><code>{{ mailbox.issuedCapability.value.token }}</code></div>
            <button class="icon-text-button" type="button" @click="copyText(mailbox.issuedCapability.value.token, 'token')">
              <Check v-if="copied === 'token'" :size="18" aria-hidden="true" /><Copy v-else :size="18" aria-hidden="true" />
              {{ copied === 'token' ? t('issued.copied') : copyFailed === 'token' ? t('issued.copyFailed') : t('issued.copyToken') }}
            </button>
          </div>
          <div class="security-notice"><ShieldCheck :size="19" aria-hidden="true" /><p>{{ t('issued.warning') }}</p></div>
          <button class="button primary-button continue-button" type="button" @click="mailbox.openInbox()">
            {{ t('issued.continue') }}<ArrowRight :size="18" aria-hidden="true" />
          </button>
        </div>
      </section>

      <section v-else-if="mailbox.phase.value === 'error' && mailbox.error.value" class="error-view view-enter" role="alert">
        <span class="error-symbol" aria-hidden="true"><AlertTriangle :size="28" /></span>
        <p class="section-label">{{ t('error.title') }}</p>
        <h1>{{ errorMessage(mailbox.error.value.code) }}</h1>
        <p v-if="mailbox.error.value.requestID" class="request-id">{{ t('error.requestID', { id: mailbox.error.value.requestID }) }}</p>
        <button class="button secondary-button" type="button" :disabled="mailbox.leaving.value" @click="mailbox.leaveInbox">
          <RotateCcw :size="18" aria-hidden="true" />{{ mailbox.leaving.value ? t('inbox.leaving') : t('error.retry') }}
        </button>
      </section>

      <section v-else-if="mailbox.inbox.value" class="mail-workspace" :class="{ 'show-detail': mailbox.phase.value === 'detail' }">
        <aside class="inbox-pane" :aria-label="t('inbox.messages')">
          <div class="address-bar">
            <span class="address-icon" aria-hidden="true"><Mail :size="19" /></span>
            <div class="address-copy">
              <span>{{ t('inbox.label') }}</span>
              <h1 :title="mailbox.inbox.value.address" :aria-label="mailbox.inbox.value.address"><span aria-hidden="true">{{ addressParts(mailbox.inbox.value.address).local }}</span><span class="address-domain" aria-hidden="true">{{ addressParts(mailbox.inbox.value.address).domain }}</span></h1>
            </div>
            <button class="icon-button" type="button" :aria-label="t('inbox.copy')" :title="t('inbox.copy')" @click="copyText(mailbox.inbox.value.address, 'inbox-address')">
              <Check v-if="copied === 'inbox-address'" :size="18" aria-hidden="true" /><Copy v-else :size="18" aria-hidden="true" />
            </button>
          </div>

          <div class="inbox-meta">
            <span><Clock3 :size="15" aria-hidden="true" />{{ t('inbox.expires') }} {{ formattedExpiry }}</span>
            <div class="inbox-meta-actions">
              <button class="icon-button subtle" type="button" :aria-label="mailbox.leaving.value ? t('inbox.leaving') : t('inbox.signOut')" :title="mailbox.leaving.value ? t('inbox.leaving') : t('inbox.signOut')" :disabled="mailbox.leaving.value" @click="mailbox.leaveInbox">
                <LoaderCircle v-if="mailbox.leaving.value" class="spinning" :size="18" aria-hidden="true" /><LogOut v-else :size="18" aria-hidden="true" />
              </button>
              <button v-if="!deletingInbox" class="icon-button subtle danger" type="button" :aria-label="t('inbox.delete')" :title="t('inbox.delete')" @click="confirmDeleteInbox"><Trash2 :size="18" aria-hidden="true" /></button>
              <button v-else class="confirm-delete" type="button" @click="confirmDeleteInbox"><Trash2 :size="16" aria-hidden="true" />{{ t('inbox.deleteConfirm') }}</button>
            </div>
          </div>

          <div class="list-heading">
            <div><h2>{{ t('inbox.messages') }}</h2><span>{{ t('inbox.count', { count: mailbox.messages.value.length }) }}</span></div>
            <button class="icon-button" type="button" :aria-label="t('inbox.refresh')" :title="t('inbox.refresh')" :disabled="mailbox.refreshing.value || mailbox.loadingMore.value" @click="mailbox.refreshMessages">
              <RefreshCw :class="{ spinning: mailbox.refreshing.value }" :size="18" aria-hidden="true" />
            </button>
          </div>

          <div v-if="mailbox.messages.value.length" class="message-list">
            <button v-for="item in mailbox.messages.value" :key="item.id" class="message-row" :class="{ unread: !item.seen, selected: mailbox.selected.value?.id === item.id }" type="button" :aria-current="mailbox.selected.value?.id === item.id ? 'true' : undefined" @click="mailbox.openMessage(item)">
              <span class="unread-dot" aria-hidden="true" />
              <span class="message-row-main">
                <span class="message-row-top"><span class="message-sender">{{ senderLabel(item.envelope_sender) }}</span><time class="message-time">{{ formatRelative(item.received_at) }}</time></span>
                <strong class="message-subject">{{ item.subject || t('inbox.noSubject') }}</strong>
                <span class="message-preview">{{ item.preview }}</span>
                <span class="message-row-foot">
                  <span v-if="item.has_attachments" class="attachment-indicator"><Paperclip :size="14" aria-hidden="true" />{{ t('inbox.attachment') }}</span>
                  <span v-if="item.parse_status !== 'parsed'" class="status-chip" :class="{ failed: item.parse_status === 'failed' }">{{ item.parse_status === 'failed' ? t('inbox.failed') : t('inbox.pending') }}</span>
                </span>
              </span>
            </button>
            <div v-if="mailbox.nextCursor.value || mailbox.loadMoreError.value" class="message-pagination">
              <p v-if="mailbox.loadMoreError.value" role="status">{{ t('inbox.loadMoreError') }}</p>
              <button class="button quiet-button" type="button" :disabled="mailbox.loadingMore.value || mailbox.refreshing.value" @click="mailbox.loadMoreMessages">
                <LoaderCircle v-if="mailbox.loadingMore.value" class="spinning" :size="17" aria-hidden="true" />
                {{ mailbox.loadingMore.value ? t('inbox.loadingMore') : t('inbox.loadMore') }}
              </button>
            </div>
          </div>
          <div v-else class="empty-list">
            <span aria-hidden="true"><Inbox :size="30" /></span>
            <h2>{{ t('inbox.emptyTitle') }}</h2>
            <p>{{ t('inbox.emptyBody') }}</p>
          </div>
        </aside>

        <article class="message-pane" :aria-label="t('message.panel')">
          <template v-if="mailbox.phase.value === 'detail' && mailbox.selected.value">
            <div class="detail-toolbar">
              <button class="icon-text-button back-button" type="button" @click="mailbox.closeMessage"><ArrowLeft :size="18" aria-hidden="true" />{{ t('message.back') }}</button>
              <div class="detail-actions">
                <button class="icon-text-button" type="button" :title="t('message.downloadSource')" @click="mailbox.downloadSource"><Download :size="18" aria-hidden="true" />{{ t('message.downloadSource') }}</button>
                <button class="icon-text-button danger" type="button" :title="deletingMessage ? t('message.deleteConfirm') : t('message.delete')" @click="confirmDeleteMessage"><Trash2 :size="18" aria-hidden="true" />{{ deletingMessage ? t('message.deleteConfirm') : t('message.delete') }}</button>
              </div>
            </div>

            <div class="message-scroll">
              <header class="message-header">
                <p class="message-kicker"><MailOpen :size="16" aria-hidden="true" />{{ senderLabel(mailbox.selected.value.envelope_sender) }}</p>
                <h1>{{ mailbox.selected.value.subject || t('inbox.noSubject') }}</h1>
                <dl class="message-metadata">
                  <div><dt>{{ t('message.from') }}</dt><dd>{{ formatAddresses(mailbox.selected.value.from) }}</dd></div>
                  <div><dt>{{ t('message.to') }}</dt><dd>{{ formatAddresses(mailbox.selected.value.to) }}</dd></div>
                  <div><dt>{{ t('message.received') }}</dt><dd>{{ formatDate(mailbox.selected.value.received_at) }}</dd></div>
                  <div><dt>{{ t('message.size') }}</dt><dd>{{ formatBytes(mailbox.selected.value.size_bytes) }}</dd></div>
                </dl>
              </header>

              <div v-if="mailbox.selected.value.parse_status === 'failed'" class="parse-notice failed" role="status"><AlertTriangle :size="19" aria-hidden="true" />{{ t('message.parseFailed') }}</div>
              <div v-else-if="mailbox.selected.value.parse_status !== 'parsed'" class="parse-notice" role="status"><LoaderCircle class="spinning" :size="19" aria-hidden="true" />{{ t('message.parsePending') }}</div>

              <template v-else>
                <div class="content-toolbar">
                  <div class="content-tabs" role="tablist" :aria-label="t('message.contentViews')" @keydown="handleContentTabKeydown">
                    <button id="message-tab-text" type="button" role="tab" aria-controls="message-panel-text" :aria-selected="contentMode === 'text'" :tabindex="contentMode === 'text' ? 0 : -1" @click="contentMode = 'text'"><FileText :size="16" aria-hidden="true" />{{ t('message.text') }}</button>
                    <button id="message-tab-html" type="button" role="tab" aria-controls="message-panel-html" :aria-selected="contentMode === 'html'" :tabindex="contentMode === 'html' ? 0 : -1" @click="contentMode = 'html'"><Mail :size="16" aria-hidden="true" />{{ t('message.html') }}</button>
                  </div>
                </div>
                <pre v-if="contentMode === 'text'" id="message-panel-text" class="plain-content" role="tabpanel" aria-labelledby="message-tab-text" tabindex="0">{{ mailbox.selected.value.text || mailbox.selected.value.preview }}</pre>
                <div v-else id="message-panel-html" class="html-content" role="tabpanel" aria-labelledby="message-tab-html" tabindex="0">
                  <p class="privacy-note"><ShieldCheck :size="17" aria-hidden="true" />{{ t('message.privacy') }}</p>
                  <SafeMessageFrame v-if="mailbox.selected.value.html_source" :html="mailbox.selected.value.html_source" :title="t('message.htmlFrameTitle', { subject: mailbox.selected.value.subject || t('inbox.noSubject') })" :cid-sources="mailbox.inlineImages.value" />
                  <p v-else class="html-unavailable">{{ t('message.htmlUnavailable') }}</p>
                </div>
                <section v-if="mailbox.selected.value.attachments.length" class="attachments">
                  <h2><Paperclip :size="18" aria-hidden="true" />{{ t('message.attachments') }}<span>{{ mailbox.selected.value.attachments.length }}</span></h2>
                  <ul>
                    <li v-for="attachment in mailbox.selected.value.attachments" :key="attachment.part_path">
                      <span class="attachment-file"><FileText :size="18" aria-hidden="true" /><span><strong>{{ attachment.file_name || attachment.content_type }}</strong><small>{{ attachment.content_type }} · {{ formatBytes(attachment.size_bytes) }}</small></span></span>
                      <button class="icon-button" type="button" :aria-label="t('message.download')" :title="`${t('message.download')} ${attachment.file_name || attachment.content_type}`" @click="mailbox.downloadAttachment(attachment)"><Download :size="18" aria-hidden="true" /></button>
                    </li>
                  </ul>
                </section>
              </template>
            </div>
          </template>

          <div v-else class="detail-empty">
            <span aria-hidden="true"><MailOpen :size="34" /></span>
            <h2>{{ t('message.emptyTitle') }}</h2>
            <p>{{ t('message.emptyBody') }}</p>
          </div>
        </article>
      </section>
    </main>
  </div>
</template>
