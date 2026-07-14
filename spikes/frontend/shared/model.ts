export type Locale = 'zh-CN' | 'en-US'
export type Theme = 'system' | 'light' | 'dark' | 'mist'

export interface Message {
  id: string
  sender: string
  subject: string
  preview: string
  receivedAt: string
}

export const messages: Message[] = [
  {
    id: 'msg-1',
    sender: 'security@example.com',
    subject: 'Your verification code is 482913',
    preview: 'Use 482913 to finish signing in. The code expires in ten minutes.',
    receivedAt: '2026-07-14T04:18:00Z',
  },
  {
    id: 'msg-2',
    sender: 'welcome@example.net',
    subject: 'Welcome to the preview',
    preview: 'This message verifies list, selection, locale, and theme behavior.',
    receivedAt: '2026-07-14T03:52:00Z',
  },
]

export const copy = {
  'zh-CN': {
    brand: 'MailWisp',
    tagline: '来信即现，过时即逝。',
    login: '进入收件箱',
    token: '访问令牌',
    tokenPlaceholder: '粘贴临时邮箱访问令牌',
    inbox: '收件箱',
    mailbox: 'demo@mailwisp.local',
    empty: '暂时没有邮件',
    back: '返回列表',
    language: '语言',
    theme: '主题',
    system: '跟随系统',
    light: '浅色',
    dark: '深色',
    mist: '雾蓝',
    logout: '退出',
  },
  'en-US': {
    brand: 'MailWisp',
    tagline: 'Fast mail. Zero trace.',
    login: 'Open inbox',
    token: 'Access token',
    tokenPlaceholder: 'Paste a temporary inbox access token',
    inbox: 'Inbox',
    mailbox: 'demo@mailwisp.local',
    empty: 'No messages yet',
    back: 'Back to inbox',
    language: 'Language',
    theme: 'Theme',
    system: 'Follow system',
    light: 'Light',
    dark: 'Dark',
    mist: 'Mist',
    logout: 'Sign out',
  },
} as const

export function formatTime(value: string, locale: Locale): string {
  return new Intl.DateTimeFormat(locale, {
    dateStyle: 'medium',
    timeStyle: 'short',
    timeZone: 'UTC',
  }).format(new Date(value))
}

export function applyTheme(theme: Theme): void {
  const resolved = theme === 'system'
    ? (window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light')
    : theme
  document.documentElement.dataset.theme = resolved
}
