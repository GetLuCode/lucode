import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import './index.css'
import './shadcn.css'
import { AppRoutes } from './routes.tsx'

// PWA: solo en el build (en `pnpm dev` el service worker estorbaria al HMR de Parcel).
if ('serviceWorker' in navigator && process.env.NODE_ENV === 'production') {
  window.addEventListener('load', () => {
    navigator.serviceWorker
      .register(new URL('./sw.js', import.meta.url), { scope: '/' })
      .catch((err) => console.warn('service worker', err))
  })
}

createRoot(document.getElementById('root')!).render(
  <BrowserRouter>
    <AppRoutes />
  </BrowserRouter>,
)
