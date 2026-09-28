import type { Integration } from '@omnara/sdk'
import { type ReactNode, useState } from 'react'

import { Button } from '@/components/ui/button'

import { GitHubGuidedSetup } from './GitHubGuidedSetup'
import { IntegrationSetupForm } from './IntegrationSetup'
import { useGitHubGuidedSetup } from './useGitHubGuidedSetup'
import { useGitHubSetupReturn } from './useGitHubSetupReturn'
import { useIntegrationDraft } from './useIntegrationDraft'
import { useIntegrationSetupState } from './useIntegrationSetupState'

export function ConnectGitHubForm({
  orgId,
  projectId,
  integration: existing,
  onConnected,
  onCancel,
  footerAction,
}: {
  orgId: string
  projectId: string
  integration?: Integration
  onConnected: (integration: Integration) => void
  onCancel?: () => void
  footerAction?: ReactNode
}) {
  const draft = useIntegrationDraft(orgId, projectId, 'github_pr', existing)
  const returned = useGitHubSetupReturn()
  const session = useIntegrationSetupState(existing, {
    credentialSecretId: returned.secretId !== '' ? returned.secretId : undefined,
    error: returned.error,
  })
  const guided = useGitHubGuidedSetup({
    orgId,
    projectId,
    ensureIntegration: draft.ensureIntegration,
    session,
    installationHint: returned.installationId,
    onConnected,
  })
  const [manual, setManual] = useState(
    returned.recoverManually || (Boolean(existing?.provider_tenant_id) && !returned.secretId),
  )
  const trustNotice = (
    <p className="text-muted-foreground text-sm">
      By default, people with repository write access can trigger mention launches and steer PR
      agents through comments. Choose Anyone in Who can direct agents to allow all human commenters.
      Public and fork PRs can trigger configured PR-open launches. Choose a profile whose tools and
      secrets are appropriate for untrusted input.
    </p>
  )

  if (manual)
    return (
      <div className="flex flex-col gap-4">
        {trustNotice}
        {!draft.integration?.provider_tenant_id && (
          <Button
            type="button"
            variant="link"
            className="self-start px-0"
            disabled={session.busy}
            onClick={() => {
              guided.returnToGuided()
              setManual(false)
            }}
          >
            Back to guided setup
          </Button>
        )}
        <IntegrationSetupForm
          orgId={orgId}
          projectId={projectId}
          integration={existing}
          draft={draft}
          state={session}
          integrationKind="github_pr"
          onSaved={onConnected}
          onCancel={onCancel}
          footerAction={footerAction}
        />
      </div>
    )

  return (
    <div className="flex flex-col gap-4">
      {trustNotice}
      <GitHubGuidedSetup
        orgId={orgId}
        projectId={projectId}
        existing={existing}
        draft={draft}
        guided={guided}
        busy={session.busy}
        error={session.error}
        onUseExistingApp={() => {
          guided.handOffToManual()
          setManual(true)
        }}
        onCancel={onCancel}
        footerAction={footerAction}
      />
    </div>
  )
}
