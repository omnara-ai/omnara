import { describe, expect, it } from 'vitest'
import { parse } from 'yaml'

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

  it.each([null, [], ['model_output', 'tool_call_update']])(
    'preserves the event filter %j through unrelated edits',
    (events) => {
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
      if (events === null) expect(edited).not.toHaveProperty('event_webhook.events')
      else expect(edited).toHaveProperty('event_webhook.events', events)
      const all: unknown = parse(
        session.apply({ ...session.initialDraft, eventWebhookEvents: null }),
      )
      expect(all).not.toHaveProperty('event_webhook.events')
    },
  )

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
    expect(basicConfigValid({ ...config, eventWebhookEvents: null })).toBe(true)
    expect(basicConfigValid({ ...config, eventWebhookEvents: ['model_output'] })).toBe(true)
    expect(basicConfigValid({ ...config, eventWebhookUrl: '' })).toBe(true)
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
    })
    const unsigned: unknown = parse(
      session.apply({ ...session.initialDraft, eventWebhookSigningSecretId: '' }),
    )
    expect(unsigned).toHaveProperty('event_webhook', { url: 'https://example.com/events' })
    const removed = session.apply({ ...session.initialDraft, eventWebhookUrl: '' })
    const removedDocument: unknown = parse(removed)
    expect(removedDocument).not.toHaveProperty('event_webhook')
  })
})
