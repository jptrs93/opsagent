import { defineConfig } from 'vite'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
    plugins: [
        tailwindcss(),
    ],
    build: {
        outDir: '../backend/web/dist',
        emptyOutDir: true,
    },
    server: {
        proxy: {
            '/v1': {
                target: 'http://localhost:5001',
                changeOrigin: true,
            }
        }
    }
})
