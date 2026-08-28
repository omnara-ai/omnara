import { getLastUsedAuthMethod, sessionLogin } from '@omnara/sdk/browser'
import { Link } from '@tanstack/react-router'
import type { SyntheticEvent } from 'react'
import { useState } from 'react'

import { AuthHeading, AuthLayout } from '@/components/auth/AuthLayout'
import { LastUsedBadge } from '@/components/auth/LastUsedBadge'
import { SocialButtons } from '@/components/auth/SocialButtons'
import { Button } from '@/components/ui/button'
import { Field, FieldError, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { safeReturnTo } from '@/lib/auth-return-to'
import type { SubmitStatus } from '@/lib/submit-status'
import { idle, statusError, submitError, submitting } from '@/lib/submit-status'

function oauthErrorMessage(value: string | null): string | null {
  if (!value) return null
  if (value === 'access_denied') return 'Sign-in was cancelled or denied.'
  return 'Could not sign in with that provider. Please try again.'
}

interface LoginState {
  email: string
  password: string
  status: SubmitStatus
}

export function Login() {
  const params = new URLSearchParams(window.location.search)
  const returnTo = safeReturnTo(params.get('return_to'))
  const [state, setState] = useState<LoginState>({
    email: '',
    password: '',
    status: idle,
  })
  const [oauthError, setOAuthError] = useState(() => oauthErrorMessage(params.get('auth_error')))
  const [lastUsedMethod] = useState(getLastUsedAuthMethod)
  const isSubmitting = state.status.phase === 'submitting'
  const formError = statusError(state.status)
  const errorMessage = formError ?? oauthError

  async function handleSubmit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    setOAuthError(null)
    setState((prev) => ({ ...prev, status: submitting }))
    try {
      await sessionLogin(state.email, state.password)
      window.location.assign(returnTo)
    } catch (err) {
      const status = submitError(err, 'Login failed')
      setState((prev) => ({ ...prev, status }))
    }
  }

  return (
    <AuthLayout>
      <div className="flex flex-col gap-6">
        <AuthHeading
          title="Sign in to Omnara"
          subtitle="Welcome back. Enter your credentials to continue."
        />
        <form
          onSubmit={(event) => {
            void handleSubmit(event)
          }}
        >
          <FieldGroup>
            <SocialButtons returnTo={returnTo} lastUsedMethod={lastUsedMethod} />
            <Field>
              <FieldLabel htmlFor="email">Email</FieldLabel>
              <Input
                id="email"
                type="email"
                autoComplete="email"
                required
                value={state.email}
                aria-invalid={formError ? true : undefined}
                onChange={(event) => {
                  setState((prev) => ({ ...prev, email: event.target.value }))
                }}
              />
            </Field>
            <Field>
              <div className="flex items-center">
                <FieldLabel htmlFor="password">Password</FieldLabel>
                <Link
                  to="/forgot-password"
                  className="text-muted-foreground hover:text-foreground ml-auto text-sm"
                >
                  Forgot password?
                </Link>
              </div>
              <Input
                id="password"
                type="password"
                autoComplete="current-password"
                required
                value={state.password}
                aria-invalid={formError ? true : undefined}
                onChange={(event) => {
                  setState((prev) => ({ ...prev, password: event.target.value }))
                }}
              />
            </Field>
            {errorMessage && <FieldError>{errorMessage}</FieldError>}
            <Button
              type="submit"
              className="relative w-full"
              disabled={isSubmitting}
              loading={isSubmitting}
            >
              Sign in
              {lastUsedMethod === 'password' && <LastUsedBadge className="absolute right-3" />}
            </Button>
          </FieldGroup>
        </form>
        <p className="text-muted-foreground text-center text-sm">
          Don&apos;t have an account?{' '}
          <Link
            to="/signup"
            search={returnTo === '/' ? {} : { return_to: returnTo }}
            className="text-foreground font-medium underline-offset-4 hover:underline"
          >
            Sign up
          </Link>
        </p>
      </div>
    </AuthLayout>
  )
}
