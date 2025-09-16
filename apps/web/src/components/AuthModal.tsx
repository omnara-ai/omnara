
import { useState, useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import { useAuth } from '@/lib/auth/AuthContext'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Separator } from '@/components/ui/separator'
import { X } from 'lucide-react'

interface AuthModalProps {
  isOpen: boolean
  onClose: () => void
  onSuccess?: () => void  // Optional callback after successful auth
  redirectTo?: string  // Optional redirect URL for OAuth
  initialMode?: 'signin' | 'signup'  // Initial mode for the modal
}

export function AuthModal({ isOpen, onClose, onSuccess, redirectTo, initialMode = 'signin' }: AuthModalProps) {
  const navigate = useNavigate()
  const { signIn, signUp, signInWithGoogle, signInWithApple, signInWithGithub } = useAuth()
  const [isSignUp, setIsSignUp] = useState(initialMode === 'signup')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [loading, setLoading] = useState(false)
  const [googleLoading, setGoogleLoading] = useState(false)
  const [appleLoading, setAppleLoading] = useState(false)
  const [githubLoading, setGithubLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [showConfirmation, setShowConfirmation] = useState(false)

  // Reset the mode when initialMode changes
  useEffect(() => {
    setIsSignUp(initialMode === 'signup')
  }, [initialMode])

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setLoading(true)
    setError(null)

    try {
      if (isSignUp) {
        await signUp({ email, password })
        // Show confirmation message instead of navigating
        setShowConfirmation(true)
      } else {
        await signIn({ email, password })
        onClose()
        // Small delay to ensure auth state propagates
        setTimeout(() => {
          if (onSuccess) {
            onSuccess()
          } else {
            // Always navigate to dashboard after sign-in
            navigate('/dashboard')
          }
        }, 100)
      }
    } catch (err: any) {
      setError(err.message || 'An unexpected error occurred')
    } finally {
      setLoading(false)
    }
  }

  const handleGoogleSignIn = async () => {
    setGoogleLoading(true)
    setError(null)

    try {
      // Don't pass any redirect URL - let authClient handle the logic
      await signInWithGoogle(redirectTo)
      onClose()
      // Note: Google OAuth redirects, so navigation happens automatically
    } catch (err: any) {
      setError(err.message || 'Google sign-in failed')
    } finally {
      setGoogleLoading(false)
    }
  }

  const handleAppleSignIn = async () => {
    setAppleLoading(true)
    setError(null)

    try {
      // Don't pass any redirect URL - let authClient handle the logic
      await signInWithApple(redirectTo)
      onClose()
      // Note: Apple OAuth redirects, so navigation happens automatically
    } catch (err: any) {
      setError(err.message || 'Apple sign-in failed')
    } finally {
      setAppleLoading(false)
    }
  }

  const handleGithubSignIn = async () => {
    setGithubLoading(true)
    setError(null)

    try {
      await signInWithGithub(redirectTo)
      onClose()
      // Note: GitHub OAuth redirects, so navigation happens automatically
    } catch (err: any) {
      setError(err.message || 'GitHub sign-in failed')
    } finally {
      setGithubLoading(false)
    }
  }

  const resetForm = () => {
    setEmail('')
    setPassword('')
    setError(null)
    setLoading(false)
    setGoogleLoading(false)
    setAppleLoading(false)
    setShowConfirmation(false)
    setGithubLoading(false)
  }

  const handleClose = () => {
    resetForm()
    onClose()
  }

  const toggleMode = () => {
    setIsSignUp(!isSignUp)
    setError(null)
  }

  return (
    <Dialog open={isOpen} onOpenChange={handleClose}>
      <DialogContent className="dialog-omnara sm:max-w-md text-cream">
        {showConfirmation ? (
          // Email confirmation message
          <div className="space-y-4 py-6">
            <div className="mx-auto w-16 h-16 bg-green-500/20 rounded-full flex items-center justify-center">
              <svg className="w-8 h-8 text-green-400" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M9 12l2 2 4-4m6 2a9 9 0 11-18 0 9 9 0 0118 0z" />
              </svg>
            </div>
            <DialogHeader>
              <DialogTitle className="dialog-title-omnara text-2xl font-bold text-center">
                Check your email!
              </DialogTitle>
              <DialogDescription className="text-center text-neutral-400 space-y-2">
                <p>We've sent a confirmation link to:</p>
                <p className="font-semibold text-cozy-amber">{email}</p>
                <p className="text-sm mt-4">Click the link in the email to complete your registration.</p>
              </DialogDescription>
            </DialogHeader>
            <Button 
              onClick={handleClose}
              className="w-full bg-cozy-amber text-warm-charcoal font-bold hover:bg-soft-gold transition-all duration-300"
            >
              Got it
            </Button>
          </div>
        ) : (
          // Normal auth form
          <>
            <DialogHeader className="space-y-3">
              <DialogTitle className="dialog-title-omnara text-2xl font-bold text-center">
                {isSignUp ? 'Create Account' : 'Welcome Back'}
              </DialogTitle>
              <DialogDescription className="text-center text-neutral-400">
                {isSignUp 
                  ? 'Sign up to start managing your AI agents' 
                  : 'Sign in to access your dashboard'
                }
              </DialogDescription>
            </DialogHeader>

        <div className="space-y-6 mt-6">
          {/* Google Sign In - Now at the top */}
          <Button
            onClick={handleGoogleSignIn}
            disabled={loading || googleLoading || appleLoading || githubLoading}
            className="w-full bg-white/5 border border-neutral-700 text-neutral-50 hover:bg-white/10 hover:border-neutral-600 transition-all duration-300 font-medium h-12 flex items-center justify-center space-x-3"
          >
            <svg className="w-5 h-5" viewBox="0 0 24 24">
              <path fill="#4285F4" d="M22.56 12.25c0-.78-.07-1.53-.2-2.25H12v4.26h5.92c-.26 1.37-1.04 2.53-2.21 3.31v2.77h3.57c2.08-1.92 3.28-4.74 3.28-8.09z"/>
              <path fill="#34A853" d="M12 23c2.97 0 5.46-.98 7.28-2.66l-3.57-2.77c-.98.66-2.23 1.06-3.71 1.06-2.86 0-5.29-1.93-6.16-4.53H2.18v2.84C3.99 20.53 7.7 23 12 23z"/>
              <path fill="#FBBC05" d="M5.84 14.09c-.22-.66-.35-1.36-.35-2.09s.13-1.43.35-2.09V7.07H2.18C1.43 8.55 1 10.22 1 12s.43 3.45 1.18 4.93l2.85-2.22.81-.62z"/>
              <path fill="#EA4335" d="M12 5.38c1.62 0 3.06.56 4.21 1.64l3.15-3.15C17.45 2.09 14.97 1 12 1 7.7 1 3.99 3.47 2.18 7.07l3.66 2.84c.87-2.6 3.3-4.53 6.16-4.53z"/>
            </svg>
            <span>{googleLoading ? 'Connecting...' : `Continue with Google`}</span>
          </Button>

          {/* GitHub Sign In */}
          <Button
            onClick={handleGithubSignIn}
            disabled={loading || googleLoading || appleLoading || githubLoading}
            className="w-full bg-white/5 border border-neutral-700 text-neutral-50 hover:bg-white/10 hover:border-neutral-600 transition-all duration-300 font-medium h-12 flex items-center justify-center space-x-3"
          >
            <svg viewBox="0 0 16 16" width="20" height="20" fill="currentColor" aria-hidden="true" className="mr-1">
              <path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0016 8c0-4.42-3.58-8-8-8z" />
            </svg>
            <span>{githubLoading ? 'Connecting...' : `Continue with GitHub`}</span>
          </Button>

          {/* Apple Sign In */}
          <Button
            onClick={handleAppleSignIn}
            disabled={loading || googleLoading || appleLoading || githubLoading}
            className="w-full bg-white/5 border border-neutral-700 text-neutral-50 hover:bg-white/10 hover:border-neutral-600 transition-all duration-300 font-medium h-12 flex items-center justify-center space-x-3"
          >
            <svg className="w-7 h-7" viewBox="0 0 24 24" fill="currentColor">
              <path d="M17.05 20.28c-.98.95-2.05.8-3.08.35-1.09-.46-2.09-.48-3.24 0-1.44.62-2.2.44-3.06-.35C2.79 15.25 3.51 7.59 9.05 7.31c1.35.07 2.29.74 3.08.8 1.18-.24 2.31-.93 3.57-.84 1.51.12 2.65.72 3.4 1.8-3.12 1.87-2.38 5.98.48 7.13-.57 1.5-1.31 2.99-2.53 4.09l-.05-.01zM12.03 7.25c-.15-2.23 1.66-4.07 3.74-4.25.29 2.58-2.34 4.52-3.74 4.25z"/>
            </svg>
            <span>{appleLoading ? 'Connecting...' : `Continue with Apple`}</span>
          </Button>

          {/* Divider */}
          <div className="flex items-center space-x-4">
            <Separator className="flex-1 bg-neutral-600" />
            <span className="text-sm text-neutral-500 px-2">or</span>
            <Separator className="flex-1 bg-neutral-600" />
          </div>

          {/* Email/Password Form */}
          <form onSubmit={handleSubmit} className="space-y-4">
            <div className="space-y-4">
              <Input
                type="email"
                placeholder="Email"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                required
                className="bg-warm-charcoal/50 border-neutral-700 text-neutral-50 placeholder:text-neutral-400 focus:border-cozy-amber focus:ring-cozy-amber/30 transition-all duration-200 h-12"
              />
              <Input
                type="password"
                placeholder="Password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
                className="bg-warm-charcoal/50 border-neutral-700 text-neutral-50 placeholder:text-neutral-400 focus:border-cozy-amber focus:ring-cozy-amber/30 transition-all duration-200 h-12"
              />
            </div>
            
            {error && (
              <div className="text-sm text-red-400 text-center bg-red-400/10 p-3 rounded-lg border border-red-400/20">
                {error}
              </div>
            )}
            
            <Button 
              type="submit" 
              className="w-full bg-cozy-amber text-warm-charcoal font-bold hover:bg-soft-gold hover:shadow-lg hover:shadow-cozy-amber/20 transition-all duration-300 h-12" 
              disabled={loading || googleLoading || appleLoading || githubLoading}
          >
              {loading ? 'Loading...' : (isSignUp ? 'Create Account' : 'Sign In')}
            </Button>
          </form>
        </div>

        <div className="text-center mt-6">
          <button
            type="button"
            onClick={toggleMode}
            className="text-sm text-cozy-amber hover:text-soft-gold transition-colors duration-200 underline underline-offset-2"
            disabled={loading || googleLoading || appleLoading}
          >
            {isSignUp 
              ? 'Already have an account? Sign in' 
              : "Don't have an account? Sign up"
            }
          </button>
        </div>
          </>
        )}
      </DialogContent>
    </Dialog>
  )
} 
