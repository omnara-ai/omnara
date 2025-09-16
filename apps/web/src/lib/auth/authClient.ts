import { UserProfile } from '@/types/dashboard'
import { supabase } from '../supabase'

const API_BASE_URL = import.meta.env.VITE_API_URL || 'https://agent-dashboard-backend.onrender.com'

export interface SignUpData {
  email: string
  password: string
}

export interface SignInData {
  email: string
  password: string
}

export interface AuthResponse {
  user: UserProfile
  message: string
}

// Centralized local auth cleanup exported for reuse
export function clearLocalAuth(): void {
  try {
    // App-specific token (legacy/custom)
    try { localStorage.removeItem('authToken') } catch {}

    // Supabase client keys that may linger locally
    const patterns: RegExp[] = [
      /^sb-.*-auth-token$/i,   // Primary DB auth token
      /^sb-.*-auth-event$/i,   // Last auth event metadata
      /^sb-.*-refresh-token$/i // Refresh token (if present in some setups)
    ]

    Object.keys(localStorage).forEach((key) => {
      if (patterns.some((re) => re.test(key))) {
        try { localStorage.removeItem(key) } catch {}
      }
    })
  } catch {}
}

class AuthClient {
  private async getSupabaseToken(): Promise<string | null> {
    const { data: { session } } = await supabase.auth.getSession()
    return session?.access_token || null
  }

  private async request<T>(endpoint: string, options?: RequestInit): Promise<T> {
    const token = await this.getSupabaseToken()
    const headers: Record<string, string> = {
      'Content-Type': 'application/json',
    }
    
    // Add existing headers
    if (options?.headers) {
      Object.assign(headers, options.headers)
    }
    
    if (token) {
      headers['Authorization'] = `Bearer ${token}`
    }

    const response = await fetch(`${API_BASE_URL}${endpoint}`, {
      headers,
      ...options,
    })

    if (!response.ok) {
      if (response.status === 401) {
        // Remote-first sign-out; token clear happens in signOut's catch/finally
        await this.signOut().catch(() => {})
        // Redirect to home/login
        window.location.assign('/')
        throw new Error('Authentication required')
      }

      const errorText = await response.text().catch(() => '')
      throw new Error(errorText || `Request failed: ${response.statusText}`)
    }

    return response.json()
  }

  async signUp(data: SignUpData): Promise<AuthResponse> {
    const { data: authData, error } = await supabase.auth.signUp({
      email: data.email,
      password: data.password,
    })

    if (error) {
      throw new Error(error.message)
    }

    if (!authData.user) {
      throw new Error('Failed to create user')
    }

    // Create/sync user in our database
    await this.syncUserWithBackend(authData.user)

    return {
      user: {
        id: authData.user.id,
        email: authData.user.email || '',
        display_name: authData.user.user_metadata?.display_name || null,
        created_at: authData.user.created_at || new Date().toISOString(),
      },
      message: 'User created successfully',
    }
  }

  async signIn(data: SignInData): Promise<AuthResponse> {
    const { data: authData, error } = await supabase.auth.signInWithPassword({
      email: data.email,
      password: data.password,
    })

    if (error) {
      throw new Error(error.message)
    }

    if (!authData.user) {
      throw new Error('Invalid credentials')
    }

    // Sync user with our backend
    await this.syncUserWithBackend(authData.user)

    return {
      user: {
        id: authData.user.id,
        email: authData.user.email || '',
        display_name: authData.user.user_metadata?.display_name || null,
        created_at: authData.user.created_at || new Date().toISOString(),
      },
      message: 'Signed in successfully',
    }
  }

  async signInWithGoogle(redirectTo?: string): Promise<void> {
    // If on homepage (with or without hash), redirect to dashboard
    // Otherwise, stay on the current page (without hash to avoid redirect issues)
    const isHomepage = window.location.pathname === '/'
    const cleanUrl = window.location.origin + window.location.pathname
    const defaultRedirect = isHomepage 
      ? `${window.location.origin}/dashboard`
      : cleanUrl
    
    const { error } = await supabase.auth.signInWithOAuth({
      provider: 'google',
      options: {
        redirectTo: redirectTo || defaultRedirect,
        queryParams: {
          prompt: 'consent',
          access_type: 'offline'
        }
      }
    })

    if (error) {
      throw new Error(error.message)
    }

    // Note: OAuth redirects to the specified URL, so no return value needed
  }

  async signInWithApple(redirectTo?: string): Promise<void> {
    // If on homepage (with or without hash), redirect to dashboard
    // Otherwise, stay on the current page (without hash to avoid redirect issues)
    const isHomepage = window.location.pathname === '/'
    const cleanUrl = window.location.origin + window.location.pathname
    const defaultRedirect = isHomepage 
      ? `${window.location.origin}/dashboard`
      : cleanUrl
    
    const { error } = await supabase.auth.signInWithOAuth({
      provider: 'apple',
      options: {
        redirectTo: redirectTo || defaultRedirect,
        queryParams: {
          prompt: 'consent'
        }
      }
    })

    if (error) {
      throw new Error(error.message)
    }

    // Note: OAuth redirects to the specified URL, so no return value needed
  }

  async signInWithGithub(redirectTo?: string): Promise<void> {
    // If on homepage (with or without hash), redirect to dashboard
    // Otherwise, stay on the current page (without hash to avoid redirect issues)
    const isHomepage = window.location.pathname === '/'
    const cleanUrl = window.location.origin + window.location.pathname
    const defaultRedirect = isHomepage 
      ? `${window.location.origin}/dashboard`
      : cleanUrl

    const { error } = await supabase.auth.signInWithOAuth({
      provider: 'github',
      options: {
        redirectTo: redirectTo || defaultRedirect,
        scopes: 'user:email',
      }
    })

    if (error) {
      throw new Error(error.message)
    }

    // Note: OAuth redirects to the specified URL, so no return value needed
  }

  async signOut(): Promise<{ message: string; remoteLoggedOut: boolean }> {
    let remoteLoggedOut = true
    try {
      const { error } = await supabase.auth.signOut({ scope: 'global' })
      if (error) {
        remoteLoggedOut = false
        throw new Error(error.message)
      }
    } catch (error: any) {
      remoteLoggedOut = false
      console.warn('Failed to sign out from Supabase, clearing localStorage:', error?.message || error)
      // Clear locally if remote sign-out fails
      clearLocalAuth()
    } finally {
      // Ensure any app-specific remnants are cleared regardless
      clearLocalAuth()
    }
    return {
      message: remoteLoggedOut ? 'Signed out successfully' : 'Signed out locally; remote sign-out failed',
      remoteLoggedOut,
    }
  }

  async getSession(): Promise<UserProfile | null> {
    try {
      const { data: { session } } = await supabase.auth.getSession()
      
      if (!session?.user) {
        return null
      }

      return {
        id: session.user.id,
        email: session.user.email || '',
        display_name: session.user.user_metadata?.display_name || null,
        created_at: session.user.created_at || new Date().toISOString(),
      }
    } catch (error) {
      console.warn('Auth session check failed:', error)
      return null
    }
  }

  async getCurrentUser(): Promise<UserProfile> {
    const user = await this.getSession()
    if (!user) {
      throw new Error('Not authenticated')
    }
    return user
  }

  async syncUserWithBackend(user: any): Promise<void> {
    try {
      // This endpoint will create or update the user in our backend database
      await this.request('/api/v1/auth/sync-user', {
        method: 'POST',
        body: JSON.stringify({
          id: user.id,
          email: user.email,
          display_name: user.user_metadata?.display_name || null,
        }),
      })
    } catch (error) {
      console.warn('Failed to sync user with backend:', error)
      // Don't throw here - auth can still work without backend sync
    }
  }
}

export const authClient = new AuthClient()
