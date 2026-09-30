import { type Integration, type IntegrationKind } from '@omnara/sdk'

import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import { type IntegrationFormValues } from './integrationFormState'
import { IntegrationProfilePicker } from './IntegrationProfilePicker'

interface LauncherFieldsProps {
  orgId: string
  projectId: string
  integrationKind: IntegrationKind
  integration: Integration
  values: IntegrationFormValues
  onChange: (patch: Partial<IntegrationFormValues>) => void
  disabled: boolean
  profileCount: number | null
}

export function IntegrationLauncherFields(props: LauncherFieldsProps) {
  const { values, onChange, integrationKind } = props
  const github = integrationKind === 'github_pr'
  return (
    <Field>
      {github ? (
        <>
          <label className="flex gap-2 text-sm font-medium">
            <input
              type="checkbox"
              name="launcher"
              checked={values.launcher}
              onChange={(event) => {
                onChange({ launcher: event.target.checked })
              }}
            />
            Launch agents from GitHub events
          </label>
          <FieldDescription>
            Changes apply to future launches. PR-open launches, mentions and comments that direct
            agents require repository write access.
          </FieldDescription>
        </>
      ) : (
        <FieldDescription>
          Choose which profiles people can start by mentioning the bot. Leave the profiles empty to
          use schedules only. Each schedule has its own profile and destination channel. Existing
          conversations continue unchanged.
        </FieldDescription>
      )}
      {(!github || values.launcher) && (
        <>
          {github && (
            <Field>
              <FieldLabel htmlFor="integration-trigger">Launch when</FieldLabel>
              <Select
                value={values.trigger}
                disabled={props.disabled}
                onValueChange={(trigger) => {
                  onChange({ trigger })
                }}
              >
                <SelectTrigger id="integration-trigger" className="w-full">
                  <SelectValue>
                    {values.trigger === 'mention'
                      ? 'The bot is mentioned on a pull request'
                      : values.trigger === 'both'
                        ? 'PR opened or bot mentioned'
                        : 'A pull request is opened'}
                  </SelectValue>
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="both" disabled={props.disabled}>
                    PR opened or bot mentioned
                  </SelectItem>
                  <SelectItem value="pull_request_opened" disabled={props.disabled}>
                    A pull request is opened
                  </SelectItem>
                  <SelectItem value="mention" disabled={props.disabled}>
                    The bot is mentioned on a pull request
                  </SelectItem>
                </SelectContent>
              </Select>
              <FieldDescription>
                Each pull request gets one agent; later messages go to that agent.
              </FieldDescription>
            </Field>
          )}
          <IntegrationLauncherScopeFields {...props} />
          <IntegrationProfilePicker
            orgId={props.orgId}
            projectId={props.projectId}
            value={values.profileIds}
            onChange={(profileIds) => {
              onChange({ profileIds })
            }}
            single={github}
            label={github ? 'Agent profile' : 'Profiles for mentions'}
            description={
              github
                ? 'Choose a profile whose tools and secrets are appropriate for reviewing untrusted PR content.'
                : undefined
            }
            disabled={props.disabled}
            profileCount={props.profileCount}
          />
        </>
      )}
    </Field>
  )
}

function IntegrationLauncherScopeFields({
  integrationKind,
  integration,
  values,
  onChange,
  disabled,
}: LauncherFieldsProps) {
  if (integrationKind === 'discord_thread')
    return (
      <FieldDescription>
        Mentions work in every server where this bot is installed and has access. Manage server and
        channel access in Discord. People in those conversations can launch these profiles and
        answer agent questions and approvals without Omnara project membership.
      </FieldDescription>
    )
  const github = integrationKind === 'github_pr'
  if (github)
    return (
      <FieldDescription>
        {values.scopeKind === 'repository'
          ? `Restricted to repository ${values.scopeRef}. This saved restriction is kept when editing profiles or triggers.`
          : 'Applies to repositories this integration can access. Manage repository access in GitHub.'}
      </FieldDescription>
    )
  const workspace = integration.provider_tenant_id ?? ''
  return (
    <>
      <Field>
        <FieldLabel htmlFor="integration-launch-scope">Respond to mentions in</FieldLabel>
        <Select
          value={values.scopeKind || 'workspace'}
          disabled={disabled}
          onValueChange={(scopeKind) => {
            onChange({ scopeKind: scopeKind === 'workspace' ? '' : scopeKind, scopeRef: '' })
          }}
        >
          <SelectTrigger id="integration-launch-scope" className="w-full">
            <SelectValue>{values.scopeKind ? 'One channel' : 'Connected workspace'}</SelectValue>
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="workspace" disabled={disabled}>
              Connected workspace
            </SelectItem>
            <SelectItem value="channel" disabled={disabled}>
              One channel
            </SelectItem>
          </SelectContent>
        </Select>
      </Field>
      {values.scopeKind ? (
        <Field>
          <FieldLabel htmlFor="launcher-scope">Channel ID</FieldLabel>
          <Input
            id="launcher-scope"
            name="scope"
            value={values.scopeRef}
            onChange={(event) => {
              onChange({ scopeRef: event.target.value })
            }}
          />
        </Field>
      ) : (
        <FieldDescription>{`Workspace: ${workspace || 'Connect this Slack app'}. The bot must have access to the conversation.`}</FieldDescription>
      )}
    </>
  )
}
