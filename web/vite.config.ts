import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import path from 'node:path'

// https://vite.dev/config/
//
// The frontend talks to three backends in dev:
//   - lucode  (orchestrator) on :8090 — projects, sessions, messages, tools, agents, kanban, usage, ws, etc.
//   - aiapi   (executor)     on :8091 — checkpoints, diff, revert, blob, turns, models
//   - authapi (auth service) on :8092 — auth/signup/login/logout, apikeys
//
// Vite proxy matches by string prefix — list specific routes first, catch-all last.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: { alias: { '@': path.resolve(__dirname, 'src') } },
  server: {
    // Vite 8 forwards browser console output to the server when it detects a coding agent; if the
    // HMR socket drops, its sendError loops forever ("send was called before connect").
    forwardConsole: false,
    proxy: {
      // authapi (8092) — strip /api prefix
      '/api/auth': {
        target: 'http://localhost:8092',
        rewrite: (path) => path.replace(/^\/api\//, ''),
      },
      '/api/apikeys': {
        target: 'http://localhost:8092',
        rewrite: (path) => path.replace(/^\/api\//, ''),
      },

      // aiapi (8091) — versioning, turns, models
      '/api/versioning': 'http://localhost:8091',
      '/api/turns': {
        target: 'http://localhost:8091',
        rewrite: (path) => path.replace(/^\/api\//, ''),
      },
      '/api/models': {
        target: 'http://localhost:8091',
        rewrite: (path) => path.replace(/^\/api\//, ''),
      },

      // lucode (8090) — catch-all for everything else
      '/api': 'http://localhost:8090',
      '/ws':  'http://localhost:8090',
    },
  },
})