import { ref, watch } from 'vue'

export type ThemePreference = 'system' | 'light' | 'dark' | 'mist'

const stored = localStorage.getItem('mailwisp.theme')
export const theme = ref<ThemePreference>(stored === 'light' || stored === 'dark' || stored === 'mist' ? stored : 'system')

const media = window.matchMedia('(prefers-color-scheme: dark)')

function applyTheme(): void {
  const resolved = theme.value === 'system' ? (media.matches ? 'dark' : 'light') : theme.value
  document.documentElement.dataset.theme = resolved
  document.documentElement.dataset.themePreference = theme.value
  localStorage.setItem('mailwisp.theme', theme.value)
}

watch(theme, applyTheme, { immediate: true })
media.addEventListener('change', applyTheme)
