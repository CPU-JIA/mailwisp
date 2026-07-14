<script setup lang="ts">
import { computed, ref, watchEffect } from 'vue'
import { applyTheme, copy, formatTime, messages, type Locale, type Message, type Theme } from '../../shared/model'

const authenticated = ref(false)
const locale = ref<Locale>('zh-CN')
const theme = ref<Theme>('system')
const selected = ref<Message | null>(null)
const t = computed(() => copy[locale.value])

watchEffect(() => {
  document.documentElement.lang = locale.value
  applyTheme(theme.value)
})
</script>

<template>
  <div class="shell">
    <header class="topbar">
      <div><h1 class="brand">{{ t.brand }}</h1><div class="tagline">{{ t.tagline }}</div></div>
      <div class="controls">
        <label>{{ t.language }}<select v-model="locale"><option value="zh-CN">中文</option><option value="en-US">English</option></select></label>
        <label>{{ t.theme }}<select v-model="theme"><option value="system">{{ t.system }}</option><option value="light">{{ t.light }}</option><option value="dark">{{ t.dark }}</option><option value="mist">{{ t.mist }}</option></select></label>
      </div>
    </header>

    <section v-if="!authenticated" class="panel login">
      <h2>{{ t.login }}</h2>
      <form class="login-form" @submit.prevent="authenticated = true">
        <label>{{ t.token }}<input autocomplete="off" :placeholder="t.tokenPlaceholder" required /></label>
        <button class="primary" type="submit">{{ t.login }}</button>
      </form>
    </section>

    <section v-else-if="selected" class="panel">
      <button class="ghost" @click="selected = null">← {{ t.back }}</button>
      <h2>{{ selected.subject }}</h2>
      <div class="muted">{{ selected.sender }} · {{ formatTime(selected.receivedAt, locale) }}</div>
      <div class="detail-body">{{ selected.preview }}</div>
    </section>

    <section v-else class="panel">
      <div class="toolbar">
        <div><h2>{{ t.inbox }}</h2><div class="muted">{{ t.mailbox }}</div></div>
        <button class="ghost" @click="authenticated = false">{{ t.logout }}</button>
      </div>
      <div class="message-list">
        <button v-for="message in messages" :key="message.id" class="message" @click="selected = message">
          <strong>{{ message.subject }}</strong><span>{{ message.preview }}</span>
          <div class="message-meta"><span>{{ message.sender }}</span><span>{{ formatTime(message.receivedAt, locale) }}</span></div>
        </button>
        <div v-if="messages.length === 0">{{ t.empty }}</div>
      </div>
    </section>
  </div>
</template>
