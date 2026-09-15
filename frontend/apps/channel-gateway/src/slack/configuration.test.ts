import type { ChannelConnectorInstallationConfiguration } from '@omnara/sdk'
import { describe, expect, it } from 'vitest'

import { slackCredentials } from './configuration'
import { credentials } from './test-support'

const configuration: ChannelConnectorInstallationConfiguration = {
  integration_app_id: 'app-fixture',
  app_configuration_revision: 1,
  install: {
    id: 'install-fixture',
    project_id: 'proj_aaaaaaaaaaaaaaaaaaaaaaaaae',
    provider_account_ref: 'app-fixture',
    display_name: 'Existing Slack app',
    metadata: { team_name: 'Local fixture' },
    provider_config: {},
    provider_identity: { bot_user_id: credentials.botUserId },
    configuration_revision: 1,
    updated_at: '2026-09-14T00:00:00Z',
  },
  credential: {
    kind: 'slack_app_credentials',
    payload: {
      access_token: credentials.botToken,
      signing_secret: credentials.signingSecret,
      client_id: 'existing-client-id',
      client_secret: 'existing-client-secret',
    },
  },
}

describe('slackCredentials', () => {
  it('uses the existing combined installation secret without app credentials or a fake tenant', () => {
    const before = JSON.stringify(configuration)
    expect(slackCredentials(configuration)).toEqual(credentials)
    expect(JSON.stringify(configuration)).toBe(before)
  })

  it('diagnoses incomplete secrets or identity without exposing their values', () => {
    expect(() => slackCredentials({ ...configuration, credential: undefined })).toThrow(
      'Slack operation failed: invalid_configuration',
    )
    expect(() =>
      slackCredentials({
        ...configuration,
        credential: {
          kind: 'slack_app_credentials',
          payload: { access_token: credentials.botToken },
        },
      }),
    ).toThrow('Slack operation failed: invalid_configuration')
    expect(() =>
      slackCredentials({
        ...configuration,
        install: { ...configuration.install, provider_identity: {} },
      }),
    ).toThrow('Slack operation failed: invalid_configuration')
  })
})
