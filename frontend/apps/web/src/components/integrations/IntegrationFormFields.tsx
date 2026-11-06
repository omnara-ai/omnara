import { type Integration, type IntegrationKind } from '@omnara/sdk'
import { useId } from 'react'

import { CopyButton } from '@/components/overview/CodeBlock'
import { CheckboxField, Field, FieldDescription } from '@/components/ui/field'

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
}

export function IntegrationLauncherFields({
  orgId,
  projectId,
  integrationKind,
  integration,
  values,
  onChange,
  disabled,
}: LauncherFieldsProps) {
  const github = integrationKind === 'github_pr'
  return (
    <Field>
      {github ? (
        <GitHubLauncherFields
          integration={integration}
          values={values}
          onChange={onChange}
          disabled={disabled}
        />
      ) : (
        <FieldDescription>
          A mention starts the selected profile. If you select several, people choose one in{' '}
          {integrationKind === 'slack_thread' ? 'Slack' : 'Discord'}.
        </FieldDescription>
      )}
      <IntegrationProfilePicker
        orgId={orgId}
        projectId={projectId}
        value={values.profileIds}
        onChange={(profileIds) => {
          onChange({ profileIds })
        }}
        single={github}
        label={github ? 'Agent profile' : 'Profiles for mentions'}
        disabled={disabled || (github && !values.launcher)}
      />
      {!github && values.profileIds.length === 0 && (
        <FieldDescription>Choose a profile to enable mentions.</FieldDescription>
      )}
      {!github && (
        <FieldDescription>
          {integrationKind === 'slack_thread'
            ? 'People in your Slack workspace'
            : 'People who can reach the bot'}{' '}
          can launch these profiles and answer questions and approvals without an Omnara account.
        </FieldDescription>
      )}
    </Field>
  )
}

export function IntegrationAccessFields({
  integrationKind,
  values,
  onChange,
  disabled,
}: Pick<LauncherFieldsProps, 'integrationKind' | 'values' | 'onChange' | 'disabled'>) {
  const id = useId()
  if (integrationKind === 'slack_thread') {
    return (
      <CheckboxField
        className="items-start"
        inputClassName="mt-0.5"
        label="Allow external participants in shared channels"
        description="Let people from other Slack organizations start agents with these profiles, choose a profile, and reply in shared channels this bot has joined. Only people in your Slack workspace can answer agent questions or approvals."
        checked={values.allowExternalParticipants}
        disabled={disabled}
        onChange={(event) => {
          onChange({ allowExternalParticipants: event.target.checked })
        }}
      />
    )
  }
  if (integrationKind !== 'github_pr') return null
  return (
    <div role="group" aria-labelledby={`${id}-label`} className="flex flex-col gap-3">
      <span id={`${id}-label`} className="type-label">
        Git credentials
      </span>
      <CheckboxField
        className="items-start"
        inputClassName="mt-0.5"
        label="Let agents push code"
        description="Without this, agents can clone but not push."
        checked={values.codeWrite}
        disabled={disabled}
        onChange={(event) => {
          onChange({ codeWrite: event.target.checked })
        }}
      />
    </div>
  )
}

function GitHubLauncherFields({
  integration,
  values,
  onChange,
  disabled,
}: Pick<LauncherFieldsProps, 'integration' | 'values' | 'onChange' | 'disabled'>) {
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
        : { launcher: false, trigger: 'both' },
    )
  }
  return (
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
        disabled={disabled}
        onChange={(event) => {
          setTriggers(event.target.checked, mentioned)
        }}
      />
      <CheckboxField
        label="Bot mentioned"
        checked={mentioned}
        disabled={disabled}
        onChange={(event) => {
          setTriggers(opened, event.target.checked)
        }}
      />
      {mentioned && integration.bot_mention && (
        <div className="flex min-w-0 items-center gap-2 text-sm">
          <code className="break-all">{integration.bot_mention} please review this PR</code>
          <CopyButton
            text={`${integration.bot_mention} please review this PR`}
            label="bot mention"
          />
        </div>
      )}
      <FieldDescription id={`${id}-hint`}>
        Only people with repository write access can launch or steer agents.
      </FieldDescription>
      {values.repositoryId && (
        <FieldDescription>Restricted to repository {values.repositoryId}.</FieldDescription>
      )}
      {!values.launcher && (
        <FieldDescription>
          {values.profileIds.length > 0
            ? values.repositoryId
              ? 'Saving with both triggers off clears the profile and repository restriction.'
              : 'Saving with both triggers off clears the selected profile.'
            : 'Enable a trigger to choose a profile.'}
        </FieldDescription>
      )}
    </div>
  )
}
