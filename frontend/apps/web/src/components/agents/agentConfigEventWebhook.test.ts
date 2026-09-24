import { describe, expect, it } from 'vitest'
import { parse } from 'yaml'

import { eventWebhookEventTypes } from './agentConfigEventWebhook'
import { basicConfigValid, createBasicConfigSession, emptyBasicConfig } from './useAgentBuilderForm'

describe('event webhook', () => {
  it.each([
    ['', true],
    ['https://example.com/events', true],
    [' https://example.com/events ', true],
    ['http://example.com/events', false],
    ['http://localhost:5174/events', false],
    ['ftp://example.com/events', false],
    ['example.com/events', false],
    ['https://', false],
  ])('validates the webhook URL %j before saving', (eventWebhookUrl, valid) => {
    expect(
      basicConfigValid({
        ...emptyBasicConfig,
        instruction: 'Webhook agent',
        providerConfig: 'openai',
        modelName: 'test-model',
        eventWebhookUrl,
      }),
    ).toBe(valid)
  })

  it.each(
    [[], ['tool_call_update'], ['model_output', 'tool_call_update']].map((events) => ({ events })),
  )('preserves the event filter %j through unrelated edits', ({ events }) => {
    const source = createBasicConfigSession('').apply({
      ...emptyBasicConfig,
      instruction: 'Webhook agent',
      providerConfig: 'openai',
      modelName: 'test-model',
      eventWebhookUrl: 'https://example.com/events',
      eventWebhookEvents: events,
    })
    const session = createBasicConfigSession(source)
    if (session.initialDraft === null) throw new Error('Expected builder config')
    expect(session.initialDraft.eventWebhookEvents).toEqual(events)
    const edited: unknown = parse(
      session.apply({ ...session.initialDraft, instruction: 'Updated' }),
    )
    expect(edited).toHaveProperty('event_webhook.events', events)
    const all: unknown = parse(
      session.apply({
        ...session.initialDraft,
        eventWebhookEvents: eventWebhookEventTypes.map((event) => event.value),
      }),
    )
    expect(all).toHaveProperty(
      'event_webhook.events',
      eventWebhookEventTypes.map((event) => event.value),
    )
  })

  it('requires a nonempty custom selection only when a webhook is configured', () => {
    const config = {
      ...emptyBasicConfig,
      instruction: 'Webhook agent',
      providerConfig: 'openai',
      modelName: 'test-model',
      eventWebhookUrl: 'https://example.com/events',
      eventWebhookEvents: [],
    }
    expect(basicConfigValid(config)).toBe(false)
    expect(
      basicConfigValid({
        ...config,
        eventWebhookEvents: eventWebhookEventTypes.map((event) => event.value),
      }),
    ).toBe(true)
    expect(basicConfigValid({ ...config, eventWebhookEvents: ['model_output'] })).toBe(true)
    expect(basicConfigValid({ ...config, eventWebhookUrl: '' })).toBe(true)
  })

  it('defaults new webhooks to tool-call updates without treating a missing YAML list as all', () => {
    expect(emptyBasicConfig.eventWebhookEvents).toEqual(['tool_call_update'])
    const session = createBasicConfigSession(
      'instruction: Test\nmodel: {provider_config: openai, name: test}\nevent_webhook: {url: https://example.com/events}\n',
    )
    expect(session.initialDraft?.eventWebhookEvents).toEqual([])
    if (session.initialDraft === null) throw new Error('Expected builder config')
    expect(basicConfigValid(session.initialDraft)).toBe(false)
  })

  it('round trips the URL and signing secret through builder edits and supports removal', () => {
    const source = createBasicConfigSession('').apply({
      ...emptyBasicConfig,
      instruction: 'Webhook agent',
      providerConfig: 'openai',
      modelName: 'test-model',
      eventWebhookUrl: 'https://example.com/events',
      eventWebhookSigningSecretId: 'sec_example',
    })
    const session = createBasicConfigSession(source)
    expect(session.initialDraft?.eventWebhookUrl).toBe('https://example.com/events')
    if (session.initialDraft === null) throw new Error('Expected builder config')
    const edited = session.apply({ ...session.initialDraft, instruction: 'Updated instructions' })
    const editedDocument: unknown = parse(edited)
    expect(editedDocument).toHaveProperty('event_webhook', {
      url: 'https://example.com/events',
      signing_secret_id: 'sec_example',
      events: ['tool_call_update'],
    })
    const unsigned: unknown = parse(
      session.apply({ ...session.initialDraft, eventWebhookSigningSecretId: '' }),
    )
    expect(unsigned).toHaveProperty('event_webhook', {
      url: 'https://example.com/events',
      events: ['tool_call_update'],
    })
    const removed = session.apply({ ...session.initialDraft, eventWebhookUrl: '' })
    const removedDocument: unknown = parse(removed)
    expect(removedDocument).not.toHaveProperty('event_webhook')
  })
})
