import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// base: './' 让构建产物用相对路径,便于被 Go 的 go:embed 直接托管。
export default defineConfig({
  plugins: [react()],
  base: './',
  server: {
    port: 5173,
    proxy: { '/api': 'http://localhost:8080' },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
})
