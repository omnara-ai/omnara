import {
  useCreateProjectAppOAuthSetup,
  useCreateProjectAppSlackSetup,
  useOmnaraClient,
  useProjectAppOAuthCompletion,
} from '@omnara/react'
import {
  ApiError,
  type GetProjectAppError,
  type IntegrationOAuthSetup,
  type ProjectApp,
} from '@omnara/sdk'
import { listProjectAppsQueryKey } from '@omnara/sdk/tanstack'
import { useForm } from '@tanstack/react-form'
import { useQueryClient } from '@tanstack/react-query'
import { useEffect, useState } from 'react'

import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { errorMessage } from '@/lib/submit-status'

import {
  noAppIcon,
  slackAppIconPayload,
  slackAppNameMaxLength,
  slackConnectionFormValid,
} from './ConnectSlackFormState'
import { SlackAppIconField } from './SlackAppIconField'
import { slackOAuthErrorDescription } from './slackOAuthErrors'

interface SlackConnectionProps {
  orgId: string
  projectId: string
  app: ProjectApp
  onConnected?: (app: ProjectApp) => void
}

export function ConnectSlackForm({ orgId, projectId, app, onConnected }: SlackConnectionProps) {
  const createSlackSetup = useCreateProjectAppSlackSetup(orgId, projectId, app.id)
  const createOAuthSetup = useCreateProjectAppOAuthSetup(orgId, projectId, app.id)
  const client = useOmnaraClient()
  const apiOrigin = new URL(client.getConfig().baseUrl ?? '/api/v1', window.location.origin).origin
  const reconnect = Boolean(app.provider_tenant_id)
  const [existingAppSelected, setExistingAppSelected] = useState(false)
  const existingApp = reconnect || existingAppSelected
  const [pending, setPending] = useState<IntegrationOAuthSetup>()
  const [error, setError] = useState('')
  const completion = useProjectAppOAuthCompletion(orgId, projectId, app.id, pending)
  const completed =
    completion.data?.state === 'active' && completion.data.last_oauth_flow_id === pending?.flow_id
  const failure = slackSetupFailure(pending, completed, completion.data, completion.error)
  const cache = useQueryClient()
  useEffect(() => {
    if (!pending) return
    if (completed && completion.data) {
      const connectedApp = completion.data
      let canceled = false
      void cache
        .invalidateQueries({
          queryKey: listProjectAppsQueryKey({
            path: { orgID: orgId, projectID: projectId },
            client,
          }),
        })
        .then(() => {
          if (canceled) return
          setPending(undefined)
          onConnected?.(connectedApp)
        })
      return () => {
        canceled = true
      }
    }
    return undefined
  }, [pending, completed, completion.data, cache, client, orgId, projectId, onConnected])
  const form = useForm({
    defaultValues: {
      clientId: '',
      clientSecret: '',
      signingSecret: '',
      appName: 'Omnara',
      appConfigurationToken: '',
      appIcon: noAppIcon,
    },
    onSubmit: async ({ value }) => {
      if (!(existingApp ? existingCredentialsValid(value) : slackConnectionFormValid(value))) return
      setError('')
      try {
        let setup: IntegrationOAuthSetup
        if (existingApp) {
          setup = await createOAuthSetup.mutateAsync({
            client_id: value.clientId.trim(),
            client_secret: value.clientSecret.trim(),
            signing_secret: value.signingSecret.trim(),
            return_to: window.location.pathname,
          })
        } else {
          const icon = await slackAppIconPayload(value.appIcon)
          setup = await createSlackSetup.mutateAsync({
            app_name: value.appName.trim(),
            app_configuration_token: value.appConfigurationToken.trim(),
            icon,
            return_to: window.location.pathname,
          })
        }
        if (setup.app_id !== app.id) {
          setError('Authorization returned a different app. Please try again.')
        } else {
          setPending(setup)
        }
      } catch (err) {
        setError(errorMessage(err, 'Could not start integration setup'))
      }
    },
  })

  return (
    <div>
      {pending && !failure ? (
        <SlackAuthorizationPending
          pending={pending}
          isError={completion.isError}
          onRetry={() => void completion.refetch()}
          onRestart={() => {
            setPending(undefined)
          }}
        />
      ) : (
        <form
          onSubmit={(event) => {
            event.preventDefault()
            void form.handleSubmit()
          }}
        >
          <FieldGroup>
            {!reconnect && (
              <label className="flex gap-2 text-sm">
                <input
                  type="checkbox"
                  checked={existingApp}
                  onChange={(event) => {
                    setExistingAppSelected(event.target.checked)
                  }}
                />
                Use an existing Slack app
              </label>
            )}
            {existingApp ? (
              <>
                <FieldDescription>
                  Authorize your Slack app. Reconnect using the same Slack app and workspace; create
                  another Omnara app to connect a different account. Your launch settings are kept.
                </FieldDescription>
                <div className="text-muted-foreground flex flex-col gap-1 break-all text-xs">
                  <p>Configure these URLs in your Slack app before authorizing:</p>
                  <p>
                    OAuth redirect: <code>{apiOrigin}/api/integrations/oauth/callback</code>
                  </p>
                  <p>
                    Events: <code>{apiOrigin}/api/integrations/slack/events</code>
                  </p>
                  <p>
                    Interactivity: <code>{apiOrigin}/api/integrations/slack/actions</code>
                  </p>
                </div>
                {(
                  [
                    ['clientId', 'Client ID'],
                    ['clientSecret', 'Client secret'],
                    ['signingSecret', 'Signing secret'],
                  ] as const
                ).map(([name, label]) => (
                  <form.Field key={name} name={name}>
                    {(field) => (
                      <Field>
                        <FieldLabel htmlFor={name}>{label}</FieldLabel>
                        <Input
                          id={name}
                          required
                          type={name === 'clientId' ? 'text' : 'password'}
                          value={field.state.value}
                          onChange={(event) => {
                            field.handleChange(event.target.value)
                          }}
                        />
                      </Field>
                    )}
                  </form.Field>
                ))}
              </>
            ) : (
              <>
                <form.Field name="appName">
                  {(field) => (
                    <Field>
                      <FieldLabel htmlFor="slack-app-name">App name</FieldLabel>
                      <Input
                        id="slack-app-name"
                        required
                        value={field.state.value}
                        onChange={(event) => {
                          field.handleChange(event.target.value)
                        }}
                      />
                      {Array.from(field.state.value.trim()).length > slackAppNameMaxLength && (
                        <FieldDescription className="text-destructive">
                          App name must be 35 characters or fewer.
                        </FieldDescription>
                      )}
                    </Field>
                  )}
                </form.Field>
                <form.Field name="appConfigurationToken">
                  {(field) => (
                    <Field className="gap-2.5">
                      <FieldLabel htmlFor="slack-app-configuration-token">
                        App configuration token
                      </FieldLabel>
                      <FieldDescription className="text-caption leading-snug">
                        This token lets Omnara create and configure the Slack app automatically.
                        Generate one in{' '}
                        <a
                          href="https://api.slack.com/apps"
                          target="_blank"
                          rel="noreferrer"
                          className="text-foreground underline underline-offset-2"
                        >
                          Slack app settings
                        </a>
                        .
                      </FieldDescription>
                      <Input
                        id="slack-app-configuration-token"
                        required
                        type="password"
                        value={field.state.value}
                        onChange={(event) => {
                          field.handleChange(event.target.value)
                        }}
                      />
                    </Field>
                  )}
                </form.Field>
                <form.Field name="appIcon">
                  {(field) => (
                    <SlackAppIconField value={field.state.value} onChange={field.handleChange} />
                  )}
                </form.Field>
              </>
            )}
            {(error || failure) && (
              <p role="alert" className="text-destructive whitespace-pre-wrap text-sm">
                {error || failure}
              </p>
            )}
            <div className="flex justify-end">
              <form.Subscribe
                selector={(state) =>
                  [
                    existingApp
                      ? existingCredentialsValid(state.values)
                      : slackConnectionFormValid(state.values),
                    state.isSubmitting,
                  ] as const
                }
              >
                {([valid, isSubmitting]) => (
                  <Button type="submit" disabled={isSubmitting || !valid} loading={isSubmitting}>
                    Continue
                  </Button>
                )}
              </form.Subscribe>
            </div>
          </FieldGroup>
        </form>
      )}
    </div>
  )
}

function existingCredentialsValid(value: {
  clientId: string
  clientSecret: string
  signingSecret: string
}) {
  return (
    value.clientId.trim() !== '' &&
    value.clientSecret.trim() !== '' &&
    value.signingSecret.trim() !== ''
  )
}

function SlackAuthorizationPending({
  pending,
  isError,
  onRetry,
  onRestart,
}: {
  pending: IntegrationOAuthSetup
  isError: boolean
  onRetry: () => void
  onRestart: () => void
}) {
  return (
    <div className="flex flex-col gap-4 text-sm">
      <p>
        Authorize this app in Slack, then return here. This page updates when authorization
        completes.
      </p>
      <Button asChild>
        <a href={pending.oauth_url} target="_blank" rel="noopener noreferrer">
          Authorize in Slack
        </a>
      </Button>
      {isError && (
        <p role="alert">
          Could not check authorization.{' '}
          <Button variant="link" onClick={onRetry}>
            Check again
          </Button>
        </p>
      )}
      <p className="text-muted-foreground">
        Authorization expires at {new Date(pending.expires_at).toLocaleTimeString()}.
      </p>
      <Button variant="outline" onClick={onRestart}>
        Start again
      </Button>
    </div>
  )
}

function slackSetupFailure(
  pending: IntegrationOAuthSetup | undefined,
  completed: boolean,
  app: ProjectApp | undefined,
  error: GetProjectAppError | null,
) {
  if (!pending || completed) return ''
  if (error instanceof ApiError && error.status === 404)
    return slackOAuthErrorDescription('app_deleted')
  if (app && app.setup_revision > pending.setup_revision)
    return slackOAuthErrorDescription('app_setup_changed')
  return ''
}
