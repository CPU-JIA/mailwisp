<script lang="ts">
  import { applyTheme, copy, formatTime, messages, type Locale, type Message, type Theme } from '../../shared/model'

  let authenticated = false
  let locale: Locale = 'zh-CN'
  let theme: Theme = 'system'
  let selected: Message | null = null

  $: t = copy[locale]
  $: {
    document.documentElement.lang = locale
    applyTheme(theme)
  }
</script>

<div class="shell">
  <header class="topbar">
    <div><h1 class="brand">{t.brand}</h1><div class="tagline">{t.tagline}</div></div>
    <div class="controls">
      <label>{t.language}<select bind:value={locale}><option value="zh-CN">中文</option><option value="en-US">English</option></select></label>
      <label>{t.theme}<select bind:value={theme}><option value="system">{t.system}</option><option value="light">{t.light}</option><option value="dark">{t.dark}</option><option value="mist">{t.mist}</option></select></label>
    </div>
  </header>

  {#if !authenticated}
    <section class="panel login">
      <h2>{t.login}</h2>
      <form class="login-form" on:submit|preventDefault={() => authenticated = true}>
        <label>{t.token}<input autocomplete="off" placeholder={t.tokenPlaceholder} required /></label>
        <button class="primary" type="submit">{t.login}</button>
      </form>
    </section>
  {:else if selected}
    <section class="panel">
      <button class="ghost" on:click={() => selected = null}>← {t.back}</button>
      <h2>{selected.subject}</h2>
      <div class="muted">{selected.sender} · {formatTime(selected.receivedAt, locale)}</div>
      <div class="detail-body">{selected.preview}</div>
    </section>
  {:else}
    <section class="panel">
      <div class="toolbar"><div><h2>{t.inbox}</h2><div class="muted">{t.mailbox}</div></div><button class="ghost" on:click={() => authenticated = false}>{t.logout}</button></div>
      <div class="message-list">
        {#each messages as message (message.id)}
          <button class="message" on:click={() => selected = message}><strong>{message.subject}</strong><span>{message.preview}</span><div class="message-meta"><span>{message.sender}</span><span>{formatTime(message.receivedAt, locale)}</span></div></button>
        {:else}
          <div>{t.empty}</div>
        {/each}
      </div>
    </section>
  {/if}
</div>
