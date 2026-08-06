import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// 产物 web/dist 由 Go 二进制 embed（见 main.go），勿改 outDir。
// 开发时 /api 代理到本机面板（PORT=2082）。
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:2082',
      '/sub': 'http://127.0.0.1:2082',
    },
  },
})
