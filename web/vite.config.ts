import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// Dev: the Go API (go run ./cmd/api -summaries-dir ...) serves /api on :8080; same-origin in production.
export default defineConfig({
  plugins: [react()],
  server: { proxy: { '/api': 'http://127.0.0.1:8080' } },
});
