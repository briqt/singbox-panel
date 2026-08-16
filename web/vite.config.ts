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
        // Match on whole path segments, never on substrings: "recharts"
        // contains "react", so a loose test drags the 390 kB charting library
        // into the eagerly preloaded react chunk — the exact opposite of the
        // goal.
        //
        // recharts is deliberately unnamed. Forcing it into a manual chunk puts
        // it in the entry's modulepreload list, so the browser fetches it on
        // first paint and the lazy Stats/MyInfo routes save nothing. Left
        // alone, rolldown places it in a chunk reachable only from those two
        // routes.
        manualChunks(id: string) {
          const pkg = id.split('node_modules/').pop()
          if (!pkg || !id.includes('node_modules')) return
          const inPkg = (name: string) => pkg === name || pkg.startsWith(name + '/')

          if (inPkg('@ant-design/icons') || inPkg('@ant-design/icons-svg')) return 'vendor-icons'
          if (inPkg('antd') || pkg.startsWith('@ant-design/') || pkg.startsWith('rc-')) {
            return 'vendor-antd'
          }
          if (
            inPkg('react') || inPkg('react-dom') || inPkg('react-is') ||
            inPkg('react-router') || inPkg('react-router-dom') || inPkg('scheduler')
          ) {
            return 'vendor-react'
          }
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
