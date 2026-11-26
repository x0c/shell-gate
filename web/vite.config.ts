import path from 'node:path'
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

const backend = process.env.SHELL_GATE_WEB_ORIGIN ?? 'http://127.0.0.1:18765'

export default defineConfig({
  base: '/',
  plugins: [react(), tailwindcss()],
  build: {
    outDir: path.resolve(import.meta.dirname, '../internal/server/web'),
    emptyOutDir: false,
    target: 'es2022',
    cssMinify: 'lightningcss',
    sourcemap: false,
    rollupOptions: {
      input: {
        login: path.resolve(import.meta.dirname, 'login.html'),
        workspace: path.resolve(import.meta.dirname, 'workspace.html'),
        'edge-pick': path.resolve(import.meta.dirname, 'edge-pick.html'),
      },
    },
  },
  server: {
    port: 5177,
    proxy: {
      '/api': { target: backend, changeOrigin: false },
      '/__shellgate': { target: backend, changeOrigin: false },
      '/t': { target: backend, changeOrigin: false, ws: true },
    },
  },
})
