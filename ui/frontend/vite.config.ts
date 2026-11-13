import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

// The Wails v2 host embeds `dist/` (see ../), so asset URLs are relative and
// everything the page needs is emitted next to index.html.
export default defineConfig({
  base: './',
  plugins: [svelte()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    // WebView2 (Chromium) on Windows, WebKitGTK 2.4x on Linux.
    target: ['chrome110', 'safari16'],
    sourcemap: false,
    reportCompressedSize: true,
    assetsInlineLimit: 0,
  },
  server: {
    port: 5173,
    strictPort: false,
  },
});
