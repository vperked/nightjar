/// <reference types="vitest/config" />
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import type { Plugin } from 'vite';

// preloadFonts asks for the fonts the first screen draws with as soon as the page arrives, rather
// than once the stylesheet has been read, so the text does not appear in a fallback and then shift.
function preloadFonts(): Plugin {
  const first = ['ibm-plex-sans-latin-400-normal', 'ibm-plex-sans-latin-600-normal', 'ibm-plex-mono-latin-500-normal'];
  return {
    name: 'preload-fonts',
    apply: 'build',
    transformIndexHtml(_html, ctx) {
      const files = Object.keys(ctx.bundle ?? {}).filter((f) => f.endsWith('.woff2') && first.some((n) => f.includes(n)));
      return files.map((f) => ({ tag: 'link', attrs: { rel: 'preload', href: `/${f}`, as: 'font', type: 'font/woff2', crossorigin: '' }, injectTo: 'head' }));
    },
  };
}

// The Go backend serves the built app; in dev, Vite proxies API calls to it.
const backend = process.env.BACKEND_URL ?? 'http://127.0.0.1:8080';

export default defineConfig({
  plugins: [react(), preloadFonts()],
  server: {
    proxy: {
      '/api': backend,
      '/health': backend,
    },
  },
  // Built straight into the Go module so `go build` embeds it (see backend/internal/web).
  build: { outDir: '../backend/internal/web/dist', emptyOutDir: true, sourcemap: false },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./test/setup.ts'],
    include: ['test/**/*.test.{ts,tsx}'],
  },
});
