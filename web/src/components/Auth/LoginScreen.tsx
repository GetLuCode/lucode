import { useEffect, useState } from 'react'
import { useAuthStore } from '../../store/auth'
import * as api from '../../api'
import './LoginScreen.css'

type Mode = 'login' | 'signup'

export function LoginScreen() {
  const [signupOpen, setSignupOpen] = useState(false)
  const [mode, setMode] = useState<Mode>('login')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const setAuth = useAuthStore(s => s.setAuth)

  // Check if signup is available (only when no users exist yet)
  useEffect(() => {
    api.getAuthStatus().then(s => {
      setSignupOpen(s.signup_open)
      if (s.signup_open) setMode('signup')
    }).catch(() => {})
  }, [])

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!username.trim() || !password.trim()) {
      setError('Username and password are required')
      return
    }
    setLoading(true)
    setError('')
    try {
      if (mode === 'signup') {
        await api.signup(username.trim(), password)
        // After signup, log in automatically
        const res = await api.login(username.trim(), password)
        setAuth(res.token, res.user_id, res.is_admin)
      } else {
        const res = await api.login(username.trim(), password)
        setAuth(res.token, res.user_id, res.is_admin)
      }
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : 'Unknown error'
      if (msg.includes('401')) {
        setError('Invalid username or password')
      } else if (msg.includes('409')) {
        setError('Username already taken')
      } else if (msg.includes('403')) {
        setError('Registration is closed')
      } else {
        setError(msg)
      }
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="login-screen">
      <div className="login-card">
        <h1 className="login-title">LuCode</h1>
        <p className="login-subtitle">Claude Code Orchestrator</p>

        {signupOpen && (
          <div className="login-tabs">
            <button
              className={`login-tab ${mode === 'login' ? 'active' : ''}`}
              onClick={() => { setMode('login'); setError('') }}
            >
              Sign In
            </button>
            <button
              className={`login-tab ${mode === 'signup' ? 'active' : ''}`}
              onClick={() => { setMode('signup'); setError('') }}
            >
              Create Admin
            </button>
          </div>
        )}

        {!signupOpen && (
          <p className="login-hint">Sign in to continue</p>
        )}

        {signupOpen && mode === 'signup' && (
          <p className="login-hint login-hint--info">
            No users yet — this account will be the admin.
          </p>
        )}

        <form className="login-form" onSubmit={handleSubmit}>
          <div className="login-field">
            <label htmlFor="username">Username</label>
            <input
              id="username"
              type="text"
              value={username}
              onChange={e => setUsername(e.target.value)}
              placeholder="username"
              autoComplete="username"
              autoFocus
              disabled={loading}
            />
          </div>
          <div className="login-field">
            <label htmlFor="password">Password</label>
            <input
              id="password"
              type="password"
              value={password}
              onChange={e => setPassword(e.target.value)}
              placeholder="••••••••"
              autoComplete={mode === 'signup' ? 'new-password' : 'current-password'}
              disabled={loading}
            />
          </div>

          {error && <p className="login-error">{error}</p>}

          <button
            type="submit"
            className="login-submit"
            disabled={loading}
          >
            {loading
              ? 'Loading…'
              : mode === 'signup'
                ? 'Create Admin Account'
                : 'Sign In'}
          </button>
        </form>
      </div>
    </div>
  )
}
