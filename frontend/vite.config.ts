import tailwindcss from '@tailwindcss/vite';
import { defineConfig } from 'vite';
import solidPlugin from 'vite-plugin-solid';
import devtools from 'solid-devtools/vite';

export default defineConfig({
  base: '/assets/',

  plugins: [
    devtools(),
    solidPlugin(),
    tailwindcss()
  ],

  server: {
    port: 3000,
    proxy: {
      '/api': 'http://localhost:9192',
      '/live': 'http://localhost:9192',
    },
  },

  build: {
    target: 'esnext',
    outDir: '../web/assets',
    assetsDir: '',
    emptyOutDir: true,
    rollupOptions: {
      output: {
        entryFileNames: `[name].js`,
        chunkFileNames: `[name].js`,
        assetFileNames: `[name].[ext]`,
      },
    },
  },
});