import { type ProfileAppProvider, profileAppTools, type ProjectApp } from '@omnara/sdk'

import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'

import { githubHasAdvancedSlots, type ProjectAppFormValues } from './projectAppFormState'
import { ProjectAppProfilePicker } from './ProjectAppProfilePicker'

const selectClass = 'border-input bg-background h-9 w-full rounded-md border px-3 text-sm'
const launcherScopeKinds: Record<ProfileAppProvider, readonly string[]> = {
  slack: ['workspace', 'channel'],
  github: ['repository'],
  discord: ['channel'],
}

interface LauncherFieldsProps {
  orgId: string
  projectId: string
  provider: ProfileAppProvider
  app?: ProjectApp
  values: ProjectAppFormValues
  onChange: (patch: Partial<ProjectAppFormValues>) => void
  disabled: boolean
  workspaceId?: string
  slotCount: number | null
}

export function ProjectAppLauncherFields(props: LauncherFieldsProps) {
  const { values, onChange, provider, app } = props
  return (
    <Field>
      <label className="flex gap-2 text-sm font-medium">
        <input
          type="checkbox"
          name="launcher"
          checked={values.launcher}
          onChange={(event) => {
            onChange({ launcher: event.target.checked })
          }}
        />
        Launch agents from {provider === 'github' ? 'GitHub events' : 'mentions'}
      </label>
      <FieldDescription>
        {values.launcher
          ? 'Choose when to launch and which profiles to use. Changes apply to future launches.'
          : 'No agents launch automatically. Profiles are only required when you enable a launcher.'}
        {app?.settings.launcher &&
          !values.launcher &&
          ' Saving removes this launcher and all of its launch slots.'}
      </FieldDescription>
      {values.launcher && (
        <>
          {provider === 'github' && (
            <GitHubLaunchTrigger value={values.trigger} onChange={onChange} />
          )}
          <ProjectAppLauncherScopeFields
            provider={provider}
            app={app}
            values={values}
            onChange={onChange}
            workspaceId={props.workspaceId}
          />
          <ProjectAppLaunchProfiles {...props} />
        </>
      )}
    </Field>
  )
}

function GitHubLaunchTrigger({
  value,
  onChange,
}: {
  value: string
  onChange: (patch: Partial<ProjectAppFormValues>) => void
}) {
  return (
    <Field>
      <FieldLabel htmlFor="app-trigger">Launch when</FieldLabel>
      <select
        id="app-trigger"
        aria-label="Launch when"
        className={selectClass}
        value={value}
        onChange={(event) => {
          onChange({ trigger: event.target.value })
        }}
      >
        <option value="pull_request_opened">A pull request is opened</option>
        <option value="mention">The bot is mentioned on a pull request</option>
        {!['mention', 'pull_request_opened'].includes(value) && (
          <option value={value}>{value} (saved)</option>
        )}
      </select>
    </Field>
  )
}

function ProjectAppLauncherScopeFields({
  provider,
  app,
  values,
  onChange,
  workspaceId,
}: Pick<LauncherFieldsProps, 'provider' | 'app' | 'values' | 'onChange' | 'workspaceId'>) {
  if (!launcherScopeKinds[provider].includes(values.scopeKind))
    return (
      <FieldDescription>
        Saved launcher scope: {values.scopeKind} / {values.scopeRef}. This advanced scope is kept
        unchanged; edit it through the API.
      </FieldDescription>
    )
  const launcher = app?.settings.launcher
  return (
    <>
      {provider === 'slack' && (
        <Field>
          <FieldLabel htmlFor="app-launch-scope">Launch in</FieldLabel>
          <select
            id="app-launch-scope"
            aria-label="Launch in"
            className={selectClass}
            value={values.scopeKind}
            onChange={(event) => {
              const scopeKind = event.target.value
              onChange({
                scopeKind,
                scopeRef: launcher?.scope_kind === scopeKind ? launcher.scope_ref : '',
              })
            }}
          >
            <option value="workspace">Connected workspace</option>
            <option value="channel">One channel</option>
          </select>
        </Field>
      )}
      {values.scopeKind === 'workspace' ? (
        <FieldDescription>
          Workspace: {values.scopeRef || (workspaceId ?? 'Choose a Slack connection')}. The bot must
          have access to the conversation.
        </FieldDescription>
      ) : (
        <Field>
          <FieldLabel htmlFor="launcher-scope">
            {provider === 'github' ? 'Repository ID' : 'Channel ID'}
          </FieldLabel>
          <Input
            id="launcher-scope"
            name="scope"
            value={values.scopeRef}
            onChange={(event) => {
              onChange({ scopeRef: event.target.value })
            }}
            pattern={provider === 'slack' ? '[CG][A-Z0-9]+' : '[1-9][0-9]*'}
            required
          />
          <FieldDescription>
            {provider === 'github'
              ? 'Use the numeric repository ID, not owner/repository.'
              : 'The bot must have access to this channel.'}
          </FieldDescription>
        </Field>
      )}
    </>
  )
}

function ProjectAppLaunchProfiles({
  orgId,
  projectId,
  provider,
  app,
  values,
  onChange,
  disabled,
  slotCount,
}: LauncherFieldsProps) {
  const launcher = app?.settings.launcher
  if (provider === 'github' && githubHasAdvancedSlots(launcher))
    return (
      <FieldDescription>
        This GitHub launcher has {launcher?.slots.length} saved launch slots. Their names, profiles
        and existing-agent destinations are kept unchanged. Edit these slots through the API.
      </FieldDescription>
    )
  const retainedSlots =
    launcher?.slots.filter((slot) => Boolean(slot.agent_id) || !slot.agent_profile_id) ?? []
  return (
    <>
      <ProjectAppProfilePicker
        orgId={orgId}
        projectId={projectId}
        value={values.profileIds.map((id) => ({ id, name: id }))}
        onChange={(profiles) => {
          onChange({ profileIds: profiles.map((profile) => profile.id) })
        }}
        single={provider === 'github'}
        disabled={disabled}
        slotCount={slotCount}
      />
      {retainedSlots.length > 0 && (
        <FieldDescription>
          {retainedSlots.length} existing-agent or other slots are kept unchanged and count toward
          the 16-slot limit. Selected profiles keep their saved slot names, including repeated
          slots.
        </FieldDescription>
      )}
    </>
  )
}

export function ProjectAppCapabilityFields({
  provider,
  values,
  onChange,
  app,
}: {
  provider: ProfileAppProvider
  values: ProjectAppFormValues
  onChange: (patch: Partial<ProjectAppFormValues>) => void
  app?: ProjectApp
}) {
  const selectedTools = new Set(values.tools)
  return (
    <Field>
      <FieldLabel>Capabilities</FieldLabel>
      {profileAppTools[provider].map((tool) => (
        <label key={tool} className="flex gap-2 text-sm">
          <input
            type="checkbox"
            name="tools"
            value={tool}
            checked={selectedTools.has(tool)}
            onChange={(event) => {
              onChange({
                tools: event.target.checked
                  ? [...values.tools, tool]
                  : values.tools.filter((value) => value !== tool),
              })
            }}
          />
          {tool.replaceAll('_', ' ')}
        </label>
      ))}
      <FieldDescription>
        Choose the capabilities this app offers. Agent tool permissions still apply.
      </FieldDescription>
      <label className="flex gap-2 text-sm">
        <input
          type="checkbox"
          name="listen"
          checked={values.listen}
          onChange={(event) => {
            onChange({ listen: event.target.checked })
          }}
        />
        Receive later messages{provider === 'github' ? ' and commits' : ''} in the agent’s
        conversation
      </label>
      {app?.settings.resource.listener && values.listen && (
        <FieldDescription>
          Saved event selection is kept:{' '}
          {app.settings.resource.listener.events.join(', ') || 'none'}.
        </FieldDescription>
      )}
      {provider !== 'github' && (
        <label className="flex gap-2 text-sm">
          <input
            type="checkbox"
            name="interactions"
            checked={values.interactions}
            onChange={(event) => {
              onChange({ interactions: event.target.checked })
            }}
          />
          Show agent questions and approvals in {provider === 'slack' ? 'Slack' : 'Discord'}
        </label>
      )}
      {!values.launcher && (
        <FieldDescription>
          This app stores reusable capability defaults. Choose which capabilities to use and supply
          a conversation scope when adding it to an agent. Saving these defaults does not start a
          listener or send messages.
          {app?.settings.resource.scope && ' The concrete scope already saved on this app is kept.'}
        </FieldDescription>
      )}
      {app && (
        <FieldDescription>
          Settings outside this form, including conversation scope, follow behavior, tool
          permissions and MCP configuration, are kept. Selected tools keep their saved policies and
          enabled settings.
        </FieldDescription>
      )}
    </Field>
  )
}
