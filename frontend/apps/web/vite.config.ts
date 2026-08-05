import path from 'node:path'

import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

const apiTarget = process.env.OMNARA_API_PROXY ?? 'http://localhost:8080'
const proxiedPrefixes = ['/api', '/install', '/.well-known']
const proxy = Object.fromEntries(
  proxiedPrefixes.map((prefix) => [prefix, { target: apiTarget, changeOrigin: false, ws: true }]),
)

export default defineConfig({
  plugins: [react({ babel: { plugins: [['babel-plugin-react-compiler', {}]] } }), tailwindcss()],
  resolve: {
    alias: { '@': path.resolve(import.meta.dirname, './src') },
  },
  server: {
    port: 5173,
    proxy,
  },
  build: { outDir: 'dist', emptyOutDir: true },
})
