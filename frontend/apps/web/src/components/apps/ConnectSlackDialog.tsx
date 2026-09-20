import {
  useCreateProjectAppOAuthSetup,
  useCreateProjectAppSlackSetup,
  useOmnaraClient,
  useProjectAppOAuthCompletion,
} from '@omnara/react'
import { ApiError, type IntegrationOAuthSetup, type ProjectApp } from '@omnara/sdk'
import { listProjectAppsQueryKey } from '@omnara/sdk/tanstack'
import { useForm } from '@tanstack/react-form'
import { useQueryClient } from '@tanstack/react-query'
import { useEffect, useRef, useState } from 'react'

import { Upload, X } from '@/components/icons'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { errorMessage } from '@/lib/submit-status'

import {
  fileSizeLabel,
  noAppIcon,
  readFileBase64,
  slackAppNameMaxLength,
  slackConnectionFormValid,
  validateAppIcon,
} from './ConnectSlackDialogState'
import { slackOAuthErrorDescription } from './SlackOAuthOutcomeDialogState'

export function ConnectSlackDialog({
  open,
  onOpenChange,
  orgId,
  projectId,
  app,
  onConnected,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
  projectId: string
  app: ProjectApp
  onConnected?: () => void
}) {
  const createSlackSetup = useCreateProjectAppSlackSetup(orgId, projectId, app.id)
  const createOAuthSetup = useCreateProjectAppOAuthSetup(orgId, projectId, app.id)
  const client = useOmnaraClient()
  const apiOrigin = new URL(client.getConfig().baseUrl ?? '/api/v1', window.location.origin).origin
  const reconnect = Boolean(app.provider_tenant_id)
  const [existingApp, setExistingApp] = useState(reconnect)
  const [pending, setPending] = useState<IntegrationOAuthSetup>()
  const [error, setError] = useState('')
  const completion = useProjectAppOAuthCompletion(
    orgId,
    projectId,
    app.id,
    open ? pending : undefined,
  )
  const completed =
    completion.data?.state === 'active' && completion.data.last_oauth_flow_id === pending?.flow_id
  const failure =
    !pending || completed
      ? ''
      : completion.error instanceof ApiError && completion.error.status === 404
        ? slackOAuthErrorDescription('app_deleted')
        : completion.data && completion.data.setup_revision > pending.setup_revision
          ? slackOAuthErrorDescription('app_setup_changed')
          : ''
  const cache = useQueryClient()
  useEffect(() => {
    if (!open || !pending) return
    if (completed) {
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
          onConnected?.()
          onOpenChange(false)
        })
      return () => {
        canceled = true
      }
    }
    return undefined
  }, [open, pending, completed, cache, client, orgId, projectId, onConnected, onOpenChange])
  const appIconInputRef = useRef<HTMLInputElement>(null)
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
        const icon =
          value.appIcon.kind === 'file'
            ? {
                filename: value.appIcon.file.name,
                data_base64: await readFileBase64(value.appIcon.file),
              }
            : undefined
        const setup = existingApp
          ? await createOAuthSetup.mutateAsync({
              client_id: value.clientId.trim(),
              client_secret: value.clientSecret.trim(),
              signing_secret: value.signingSecret.trim(),
              return_to: window.location.pathname,
            })
          : await createSlackSetup.mutateAsync({
              app_name: value.appName.trim(),
              app_configuration_token: value.appConfigurationToken.trim(),
              icon,
              return_to: window.location.pathname,
            })
        if (setup.app_id !== app.id)
          throw new Error('Authorization returned a different app. Please try again.')
        setPending(setup)
      } catch (err) {
        setError(errorMessage(err, 'Could not start integration setup'))
      }
    },
  })

  function resetIconInput() {
    if (appIconInputRef.current) appIconInputRef.current.value = ''
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Connect Slack</DialogTitle>
          <DialogDescription>
            Connect your Slack bot to {app.name}. After authorization, choose the profiles and
            behavior for your Omnara app.
          </DialogDescription>
        </DialogHeader>
        {pending && !failure ? (
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
            {completion.isError && (
              <p role="alert">
                Could not check authorization.{' '}
                <Button variant="link" onClick={() => void completion.refetch()}>
                  Check again
                </Button>
              </p>
            )}
            <p className="text-muted-foreground">
              Authorization expires at {new Date(pending.expires_at).toLocaleTimeString()}.
            </p>
            <Button
              variant="outline"
              onClick={() => {
                setPending(undefined)
              }}
            >
              Start again
            </Button>
          </div>
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
                      setExistingApp(event.target.checked)
                    }}
                  />
                  Use an existing Slack app
                </label>
              )}
              {existingApp ? (
                <>
                  <FieldDescription>
                    Authorize your Slack app. Reconnect using the same Slack app and workspace;
                    create another Omnara app to connect a different account. Your launch settings
                    are kept.
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
                      <Field className="gap-2.5">
                        <FieldLabel htmlFor="slack-app-icon">Slack app icon (optional)</FieldLabel>
                        <Input
                          id="slack-app-icon"
                          ref={appIconInputRef}
                          type="file"
                          accept="image/png,image/jpeg"
                          className="hidden"
                          onChange={(event) => {
                            const next = validateAppIcon(event.target.files?.[0] ?? null)
                            field.handleChange(next)
                            if (next.kind !== 'file') resetIconInput()
                          }}
                        />
                        <div className="border-input bg-muted/20 flex items-center justify-between gap-3 rounded-md border border-dashed px-3 py-3">
                          <div className="min-w-0 text-sm">
                            {field.state.value.kind === 'file' ? (
                              <span className="flex min-w-0 items-center gap-2">
                                <span className="text-foreground truncate">
                                  {field.state.value.file.name}
                                </span>
                                <span className="text-muted-foreground shrink-0">
                                  {fileSizeLabel(field.state.value.file.size)}
                                </span>
                              </span>
                            ) : (
                              <span className="text-muted-foreground">No icon selected</span>
                            )}
                          </div>
                          <div className="flex shrink-0 items-center gap-2">
                            {field.state.value.kind === 'file' && (
                              <Button
                                type="button"
                                size="sm"
                                variant="ghost"
                                onClick={() => {
                                  field.handleChange(noAppIcon)
                                  resetIconInput()
                                }}
                              >
                                <X />
                                Remove
                              </Button>
                            )}
                            <Button
                              type="button"
                              size="sm"
                              variant="outline"
                              onClick={() => appIconInputRef.current?.click()}
                            >
                              <Upload />
                              Choose icon
                            </Button>
                          </div>
                        </div>
                        {field.state.value.kind === 'error' && (
                          <FieldDescription className="text-destructive">
                            {field.state.value.message}
                          </FieldDescription>
                        )}
                      </Field>
                    )}
                  </form.Field>
                </>
              )}
              {(error || failure) && (
                <p role="alert" className="text-destructive whitespace-pre-wrap text-sm">
                  {error || failure}
                </p>
              )}
              <DialogFooter>
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
              </DialogFooter>
            </FieldGroup>
          </form>
        )}
      </DialogContent>
    </Dialog>
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
