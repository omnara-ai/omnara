import { describe, expect, it } from 'vitest'

import { discordConfiguration } from './configuration'
import { discordID } from './protocol'
import { app, config, installation } from './test-support'

describe('Discord configuration', () => {
  it('keeps app, bot and guild identities as exact independent decimal strings', () => {
    expect(discordConfiguration(app, installation)).toEqual(config)
  })
  it.each(['', '01', '0', '-1', '1e17', '18446744073709551616', '123/456', ' 123', 'NaN'])(
    'rejects a noncanonical snowflake %s without numeric coercion',
    (id) => {
      expect(discordID.safeParse(id).success).toBe(false)
      expect(() =>
        discordConfiguration({ ...app, app: { ...app.app, provider_app_ref: id } }, installation),
      ).toThrow('invalid_configuration')
    },
  )
  it('preserves the full unsigned snowflake range', () => {
    expect(discordID.parse('18446744073709551615')).toBe('18446744073709551615')
  })
  it.each(['', 'token\nprivate', 'token secret', 'é', 'x'.repeat(4097)])(
    'rejects invalid credentials without echoing them',
    (bot_token) => {
      expect(() =>
        discordConfiguration(
          { ...app, credential: { kind: 'integration_credentials', payload: { bot_token } } },
          installation,
        ),
      ).toThrow('Discord operation failed: invalid_configuration')
    },
  )
  it('requires the app credential kind and matching app/revision/provider', () => {
    for (const altered of [
      { ...app, credential: { ...app.credential, kind: 'other' } },
      { ...app, app: { ...app.app, connector_key: 'other' } },
      { ...app, app: { ...app.app, provider: 'slack' } },
      { ...app, app: { ...app.app, id: 'different' } },
      { ...app, app: { ...app.app, configuration_revision: 2 } },
    ])
      expect(() => discordConfiguration(altered, installation)).toThrow('invalid_configuration')
    for (const field of ['provider_tenant_id', 'provider_account_ref'] as const)
      expect(() =>
        discordConfiguration(app, {
          ...installation,
          install: { ...installation.install, [field]: 'bad' },
        }),
      ).toThrow('invalid_configuration')
  })
})
