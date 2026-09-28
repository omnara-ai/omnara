import {
  chatIntegrationLauncher,
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
  scopeKind: string
  scopeRef: string
  trigger: string
}

export function integrationFormValues(
  integrationKind: IntegrationKind,
  integration: Integration,
): IntegrationFormValues {
  const github =
    integrationKind === 'github_pr' ? githubIntegrationSettings(integration.settings) : undefined
  const chat =
    integrationKind !== 'github_pr' ? chatIntegrationLauncher(integration.settings) : undefined
  return {
    launcher: Boolean(integration.settings.launcher),
    profileIds: profileIntegrationProfiles(integration),
    scopeKind: github?.launcher?.repository_id ? 'repository' : chat?.channel_id ? 'channel' : '',
    scopeRef: github?.launcher?.repository_id ?? chat?.channel_id ?? '',
    trigger:
      github?.launcher?.trigger ??
      (integrationKind === 'github_pr' ? 'pull_request_opened' : 'mention'),
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
    channelId:
      integrationKind === 'slack_thread' && values.scopeKind === 'channel'
        ? values.scopeRef.trim()
        : undefined,
    repositoryId:
      integrationKind === 'github_pr' && values.scopeKind === 'repository'
        ? values.scopeRef.trim()
        : undefined,
    trigger: z.enum(['mention', 'pull_request_opened']).parse(values.trigger),
  })
  if (enabled && values.scopeKind && !values.scopeRef.trim()) throw new Error('Enter a scope ID.')
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
    return {
      error: '',
      profileCount:
        integrationKind === 'github_pr' && !values.launcher ? 0 : values.profileIds.length,
    }
  } catch (cause) {
    return {
      error: integrationFormError(cause, 'Check the integration settings.'),
      profileCount: null,
    }
  }
}
