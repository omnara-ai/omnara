import type { Integration, IntegrationKind } from '@omnara/sdk'

import { ArrowUpRight } from '@/components/icons'
import { docsUrl } from '@/lib/docs'

import { useIntegrationSetupURLs } from './useIntegrationSetupURLs'

/** Docs sections describing the tools each integration adds to the agents it launches. */
const integrationToolDocs = {
  slack_thread: 'integrations/slack#thread-tools-and-subscriptions',
  discord_thread: 'integrations/discord#thread-tools-and-subscriptions',
  github_pr: 'integrations/github#select-pr-tools',
} satisfies Record<IntegrationKind, string>

export function IntegrationAdvanced({ integration }: { integration: Integration }) {
  return (
    <section aria-label="Advanced" className="flex flex-col gap-6 text-sm">
      <IntegrationConnectionDetails integration={integration} />
      <IntegrationAgentConfigurations integration={integration} />
    </section>
  )
}

function IntegrationConnectionDetails({ integration }: { integration: Integration }) {
  const { apiOrigin, unavailable } = useIntegrationSetupURLs()
  const providerId = integration.provider_tenant_id ?? ''
  return (
    <div className="flex flex-col gap-2">
      <h3 className="font-medium">Connection details</h3>
      {providerId ? (
        <p className="text-muted-foreground break-words">
          {integration.integration_kind === 'slack_thread' ? 'Workspace' : 'Application'}{' '}
          {providerId} · {integration.provider_account_ref}
        </p>
      ) : (
        <p className="text-muted-foreground">No account has been connected.</p>
      )}
      {integration.integration_kind === 'github_pr' && (
        <p className="text-muted-foreground">
          Webhook URL:{' '}
          <code className="text-foreground break-all">
            {apiOrigin
              ? `${apiOrigin}/api/integrations/github/events`
              : unavailable
                ? 'Public API URL unavailable'
                : 'Loading setup URL…'}
          </code>
          . Subscribe to pull requests, issue comments, pull request reviews, and pull request
          review comments.
        </p>
      )}
      {integration.integration_kind === 'discord_thread' && (
        <p className="text-muted-foreground">
          Interactions Endpoint URL:{' '}
          <code className="text-foreground break-all">
            {apiOrigin
              ? `${apiOrigin}/api/integrations/discord/${providerId || 'APPLICATION_ID'}/interactions`
              : unavailable
                ? 'Public API URL unavailable'
                : 'Loading setup URL…'}
          </code>
        </p>
      )}
    </div>
  )
}

function IntegrationAgentConfigurations({ integration }: { integration: Integration }) {
  return (
    <div className="flex flex-col gap-2">
      <h3 className="font-medium">Agent configuration</h3>
      <p className="text-muted-foreground">
        Connected agents automatically get tools added to their profile. See the docs for more info.
      </p>
      <a
        href={docsUrl(integrationToolDocs[integration.integration_kind])}
        target="_blank"
        rel="noreferrer"
        className="text-muted-foreground hover:text-foreground inline-flex w-fit items-center gap-1 transition-colors"
      >
        Tools added to the agent profile
        <ArrowUpRight className="size-3.5" aria-hidden="true" />
      </a>
      {integration.capabilities.interaction_handler && (
        <>
          <h4 className="pt-2 font-medium">Interaction handler</h4>
          <p className="text-muted-foreground">
            Listed under <code>interaction_handlers</code> for questions and approvals in the
            launched conversation: <code className="text-foreground">{integration.name}</code>
          </p>
        </>
      )}
    </div>
  )
}
