import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

export default defineConfig({
  base: './',
  root: 'react',
  plugins: [react()],
  build: {
    outDir: '../dist/react',
    emptyOutDir: true,
  },
})
