import { ref, watch } from 'vue'

export type ThemePreference = 'system' | 'light' | 'dark' | 'mist'

const stored = localStorage.getItem('mailwisp.theme')
export const theme = ref<ThemePreference>(stored === 'light' || stored === 'dark' || stored === 'mist' ? stored : 'system')

const media = window.matchMedia('(prefers-color-scheme: dark)')

function applyTheme(): void {
  const resolved = theme.value === 'system' ? (media.matches ? 'dark' : 'light') : theme.value
  document.documentElement.dataset.theme = resolved
  document.documentElement.dataset.themePreference = theme.value
  document.querySelector<HTMLMetaElement>('meta[name="theme-color"]')?.setAttribute('content', {
    light: '#f5f8f9',
    dark: '#171513',
    mist: '#eef5f3',
  }[resolved])
  localStorage.setItem('mailwisp.theme', theme.value)
}

watch(theme, applyTheme, { immediate: true })
media.addEventListener('change', applyTheme)
