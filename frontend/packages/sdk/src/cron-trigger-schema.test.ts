import { describe, expect, it } from 'vitest'

import type {
  AppCronTriggerTarget,
  CreateCronTriggerRequest,
  CronTrigger,
} from './generated/types.gen'
import {
  zCreateCronTriggerRequest,
  zListCronTriggersResponse2,
  zUpdateCronTriggerRequest,
} from './generated/zod.gen'
import { relaxedResponseValidator } from './validate-response'

function request(settings: AppCronTriggerTarget['settings']): CreateCronTriggerRequest {
  return {
    name: 'daily-report',
    cron: '0 9 * * *',
    timezone: 'UTC',
    enabled: true,
    target: { type: 'app', app_id: `app_${'a'.repeat(26)}`, settings },
  }
}

function listResponse(settings: AppCronTriggerTarget['settings']) {
  const trigger: CronTrigger = {
    ...request(settings),
    id: `cron_${'a'.repeat(26)}`,
    org_id: `org_${'a'.repeat(26)}`,
    project_id: `proj_${'a'.repeat(26)}`,
    timezone: 'UTC',
    enabled: true,
    last_fired_at: null,
    next_fire_at: null,
    failure_report: null,
    last_run: {
      state: 'completed',
      created_at: '2026-09-20T09:00:00Z',
      updated_at: '2026-09-20T09:00:00Z',
      failure_message: null,
    },
    created_at: '2026-09-20T09:00:00Z',
    updated_at: '2026-09-20T09:00:00Z',
  }
  return { data: [trigger], next_cursor: null }
}

describe('app scheduled action schemas', () => {
  it.each([
    {},
    { nested: { enabled: true, count: 3 }, items: ['one', null, 2] },
    {
      agent_profile_id: `aprf_${'a'.repeat(26)}`,
      channel_id: 'C123',
      opening_message_template: ' \tCafe\u0301 {{.trigger.local_date}}\n\u200d\u202e🚀\ufe0f\n ',
      message_template: 'Write the daily report.',
    },
  ])(
    'preserves opaque app settings without requiring a top-level message or profile',
    async (settings) => {
      const input = request(settings)
      expect(zCreateCronTriggerRequest.parse(input)).toEqual(input)
      expect(zUpdateCronTriggerRequest.parse({ target: input.target }).target).toEqual(input.target)
      const response = listResponse(settings)
      expect(zListCronTriggersResponse2.parse(response)).toEqual(response)
      await expect(
        relaxedResponseValidator(zListCronTriggersResponse2)(response),
      ).resolves.toBeUndefined()
      expect(response).toEqual(listResponse(settings))
    },
  )

  it.each([null, [], 'settings', 3])('rejects non-object settings: %j', (settings) => {
    const input = request({})
    const target = { ...input.target, settings }
    expect(zCreateCronTriggerRequest.safeParse({ ...input, target }).success).toBe(false)
    expect(zUpdateCronTriggerRequest.safeParse({ target }).success).toBe(false)
  })
})
