import {
  type IntegrationLauncher,
  type IntegrationType,
  profileIntegrationDiscordKeyStatus,
  profileIntegrationLauncherScope,
  profileIntegrationProfileUpdate,
  profileIntegrationSetup,
  type ProjectIntegration,
  type SaveProjectIntegrationRequest,
} from '@omnara/sdk'
import * as z from 'zod'

export interface ProjectIntegrationFormValues {
  name: string
  launcher: boolean
  profileIds: string[]
  scopeKind: string
  scopeRef: string
  trigger: string
}

export function projectIntegrationFormValues(
  integrationType: IntegrationType,
  integration?: ProjectIntegration,
): ProjectIntegrationFormValues {
  const launcher = integration?.settings.launcher
  return {
    name: integration?.name ?? integrationType.replaceAll('_', '-'),
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
      (integrationType === 'slack_thread'
        ? 'workspace'
        : integrationType === 'github_pr'
          ? 'installation'
          : ''),
    scopeRef:
      launcher?.scope_ref ??
      (integrationType === 'slack_thread'
        ? (integration?.provider_tenant_id ?? '')
        : integrationType === 'github_pr'
          ? (integration?.provider_account_ref ?? '')
          : ''),
    trigger:
      launcher?.trigger ?? (integrationType === 'github_pr' ? 'pull_request_opened' : 'mention'),
  }
}

export function githubHasAdvancedSlots(launcher?: IntegrationLauncher) {
  return Boolean(
    launcher &&
    (launcher.slots.length !== 1 ||
      !launcher.slots[0]?.agent_profile_id ||
      launcher.slots[0].agent_id),
  )
}

export function projectIntegrationFormRequest(
  integrationType: IntegrationType,
  values: ProjectIntegrationFormValues,
  integration?: ProjectIntegration,
): SaveProjectIntegrationRequest {
  const name = values.name.trim()
  if (!/^[A-Za-z][A-Za-z0-9-]{0,31}$/.test(name))
    throw new Error('Use 1–32 letters, numbers or hyphens, starting with a letter.')
  if (
    integration &&
    (integration.name !== name || integration.integration_type !== integrationType)
  )
    throw new Error('The integration name and type cannot be changed.')
  const request: SaveProjectIntegrationRequest = {
    name,
    integration_type: integration?.integration_type ?? integrationType,
    settings: {},
  }
  if (!integration || (integrationType === 'github_pr' && !values.launcher)) return request
  const retainedSlots =
    integration.settings.launcher?.slots.filter(
      (slot) => Boolean(slot.agent_id) || !slot.agent_profile_id,
    ) ?? []
  if (
    integrationType !== 'github_pr' &&
    values.profileIds.length === 0 &&
    retainedSlots.length === 0
  )
    return request
  const initial = projectIntegrationFormValues(integrationType, integration)
  const existing = integration.settings.launcher
  const scopeRef = values.scopeRef.trim()
  const scopeKind = values.scopeKind
  const profilesChanged = JSON.stringify(initial.profileIds) !== JSON.stringify(values.profileIds)
  let slots = existing?.slots
  if (existing && !profilesChanged) {
    slots = existing.slots
  } else if (existing && integrationType !== 'github_pr') {
    slots = profileIntegrationProfileUpdate(integration, values.profileIds).settings.launcher?.slots
  } else {
    if (integrationType === 'github_pr' && githubHasAdvancedSlots(existing))
      throw new Error('Edit advanced GitHub launch slots through the API.')
    slots = profileIntegrationSetup({
      integrationType,
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
    profileIntegrationLauncherScope({
      integrationType,
      scopeKind,
      scopeRef,
      trigger: values.trigger,
    })
  }
  if (
    profileIntegrationDiscordKeyStatus({
      integrationType: integration.integration_type,
      slots,
      interactions: false,
      providerConfig: integration.provider_config,
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

export function projectIntegrationFormError(cause: unknown, fallback: string) {
  if (cause instanceof z.ZodError) return cause.issues[0]?.message ?? fallback
  return cause instanceof Error ? cause.message : fallback
}

export function validateProjectIntegrationForm(
  integrationType: IntegrationType,
  values: ProjectIntegrationFormValues,
  integration?: ProjectIntegration,
) {
  try {
    const request = projectIntegrationFormRequest(integrationType, values, integration)
    return { error: '', slotCount: request.settings.launcher?.slots.length ?? 0 }
  } catch (cause) {
    return {
      error: projectIntegrationFormError(cause, 'Check the integration settings.'),
      slotCount: null,
    }
  }
}
