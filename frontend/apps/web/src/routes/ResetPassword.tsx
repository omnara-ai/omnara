import { completePasswordReset } from '@omnara/sdk/browser'
import type { SyntheticEvent } from 'react'
import { useEffect, useState } from 'react'

import { AuthHeading, AuthLayout } from '@/components/auth/AuthLayout'
import { PasswordRequirements } from '@/components/auth/PasswordRequirements'
import { Button } from '@/components/ui/button'
import { Field, FieldError, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { authTokenFromURL, clearAuthTokenFromURL } from '@/lib/auth-link'
import type { SubmitStatus } from '@/lib/submit-status'
import { idle, statusError, submitError, submitting } from '@/lib/submit-status'

interface ResetPasswordState {
  password: string
  confirmPassword: string
  passwordsMismatch: boolean
  status: SubmitStatus
}

export function ResetPassword() {
  const [token] = useState(authTokenFromURL)
  const [state, setState] = useState<ResetPasswordState>({
    password: '',
    confirmPassword: '',
    passwordsMismatch: false,
    status: token ? idle : { phase: 'error', message: 'Reset link is missing a token.' },
  })
  const isSubmitting = state.status.phase === 'submitting'
  const errorMessage = statusError(state.status)

  useEffect(() => {
    clearAuthTokenFromURL()
  }, [])

  async function handleSubmit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!token) return
    if (state.password !== state.confirmPassword) {
      setState((prev) => ({ ...prev, passwordsMismatch: true, status: idle }))
      return
    }
    setState((prev) => ({ ...prev, status: submitting }))
    try {
      await completePasswordReset(token, state.password)
      window.location.assign('/')
    } catch (err) {
      const status = submitError(err, 'Password reset failed')
      setState((prev) => ({ ...prev, status }))
    }
  }

  return (
    <AuthLayout>
      <div className="flex flex-col gap-6">
        <AuthHeading
          title="Choose a new password"
          subtitle="Set a new password for your Omnara account."
        />
        <form
          onSubmit={(event) => {
            void handleSubmit(event)
          }}
        >
          <FieldGroup>
            <Field>
              <FieldLabel htmlFor="password">New password</FieldLabel>
              <Input
                id="password"
                type="password"
                autoComplete="new-password"
                required
                value={state.password}
                aria-describedby="password-requirements"
                aria-invalid={errorMessage ? true : undefined}
                onChange={(event) => {
                  setState((prev) => ({
                    ...prev,
                    password: event.target.value,
                    passwordsMismatch: false,
                  }))
                }}
              />
              <PasswordRequirements id="password-requirements" password={state.password} />
            </Field>
            <Field>
              <FieldLabel htmlFor="confirm-password">Confirm new password</FieldLabel>
              <Input
                id="confirm-password"
                type="password"
                autoComplete="new-password"
                required
                value={state.confirmPassword}
                aria-invalid={state.passwordsMismatch ? true : undefined}
                onChange={(event) => {
                  setState((prev) => ({
                    ...prev,
                    confirmPassword: event.target.value,
                    passwordsMismatch: false,
                  }))
                }}
              />
              {state.passwordsMismatch && <FieldError>Passwords do not match.</FieldError>}
            </Field>
            {errorMessage && <FieldError>{errorMessage}</FieldError>}
            <Button
              type="submit"
              className="w-full"
              disabled={isSubmitting || !token}
              loading={isSubmitting}
            >
              Continue
            </Button>
          </FieldGroup>
        </form>
      </div>
    </AuthLayout>
  )
}
