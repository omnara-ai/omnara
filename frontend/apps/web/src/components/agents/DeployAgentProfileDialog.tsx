import { useCreateSlackSetup } from '@omnara/react'
import { type AgentProfile } from '@omnara/sdk'
import { useForm } from '@tanstack/react-form'
import { Upload, X } from 'lucide-react'
import { useRef, useState } from 'react'

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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { errorMessage } from '@/lib/submit-status'

import {
  defaultAppName,
  deployFormValid,
  fileSizeLabel,
  noAppIcon,
  readFileBase64,
  slackAppNameMaxLength,
  validateAppIcon,
} from './DeployAgentProfileDialogState'

export function DeployAgentProfileDialog({
  open,
  onOpenChange,
  orgId,
  projectId,
  profile,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
  projectId: string
  profile: AgentProfile
}) {
  const createSlackSetup = useCreateSlackSetup(orgId, projectId, profile.id)
  const appIconInputRef = useRef<HTMLInputElement>(null)
  const [error, setError] = useState('')
  const form = useForm({
    defaultValues: {
      provider: 'slack',
      appName: defaultAppName(profile.name),
      appConfigurationToken: '',
      appIcon: noAppIcon,
    },
    onSubmit: async ({ value }) => {
      if (!deployFormValid(value)) return
      setError('')
      try {
        const icon =
          value.appIcon.kind === 'file'
            ? {
                filename: value.appIcon.file.name,
                data_base64: await readFileBase64(value.appIcon.file),
              }
            : undefined
        const setup = await createSlackSetup.mutateAsync({
          app_name: value.appName.trim(),
          app_configuration_token: value.appConfigurationToken.trim(),
          icon,
          return_to: window.location.pathname,
        })
        window.location.assign(setup.oauth_url)
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
          <DialogTitle>Deploy agent profile</DialogTitle>
          <DialogDescription>Make {profile.name} available in an external app.</DialogDescription>
        </DialogHeader>
        <form
          onSubmit={(event) => {
            event.preventDefault()
            void form.handleSubmit()
          }}
        >
          <FieldGroup>
            <form.Field name="provider">
              {(field) => (
                <Field>
                  <FieldLabel htmlFor="deploy-provider">Destination</FieldLabel>
                  <Select value={field.state.value} onValueChange={field.handleChange}>
                    <SelectTrigger id="deploy-provider" className="w-full">
                      <SelectValue>Slack</SelectValue>
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="slack">Slack</SelectItem>
                    </SelectContent>
                  </Select>
                </Field>
              )}
            </form.Field>
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
                  <FieldDescription className="text-[13px] leading-snug">
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
            {error && <p className="text-destructive whitespace-pre-wrap text-sm">{error}</p>}
            <DialogFooter>
              <form.Subscribe
                selector={(state) => [deployFormValid(state.values), state.isSubmitting] as const}
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
      </DialogContent>
    </Dialog>
  )
}
