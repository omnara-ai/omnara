import type { Integration, IntegrationSubscription } from '@omnara/sdk'
import { z } from 'zod'

const slackAddress = z.object({ channel_id: z.string(), thread_ts: z.string().optional() })
const discordAddress = z.object({
  channel_id: z.string().optional(),
  thread_id: z.string().optional(),
})
const githubAddress = z.object({ repository_id: z.number(), pull_request: z.number() })

export function integrationConversation(
  integration: Integration,
  subscription: IntegrationSubscription,
) {
  const name = subscription.conversation_name ?? ''
  switch (integration.integration_kind) {
    case 'slack_thread': {
      const address = slackAddress.safeParse(subscription.conversation)
      if (!address.success) break
      const { channel_id, thread_ts } = address.data
      const channel = `Channel ${channel_id}`
      const destination = new URLSearchParams({
        channel: channel_id,
        team: integration.provider_tenant_id ?? '',
      })
      return {
        label: name || (thread_ts ? `${channel} · Thread ${thread_ts}` : channel),
        href: integration.provider_tenant_id
          ? `https://slack.com/app_redirect?${destination.toString()}`
          : undefined,
      }
    }
    case 'discord_thread': {
      const address = discordAddress.safeParse(subscription.conversation)
      if (!address.success) break
      const { channel_id, thread_id } = address.data
      if (!channel_id && !thread_id) break
      return {
        label: name || (thread_id ? `Thread ${thread_id}` : `Channel ${channel_id}`),
      }
    }
    case 'github_pr': {
      const address = githubAddress.safeParse(subscription.conversation)
      if (!address.success) break
      return {
        label:
          name || `Repository ${address.data.repository_id} · PR #${address.data.pull_request}`,
      }
    }
  }
  return { label: name || JSON.stringify(subscription.conversation) }
}
