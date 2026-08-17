import { requestPasswordReset } from '@omnara/sdk/browser'
import { Link } from '@tanstack/react-router'
import { MailCheck } from 'lucide-react'
import type { SyntheticEvent } from 'react'
import { useState } from 'react'

import { AuthHeading, AuthLayout } from '@/components/auth/AuthLayout'
import { Button } from '@/components/ui/button'
import { Field, FieldError, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import type { SubmitStatus } from '@/lib/submit-status'
import { idle, statusError, submitError, submitting, success } from '@/lib/submit-status'

interface ForgotPasswordState {
  email: string
  status: SubmitStatus
}

export function ForgotPassword() {
  const [state, setState] = useState<ForgotPasswordState>({ email: '', status: idle })
  const isSubmitting = state.status.phase === 'submitting'
  const errorMessage = statusError(state.status)

  async function handleSubmit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    setState((prev) => ({ ...prev, status: submitting }))
    try {
      await requestPasswordReset(state.email)
      setState((prev) => ({ ...prev, status: success }))
    } catch (err) {
      const status = submitError(err, 'Password reset request failed')
      setState((prev) => ({
        ...prev,
        status,
      }))
    }
  }

  if (state.status.phase === 'success') {
    return (
      <AuthLayout>
        <div className="flex flex-col items-center gap-4 text-center">
          <span className="bg-primary/10 text-primary flex size-12 items-center justify-center rounded-full">
            <MailCheck className="size-6" />
          </span>
          <AuthHeading
            title="Check your email"
            subtitle={`We sent a reset link to ${state.email}.`}
          />
          <Button asChild variant="outline" className="mt-2">
            <Link to="/login">Back to sign in</Link>
          </Button>
        </div>
      </AuthLayout>
    )
  }

  return (
    <AuthLayout>
      <div className="flex flex-col gap-6">
        <AuthHeading
          title="Reset your password"
          subtitle="Enter your email and we'll send a reset link."
        />
        <form
          onSubmit={(event) => {
            void handleSubmit(event)
          }}
        >
          <FieldGroup>
            <Field>
              <FieldLabel htmlFor="email">Email</FieldLabel>
              <Input
                id="email"
                type="email"
                autoComplete="email"
                required
                value={state.email}
                aria-invalid={errorMessage ? true : undefined}
                onChange={(event) => {
                  setState((prev) => ({ ...prev, email: event.target.value }))
                }}
              />
            </Field>
            {errorMessage && <FieldError>{errorMessage}</FieldError>}
            <Button type="submit" className="w-full" disabled={isSubmitting} loading={isSubmitting}>
              Send reset link
            </Button>
          </FieldGroup>
        </form>
        <p className="text-muted-foreground text-center text-sm">
          Remembered it?{' '}
          <Link
            to="/login"
            className="text-foreground font-medium underline-offset-4 hover:underline"
          >
            Back to sign in
          </Link>
        </p>
      </div>
    </AuthLayout>
  )
}
