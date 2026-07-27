import { defineConfig } from 'vite'

export default defineConfig({
  build: {
    outDir: '../backend/web/dist',
    emptyOutDir: true,
  },
  server: {
    proxy: {
      '/v1': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      },
    },
  },
})
