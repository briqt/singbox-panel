import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// 产物 web/dist 由 Go 二进制 embed（见 main.go），勿改 outDir。
// 开发时 /api 代理到本机面板（PORT=2082）。
export default defineConfig({
  plugins: [react()],
  build: {
    // vendor-antd sits at ~1.19 MB and that is antd itself, not accidental
    // bloat: icons tree-shake down to 19 kB and every other chunk is under
    // 400 kB. The limit is set just above it rather than switched off, so the
    // warning still fires if antd grows or anything else balloons.
    chunkSizeWarningLimit: 1250,
    rollupOptions: {
      output: {
        // Split the libraries that dominate the bundle into their own chunks.
        // They change only on dependency bumps, so a panel rebuild no longer
        // invalidates them in the browser cache. rolldown (Vite 8) takes only
        // the function form here, not the object map.
        // Order matters: antd pulls in rc-* packages whose paths also contain
        // "react", so it must be matched first.
        manualChunks(id: string) {
          if (!id.includes('node_modules')) return
          if (id.includes('recharts') || id.includes('d3-') || id.includes('victory-vendor')) {
            return 'vendor-charts'
          }
          if (id.includes('@ant-design/icons')) return 'vendor-icons'
          if (id.includes('antd') || id.includes('@ant-design') || id.includes('rc-')) {
            return 'vendor-antd'
          }
          if (id.includes('react') || id.includes('scheduler')) return 'vendor-react'
        },
      },
    },
  },
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:2082',
      '/sub': 'http://127.0.0.1:2082',
    },
  },
})
