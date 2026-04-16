import { create } from 'zustand'
import { persist } from 'zustand/middleware'
import type { User } from '../types/api'

interface AuthState {
  accessToken: string | null
  refreshToken: string | null
  user: User | null
  isAuthenticated: boolean
  setAuth: (accessToken: string, refreshToken: string, user: User) => void
  clearAuth: () => void
  updateUser: (user: User) => void
}

export const useAuthStore = create<AuthState>()(
  persist(
    (set) => ({
      accessToken: null,
      refreshToken: null,
      user: null,
      isAuthenticated: false,

      setAuth: (accessToken, refreshToken, user) => {
        localStorage.setItem('access_token', accessToken)
        localStorage.setItem('refresh_token', refreshToken)
        set({ accessToken, refreshToken, user, isAuthenticated: true })
      },

      clearAuth: () => {
        localStorage.removeItem('access_token')
        localStorage.removeItem('refresh_token')
        set({ accessToken: null, refreshToken: null, user: null, isAuthenticated: false })
      },

      updateUser: (user) => {
        set({ user })
      },
    }),
    {
      name: 'auth-storage',
      partialize: (state) => ({
        accessToken: state.accessToken,
        refreshToken: state.refreshToken,
        user: state.user,
        isAuthenticated: state.isAuthenticated,
      }),
    }
  )
)
