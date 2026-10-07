import {
  type GitHubIntegrationLauncher,
  githubIntegrationSettings,
  type Integration,
  type IntegrationKind,
  profileIntegrationDiscordKeyStatus,
  profileIntegrationProfiles,
  profileIntegrationSetup,
  type UpdateIntegrationRequest,
} from '@omnara/sdk'
import * as z from 'zod'

export interface IntegrationFormValues {
  launcher: boolean
  profileIds: string[]
  repositoryId: string
  trigger: GitHubIntegrationLauncher['trigger']
}

export function integrationFormValues(
  integrationKind: IntegrationKind,
  integration: Integration,
  defaultLauncherEnabled = false,
): IntegrationFormValues {
  const github =
    integrationKind === 'github_pr' ? githubIntegrationSettings(integration.settings) : undefined
  return {
    launcher: Boolean(integration.settings.launcher) || defaultLauncherEnabled,
    profileIds: profileIntegrationProfiles(integration),
    repositoryId: github?.launcher?.repository_id ?? '',
    trigger: github?.launcher?.trigger ?? (integrationKind === 'github_pr' ? 'both' : 'mention'),
  }
}

export function integrationFormRequest(
  integrationKind: IntegrationKind,
  values: IntegrationFormValues,
  integration: Integration,
): UpdateIntegrationRequest {
  const enabled = integrationKind === 'github_pr' ? values.launcher : values.profileIds.length > 0
  const settings = { ...integration.settings }
  delete settings.launcher
  const configured = profileIntegrationSetup({
    integrationKind,
    name: integration.name,
    launcher: enabled,
    profileIds: values.profileIds,
    repositoryId: integrationKind === 'github_pr' ? values.repositoryId || undefined : undefined,
    trigger: values.trigger,
  })
  if (
    profileIntegrationDiscordKeyStatus({
      integrationKind,
      launcher: enabled,
      interactions: false,
      providerConfig: integration.provider_config,
    }).missing
  ) {
    throw new Error('Configure a valid Discord public key before enabling the launcher.')
  }
  return { settings: { ...settings, ...configured.settings } }
}

export function integrationFormError(cause: unknown, fallback: string) {
  if (cause instanceof z.ZodError) return cause.issues[0]?.message ?? fallback
  return cause instanceof Error ? cause.message : fallback
}

export function validateIntegrationForm(
  integrationKind: IntegrationKind,
  values: IntegrationFormValues,
  integration: Integration,
) {
  try {
    integrationFormRequest(integrationKind, values, integration)
    return ''
  } catch (cause) {
    return integrationFormError(cause, 'Check the integration settings.')
  }
}
