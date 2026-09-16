import { describe, expect, it } from 'vitest'

import { ReceiptBehaviorError } from '../types'
import { createGitHubFactory } from './factory'
import {
  app,
  contexts,
  coreFixture,
  event,
  installation,
  nativeComment,
  receipt,
  webhook,
} from './inbound-test-support'
import { json, localServer, requestBody } from './test-support'

async function fixture() {
  const paths: string[] = []
  const native = { repositoryStatus: 200, repositoryNodeID: 'R_selected' }
  const url = await localServer(async (request, response) => {
    const path = request.url ?? ''
    paths.push(path)
    if (path === '/app/installations/123/access_tokens') {
      expect(await requestBody(request)).toEqual({
        repository_ids: [456],
        permissions: { pull_requests: 'write', issues: 'read', metadata: 'read' },
      })
      json(response, {
        token: 'fixture-token',
        expires_at: new Date(Date.now() + 3_600_000).toISOString(),
      })
    } else if (path === '/repositories/456') {
      json(
        response,
        {
          id: 456,
          node_id: native.repositoryNodeID,
          name: 'current',
          owner: { login: 'example' },
        },
        native.repositoryStatus,
      )
    } else if (path === '/graphql') {
      expect(await requestBody(request)).toEqual({
        query: 'query GitHubViewer { viewer { id login } }',
        variables: {},
      })
      json(response, { data: { viewer: { id: 'U_bot', login: 'verified-native-bot' } } })
    } else throw new Error('unexpected provider request')
  })
  const context = contexts()
  const core = coreFixture()
  const runtime = await createGitHubFactory({ core, apiUrl: url }).create(context.factory)
  return {
    ...context,
    core,
    runtime,
    native,
    paths,
    run: (id: number, login = 'human', nodeID = 'U_human') =>
      runtime.processReceipt?.(
        receipt(
          event(
            {
              ...webhook,
              action: 'created',
              issue: { ...webhook.pull_request, pull_request: {} },
              comment: {
                ...nativeComment,
                id,
                user: { ...nativeComment.user, login, node_id: nodeID },
              },
            },
            'issue_comment',
          ),
        ),
        { signal: context.controller.signal, deadlineMs: Date.now() + 10_000 },
      ),
  }
}

describe('GitHub runtime authentication reuse', () => {
  it('reuses verified auth while reading fresh core scope and native repository identity for each receipt', async () => {
    const f = await fixture()
    try {
      await f.run(1)
      // A matching login with another stable ID is not our bot. Our bot's
      // renamed login still cannot feed its own comment back into the agent.
      await f.run(2, 'verified-native-bot', 'U_human')
      await f.run(3, 'renamed-bot', 'U_bot')
      expect(f.paths).toEqual([
        '/app/installations/123/access_tokens',
        '/repositories/456',
        '/graphql',
        '/repositories/456',
        '/repositories/456',
      ])
      expect(f.core.getAppConfiguration).toHaveBeenCalledTimes(3)
      expect(f.core.getInstallationConfiguration).toHaveBeenCalledTimes(3)
      expect(f.core.deliverWorkflow).toHaveBeenCalledTimes(2)
      expect(f.budget.usedBytes).toBe(16 * 1024)
    } finally {
      await f.runtime.close()
    }
    expect(f.budget.usedBytes).toBe(0)
  })

  it.each(['app revision', 'install revision', 'install identity'])(
    'does not reuse auth after a change to %s',
    async (change) => {
      const f = await fixture()
      try {
        await f.run(1)
        if (change === 'app revision') {
          f.core.getAppConfiguration.mockResolvedValue({
            ...app,
            app: { ...app.app, configuration_revision: 2 },
          })
          f.core.getInstallationConfiguration.mockResolvedValue({
            ...installation,
            app_configuration_revision: 2,
          })
        } else if (change === 'install revision') {
          f.core.getInstallationConfiguration.mockResolvedValue({
            ...installation,
            install: { ...installation.install, configuration_revision: 2 },
          })
        } else {
          // A receipt claiming the original install cannot use another install's config.
          f.core.getInstallationConfiguration.mockResolvedValue({
            ...installation,
            install: { ...installation.install, id: 'iin_bbbbbbbbbbbbbbbbbbbbbbbbbb' },
          })
          await expect(f.run(2)).rejects.toBeInstanceOf(ReceiptBehaviorError)
          expect(f.paths).toHaveLength(3)
          return
        }
        await f.run(2)
        expect(f.paths.filter((path) => path.endsWith('/access_tokens'))).toHaveLength(2)
        expect(f.paths.filter((path) => path === '/graphql')).toHaveLength(2)
      } finally {
        await f.runtime.close()
      }
      expect(f.budget.usedBytes).toBe(0)
    },
  )

  it.each(['unavailable', 'wrong identity', 'rejected token'])(
    'does not turn cached authentication into authority for a %s repository',
    async (failure) => {
      const f = await fixture()
      try {
        await f.run(1)
        if (failure === 'wrong identity') f.native.repositoryNodeID = 'R_other'
        else f.native.repositoryStatus = failure === 'rejected token' ? 401 : 404
        await expect(f.run(2)).rejects.toBeInstanceOf(ReceiptBehaviorError)
        expect(f.core.deliverWorkflow).toHaveBeenCalledOnce()
        expect(f.paths.filter((path) => path === '/graphql')).toHaveLength(1)
        expect(f.paths.filter((path) => path.endsWith('/access_tokens'))).toHaveLength(1)
        if (failure === 'rejected token') {
          f.native.repositoryStatus = 200
          await f.run(3)
          expect(f.paths.filter((path) => path.endsWith('/access_tokens'))).toHaveLength(2)
          expect(f.paths.filter((path) => path === '/graphql')).toHaveLength(2)
          expect(f.core.deliverWorkflow).toHaveBeenCalledTimes(2)
        }
      } finally {
        await f.runtime.close()
      }
      expect(f.budget.usedBytes).toBe(0)
    },
  )

  it('bounds auth entries and releases their work budget on runtime retirement', async () => {
    const f = await fixture()
    try {
      for (let revision = 1; revision <= 65; revision += 1) {
        f.core.getInstallationConfiguration.mockResolvedValue({
          ...installation,
          install: { ...installation.install, configuration_revision: revision },
        })
        await f.run(revision)
      }
      expect(f.budget.usedBytes).toBe(64 * 16 * 1024)
      await f.run(66)
      expect(f.paths.filter((path) => path.endsWith('/access_tokens'))).toHaveLength(65)
    } finally {
      await f.runtime.close()
    }
    expect(f.budget.usedBytes).toBe(0)
  })
})
