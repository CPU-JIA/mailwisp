(() => {
  const theme = localStorage.getItem('mailwisp.theme') || 'system'
  const dark = matchMedia('(prefers-color-scheme: dark)').matches
  document.documentElement.dataset.theme = theme === 'system' ? (dark ? 'dark' : 'light') : theme
  document.documentElement.dataset.themePreference = theme
  document.documentElement.lang = localStorage.getItem('mailwisp.locale') || 'zh-CN'
})()
