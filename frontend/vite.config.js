import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import tailwindcss from '@tailwindcss/vite';

export default defineConfig({
  plugins: [
    tailwindcss(),
    svelte(),
  ],
  build: {
    outDir: 'dist',
    assetsDir: 'assets',
    emptyOutDir: true,
  },
  base: '/',
  server: {
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
  },
});
