import {
  type AppLauncher,
  type IntegrationConnection,
  profileAppDiscordKeyStatus,
  profileAppProfileUpdate,
  type ProfileAppProvider,
  profileAppSetup,
  profileAppTools,
  type ProjectApp,
  type SaveProjectAppRequest,
  schemas,
} from '@omnara/sdk'
import * as z from 'zod'

export interface ProjectAppFormValues {
  name: string
  launcher: boolean
  profileIds: string[]
  scopeKind: string
  scopeRef: string
  trigger: string
  tools: string[]
  listen: boolean
  interactions: boolean
}

export function projectAppFormValues(
  provider: ProfileAppProvider,
  app?: ProjectApp,
): ProjectAppFormValues {
  const resource = app?.settings.resource
  const launcher = app?.settings.launcher
  return {
    name: app?.name ?? { slack: 'Slack', github: 'GitHub', discord: 'Discord' }[provider],
    launcher: app ? Boolean(launcher) : true,
    profileIds: [
      ...new Set(
        launcher?.slots.flatMap((slot) =>
          !slot.agent_id && slot.agent_profile_id ? [slot.agent_profile_id] : [],
        ) ?? [],
      ),
    ],
    scopeKind:
      launcher?.scope_kind ??
      (provider === 'slack' ? 'workspace' : provider === 'github' ? 'repository' : 'channel'),
    scopeRef: launcher?.scope_ref ?? '',
    trigger: launcher?.trigger ?? (provider === 'github' ? 'pull_request_opened' : 'mention'),
    tools: app
      ? profileAppTools[provider].filter((tool) => resource?.tools?.[tool] !== undefined)
      : [...profileAppTools[provider]],
    listen: app ? Boolean(resource?.listener) : true,
    interactions: app ? Boolean(resource?.interaction_handler) : provider === 'slack',
  }
}

export function githubHasAdvancedSlots(launcher?: AppLauncher) {
  return Boolean(
    launcher &&
    (launcher.slots.length !== 1 ||
      !launcher.slots[0]?.agent_profile_id ||
      launcher.slots[0].agent_id),
  )
}

/** A PUT starts from saved settings, replacing only fields the form actually changes. */
export function projectAppFormRequest(
  provider: ProfileAppProvider,
  values: ProjectAppFormValues,
  connectionId?: string,
  app?: ProjectApp,
  workspaceId?: string,
): SaveProjectAppRequest {
  if (app && app.settings.resource.definition !== `omnara.${provider}`) {
    throw new Error('The app provider cannot be changed.')
  }
  const name = values.name.trim()
  if (!schemas.zResourceName.safeParse(name).success)
    throw new Error('Enter an app name of 1–64 characters without invisible or control characters.')
  const scopeRef =
    provider === 'slack' &&
    values.scopeKind === 'workspace' &&
    app?.settings.launcher?.scope_kind !== 'workspace'
      ? (workspaceId ?? values.scopeRef)
      : values.scopeRef
  if (!app) {
    return {
      ...profileAppSetup({
        ...values,
        provider,
        name,
        connectionId,
        scopeRef,
        scopeKind: values.launcher
          ? z.enum(['workspace', 'channel', 'repository']).parse(values.scopeKind)
          : undefined,
        trigger: values.launcher
          ? z.enum(['mention', 'pull_request_opened']).parse(values.trigger)
          : undefined,
      }),
      enabled: true,
    }
  }
  if (connectionId !== app.settings.resource.connection) {
    throw new Error('The configured connection cannot be changed here.')
  }
  const initial = projectAppFormValues(provider, app)
  const resource = { ...app.settings.resource }
  const settings = { ...app.settings, resource }
  const selectedTools = new Set(values.tools)
  const providerTools = new Set(profileAppTools[provider])
  // Unknown tools and selected tools' policies, schemas and enabled flags are retained.
  if (
    initial.tools.length !== values.tools.length ||
    initial.tools.some((tool) => !selectedTools.has(tool))
  ) {
    resource.tools = Object.fromEntries(
      Object.entries(resource.tools ?? {}).filter(
        ([tool]) => !providerTools.has(tool) || selectedTools.has(tool),
      ),
    )
    for (const tool of profileAppTools[provider]) {
      if (selectedTools.has(tool)) resource.tools[tool] ??= {}
    }
  }
  if (values.listen !== initial.listen) {
    if (values.listen)
      resource.listener = {
        events:
          provider === 'github' ? ['discussion_comment', 'review_comment', 'commit'] : ['message'],
      }
    else delete resource.listener
  }
  if (values.interactions !== initial.interactions) {
    if (values.interactions) {
      if (provider === 'github') throw new Error('GitHub apps do not support interaction handlers.')
      resource.interaction_handler = { definition: `omnara.${provider}.interactions` }
    } else delete resource.interaction_handler
  }
  if (!values.launcher) delete settings.launcher
  else if (!app.settings.launcher) {
    if (!connectionId) throw new Error('A configured connection is required to enable a launcher.')
    settings.launcher = projectAppFormRequest(
      provider,
      values,
      connectionId,
      undefined,
      workspaceId,
    ).settings.launcher
  } else {
    let launcher = app.settings.launcher
    const selectedProfiles = new Set(values.profileIds)
    const profilesChanged =
      initial.profileIds.length !== values.profileIds.length ||
      initial.profileIds.some((id) => !selectedProfiles.has(id))
    if (profilesChanged) {
      if (provider === 'github') {
        const slot = launcher.slots[0]
        if (githubHasAdvancedSlots(launcher) || !slot)
          throw new Error('Edit advanced GitHub launch slots through the API.')
        const ids = z
          .array(schemas.zAgentProfileId)
          .length(1, 'Choose one profile for GitHub.')
          .parse(values.profileIds)
        launcher = { ...launcher, slots: [{ ...slot, agent_profile_id: ids[0] }] }
      } else {
        const updated = profileAppProfileUpdate(app, values.profileIds).settings.launcher
        if (!updated) throw new Error('A launcher is required to edit profiles.')
        launcher = updated
      }
    }
    settings.launcher = {
      ...launcher,
      trigger: values.trigger,
      scope_kind: values.scopeKind,
      scope_ref: scopeRef.trim(),
    }
  }
  return { name, enabled: app.enabled, settings }
}

/** Unchanged Discord setups can be renamed without changing connection credentials. */
export function projectAppFormDiscordKeyStatus(
  request?: SaveProjectAppRequest,
  app?: ProjectApp,
  providerConfig?: IntegrationConnection['provider_config'],
) {
  const status = profileAppDiscordKeyStatus({
    definition: request?.settings.resource.definition,
    slots: request?.settings.launcher?.slots ?? [],
    interactions: Boolean(request?.settings.resource.interaction_handler),
    providerConfig,
  })
  const changed =
    !app ||
    JSON.stringify(request?.settings.launcher?.slots) !==
      JSON.stringify(app.settings.launcher?.slots) ||
    (Boolean(request?.settings.resource.interaction_handler) &&
      !app.settings.resource.interaction_handler)
  return { ...status, missing: changed && status.missing }
}

export function projectAppFormError(error: Error) {
  return error instanceof z.ZodError
    ? (error.issues[0]?.message ?? 'Check the app settings.')
    : error.message
}

/** Render-time validation is pure and uses the same request builder as submission. */
export function validateProjectAppForm(
  provider: ProfileAppProvider,
  values: ProjectAppFormValues,
  connection?: IntegrationConnection,
  app?: ProjectApp,
) {
  let request: SaveProjectAppRequest | undefined
  let error = ''
  try {
    request = projectAppFormRequest(
      provider,
      values,
      app?.settings.resource.connection ?? connection?.id,
      app,
      connection?.provider_tenant_id,
    )
  } catch (cause) {
    error = cause instanceof Error ? projectAppFormError(cause) : 'Check the app settings.'
  }
  return {
    error,
    keyStatus: projectAppFormDiscordKeyStatus(request, app, connection?.provider_config),
    slotCount: request ? (request.settings.launcher?.slots.length ?? 0) : null,
  }
}
