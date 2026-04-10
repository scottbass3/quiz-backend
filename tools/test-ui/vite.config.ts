import { defineConfig, loadEnv } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '')
  // Server-side target (container name in Docker, localhost otherwise)
  const apiTarget = env.VITE_API_TARGET ?? 'http://localhost:8080'
  const wsTarget = apiTarget.replace(/^http/, 'ws')

  return {
    plugins: [vue()],
    server: {
      host: '0.0.0.0',
      port: 5173,
      proxy: {
        // All /api/* calls are forwarded to the backend (path rewritten)
        '/api': {
          target: apiTarget,
          rewrite: (path) => path.replace(/^\/api/, ''),
          changeOrigin: true,
        },
        // WebSocket connections forwarded as-is
        '/ws': {
          target: wsTarget,
          ws: true,
          changeOrigin: true,
        },
      },
    },
  }
})
