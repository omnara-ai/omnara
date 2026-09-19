import { expect, it, vi } from 'vitest'

import { fakeId } from '@/test/fixtures'

import { projectAppFormValues } from './projectAppFormState'
import { submitProjectAppSetup } from './projectAppSetupSubmission'

it.each(['name', 'scope', 'profiles', 'discord-key'] as const)(
  'rejects invalid %s before creating credentials or a connection',
  async (invalid) => {
    const provider = invalid === 'discord-key' ? 'discord' : 'github'
    const values = {
      ...projectAppFormValues(provider),
      profileIds: [fakeId('aprf')],
      scopeRef: '123',
    }
    if (invalid === 'name') values.name = '\u200b'
    if (invalid === 'scope') values.scopeRef = '9223372036854775808'
    if (invalid === 'profiles')
      values.profileIds = Array.from(
        { length: 17 },
        (_, index) => `aprf_${String.fromCharCode(97 + index).repeat(26)}`,
      )
    if (invalid === 'discord-key') values.interactions = true
    const form = new FormData()
    form.set('shards', '1')
    form.set('publicKey', 'invalid')
    type Actions = Parameters<typeof submitProjectAppSetup>[1]
    const actions: Actions = {
      createSecret: vi.fn<Actions['createSecret']>(),
      createConnection: vi.fn<Actions['createConnection']>(),
      createApp: vi.fn<Actions['createApp']>(),
      updateApp: vi.fn<Actions['updateApp']>(),
      onSecretSaved: vi.fn(),
      onConnectionSaved: vi.fn(),
    }
    await expect(
      submitProjectAppSetup(
        {
          form,
          projectId: fakeId('proj'),
          provider,
          values,
          creating: true,
          savedSecret: '',
          newCredential: true,
        },
        actions,
      ),
    ).rejects.toThrow()
    expect(actions.createSecret).not.toHaveBeenCalled()
    expect(actions.createConnection).not.toHaveBeenCalled()
    expect(actions.createApp).not.toHaveBeenCalled()
  },
)
