import { describe, expect, it } from 'vitest'

import { coreFixture, nativeFixture } from './inbound-test-support'
import { GitHubAddressError, resolveGitHubAddress } from './resolve'
import { attempt, mutationInputs } from './test-support'

describe('GitHub managed registration resolution', () => {
  it.each(['pr', 'review_thread'])(
    'proves %s identity and supplies actual parent definition',
    async (kind) => {
      const f = await nativeFixture()
      const core = coreFixture()
      const input = {
        provider_ref: `repo:456:pr:7${kind === 'pr' ? '' : ':comment:PRRC_1'}`,
        provider_ref_kind: kind,
      }
      const result = await resolveGitHubAddress(
        f.client,
        input,
        {
          installation: { integration_app_id: 'app', integration_install_id: 'install' },
          publishDefinition: core.publishDefinition,
        },
        attempt(),
      )
      expect(result).toMatchObject({ provider_ref: input.provider_ref, provider_ref_kind: kind })
      if (kind === 'review_thread')
        expect(result).toMatchObject({
          provider_metadata: { thread_id: 'PRRT_1' },
          parent: { provider_ref: 'repo:456:pr:7', provider_ref_kind: 'pr' },
        })
      else expect(result.parent).toBeUndefined()
      expect(core.publishDefinition.mock.calls.map(([, definition]) => definition.kind)).toEqual(
        kind === 'pr' ? ['GITHUB_PR'] : ['GITHUB_PR', 'GITHUB_REVIEW_THREAD'],
      )
      expect(mutationInputs(f.calls)).toEqual([])
    },
  )
  it.each([
    'repo:999:pr:7',
    'https://foreign.test',
    'repo:456:pr:9007199254740993',
    'repo:456:pr:7/contents',
  ])('rejects invalid/foreign locator %s before I/O', async (provider_ref) => {
    const f = await nativeFixture()
    const core = coreFixture()
    await expect(
      resolveGitHubAddress(
        f.client,
        { provider_ref },
        {
          installation: { integration_app_id: 'app', integration_install_id: 'install' },
          publishDefinition: core.publishDefinition,
        },
        attempt(),
      ),
    ).rejects.toEqual(new GitHubAddressError('invalid_address'))
    expect(f.calls).toEqual([])
    expect(core.publishDefinition).not.toHaveBeenCalled()
  })
  it.each([{ pending: true }, { foreign: true }, { missing: true }])(
    'does not register unavailable/unowned children %j',
    async (options) => {
      const f = await nativeFixture(options)
      const core = coreFixture()
      await expect(
        resolveGitHubAddress(
          f.client,
          { provider_ref: 'repo:456:pr:7:comment:PRRC_1' },
          {
            installation: { integration_app_id: 'app', integration_install_id: 'install' },
            publishDefinition: core.publishDefinition,
          },
          attempt(),
        ),
      ).rejects.toEqual(new GitHubAddressError('address_unavailable'))
      expect(core.publishDefinition).not.toHaveBeenCalled()
      expect(mutationInputs(f.calls)).toEqual([])
    },
  )
})
