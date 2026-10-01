import { type Integration, type IntegrationKind } from '@omnara/sdk'
import { useId } from 'react'

import { CheckboxField, Field, FieldDescription, FieldLabel } from '@/components/ui/field'
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
  const id = useId()
  const opened = values.launcher && values.trigger !== 'mention'
  const mentioned = values.launcher && values.trigger !== 'pull_request_opened'
  function setTriggers(nextOpened: boolean, nextMentioned: boolean) {
    onChange(
      nextOpened || nextMentioned
        ? {
            launcher: true,
            trigger:
              nextOpened && nextMentioned ? 'both' : nextOpened ? 'pull_request_opened' : 'mention',
          }
        : { launcher: false },
    )
  }
  return (
    <Field>
      {github ? (
        <div
          role="group"
          aria-labelledby={`${id}-label`}
          aria-describedby={`${id}-hint`}
          className="flex flex-col gap-3"
        >
          <span id={`${id}-label`} className="type-label">
            Launch when
          </span>
          <CheckboxField
            label="PR opened"
            checked={opened}
            disabled={props.disabled}
            onChange={(event) => {
              setTriggers(event.target.checked, mentioned)
            }}
          />
          <CheckboxField
            label="Bot mentioned"
            checked={mentioned}
            disabled={props.disabled}
            onChange={(event) => {
              setTriggers(opened, event.target.checked)
            }}
          />
          <FieldDescription id={`${id}-hint`}>
            Only people with repository write access can launch or steer agents.
          </FieldDescription>
        </div>
      ) : (
        <FieldDescription>
          Choose which profiles people can start by mentioning the bot. Leave the profiles empty to
          use schedules only. Each schedule has its own profile and destination channel. Existing
          conversations continue unchanged.
        </FieldDescription>
      )}
      {(!github || values.launcher) && (
        <>
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
            description={github ? null : undefined}
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
  if (integrationKind === 'github_pr')
    return values.scopeKind === 'repository' ? (
      <FieldDescription>{`Restricted to repository ${values.scopeRef}.`}</FieldDescription>
    ) : null
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
