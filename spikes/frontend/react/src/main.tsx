import { useEffect, useMemo, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { applyTheme, copy, formatTime, messages, type Locale, type Message, type Theme } from '../../shared/model'
import '../../shared/style.css'

function App() {
  const [authenticated, setAuthenticated] = useState(false)
  const [locale, setLocale] = useState<Locale>('zh-CN')
  const [theme, setTheme] = useState<Theme>('system')
  const [selected, setSelected] = useState<Message | null>(null)
  const t = useMemo(() => copy[locale], [locale])

  useEffect(() => {
    document.documentElement.lang = locale
    applyTheme(theme)
  }, [locale, theme])

  return <div className="shell">
    <header className="topbar">
      <div><h1 className="brand">{t.brand}</h1><div className="tagline">{t.tagline}</div></div>
      <div className="controls">
        <label>{t.language}<select value={locale} onChange={event => setLocale(event.target.value as Locale)}><option value="zh-CN">中文</option><option value="en-US">English</option></select></label>
        <label>{t.theme}<select value={theme} onChange={event => setTheme(event.target.value as Theme)}><option value="system">{t.system}</option><option value="light">{t.light}</option><option value="dark">{t.dark}</option><option value="mist">{t.mist}</option></select></label>
      </div>
    </header>

    {!authenticated && <section className="panel login">
      <h2>{t.login}</h2>
      <form className="login-form" onSubmit={event => { event.preventDefault(); setAuthenticated(true) }}>
        <label>{t.token}<input autoComplete="off" placeholder={t.tokenPlaceholder} required /></label>
        <button className="primary" type="submit">{t.login}</button>
      </form>
    </section>}

    {authenticated && selected && <section className="panel">
      <button className="ghost" onClick={() => setSelected(null)}>← {t.back}</button>
      <h2>{selected.subject}</h2>
      <div className="muted">{selected.sender} · {formatTime(selected.receivedAt, locale)}</div>
      <div className="detail-body">{selected.preview}</div>
    </section>}

    {authenticated && !selected && <section className="panel">
      <div className="toolbar"><div><h2>{t.inbox}</h2><div className="muted">{t.mailbox}</div></div><button className="ghost" onClick={() => setAuthenticated(false)}>{t.logout}</button></div>
      <div className="message-list">
        {messages.map(message => <button key={message.id} className="message" onClick={() => setSelected(message)}><strong>{message.subject}</strong><span>{message.preview}</span><div className="message-meta"><span>{message.sender}</span><span>{formatTime(message.receivedAt, locale)}</span></div></button>)}
        {messages.length === 0 && <div>{t.empty}</div>}
      </div>
    </section>}
  </div>
}

const root = document.querySelector('#root')
if (!root) throw new Error('app root not found')
createRoot(root).render(<App />)
