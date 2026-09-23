import {
  type AppLauncher,
  type AppType,
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

export function projectAppFormValues(appType: AppType, app?: ProjectApp): ProjectAppFormValues {
  const launcher = app?.settings.launcher
  return {
    name: app?.name ?? appType.replaceAll('_', '-'),
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
      (appType === 'slack_thread' ? 'workspace' : appType === 'github_pr' ? 'installation' : ''),
    scopeRef:
      launcher?.scope_ref ??
      (appType === 'slack_thread'
        ? (app?.provider_tenant_id ?? '')
        : appType === 'github_pr'
          ? (app?.provider_account_ref ?? '')
          : ''),
    trigger: launcher?.trigger ?? (appType === 'github_pr' ? 'pull_request_opened' : 'mention'),
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
  appType: AppType,
  values: ProjectAppFormValues,
  app?: ProjectApp,
): SaveProjectAppRequest {
  const name = values.name.trim()
  if (!/^[A-Za-z][A-Za-z0-9-]{0,31}$/.test(name))
    throw new Error('Use 1–32 letters, numbers or hyphens, starting with a letter.')
  if (app && (app.name !== name || app.app_type !== appType))
    throw new Error('The app name and type cannot be changed.')
  const request: SaveProjectAppRequest = {
    name,
    app_type: app?.app_type ?? appType,
    settings: {},
  }
  if (!app || (appType === 'github_pr' && !values.launcher)) return request
  const retainedSlots =
    app.settings.launcher?.slots.filter(
      (slot) => Boolean(slot.agent_id) || !slot.agent_profile_id,
    ) ?? []
  if (appType !== 'github_pr' && values.profileIds.length === 0 && retainedSlots.length === 0)
    return request
  const initial = projectAppFormValues(appType, app)
  const existing = app.settings.launcher
  const scopeRef = values.scopeRef.trim()
  const scopeKind = values.scopeKind
  const profilesChanged = JSON.stringify(initial.profileIds) !== JSON.stringify(values.profileIds)
  let slots = existing?.slots
  if (existing && !profilesChanged) {
    slots = existing.slots
  } else if (existing && appType !== 'github_pr') {
    slots = profileAppProfileUpdate(app, values.profileIds).settings.launcher?.slots
  } else {
    if (appType === 'github_pr' && githubHasAdvancedSlots(existing))
      throw new Error('Edit advanced GitHub launch slots through the API.')
    slots = profileAppSetup({
      appType,
      name,
      profileIds: values.profileIds,
      scopeKind: z
        .enum(['workspace', 'channel', 'repository', 'installation'])
        .optional()
        .parse(scopeKind || undefined),
      scopeRef,
      trigger: z.enum(['mention', 'pull_request_opened']).parse(values.trigger),
    }).settings.launcher?.slots
    if (existing?.slots[0] && slots?.[0])
      slots = [{ ...existing.slots[0], agent_profile_id: slots[0].agent_profile_id }]
  }
  if (!slots?.length) throw new Error('Choose at least one profile.')
  // Allow profile-only edits to preserve saved scopes that guided setup cannot express.
  if (
    !existing ||
    existing.scope_kind !== scopeKind ||
    existing.scope_ref !== scopeRef ||
    existing.trigger !== values.trigger
  ) {
    profileAppLauncherScope({ appType, scopeKind, scopeRef, trigger: values.trigger })
  }
  if (
    profileAppDiscordKeyStatus({
      appType: app.app_type,
      slots,
      interactions: false,
      providerConfig: app.provider_config,
    }).missing
  )
    throw new Error('Configure a valid Discord public key before enabling the launcher.')
  request.settings.launcher = {
    ...existing,
    trigger: values.trigger,
    scope_kind: scopeKind || undefined,
    scope_ref: scopeRef || undefined,
    slots,
  }
  return request
}

export function projectAppFormError(cause: unknown, fallback: string) {
  if (cause instanceof z.ZodError) return cause.issues[0]?.message ?? fallback
  return cause instanceof Error ? cause.message : fallback
}

export function validateProjectAppForm(
  appType: AppType,
  values: ProjectAppFormValues,
  app?: ProjectApp,
) {
  try {
    const request = projectAppFormRequest(appType, values, app)
    return { error: '', slotCount: request.settings.launcher?.slots.length ?? 0 }
  } catch (cause) {
    return {
      error: projectAppFormError(cause, 'Check the app settings.'),
      slotCount: null,
    }
  }
}
