import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import mockApi from './dev/mock-api.js';

const useMock = process.env.DRAINCTL_MOCK !== '0';

export default defineConfig({
  plugins: [
    svelte(),
    ...(useMock ? [mockApi()] : []),
  ],
  build: {
    outDir: 'dist',
    assetsDir: 'assets',
    emptyOutDir: true,
    rolldownOptions: {
      checks: { pluginTimings: false },
    },
  },
  base: '/',
  server: {
    // When mock API is active, no proxy needed — requests are handled locally.
    // Set DRAINCTL_MOCK=0 to disable mock and proxy to a real backend.
    ...(!useMock ? {
      proxy: {
        '/api': {
          target: 'https://localhost:7443',
          secure: false,
          changeOrigin: true,
        },
        '/favicon.ico': {
          target: 'https://localhost:7443',
          secure: false,
          changeOrigin: true,
        },
      },
    } : {}),
  },
});
