import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// ServerCLI frontend.
// The control plane serves this build from frontend/dist at the site root and
// proxies /api/v1 to the backend, so all requests use same-origin relative paths.
export default defineConfig({
  plugins: [react()],
  base: '/',
  server: {
    port: 5173,
    proxy: {
      '/api': { target: 'http://127.0.0.1:9045', changeOrigin: true },
      '/health': { target: 'http://127.0.0.1:9045', changeOrigin: true },
      '/version': { target: 'http://127.0.0.1:9045', changeOrigin: true },
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
  },
});
