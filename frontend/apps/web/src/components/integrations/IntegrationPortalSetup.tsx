import type { IntegrationKind } from '@omnara/sdk'
import { useState } from 'react'

import { CheckIcon, CopyIcon } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'

import { useIntegrationSetupURLs } from './useIntegrationSetupURLs'

const addReactions = 1n << 6n
const viewChannels = 1n << 10n
const sendMessages = 1n << 11n
const attachFiles = 1n << 15n
const readMessageHistory = 1n << 16n
const createPublicThreads = 1n << 35n
const sendMessagesInThreads = 1n << 38n
const discordBotPermissions =
  addReactions |
  viewChannels |
  sendMessages |
  attachFiles |
  readMessageHistory |
  createPublicThreads |
  sendMessagesInThreads

export function IntegrationPortalSetup({
  integrationKind,
  providerId = '',
  title = 'Finish in Discord',
}: {
  integrationKind: Exclude<IntegrationKind, 'slack_thread'>
  providerId?: string
  title?: string
}) {
  const { apiOrigin, unavailable } = useIntegrationSetupURLs()
  const [copied, setCopied] = useState('')
  const [copyFailed, setCopyFailed] = useState(false)
  const github = integrationKind === 'github_pr'
  const url = apiOrigin
    ? github
      ? `${apiOrigin}/api/integrations/github/events`
      : /^[1-9][0-9]*$/.test(providerId)
        ? `${apiOrigin}/api/integrations/discord/${providerId}/interactions`
        : ''
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
            placeholder={
              !apiOrigin
                ? unavailable
                  ? 'Public API URL unavailable'
                  : 'Loading setup URL…'
                : 'Enter the App ID above'
            }
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
            ? 'In your GitHub App’s settings, set this as the webhook URL with the webhook secret above, and subscribe to pull_request, issue_comment, pull_request_review, and pull_request_review_comment. Set Pull requests permission to Read and write, Issues to Read-only, and Contents to Read-only so agents can clone repositories.'
            : 'In the Discord Developer Portal, paste this into General Information → Interactions Endpoint URL and save. This enables profile choices and answers to agent questions.'}
        </FieldDescription>
        {!github && (
          <div className="flex flex-col items-start gap-2 pt-3">
            <p className="text-muted-foreground text-sm">
              Under Installation, make sure Guild Install is enabled. Then add the bot to your
              server below. Already installed? Make sure its role also allows Add Reactions. People
              in conversations the bot can access can launch offered profiles and answer agent
              questions and approvals, even without an Omnara account.
            </p>
            {url && (
              <Button asChild variant="outline">
                <a
                  href={`https://discord.com/oauth2/authorize?client_id=${providerId}&scope=bot&permissions=${discordBotPermissions}&integration_kind=0`}
                  target="_blank"
                  rel="noreferrer"
                >
                  Add bot to server
                </a>
              </Button>
            )}
            <p className="text-muted-foreground text-xs">
              Choose a server you manage. Discord will ask for access to read messages, reply in
              threads, react to messages and attach files. For internal use, turn off Public Bot in
              the Bot settings when available, and review existing servers and channel access.
              Public bots are supported; Discord controls who can install them.
            </p>
          </div>
        )}
      </Field>
    </div>
  )
}
