import {
  useCreateProjectAppOAuthSetup,
  useCreateProjectAppSlackSetup,
  useOmnaraClient,
} from '@omnara/react'
import type { IntegrationOAuthSetup, ProjectApp } from '@omnara/sdk'
import { createFormHook, createFormHookContexts, formOptions } from '@tanstack/react-form'
import { type ReactNode, useState } from 'react'

import { Button } from '@/components/ui/button'
import {
  CheckboxField,
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
} from '@/components/ui/field'
import { Input } from '@/components/ui/input'

import {
  noAppIcon,
  slackAppIconPayload,
  slackAppNameMaxLength,
  slackConnectionFormValid,
} from './ConnectSlackFormState'
import { projectAppFormError } from './projectAppFormState'
import { ProjectAppNameField } from './ProjectAppNameField'
import { ProjectAppSetupGroup } from './ProjectAppSetupGroup'
import { SlackAppIconField } from './SlackAppIconField'
import { useProjectAppDraft } from './useProjectAppDraft'
import { useSlackAuthorization } from './useSlackAuthorization'

const { fieldContext, formContext } = createFormHookContexts()
const { useAppForm, withForm } = createFormHook({
  fieldComponents: {},
  formComponents: {},
  fieldContext,
  formContext,
})

const slackSetupForm = formOptions({
  defaultValues: {
    clientId: '',
    clientSecret: '',
    signingSecret: '',
    appName: 'Omnara',
    appConfigurationToken: '',
    appIcon: noAppIcon,
  },
})

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
  const reconnect = Boolean(app?.provider_tenant_id)
  const [existingAppSelected, setExistingAppSelected] = useState(false)
  const existingApp = reconnect || existingAppSelected
  const [error, setError] = useState('')
  const authorization = useSlackAuthorization(orgId, projectId, onConnected)
  const form = useAppForm({
    ...slackSetupForm,
    onSubmit: async ({ value }) => {
      if (!slackSetupValid(existingApp, value)) return
      setError('')
      try {
        const icon = existingApp ? undefined : await slackAppIconPayload(value.appIcon)
        const draft = await ensureApp()
        const returnTo = `/projects/${projectId}/apps/${draft.id}`
        const setup = existingApp
          ? await createOAuthSetup.mutateAsync({
              appID: draft.id,
              client_id: value.clientId.trim(),
              client_secret: value.clientSecret.trim(),
              signing_secret: value.signingSecret.trim(),
              return_to: returnTo,
            })
          : await createSlackSetup.mutateAsync({
              appID: draft.id,
              app_name: value.appName.trim(),
              app_configuration_token: value.appConfigurationToken.trim(),
              icon,
              return_to: returnTo,
            })
        if (setup.app_id !== draft.id) {
          setError('Authorization returned a different app. Please try again.')
        } else {
          authorization.start(setup)
        }
      } catch (err) {
        setError(projectAppFormError(err, 'Could not start integration setup'))
      }
    },
  })
  const methodToggle = reconnect ? null : (
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
  )

  return (
    <div>
      {authorization.pending && !authorization.failure ? (
        <SlackAuthorizationPending
          pending={authorization.pending}
          isError={authorization.checkFailed}
          onRetry={authorization.recheck}
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
              {existingApp ? (
                <ExistingSlackAppFields form={form} reconnect={reconnect}>
                  {methodToggle}
                </ExistingSlackAppFields>
              ) : (
                <NewSlackAppFields form={form}>{methodToggle}</NewSlackAppFields>
              )}
            </div>
            {(error || authorization.failure) && (
              <p role="alert" className="text-destructive whitespace-pre-wrap text-sm">
                {error || authorization.failure}
              </p>
            )}
            <form.Subscribe
              selector={(state) =>
                [slackSetupValid(existingApp, state.values), state.isSubmitting] as const
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

const NewSlackAppFields = withForm({
  ...slackSetupForm,
  render: function Render({ form, children }) {
    return (
      <>
        <ProjectAppSetupGroup
          title="Slack app"
          hint="Use an existing Slack app, or let Omnara create one for you."
        >
          {children}
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
              <SlackAppIconField value={field.state.value} onChange={field.handleChange} />
            )}
          </form.Field>
        </ProjectAppSetupGroup>
        <ProjectAppSetupGroup
          title="Credentials"
          hint="An app configuration token lets Omnara create and configure your Slack app."
        >
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
                  , click Generate Token, and select a workspace. Copy only the Access Token and
                  paste it here.
                </FieldDescription>
              </Field>
            )}
          </form.Field>
        </ProjectAppSetupGroup>
      </>
    )
  },
})

const ExistingSlackAppFields = withForm({
  ...slackSetupForm,
  props: { reconnect: false },
  render: function Render({ form, reconnect, children }) {
    return (
      <>
        <ProjectAppSetupGroup
          title="Slack app"
          hint={
            reconnect
              ? 'Find the Client ID in Basic Information in your Slack app settings.'
              : 'Use an existing Slack app, or let Omnara create one for you.'
          }
        >
          {children}
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
        </ProjectAppSetupGroup>
        <ProjectAppSetupGroup
          title="Credentials"
          hint="Find these in Basic Information in your Slack app settings."
        >
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
        </ProjectAppSetupGroup>
        <SlackAppUrls />
      </>
    )
  },
})

function slackSetupValid(existingApp: boolean, values: typeof slackSetupForm.defaultValues) {
  if (!existingApp) return slackConnectionFormValid(values)
  return [values.clientId, values.clientSecret, values.signingSecret].every(
    (value) => value.trim() !== '',
  )
}

function SlackAppUrls() {
  const client = useOmnaraClient()
  const apiOrigin = new URL(client.getConfig().baseUrl ?? '/api/v1', window.location.origin).origin
  return (
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
