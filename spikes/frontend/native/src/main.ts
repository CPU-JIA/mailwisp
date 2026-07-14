import '../../shared/style.css'
import { applyTheme, copy, formatTime, messages, type Locale, type Message, type Theme } from '../../shared/model'

type State = {
  authenticated: boolean
  locale: Locale
  theme: Theme
  selected: Message | null
}

const state: State = {
  authenticated: false,
  locale: 'zh-CN',
  theme: 'system',
  selected: null,
}

const root = requireElement('#app')

function requireElement(selector: string): HTMLElement {
  const element = document.querySelector<HTMLElement>(selector)
  if (!element) throw new Error(`${selector} not found`)
  return element
}

function escapeHTML(value: string): string {
  const element = document.createElement('span')
  element.textContent = value
  return element.innerHTML
}

function render(): void {
  const t = copy[state.locale]
  document.documentElement.lang = state.locale
  applyTheme(state.theme)
  root.innerHTML = `
    <div class="shell">
      <header class="topbar">
        <div><h1 class="brand">${t.brand}</h1><div class="tagline">${t.tagline}</div></div>
        <div class="controls">
          <label>${t.language}<select data-action="locale"><option value="zh-CN">中文</option><option value="en-US">English</option></select></label>
          <label>${t.theme}<select data-action="theme"><option value="system">${t.system}</option><option value="light">${t.light}</option><option value="dark">${t.dark}</option><option value="mist">${t.mist}</option></select></label>
        </div>
      </header>
      ${state.authenticated ? renderInbox() : renderLogin()}
    </div>`

  root.querySelector<HTMLSelectElement>('[data-action="locale"]')!.value = state.locale
  root.querySelector<HTMLSelectElement>('[data-action="theme"]')!.value = state.theme
  bindEvents()
}

function renderLogin(): string {
  const t = copy[state.locale]
  return `<section class="panel login"><h2>${t.login}</h2><form class="login-form" data-action="login"><label>${t.token}<input name="token" autocomplete="off" placeholder="${t.tokenPlaceholder}" required /></label><button class="primary" type="submit">${t.login}</button></form></section>`
}

function renderInbox(): string {
  const t = copy[state.locale]
  if (state.selected) {
    return `<section class="panel"><button class="ghost" data-action="back">← ${t.back}</button><h2>${escapeHTML(state.selected.subject)}</h2><div class="muted">${escapeHTML(state.selected.sender)} · ${formatTime(state.selected.receivedAt, state.locale)}</div><div class="detail-body">${escapeHTML(state.selected.preview)}</div></section>`
  }
  return `<section class="panel"><div class="toolbar"><div><h2>${t.inbox}</h2><div class="muted">${t.mailbox}</div></div><button class="ghost" data-action="logout">${t.logout}</button></div><div class="message-list">${messages.length ? messages.map(renderMessage).join('') : t.empty}</div></section>`
}

function renderMessage(message: Message): string {
  return `<button class="message" data-message-id="${message.id}"><strong>${escapeHTML(message.subject)}</strong><span>${escapeHTML(message.preview)}</span><div class="message-meta"><span>${escapeHTML(message.sender)}</span><span>${formatTime(message.receivedAt, state.locale)}</span></div></button>`
}

function bindEvents(): void {
  root.querySelector<HTMLSelectElement>('[data-action="locale"]')?.addEventListener('change', event => {
    state.locale = (event.currentTarget as HTMLSelectElement).value as Locale
    render()
  })
  root.querySelector<HTMLSelectElement>('[data-action="theme"]')?.addEventListener('change', event => {
    state.theme = (event.currentTarget as HTMLSelectElement).value as Theme
    render()
  })
  root.querySelector<HTMLFormElement>('[data-action="login"]')?.addEventListener('submit', event => {
    event.preventDefault()
    state.authenticated = true
    render()
  })
  root.querySelector<HTMLElement>('[data-action="logout"]')?.addEventListener('click', () => {
    state.authenticated = false
    state.selected = null
    render()
  })
  root.querySelector<HTMLElement>('[data-action="back"]')?.addEventListener('click', () => {
    state.selected = null
    render()
  })
  root.querySelectorAll<HTMLElement>('[data-message-id]').forEach(element => {
    element.addEventListener('click', () => {
      state.selected = messages.find(message => message.id === element.dataset.messageId) ?? null
      render()
    })
  })
}

render()
