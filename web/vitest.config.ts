import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';
import { fileURLToPath } from 'node:url';

// Vitest configuration for the component unit tests. Reuses the same `go-ui`
// source alias as vite.config.ts so the shared library resolves identically,
// and runs in jsdom with globals + a setup file that wires jest-dom matchers.
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      'go-ui': fileURLToPath(new URL('./vendor/go/ui/src/index.ts', import.meta.url)),
    },
  },
  test: {
    globals: true,
    environment: 'jsdom',
    setupFiles: ['./tests/setup.ts'],
    include: ['tests/unit/**/*.test.{ts,tsx}'],
    css: false,
  },
});
