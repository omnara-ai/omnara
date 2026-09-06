import { requestSignup } from '@omnara/sdk/browser'
import { Link } from '@tanstack/react-router'
import type { SyntheticEvent } from 'react'
import { useState } from 'react'

import { AuthHeading, AuthLayout } from '@/components/auth/AuthLayout'
import { SocialButtons } from '@/components/auth/SocialButtons'
import { MailCheck } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Field, FieldError, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { isDeviceApprovalReturnTo, safeReturnTo } from '@/lib/auth-return-to'
import type { SubmitStatus } from '@/lib/submit-status'
import { idle, statusError, submitError, submitting, success } from '@/lib/submit-status'

interface SignUpState {
  email: string
  status: SubmitStatus
}

export function SignUp() {
  const returnTo = safeReturnTo(new URLSearchParams(window.location.search).get('return_to'))
  const approvingDevice = isDeviceApprovalReturnTo(returnTo)
  const [state, setState] = useState<SignUpState>({ email: '', status: idle })
  const isSubmitting = state.status.phase === 'submitting'
  const errorMessage = statusError(state.status)

  async function handleSubmit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    setState((prev) => ({ ...prev, status: submitting }))
    try {
      await requestSignup(state.email, returnTo === '/' ? undefined : returnTo)
      setState((prev) => ({ ...prev, status: success }))
    } catch (err) {
      const status = submitError(err, 'Signup request failed')
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
            subtitle={
              approvingDevice
                ? `We sent a verification link to ${state.email}. Finish setup within 15 minutes to approve the CLI login; otherwise run the CLI login again.`
                : `We sent a verification link to ${state.email}.`
            }
          />
          <Button asChild variant="outline" className="mt-2">
            <Link to="/login" search={returnTo === '/' ? {} : { return_to: returnTo }}>
              Back to sign in
            </Link>
          </Button>
        </div>
      </AuthLayout>
    )
  }

  return (
    <AuthLayout>
      <div className="flex flex-col gap-6">
        <AuthHeading
          title="Create your Omnara account"
          subtitle={
            approvingDevice
              ? "Create an account to approve the CLI login you just started. Enter your email and we'll send a verification link that brings you back to the approval page."
              : "Enter your email and we'll send a verification link."
          }
        />
        <form
          onSubmit={(event) => {
            void handleSubmit(event)
          }}
        >
          <FieldGroup>
            <SocialButtons returnTo={returnTo} />
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
              Send verification link
            </Button>
          </FieldGroup>
        </form>
        <p className="text-muted-foreground text-center text-sm">
          Already have an account?{' '}
          <Link
            to="/login"
            search={returnTo === '/' ? {} : { return_to: returnTo }}
            className="text-foreground font-medium underline-offset-4 hover:underline"
          >
            Sign in
          </Link>
        </p>
      </div>
    </AuthLayout>
  )
}
