import { type Integration, type IntegrationKind } from '@omnara/sdk'

import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'

import { type IntegrationFormValues } from './integrationFormState'
import { IntegrationProfilePicker } from './IntegrationProfilePicker'

const selectClass = 'border-input bg-background h-9 w-full rounded-md border px-3 text-sm'

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
          <Field>
            <FieldLabel htmlFor="integration-sender-policy">Who can direct agents</FieldLabel>
            <select
              id="integration-sender-policy"
              className={selectClass}
              value={values.senderPolicy}
              onChange={(event) => {
                onChange({ senderPolicy: event.target.value === 'anyone' ? 'anyone' : 'writers' })
              }}
            >
              <option value="writers">People with repository write access</option>
              <option value="anyone">Anyone</option>
            </select>
            <FieldDescription>
              Controls who can launch or steer agents through comments. Automatic launches when a
              pull request opens are configured separately.
            </FieldDescription>
          </Field>
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
            Choose when to launch and which profile to use. Changes apply to future launches.
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
              <select
                id="integration-trigger"
                className={selectClass}
                value={values.trigger}
                onChange={(event) => {
                  onChange({ trigger: event.target.value })
                }}
              >
                <option value="pull_request_opened">A pull request is opened</option>
                <option value="mention">The bot is mentioned on a pull request</option>
              </select>
            </Field>
          )}
          <IntegrationLauncherScopeFields {...props} />
          <IntegrationProfilePicker
            orgId={props.orgId}
            projectId={props.projectId}
            value={values.profileIds.map((id) => ({ id, name: id }))}
            onChange={(profiles) => {
              onChange({ profileIds: profiles.map((profile) => profile.id) })
            }}
            single={github}
            label={github ? 'Agent profile' : 'Profiles for mentions'}
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
}: LauncherFieldsProps) {
  if (integrationKind === 'discord_thread')
    return (
      <FieldDescription>
        Mentions work in every server where this bot is installed and has access. Manage server and
        channel access in Discord.
      </FieldDescription>
    )
  const github = integrationKind === 'github_pr'
  if (github)
    return (
      <FieldDescription>
        {values.scopeKind === 'repository'
          ? `Restricted to repository ${values.scopeRef}. This saved restriction is kept when editing profiles or triggers.`
          : 'Applies to repositories granted to this GitHub installation. Manage repository access in GitHub.'}
      </FieldDescription>
    )
  return (
    <>
      <Field>
        <FieldLabel htmlFor="integration-launch-scope">Respond to mentions in</FieldLabel>
        <select
          id="integration-launch-scope"
          className={selectClass}
          value={values.scopeKind}
          onChange={(event) => {
            onChange({ scopeKind: event.target.value, scopeRef: '' })
          }}
        >
          <option value="">Connected workspace</option>
          <option value="channel">One channel</option>
        </select>
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
        <FieldDescription>{`Workspace: ${integration.provider_tenant_id || 'Connect this Slack app'}. The bot must have access to the conversation.`}</FieldDescription>
      )}
    </>
  )
}
