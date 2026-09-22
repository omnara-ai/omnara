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
import { type ReactNode, useEffect, useState } from 'react'

import { Button } from '@/components/ui/button'
import {
  CheckboxField,
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
} from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { errorMessage } from '@/lib/submit-status'

import {
  noAppIcon,
  slackAppIconPayload,
  slackAppNameMaxLength,
  slackConnectionFormValid,
} from './ConnectSlackFormState'
import { ProjectAppNameField } from './ProjectAppNameField'
import { ProjectAppSetupGroup } from './ProjectAppSetupGroup'
import { SlackAppIconField } from './SlackAppIconField'
import { slackOAuthErrorDescription } from './slackOAuthErrors'
import { useProjectAppDraft } from './useProjectAppDraft'

interface SlackConnectionProps {
  orgId: string
  projectId: string
  app?: ProjectApp
  onConnected?: (app: ProjectApp) => void
  onCancel?: () => void
  footerAction?: ReactNode
}

export function ConnectSlackForm({
  orgId,
  projectId,
  app: existing,
  onConnected,
  onCancel,
  footerAction,
}: SlackConnectionProps) {
  const { app, name, setName, ensureApp } = useProjectAppDraft(
    orgId,
    projectId,
    'slack_thread',
    existing,
  )
  const createOAuthSetup = useCreateProjectAppOAuthSetup(orgId, projectId)
  const createSlackSetup = useCreateProjectAppSlackSetup(orgId, projectId)
  const client = useOmnaraClient()
  const apiOrigin = new URL(client.getConfig().baseUrl ?? '/api/v1', window.location.origin).origin
  const reconnect = Boolean(app?.provider_tenant_id)
  const [existingAppSelected, setExistingAppSelected] = useState(false)
  const existingApp = reconnect || existingAppSelected
  const [pending, setPending] = useState<IntegrationOAuthSetup>()
  const [error, setError] = useState('')
  const completion = useProjectAppOAuthCompletion(orgId, projectId, app?.id ?? '', pending)
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
        const icon = existingApp ? undefined : await slackAppIconPayload(value.appIcon)
        const draft = await ensureApp()
        const returnTo = `/projects/${projectId}/apps/${draft.id}`
        let setup: IntegrationOAuthSetup
        if (existingApp) {
          setup = await createOAuthSetup.mutateAsync({
            appID: draft.id,
            client_id: value.clientId.trim(),
            client_secret: value.clientSecret.trim(),
            signing_secret: value.signingSecret.trim(),
            return_to: returnTo,
          })
        } else {
          setup = await createSlackSetup.mutateAsync({
            appID: draft.id,
            app_name: value.appName.trim(),
            app_configuration_token: value.appConfigurationToken.trim(),
            icon,
            return_to: returnTo,
          })
        }
        if (setup.app_id !== draft.id) {
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
        />
      ) : (
        <form
          onSubmit={(event) => {
            event.preventDefault()
            void form.handleSubmit()
          }}
        >
          <FieldGroup className="gap-8 text-sm">
            <div className="flex flex-col gap-2">
              <h2 className="font-medium">{reconnect ? 'Reconnect Slack' : 'Connect Slack'}</h2>
              <p className="text-muted-foreground">
                {reconnect
                  ? 'Reconnect the same Slack app and workspace. Your launch settings are kept.'
                  : 'Connect your Slack app here, then authorize it in Slack and choose which agents people can start.'}
              </p>
            </div>
            <div className="flex flex-col gap-8">
              {!existing && (
                <form.Subscribe selector={(state) => state.isSubmitting}>
                  {(isSubmitting) => (
                    <fieldset disabled={isSubmitting}>
                      <ProjectAppSetupGroup
                        title="Name in Omnara"
                        hint="A permanent name for this app in your project."
                      >
                        <ProjectAppNameField name={name} onChange={setName} saved={app} />
                      </ProjectAppSetupGroup>
                    </fieldset>
                  )}
                </form.Subscribe>
              )}
              <ProjectAppSetupGroup
                title="Slack app"
                hint={
                  reconnect
                    ? 'Find the Client ID in Basic Information in your Slack app settings.'
                    : 'Use an existing Slack app, or let Omnara create one for you.'
                }
              >
                {!reconnect && (
                  <CheckboxField
                    label="Use an existing Slack app"
                    checked={existingApp}
                    onChange={(event) => {
                      if (form.state.values.appIcon.kind === 'checking') {
                        form.setFieldValue('appIcon', noAppIcon)
                      }
                      setExistingAppSelected(event.target.checked)
                    }}
                  />
                )}
                {existingApp ? (
                  <form.Field name="clientId">
                    {(field) => (
                      <Field>
                        <FieldLabel htmlFor="clientId">Client ID</FieldLabel>
                        <Input
                          id="clientId"
                          required
                          value={field.state.value}
                          onChange={(event) => {
                            field.handleChange(event.target.value)
                          }}
                        />
                      </Field>
                    )}
                  </form.Field>
                ) : (
                  <>
                    <form.Field name="appName">
                      {(field) => (
                        <Field>
                          <FieldLabel htmlFor="slack-app-name">Name in Slack</FieldLabel>
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
                    <form.Field name="appIcon">
                      {(field) => (
                        <SlackAppIconField
                          value={field.state.value}
                          onChange={field.handleChange}
                        />
                      )}
                    </form.Field>
                  </>
                )}
              </ProjectAppSetupGroup>
              <ProjectAppSetupGroup
                title="Credentials"
                hint={
                  existingApp
                    ? 'Find these in Basic Information in your Slack app settings.'
                    : 'An app configuration token lets Omnara create and configure your Slack app.'
                }
              >
                {existingApp ? (
                  <div className="grid gap-4 sm:grid-cols-2">
                    {(
                      [
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
                              type="password"
                              value={field.state.value}
                              onChange={(event) => {
                                field.handleChange(event.target.value)
                              }}
                            />
                          </Field>
                        )}
                      </form.Field>
                    ))}
                  </div>
                ) : (
                  <form.Field name="appConfigurationToken">
                    {(field) => (
                      <Field>
                        <FieldLabel htmlFor="slack-app-configuration-token">
                          App configuration token
                        </FieldLabel>
                        <Input
                          id="slack-app-configuration-token"
                          required
                          type="password"
                          value={field.state.value}
                          onChange={(event) => {
                            field.handleChange(event.target.value)
                          }}
                        />
                        <FieldDescription>
                          Open{' '}
                          <a
                            href="https://api.slack.com/apps"
                            target="_blank"
                            rel="noreferrer"
                            className="text-foreground underline underline-offset-2"
                          >
                            Slack app settings
                          </a>
                          , click Generate Token, and select a workspace. Copy only the Access Token
                          and paste it here.
                        </FieldDescription>
                      </Field>
                    )}
                  </form.Field>
                )}
              </ProjectAppSetupGroup>
              {existingApp && (
                <ProjectAppSetupGroup
                  title="In Slack"
                  hint="Configure these URLs in your Slack app before authorizing."
                >
                  <div className="text-muted-foreground flex flex-col gap-2 break-all text-sm">
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
                </ProjectAppSetupGroup>
              )}
            </div>
            {(error || failure) && (
              <p role="alert" className="text-destructive whitespace-pre-wrap text-sm">
                {error || failure}
              </p>
            )}
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
                <fieldset
                  disabled={isSubmitting}
                  className="flex flex-wrap items-start justify-end gap-2"
                >
                  {footerAction}
                  {onCancel && (
                    <Button
                      type="button"
                      variant="outline"
                      disabled={isSubmitting}
                      onClick={onCancel}
                    >
                      Cancel
                    </Button>
                  )}
                  <Button type="submit" disabled={isSubmitting || !valid} loading={isSubmitting}>
                    {!existing ? 'Create and connect' : reconnect ? 'Reconnect app' : 'Connect app'}
                  </Button>
                </fieldset>
              )}
            </form.Subscribe>
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
}: {
  pending: IntegrationOAuthSetup
  isError: boolean
  onRetry: () => void
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
