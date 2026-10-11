import { svelte } from '@sveltejs/vite-plugin-svelte'
import { defineConfig } from 'vite'

// The Go binaries embed ../internal/webui/dist. During development run the
// client (or server) and `npm run dev`; API calls are proxied to it.
export default defineConfig({
  plugins: [svelte()],
  server: {
    port: 5173,
    proxy: { '/api': { target: process.env.BOND007_API ?? 'http://127.0.0.1:7007', changeOrigin: true } },
  },
  build: { outDir: '../internal/webui/dist', emptyOutDir: true, chunkSizeWarningLimit: 1200 },
})
