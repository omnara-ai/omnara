import { createContext, useContext, useEffect, useState, ReactNode } from 'react'
import { UserProfile } from '@/types/dashboard'
import { authClient, SignUpData, SignInData } from './authClient'
import { supabase } from '../supabase'

interface AuthContextType {
  user: UserProfile | null
  loading: boolean
  signUp: (data: SignUpData) => Promise<void>
  signIn: (data: SignInData) => Promise<void>
  signInWithGoogle: (redirectTo?: string) => Promise<void>
  signInWithApple: (redirectTo?: string) => Promise<void>
  signInWithGithub: (redirectTo?: string) => Promise<void>
  signOut: () => Promise<void>
  refreshSession: () => Promise<void>
}

const AuthContext = createContext<AuthContextType | undefined>(undefined)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<UserProfile | null>(null)
  const [loading, setLoading] = useState(true)

  const refreshSession = async () => {
    try {
      const sessionUser = await authClient.getSession()
      setUser(sessionUser)
    } catch (error) {
      setUser(null)
    } finally {
      setLoading(false)
    }
  }

  const signUp = async (data: SignUpData) => {
    const response = await authClient.signUp(data)
    setUser(response.user)
  }

  const signIn = async (data: SignInData) => {
    const response = await authClient.signIn(data)
    setUser(response.user)
  }

  const signInWithGoogle = async (redirectTo?: string) => {
    await authClient.signInWithGoogle(redirectTo)
    // Note: Google OAuth redirects, so we don't set user state here
  }

  const signInWithApple = async (redirectTo?: string) => {
    await authClient.signInWithApple(redirectTo)
    // Note: Apple OAuth redirects, so we don't set user state here
  }

  const signInWithGithub = async (redirectTo?: string) => {
    await authClient.signInWithGithub(redirectTo)
    // Note: GitHub OAuth redirects, so we don't set user state here
  }

  const signOut = async () => {
    try {
      await authClient.signOut()
      setUser(null)
    } catch (error) {
      // Even if the request fails, clear local state
      setUser(null)
    }
  }

  useEffect(() => {
    let mounted = true

    // Listen for auth state changes
    const { data: { subscription } } = supabase.auth.onAuthStateChange(
      async (event, session) => {
        if (!mounted) return

        if (session?.user) {
          setUser({
            id: session.user.id,
            email: session.user.email || '',
            display_name: session.user.user_metadata?.display_name || null,
            created_at: session.user.created_at || new Date().toISOString(),
          })
          
          // Check for CLI auth redirect
          const cliAuthRedirect = localStorage.getItem('cli-auth-redirect')
          if (cliAuthRedirect && window.location.pathname !== '/cli-auth') {
            localStorage.removeItem('cli-auth-redirect')
            window.location.href = cliAuthRedirect
          }
        } else {
          setUser(null)
        }
        setLoading(false)
      }
    )

    // Get initial session
    const getInitialSession = async () => {
      try {
        const { data: { session } } = await supabase.auth.getSession()
        console.log('[AuthContext] Initial session:', session)
        if (!mounted) return

        if (session?.user) {
          // Check for CLI auth redirect before setting user
          const cliAuthRedirect = localStorage.getItem('cli-auth-redirect')
          if (cliAuthRedirect && window.location.pathname !== '/cli-auth') {
            localStorage.removeItem('cli-auth-redirect')
            window.location.href = cliAuthRedirect
            return // Don't set user state since we're redirecting
          }
          
          setUser({
            id: session.user.id,
            email: session.user.email || '',
            display_name: session.user.user_metadata?.display_name || null,
            created_at: session.user.created_at || new Date().toISOString(),
          })
          // Also sync with backend if we have a session
          try {
            await authClient.syncUserWithBackend(session.user)
          } catch (error) {
            console.warn('Failed to sync user with backend:', error)
          }
        } else {
          setUser(null)
        }
      } catch (error) {
        console.warn('Failed to get initial session:', error)
        setUser(null)
      } finally {
        if (mounted) {
          setLoading(false)
        }
      }
    }

    getInitialSession()

    return () => {
      mounted = false
      subscription.unsubscribe()
    }
  }, [])

  const value = {
    user,
    loading,
    signUp,
    signIn,
    signInWithGoogle,
    signInWithApple,
    signInWithGithub,
    signOut,
    refreshSession,
  }

  return (
    <AuthContext.Provider value={value}>
      {children}
    </AuthContext.Provider>
  )
}

export function useAuth() {
  const context = useContext(AuthContext)
  if (context === undefined) {
    throw new Error('useAuth must be used within an AuthProvider')
  }
  return context
}
