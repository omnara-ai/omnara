import { createContext, useContext, useEffect, useState, ReactNode } from 'react'
import { UserProfile } from '@/types/dashboard'
import { authClient, SignUpData, SignInData } from './authClient'
import { supabase } from '../supabase'
import { analytics, getDeviceType, useAnalytics } from '../analytics'

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
  // Initialize analytics early so identify/track calls work
  useAnalytics()
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

    // Track sign up event
    if (response.user) {
      analytics.track('user_signed_up', {
        auth_method: 'email',
        source: window.location.pathname
      })
    }
  }

  const signIn = async (data: SignInData) => {
    const response = await authClient.signIn(data)
    setUser(response.user)

    // Track sign in event
    if (response.user) {
      analytics.track('user_signed_in', {
        auth_method: 'email',
        source: window.location.pathname
      })
    }
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
    // Track sign out before clearing state
    analytics.track('user_signed_out', {
      source: window.location.pathname
    })
    analytics.reset()

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
          const userProfile = {
            id: session.user.id,
            email: session.user.email || '',
            display_name: session.user.user_metadata?.display_name || null,
            created_at: session.user.created_at || new Date().toISOString(),
          }
          setUser(userProfile)

          // Identify user for analytics
          analytics.identify(session.user.id, {
            email: userProfile.email,
            created_at: userProfile.created_at,
            is_mobile: getDeviceType() === 'mobile'
          })

          // Track OAuth sign-ins (when event is triggered by OAuth)
          if (event === 'SIGNED_IN' && session.user.app_metadata?.provider !== 'email') {
            analytics.track('user_signed_in', {
              auth_method: session.user.app_metadata?.provider || 'oauth',
              source: window.location.pathname
            })
          }

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
          
          const userProfile = {
            id: session.user.id,
            email: session.user.email || '',
            display_name: session.user.user_metadata?.display_name || null,
            created_at: session.user.created_at || new Date().toISOString(),
          }
          setUser(userProfile)

          // Identify user for analytics on initial load
          analytics.identify(session.user.id, {
            email: userProfile.email,
            created_at: userProfile.created_at,
            is_mobile: getDeviceType() === 'mobile'
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
