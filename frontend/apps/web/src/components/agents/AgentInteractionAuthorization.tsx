import type { AgentInteraction } from '@omnara/sdk'
import { useState } from 'react'

import { KeyRound } from '@/components/icons'
import { OAuthOutcomeDialog } from '@/components/oauth/OAuthOutcomeDialog'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'

import { keyedContextItems, type Resolve, subagentSourceLabel } from './agentInteractionCards'

export type Authorize = (interaction: AgentInteraction) => Promise<void>

const authorizationDeclineOptionIndex = 1

/**
 * An MCP server asked for OAuth access the agent's credential lacks. Granting
 * access leaves for the server's consent screen; the OAuth callback resolves the
 * interaction, so only Decline is answered here.
 */
function AuthorizationActions({
  interaction,
  resolve,
  authorize,
  pending,
  canOperate,
}: {
  interaction: AgentInteraction
  resolve: Resolve
  authorize: Authorize
  pending: boolean
  canOperate: boolean
}) {
  const [chosen, setChosen] = useState<'grant' | 'decline' | null>(null)
  const disabled = !canOperate || pending
  const connecting = interaction.authorization?.secret_id == null
  return (
    <div className="flex min-w-0 flex-wrap items-center justify-end gap-2">
      <Button
        type="button"
        size="sm"
        variant="outline"
        className="min-w-20"
        disabled={disabled}
        loading={pending && chosen === 'decline'}
        onClick={() => {
          setChosen('decline')
          void resolve(interaction, {
            answers: [{ option_indices: [authorizationDeclineOptionIndex] }],
          }).catch(() => undefined)
        }}
      >
        Decline
      </Button>
      <Button
        type="button"
        size="sm"
        className="min-w-20"
        disabled={disabled}
        loading={pending && chosen === 'grant'}
        onClick={() => {
          setChosen('grant')
          void authorize(interaction).catch(() => {
            setChosen(null)
          })
        }}
      >
        {connecting ? 'Connect' : 'Grant access'}
      </Button>
    </div>
  )
}

export function AuthorizationOutcomeDialog() {
  return (
    <OAuthOutcomeDialog
      successParam="mcp_oauth"
      errorParam="mcp_oauth_error"
      extraParams={['secret_id', 'interaction_id']}
      successOutcome={() => ({
        title: 'Access granted',
        description: 'The MCP server authorized access. The agent is retrying the tool call.',
      })}
      errorOutcome={(code) => ({
        title: 'Authorization failed',
        description: authorizationErrorDescription(code),
      })}
    />
  )
}

function authorizationErrorDescription(code: string) {
  switch (code) {
    case 'insufficient_scope_granted':
      return 'The MCP server granted less access than the tool needs. Try again and approve every requested permission.'
    case 'account_mismatch':
      return 'You signed in to a different account than the one already connected. Try again with the same account.'
    case 'agent_config_update_failed':
      return "The credential was saved, but the agent's config could not be updated. Add the secret to the MCP server's auth in the agent config."
    case 'interaction_resolve_failed':
      return 'The credential was saved, but the waiting tool call could not be resumed. Ask the agent to try again.'
    case 'access_denied':
      return 'Authorization was denied. The request is still waiting if you want to try again.'
    default:
      return 'MCP authorization failed. Please try again.'
  }
}

/** An authorization request rendered with its own grant and decline actions. */
export function AuthorizationCard({
  interaction,
  resolve,
  authorize,
  pending,
  canOperate,
}: {
  interaction: AgentInteraction
  resolve: Resolve
  authorize: Authorize
  pending: boolean
  canOperate: boolean
}) {
  const contextItems = keyedContextItems(interaction)
  const subagentLabel = subagentSourceLabel(interaction)
  return (
    <Card className="border-primary/35 bg-primary/[0.04] min-w-0 gap-3 shadow-none">
      <CardHeader className="min-w-0 gap-1">
        <CardTitle className="text-link flex flex-wrap items-center gap-2 text-sm wrap-anywhere">
          <KeyRound className="size-4 shrink-0" />
          {interaction.tool_name == null ? (
            interaction.request.title
          ) : (
            <>
              Authorization requested for
              <span className="bg-muted text-foreground rounded-md px-2 py-1 font-mono text-xs">
                {interaction.tool_name}
              </span>
            </>
          )}
        </CardTitle>
      </CardHeader>
      <CardContent className="grid min-w-0 gap-4">
        {subagentLabel != null && (
          <p className="text-muted-foreground text-xs wrap-anywhere">{subagentLabel}</p>
        )}
        {contextItems.length > 0 && (
          <dl className="bg-background grid min-w-0 gap-2 rounded-md border p-3 text-xs">
            {contextItems.map(({ item, key }) => (
              <div key={key} className="grid min-w-0 gap-1">
                <dt className="text-muted-foreground font-medium wrap-anywhere">{item.label}</dt>
                <dd className="font-mono wrap-anywhere whitespace-pre-wrap">{item.value}</dd>
              </div>
            ))}
          </dl>
        )}
        <p className="text-sm wrap-anywhere">{interaction.request.questions[0]?.prompt}</p>
        <AuthorizationActions
          interaction={interaction}
          resolve={resolve}
          authorize={authorize}
          pending={pending}
          canOperate={canOperate}
        />
      </CardContent>
    </Card>
  )
}
