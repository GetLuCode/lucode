import { Routes, Route, Navigate } from 'react-router-dom'
import App from './App'
import UsageApp from './UsageApp'
import { LoginScreen } from './components/Auth/LoginScreen'
import { useAuthStore } from './store/auth'

function AuthGuard({ children }: { children: React.ReactNode }) {
  const token = useAuthStore(s => s.token)
  if (!token) return <LoginScreen />
  return <>{children}</>
}

export function AppRoutes() {
  return (
    <AuthGuard>
      <Routes>
        <Route path="/usage" element={<UsageApp />} />
        <Route path="/" element={<App />} />
        <Route path="/projects/:projectId" element={<App />} />
        <Route path="/projects/:projectId/sessions/:sessionId" element={<App />} />
        <Route path="/projects/:projectId/agents" element={<App />} />
        <Route path="/projects/:projectId/kanban" element={<App />} />
        <Route path="/projects/:projectId/checkpoints" element={<App />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </AuthGuard>
  )
}
