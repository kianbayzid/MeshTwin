import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// Dev: the Go API (go run ./cmd/api -summaries-dir ...) serves /api on :8080; same-origin in production.
export default defineConfig({
  plugins: [react()],
  server: { proxy: { '/api': 'http://127.0.0.1:8080' } },
  build: {
    // Never inline fonts as data: URIs; the API's CSP allows fonts from 'self' only (docs/api-contract.md).
    assetsInlineLimit: (file) => (/\.(woff2?|ttf|otf)$/.test(file) ? false : undefined),
  },
});
