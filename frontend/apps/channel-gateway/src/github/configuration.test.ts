import type {
  ChannelConnectorAppConfiguration,
  ChannelConnectorInstallationConfiguration,
} from '@omnara/sdk'
import { describe, expect, it } from 'vitest'

import { githubConfiguration } from './configuration'
import { configuration } from './test-support'

const app: ChannelConnectorAppConfiguration = {
  app: {
    id: 'app_fixture',
    provider: 'github',
    provider_app_ref: '42',
    display_name: 'Fixture',
    connector_key: 'github',
    provider_config: {},
    provider_metadata: {},
    configuration_revision: 1,
    updated_at: '2026-09-15T00:00:00Z',
  },
  credential: {
    kind: 'integration_credentials',
    payload: {
      private_key: configuration.privateKey,
      webhook_secret: configuration.webhookSecret,
      client_id: 'unused',
      client_secret: 'unused',
    },
  },
}
const installation: ChannelConnectorInstallationConfiguration = {
  integration_app_id: app.app.id,
  app_configuration_revision: 1,
  install: {
    id: configuration.integrationInstallID,
    project_id: configuration.projectID,
    provider_tenant_id: '123',
    provider_account_ref: '456',
    display_name: 'Fixture',
    provider_config: {},
    metadata: {},
    provider_identity: {
      repository_owner: 'example',
      repository_name: 'project',
      repository_node_id: 'R_selected',
    },
    configuration_revision: 1,
    updated_at: '2026-09-15T00:00:00Z',
  },
}

describe('GitHub configuration', () => {
  it('projects the real app/install/repository tuple without OAuth fields', () => {
    expect(githubConfiguration(app, installation)).toEqual(configuration)
    expect(githubConfiguration(app, installation)).not.toHaveProperty('clientSecret')
  })
  it.each(['9007199254740993', '123.0', '1e3', '001', '-1', 'https://github.com/example'])(
    'rejects unsafe or noncanonical IDs %s',
    (id) => {
      expect(() =>
        githubConfiguration(app, {
          ...installation,
          install: { ...installation.install, provider_tenant_id: id },
        }),
      ).toThrow('invalid_configuration')
      expect(() =>
        githubConfiguration(app, {
          ...installation,
          install: { ...installation.install, provider_account_ref: id },
        }),
      ).toThrow('invalid_configuration')
    },
  )
  it('rejects mismatched app scope and incomplete credentials with fixed diagnostics', () => {
    expect(() =>
      githubConfiguration(app, { ...installation, app_configuration_revision: 2 }),
    ).toThrow('invalid_configuration')
    expect(() =>
      githubConfiguration(app, { ...installation, integration_app_id: 'app_other' }),
    ).toThrow('invalid_configuration')
    expect(() =>
      githubConfiguration(
        {
          ...app,
          credential: { kind: 'integration_credentials', payload: { private_key: 'DO-NOT-LOG' } },
        },
        installation,
      ),
    ).toThrow('GitHub operation failed: invalid_configuration')
    expect(() =>
      githubConfiguration(app, {
        ...installation,
        install: { ...installation.install, provider_identity: {} },
      }),
    ).toThrow('invalid_configuration')
  })
})
