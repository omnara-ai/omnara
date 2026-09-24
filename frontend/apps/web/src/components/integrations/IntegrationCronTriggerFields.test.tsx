/** @vitest-environment happy-dom */

import { type IntegrationCapabilityDefinition, schemas } from '@omnara/sdk'
import { act } from 'react'
import { expect, it, vi } from 'vitest'

import {
  cronPath,
  orgId,
  path,
  profileRoutes,
  projectId,
  render,
  submit,
  trigger,
} from '@/components/agents/cron-trigger-test-fixture'
import { EditCronTriggerDialog } from '@/components/agents/CronTriggerDialog'
import { ProjectIntegrationDetail } from '@/routes/ProjectIntegrationPage'
import { fakeApi, jsonResponse } from '@/test/fake-api'
import { projectIntegration } from '@/test/fixtures'
import { button, enter, field, waitForUI } from '@/test/secret-editor'

it('uses the declared schedule capability, defaults, order and constraints without thread fields', async () => {
  const integration = projectIntegration({
    integration_type: 'github_pr',
    state: 'active',
    capabilities: {
      tools: {},
      schedule: {
        description: 'Check a work queue.',
        input_schema: {
          type: 'object',
          additionalProperties: false,
          required: ['queue'],
          'x-omnara-field-order': ['queue', 'note'],
          properties: {
            note: {
              type: 'string',
              title: 'Note',
              maxLength: 2,
              default: '🚀🚀',
              'x-omnara-control': 'textarea',
            },
            queue: { type: 'string', title: 'Queue', pattern: '^job-[0-9]+$', default: 'job-1' },
          },
        },
      },
    },
  })
  const api = fakeApi([
    {
      method: 'GET',
      path: path + '/integrations/' + integration.id,
      respond: () => Response.json(integration),
    },
    {
      method: 'GET',
      path: cronPath,
      respond: () => Response.json({ data: [], next_cursor: null }),
    },
    {
      method: 'POST',
      path: cronPath,
      respond: ({ body }) =>
        Response.json(trigger(schemas.zCreateCronTriggerRequest.parse(body)), { status: 201 }),
    },
  ])
  render(
    api,
    <ProjectIntegrationDetail
      orgId={orgId}
      projectId={projectId}
      integrationId={integration.id}
      canManage
    />,
  )
  await waitForUI(() => {
    expect(button('Add schedule')).toBeDefined()
  })
  act(() => {
    button('Add schedule').click()
  })
  expect(document.body.textContent).toContain('Check a work queue.')
  expect(field('Queue').value).toBe('job-1')
  expect(field('Note').value).toBe('🚀🚀')
  expect(field('Note')).toBeInstanceOf(HTMLTextAreaElement)
  expect(
    [...document.querySelectorAll('[role="dialog"] label')].map((label) => label.textContent),
  ).toEqual(['Name', 'Queue', 'Note', 'Cron expression', 'Timezone'])
  expect(document.querySelector('#integration-profiles')).toBeNull()
  await enter('Name', 'queue-check')
  await enter('Cron expression', '0 9 * * *')
  await enter('Queue', 'invalid')
  await submit()
  expect(api.requestsTo('POST', cronPath)).toHaveLength(0)
  await enter('Queue', 'job-2')
  await enter('Note', '🚀🚀🚀')
  await submit()
  expect(api.requestsTo('POST', cronPath)).toHaveLength(0)
  expect(document.body.textContent).toContain('Use at most 2 characters.')
  await enter('Note', '🚀🚀')
  await submit()
  await waitForUI(() => {
    expect(document.querySelector('[role="dialog"]')).toBeNull()
  })
  expect(api.requestsTo('POST', cronPath)[0]?.body).toEqual({
    name: 'queue-check',
    cron: '0 9 * * *',
    timezone: new Intl.DateTimeFormat().resolvedOptions().timeZone,
    target: {
      type: 'integration',
      integration_id: integration.id,
      settings: { queue: 'job-2', note: '🚀🚀' },
    },
  })
  expect(api.requestsTo('GET', path + '/agent-profiles')).toHaveLength(0)
})

it.each(['nested schema', 'additional saved settings'])(
  'keeps a JSON editor for %s, preserves settings and blocks malformed drafts',
  async (scenario) => {
    const settings = { queue: 'jobs', options: { retries: 2, labels: ['one', 'two'] } }
    const properties: IntegrationCapabilityDefinition['input_schema'] = {
      queue: { type: 'string' },
    }
    if (scenario === 'nested schema')
      properties.options = {
        type: 'object',
        properties: {
          retries: { type: 'integer' },
          labels: { type: 'array', items: { type: 'string' } },
        },
      }
    const integration = projectIntegration({
      state: 'active',
      capabilities: {
        tools: {},
        schedule: {
          description: 'Process jobs.',
          input_schema: {
            type: 'object',
            additionalProperties: false,
            properties,
          },
        },
      },
    })
    const saved = trigger({
      target: { type: 'integration', integration_id: integration.id, settings },
    })
    let fail = true
    const onOpenChange = vi.fn()
    const api = fakeApi([
      {
        method: 'GET',
        path: path + '/integrations/' + integration.id,
        respond: () => Response.json(integration),
      },
      {
        method: 'PATCH',
        path: cronPath + '/' + saved.id,
        respond: ({ body }) =>
          fail
            ? jsonResponse({ code: 'invalid_argument', error: 'Settings need another field' }, 400)
            : Response.json({ ...saved, ...schemas.zUpdateCronTriggerRequest.parse(body) }),
      },
    ])
    render(
      api,
      <EditCronTriggerDialog
        open
        onOpenChange={onOpenChange}
        orgId={orgId}
        projectId={projectId}
        trigger={saved}
      />,
    )
    await waitForUI(() => {
      expect(field('Settings (JSON)').value).toBe(JSON.stringify(settings, null, 2))
    })
    for (const invalid of ['{', '[]', 'null']) {
      await enter('Settings (JSON)', invalid)
      expect(button('Save changes').disabled).toBe(true)
      await submit()
    }
    expect(api.requestsTo('PATCH', cronPath + '/' + saved.id)).toHaveLength(0)
    const changed = { ...settings, queue: 'next', options: { ...settings.options, retries: 4 } }
    const draft = JSON.stringify(changed, null, 4)
    await enter('Settings (JSON)', draft)
    await submit()
    await waitForUI(() => {
      expect(document.body.textContent).toContain('Settings need another field')
    })
    expect(field('Settings (JSON)').value).toBe(draft)
    fail = false
    await submit()
    await waitForUI(() => {
      expect(onOpenChange).toHaveBeenCalledWith(false)
    })
    expect(api.requestsTo('PATCH', cronPath + '/' + saved.id)[1]?.body).toEqual({
      name: saved.name,
      cron: saved.cron,
      timezone: saved.timezone,
      target: { type: 'integration', integration_id: integration.id, settings: changed },
    })
  },
)

it('blocks saving until the integration schedule schema loads and retries it from the dialog', async () => {
  const integration = projectIntegration({ state: 'active' })
  const saved = trigger()
  let available = false
  const api = fakeApi([
    ...profileRoutes,
    {
      method: 'GET',
      path: path + '/integrations/' + integration.id,
      respond: () =>
        available
          ? Response.json(integration)
          : jsonResponse({ code: 'unavailable', error: 'Later' }, 503),
    },
    {
      method: 'PATCH',
      path: cronPath + '/' + saved.id,
      respond: ({ body }) =>
        Response.json({ ...saved, ...schemas.zUpdateCronTriggerRequest.parse(body) }),
    },
  ])
  render(
    api,
    <EditCronTriggerDialog
      open
      onOpenChange={vi.fn()}
      orgId={orgId}
      projectId={projectId}
      trigger={saved}
    />,
  )
  await waitForUI(() => {
    expect(document.body.textContent).toContain('Could not load schedule settings.')
  })
  expect(button('Save changes').disabled).toBe(true)
  await submit()
  expect(api.requestsTo('PATCH', cronPath + '/' + saved.id)).toHaveLength(0)
  available = true
  act(() => {
    button('Retry settings').click()
  })
  await waitForUI(() => {
    expect(field('Channel ID').value).toBe('C123')
  })
  expect(button('Save changes').disabled).toBe(false)
})
