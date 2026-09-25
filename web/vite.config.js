import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = path.dirname(fileURLToPath(import.meta.url))

export default defineConfig({
  base: '/',
  plugins: [react()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, 'src'),
    },
  },
  server: {
    port: 7106,
    host: true,
    strictPort: true,
    proxy: {
      '/api': { target: ['http:', '', '127.0.0.1:8080'].join('/'), changeOrigin: true },
    },
  },
})
