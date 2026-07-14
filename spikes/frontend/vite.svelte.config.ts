import { svelte } from '@sveltejs/vite-plugin-svelte'
import { defineConfig } from 'vite'

export default defineConfig({
  base: './',
  root: 'svelte',
  plugins: [svelte()],
  build: {
    outDir: '../dist/svelte',
    emptyOutDir: true,
  },
})
