import type { AppSubscription, ProjectApp } from '@omnara/sdk'
import { z } from 'zod'

const slackAddress = z.object({ channel_id: z.string(), thread_ts: z.string().optional() })
const discordAddress = z.object({
  channel_id: z.string(),
  thread_id: z.string().optional(),
})
const githubAddress = z.object({ repository_id: z.number(), pull_request: z.number() })

/** Format indexed provider addresses without fetching provider metadata. */
export function appConversation(app: ProjectApp, subscription: AppSubscription) {
  switch (app.app_type) {
    case 'slack_thread': {
      const address = slackAddress.safeParse(subscription.conversation)
      if (!address.success) break
      const { channel_id, thread_ts } = address.data
      const channel = `Channel ${channel_id}`
      const destination = new URLSearchParams({ channel: channel_id, team: app.provider_tenant_id })
      return {
        label: thread_ts ? `${channel} · Thread ${thread_ts}` : channel,
        href: app.provider_tenant_id
          ? `https://slack.com/app_redirect?${destination.toString()}`
          : undefined,
      }
    }
    case 'discord_thread': {
      const address = discordAddress.safeParse(subscription.conversation)
      if (!address.success) break
      const { channel_id, thread_id } = address.data
      // Indexed subscription addresses contain no guild ID for a Discord URL.
      return {
        label: `Channel ${channel_id}${thread_id ? ` · Thread ${thread_id}` : ''}`,
      }
    }
    case 'github_pr': {
      const address = githubAddress.safeParse(subscription.conversation)
      if (!address.success) break
      // The API supplies a repository ID, not the owner/name needed for a PR URL.
      return {
        label: `Repository ${address.data.repository_id} · PR #${address.data.pull_request}`,
      }
    }
  }
  return { label: JSON.stringify(subscription.conversation) }
}
