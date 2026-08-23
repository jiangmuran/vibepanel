import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// The build lands directly in the Go module's embed directory. Keeping the
// artefact anywhere else would mean a copy step that someone eventually forgets,
// and a binary shipping a stale UI is very hard to notice.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    outDir: '../internal/webui/dist',
    emptyOutDir: true,
  },
  server: {
    port: 5273,
    proxy: {
      '/api': 'http://127.0.0.1:7788',
      // ws:true is what makes the terminal work under `npm run dev`; without
      // it the upgrade request is proxied as plain HTTP and every session
      // silently fails to connect.
      '/ws': { target: 'ws://127.0.0.1:7788', ws: true },
    },
  },
})
