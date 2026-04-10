import { Routes, Route, Navigate } from 'react-router-dom'
import App from './App'
import UsageApp from './UsageApp'

export function AppRoutes() {
  return (
    <Routes>
      <Route path="/usage" element={<UsageApp />} />
      <Route path="/" element={<App />} />
      <Route path="/projects/:projectId" element={<App />} />
      <Route path="/projects/:projectId/sessions/:sessionId" element={<App />} />
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  )
}
