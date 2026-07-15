import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vite'

export default defineConfig({
  base: './',
  root: 'vue',
  plugins: [vue()],
  build: {
    outDir: '../dist/vue',
    emptyOutDir: true,
  },
})
