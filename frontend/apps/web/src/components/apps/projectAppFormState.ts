import {
  type AppLauncher,
  type IntegrationProvider,
  profileAppDiscordKeyStatus,
  profileAppLauncherScope,
  profileAppProfileUpdate,
  profileAppSetup,
  type ProjectApp,
  type SaveProjectAppRequest,
} from '@omnara/sdk'
import * as z from 'zod'

export interface ProjectAppFormValues {
  name: string
  launcher: boolean
  profileIds: string[]
  scopeKind: string
  scopeRef: string
  trigger: string
}

export function projectAppFormValues(
  provider: IntegrationProvider,
  app?: ProjectApp,
): ProjectAppFormValues {
  const launcher = app?.settings.launcher
  return {
    name: app?.name ?? provider,
    launcher: Boolean(launcher),
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
    scopeRef: launcher?.scope_ref ?? (provider === 'slack' ? (app?.provider_tenant_id ?? '') : ''),
    trigger: launcher?.trigger ?? (provider === 'github' ? 'pull_request_opened' : 'mention'),
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

export function projectAppFormRequest(
  provider: IntegrationProvider,
  values: ProjectAppFormValues,
  app?: ProjectApp,
): SaveProjectAppRequest {
  const name = values.name.trim()
  if (!/^[A-Za-z][A-Za-z0-9-]{0,31}$/.test(name))
    throw new Error('Use 1–32 letters, numbers or hyphens, starting with a letter.')
  if (app && (app.name !== name || app.provider !== provider))
    throw new Error('The app name and provider cannot be changed.')
  const request: SaveProjectAppRequest = {
    name,
    definition_id: app?.definition_id ?? `omnara.${provider}`,
    settings: {},
  }
  if (!values.launcher) return request
  if (!app) throw new Error('Create and connect this app before choosing launch settings.')
  const initial = projectAppFormValues(provider, app)
  const existing = app.settings.launcher
  const scopeRef = values.scopeRef.trim()
  const scopeKind = values.scopeKind
  const profilesChanged = JSON.stringify(initial.profileIds) !== JSON.stringify(values.profileIds)
  let slots = existing?.slots
  if (existing && !profilesChanged) {
    slots = existing.slots
  } else if (existing && provider !== 'github') {
    slots = profileAppProfileUpdate(app, values.profileIds).settings.launcher?.slots
  } else {
    if (provider === 'github' && githubHasAdvancedSlots(existing))
      throw new Error('Edit advanced GitHub launch slots through the API.')
    slots = profileAppSetup({
      provider,
      name,
      profileIds: values.profileIds,
      scopeKind: z.enum(['workspace', 'channel', 'repository']).parse(scopeKind),
      scopeRef,
      trigger: z.enum(['mention', 'pull_request_opened']).parse(values.trigger),
    }).settings.launcher?.slots
    if (existing?.slots[0] && slots?.[0])
      slots = [{ ...existing.slots[0], agent_profile_id: slots[0].agent_profile_id }]
  }
  if (!slots?.length) throw new Error('Choose at least one profile.')
  // Normal scope edits use the SDK validator. Preserve a saved advanced scope
  // when only changing its profile choices.
  if (
    !existing ||
    existing.scope_kind !== scopeKind ||
    existing.scope_ref !== scopeRef ||
    existing.trigger !== values.trigger
  ) {
    profileAppLauncherScope({ provider, scopeKind, scopeRef, trigger: values.trigger })
  }
  if (
    profileAppDiscordKeyStatus({
      definition: app.definition_id,
      slots,
      interactions: false,
      providerConfig: app.provider_config,
    }).missing
  )
    throw new Error('Configure a valid Discord public key before enabling the launcher.')
  request.settings.launcher = {
    ...existing,
    trigger: values.trigger,
    scope_kind: scopeKind,
    scope_ref: scopeRef,
    slots,
  }
  return request
}

export function projectAppFormError(error: Error) {
  return error instanceof z.ZodError
    ? (error.issues[0]?.message ?? 'Check the app settings.')
    : error.message
}

export function validateProjectAppForm(
  provider: IntegrationProvider,
  values: ProjectAppFormValues,
  app?: ProjectApp,
) {
  try {
    const request = projectAppFormRequest(provider, values, app)
    return { error: '', slotCount: request.settings.launcher?.slots.length ?? 0 }
  } catch (cause) {
    return {
      error: cause instanceof Error ? projectAppFormError(cause) : 'Check the app settings.',
      slotCount: null,
    }
  }
}
