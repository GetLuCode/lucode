import { create } from 'zustand'
import { persist } from 'zustand/middleware'

interface AuthState {
  token: string | null
  userId: string | null
  isAdmin: boolean

  setAuth: (token: string, userId: string, isAdmin: boolean) => void
  clearAuth: () => void
}

export const useAuthStore = create<AuthState>()(
  persist(
    (set) => ({
      token: null,
      userId: null,
      isAdmin: false,

      setAuth: (token, userId, isAdmin) => set({ token, userId, isAdmin }),
      clearAuth: () => set({ token: null, userId: null, isAdmin: false }),
    }),
    {
      name: 'lucode-auth',
    }
  )
)
