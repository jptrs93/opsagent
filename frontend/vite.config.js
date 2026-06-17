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
                target: 'https://opendeploy.d.flippingcopilot.com',
                changeOrigin: true,
            }
        }
    }
})
