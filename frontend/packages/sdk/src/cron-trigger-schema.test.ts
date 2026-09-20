import { describe, expect, it } from 'vitest'

import type { CreateCronTriggerRequest, CronTrigger } from './generated/types.gen'
import {
  zCreateCronTriggerRequest,
  zListCronTriggersResponse2,
  zUpdateCronTriggerRequest,
} from './generated/zod.gen'
import { relaxedResponseValidator } from './validate-response'

function request(openingMessageTemplate: string): CreateCronTriggerRequest {
  return {
    name: 'daily-report',
    cron: '0 9 * * *',
    timezone: 'UTC',
    enabled: true,
    message_template: 'Write the daily report.',
    target: {
      type: 'app_launch',
      app_id: `app_${'a'.repeat(26)}`,
      agent_profile_id: `aprf_${'a'.repeat(26)}`,
      destination: { channel_id: 'C123' },
      opening_message_template: openingMessageTemplate,
    },
  }
}

function listResponse(openingMessageTemplate: string) {
  const trigger: CronTrigger = {
    ...request(openingMessageTemplate),
    id: `cron_${'a'.repeat(26)}`,
    org_id: `org_${'a'.repeat(26)}`,
    project_id: `proj_${'a'.repeat(26)}`,
    timezone: 'UTC',
    enabled: true,
    last_fired_at: null,
    next_fire_at: null,
    failure_report: null,
    last_run: null,
    created_at: '2026-09-20T09:00:00Z',
    updated_at: '2026-09-20T09:00:00Z',
  }
  return { data: [trigger], next_cursor: null }
}

describe('generated app schedule opening template schemas', () => {
  it.each(['x'.repeat(1999) + '🚀', '🚀'.repeat(2000)])(
    'accepts exactly 2000 codepoints including astral characters',
    async (template) => {
      const input = request(template)
      expect(zCreateCronTriggerRequest.parse(input)).toEqual(input)
      expect(zUpdateCronTriggerRequest.parse({ target: input.target }).target).toEqual(input.target)
      await expect(
        relaxedResponseValidator(zListCronTriggersResponse2)(listResponse(template)),
      ).resolves.toBeUndefined()
    },
  )

  it.each(['x'.repeat(2000) + '🚀', '🚀'.repeat(2001), 'x'.repeat(2001), ''])(
    'rejects lengths outside 1–2000 codepoints in requests and relaxed responses',
    async (template) => {
      const input = request(template)
      expect(zCreateCronTriggerRequest.safeParse(input).success).toBe(false)
      expect(zUpdateCronTriggerRequest.safeParse({ target: input.target }).success).toBe(false)
      await expect(
        relaxedResponseValidator(zListCronTriggersResponse2)(listResponse(template)),
      ).rejects.toThrow()
    },
  )

  it('preserves whitespace, newlines, format characters and decomposed Unicode without resource-name rules', async () => {
    const template = ' \tCafe\u0301 {{.trigger.local_date}}\n\u200d\u202e🚀\ufe0f\n '
    const input = request(template)
    expect(zCreateCronTriggerRequest.parse(input)).toEqual(input)
    expect(zUpdateCronTriggerRequest.parse({ target: input.target }).target).toEqual(input.target)
    const response = listResponse(template)
    expect(zListCronTriggersResponse2.parse(response)).toEqual(response)
    await expect(
      relaxedResponseValidator(zListCronTriggersResponse2)(response),
    ).resolves.toBeUndefined()
    expect(response).toEqual(listResponse(template))
  })
})
