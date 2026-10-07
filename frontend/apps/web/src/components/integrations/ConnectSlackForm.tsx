import { useCreateIntegrationOAuthSetup, useCreateIntegrationSlackSetup } from '@omnara/react'
import type { Integration, IntegrationOAuthSetup } from '@omnara/sdk'
import { createFormHook, createFormHookContexts, formOptions } from '@tanstack/react-form'
import { type ReactNode, useState } from 'react'

import { Button } from '@/components/ui/button'
import {
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
  FieldSeparator,
} from '@/components/ui/field'
import { Input } from '@/components/ui/input'

import {
  noAppIcon,
  slackAppIconPayload,
  slackAppNameMaxLength,
  slackConnectionFormValid,
} from './ConnectSlackFormState'
import { integrationFormError } from './integrationFormState'
import { IntegrationNameField } from './IntegrationNameField'
import { IntegrationSetupGroup } from './IntegrationSetupGroup'
import { SlackAppIconField } from './SlackAppIconField'
import { useIntegrationDraft } from './useIntegrationDraft'
import { useIntegrationSetupURLs } from './useIntegrationSetupURLs'
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
    appName: '',
    appConfigurationToken: '',
    appIcon: noAppIcon,
  },
})

interface SlackConnectionProps {
  orgId: string
  projectId: string
  integration?: Integration
  defaultExistingApp?: boolean
  onCancel?: () => void
  footerAction?: ReactNode
}

export function ConnectSlackForm({
  orgId,
  projectId,
  integration: existing,
  defaultExistingApp = false,
  onCancel,
  footerAction,
}: SlackConnectionProps) {
  const { integration, name, setName, ensureIntegration } = useIntegrationDraft(
    orgId,
    projectId,
    'slack_thread',
    existing,
  )
  const createOAuthSetup = useCreateIntegrationOAuthSetup(orgId, projectId)
  const createSlackSetup = useCreateIntegrationSlackSetup(orgId, projectId)
  const reconnect = Boolean(integration?.provider_tenant_id)
  const [existingAppSelected, setExistingAppSelected] = useState(defaultExistingApp)
  const existingApp = reconnect || existingAppSelected
  const [error, setError] = useState('')
  const authorization = useSlackAuthorization()
  const form = useAppForm({
    ...slackSetupForm,
    onSubmit: async ({ value }) => {
      if (!slackSetupValid(existingApp, value)) return
      setError('')
      await startSetup(value).catch((cause: unknown) => {
        setError(integrationFormError(cause, 'Could not start integration setup'))
      })
    },
  })
  async function startSetup(value: typeof slackSetupForm.defaultValues) {
    const icon = existingApp ? undefined : await slackAppIconPayload(value.appIcon)
    const draft = await ensureIntegration()
    const returnTo = `/projects/${projectId}/integrations/${draft.id}`
    const setup = existingApp
      ? await createOAuthSetup.mutateAsync({
          integrationID: draft.id,
          client_id: value.clientId.trim(),
          client_secret: value.clientSecret.trim(),
          signing_secret: value.signingSecret.trim(),
          return_to: returnTo,
        })
      : await createSlackSetup.mutateAsync({
          integrationID: draft.id,
          app_name: value.appName.trim(),
          app_configuration_token: value.appConfigurationToken.trim(),
          icon,
          return_to: returnTo,
        })
    if (setup.integration_id !== draft.id) {
      setError('Authorization returned a different integration. Please try again.')
    } else {
      authorization.start(setup)
    }
  }

  return (
    <div>
      {authorization.pending ? (
        <SlackAuthorizationPending
          pending={authorization.pending}
          expired={authorization.expired}
          onRestart={() => {
            setExistingAppSelected(true)
            authorization.start()
          }}
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
                  : existingApp
                    ? 'Enter your Slack app’s credentials, then authorize it in Slack.'
                    : 'Enter the details below to create your Slack app, then authorize it in Slack.'}
              </p>
            </div>
            <div className="flex flex-col gap-8">
              {!existing && (
                <form.Subscribe selector={(state) => state.isSubmitting}>
                  {(isSubmitting) => (
                    <fieldset disabled={isSubmitting}>
                      <IntegrationNameField name={name} onChange={setName} saved={integration} />
                    </fieldset>
                  )}
                </form.Subscribe>
              )}
              <SlackSetupFields form={form} existingApp={existingApp} />
            </div>
            {error && (
              <p role="alert" className="text-destructive whitespace-pre-wrap text-sm">
                {error}
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
                    {!existing
                      ? 'Create and connect'
                      : reconnect
                        ? 'Reconnect integration'
                        : 'Connect integration'}
                  </Button>
                </fieldset>
              )}
            </form.Subscribe>
            {!reconnect && (
              <form.Subscribe selector={(state) => state.isSubmitting}>
                {(isSubmitting) => (
                  <SlackSetupAlternatives
                    existingApp={existingApp}
                    disabled={isSubmitting}
                    onSwitchSetup={() => {
                      if (form.state.values.appIcon.kind === 'checking') {
                        form.setFieldValue('appIcon', noAppIcon)
                      }
                      setExistingAppSelected(!existingApp)
                    }}
                  />
                )}
              </form.Subscribe>
            )}
          </FieldGroup>
        </form>
      )}
    </div>
  )
}

function SlackSetupAlternatives({
  existingApp,
  disabled,
  onSwitchSetup,
}: {
  existingApp: boolean
  disabled: boolean
  onSwitchSetup: () => void
}) {
  return (
    <>
      <FieldSeparator>OR</FieldSeparator>
      <div className="flex flex-col gap-6">
        <div className="flex flex-col gap-2">
          <h2 className="font-medium">
            {existingApp ? 'Create a new Slack app' : 'Use an existing Slack app'}
          </h2>
          <p className="text-muted-foreground">
            {existingApp
              ? 'Let Omnara create and configure an app for you.'
              : 'Copy your app’s credentials from Slack.'}
          </p>
        </div>
        <Button
          type="button"
          variant="outline"
          className="self-start"
          disabled={disabled}
          onClick={onSwitchSetup}
        >
          {existingApp ? 'Create a new Slack app' : 'Enter app details'}
        </Button>
      </div>
    </>
  )
}

const SlackSetupFields = withForm({
  ...slackSetupForm,
  props: { existingApp: false },
  render: function Render({ form, existingApp }) {
    return (
      <>
        <IntegrationSetupGroup
          title="Slack app"
          hint={
            existingApp
              ? 'Find the Client ID in Basic Information in your Slack app settings.'
              : 'Omnara will create and configure a Slack app for you.'
          }
        >
          {existingApp ? (
            <form.Field name="clientId">
              {(field) => (
                <Field>
                  <FieldLabel htmlFor="clientId">Client ID</FieldLabel>
                  <Input
                    id="clientId"
                    required
                    autoComplete="off"
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
                      placeholder="Engineering helper"
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
            </>
          )}
        </IntegrationSetupGroup>
        <IntegrationSetupGroup
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
                        autoComplete="new-password"
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
                    autoComplete="new-password"
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
          )}
        </IntegrationSetupGroup>
        {existingApp && <SlackAppUrls />}
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
  const { publicURL, unavailable } = useIntegrationSetupURLs()
  return (
    <IntegrationSetupGroup
      title="In Slack"
      hint="Configure these URLs and bot events in your Slack app before authorizing."
    >
      {publicURL ? (
        <div className="text-muted-foreground flex flex-col gap-2 break-all text-sm">
          <p>
            OAuth redirect: <code>{publicURL}/api/integrations/oauth/callback</code>
          </p>
          <p>
            Events: <code>{publicURL}/api/integrations/slack/events</code>
          </p>
          <p>
            Interactivity: <code>{publicURL}/api/integrations/slack/actions</code>
          </p>
        </div>
      ) : (
        <p>
          {unavailable
            ? 'Public URL unavailable. Check the server configuration.'
            : 'Loading setup URLs…'}
        </p>
      )}
      <div className="flex flex-col gap-2">
        <p>Under Event Subscriptions, enable events and add these under Subscribe to bot events:</p>
        <ul className="grid gap-1 sm:grid-cols-2">
          {[
            'app_mention',
            'app_uninstalled',
            'channel_rename',
            'group_rename',
            'message.channels',
            'message.groups',
            'message.im',
            'message.mpim',
            'tokens_revoked',
            'user_profile_changed',
          ].map((event) => (
            <li key={event}>
              <code>{event}</code>
            </li>
          ))}
        </ul>
        <p className="text-muted-foreground">Save your changes before authorizing.</p>
      </div>
    </IntegrationSetupGroup>
  )
}

function SlackAuthorizationPending({
  pending,
  expired,
  onRestart,
}: {
  pending: IntegrationOAuthSetup
  expired: boolean
  onRestart: () => void
}) {
  return (
    <div className="flex flex-col gap-4 text-sm">
      {expired ? (
        <>
          <p role="alert">
            Authorization expired. Use your existing Slack app’s credentials to start authorization
            again.
          </p>
          <Button onClick={onRestart}>Start authorization again</Button>
        </>
      ) : (
        <>
          <p>Authorize your app in Slack. You’ll return here automatically to finish setup.</p>
          <Button asChild>
            <a href={pending.oauth_url}>Authorize in Slack</a>
          </Button>
          <p className="text-muted-foreground">
            Authorization expires at {new Date(pending.expires_at).toLocaleTimeString()}.
          </p>
        </>
      )}
    </div>
  )
}
