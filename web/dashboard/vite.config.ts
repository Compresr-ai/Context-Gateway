import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

const gatewayPort = process.env.GATEWAY_PORT ?? '18080'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  base: '/dashboard/',
  server: {
    proxy: {
      '/api': `http://localhost:${gatewayPort}`,
    },
  },
  build: {
    outDir: '../../cmd/dashboard_dist',
    emptyOutDir: true,
  },
})
