import { useOmnaraClient } from '@omnara/react'
import type { AppType } from '@omnara/sdk'
import { useState } from 'react'

import { CheckIcon, CopyIcon } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'

// Add Reactions, View Channels, Send Messages, Attach Files, Read Message History,
// Create Public Threads and Send Messages in Threads. No administrator access.
const discordBotPermissions = '309237746752'

/** The URL a provider portal needs. It stays empty until the typed provider ID can complete it. */
export function ProjectAppPortalSetup({
  appType,
  providerId,
  title = 'Finish in Discord',
}: {
  appType: Exclude<AppType, 'slack_thread'>
  providerId: string
  title?: string
}) {
  const client = useOmnaraClient()
  const [copied, setCopied] = useState('')
  const [copyFailed, setCopyFailed] = useState(false)
  const github = appType === 'github_pr'
  const apiOrigin = new URL(client.getConfig().baseUrl ?? '/api/v1', window.location.origin).origin
  const url = /^[1-9][0-9]*$/.test(providerId)
    ? `${apiOrigin}/api/integrations/${github ? `github/${providerId}/events` : `discord/${providerId}/interactions`}`
    : ''
  return (
    <div className="flex flex-col gap-4 text-sm">
      {!github && <h2 className="font-medium">{title}</h2>}
      <Field>
        <FieldLabel htmlFor="provider-endpoint">
          {github ? 'Webhook URL' : 'Interactions Endpoint URL'}
        </FieldLabel>
        <div className="flex gap-2">
          <Input
            id="provider-endpoint"
            readOnly
            value={url}
            placeholder="Enter the App ID above"
            className="font-mono"
            onFocus={(event) => {
              event.currentTarget.select()
            }}
          />
          <Button
            type="button"
            variant="outline"
            disabled={!url}
            icon={copied === url && url ? <CheckIcon /> : <CopyIcon />}
            onClick={() => {
              setCopyFailed(false)
              void navigator.clipboard.writeText(url).then(
                () => {
                  setCopied(url)
                },
                () => {
                  setCopied('')
                  setCopyFailed(true)
                },
              )
            }}
          >
            {copied === url && url ? 'Copied' : 'Copy'}
          </Button>
        </div>
        {copyFailed && (
          <p role="alert" className="text-destructive text-sm">
            Could not copy. Select the URL and copy it manually.
          </p>
        )}
        <FieldDescription>
          {github
            ? 'In your GitHub App’s settings, set this as the webhook URL with the webhook secret above, and subscribe to pull requests, issue comments, and pull request review comments.'
            : 'In the Discord Developer Portal, paste this into General Information → Interactions Endpoint URL and save. This enables profile choices and answers to agent questions.'}
        </FieldDescription>
        {!github && (
          <div className="flex flex-col items-start gap-2 pt-3">
            <p className="text-muted-foreground text-sm">
              Under Installation, make sure Guild Install is enabled. Then add the bot to your
              server below. Already installed? Make sure its role also allows Add Reactions.
            </p>
            {url && (
              <Button asChild variant="outline">
                <a
                  href={`https://discord.com/oauth2/authorize?client_id=${providerId}&scope=bot&permissions=${discordBotPermissions}&integration_type=0`}
                  target="_blank"
                  rel="noreferrer"
                >
                  Add bot to server
                </a>
              </Button>
            )}
            <p className="text-muted-foreground text-xs">
              Choose a server you manage. Discord will ask for access to read messages, reply in
              threads, react to messages and attach files.
            </p>
          </div>
        )}
      </Field>
    </div>
  )
}
