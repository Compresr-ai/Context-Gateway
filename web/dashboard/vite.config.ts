import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  base: '/dashboard/',
  server: {
    proxy: {
      '/api': 'http://localhost:18080',
    },
  },
  build: {
    outDir: '../../cmd/dashboard_dist',
    emptyOutDir: true,
  },
})
