import { defineConfig } from 'vite'

export default defineConfig({
  base: './',
  root: 'native',
  build: {
    outDir: '../dist/native',
    emptyOutDir: true,
  },
})
