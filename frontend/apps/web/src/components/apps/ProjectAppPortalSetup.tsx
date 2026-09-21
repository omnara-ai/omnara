import { useOmnaraClient } from '@omnara/react'
import type { AppType } from '@omnara/sdk'
import { useState } from 'react'

import { CheckIcon, CopyIcon } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'

/** The URL a provider portal needs. It stays empty until the typed provider ID can complete it. */
export function ProjectAppPortalSetup({
  appType,
  providerId,
}: {
  appType: Exclude<AppType, 'slack_thread'>
  providerId: string
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
          : 'In the Developer Portal, paste this into General Information → Interactions Endpoint URL and save.'}
      </FieldDescription>
    </Field>
  )
}
