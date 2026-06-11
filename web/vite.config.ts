import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// https://vite.dev/config/
//
// The frontend talks to two backends in dev:
//   - lucode (orchestrator) on :8090 — sessions, projects, turns, ws
//   - aiapi (executor)     on :8091 — checkpoints, diff, revert, blob
//
// The Vite proxy uses the first matching prefix, so we list the
// aiapi routes first; the catch-all /api falls through to lucode.
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/api/checkpoints': 'http://localhost:8091',
      '/api/diff':       'http://localhost:8091',
      '/api/revert':     'http://localhost:8091',
      '/api/blob':       'http://localhost:8091',
      '/api':            'http://localhost:8090',
    },
  },
})
