import type { ProjectApp } from '@omnara/sdk'
import { type ReactNode, useState } from 'react'

import { Button } from '@/components/ui/button'

import { GitHubGuidedSetup } from './GitHubGuidedSetup'
import { ProjectAppSetupForm } from './ProjectAppSetup'
import { useGitHubGuidedSetup } from './useGitHubGuidedSetup'
import { useGitHubSetupReturn } from './useGitHubSetupReturn'
import { useProjectAppDraft } from './useProjectAppDraft'
import { useProjectAppSetupState } from './useProjectAppSetupState'

export function ConnectGitHubForm({
  orgId,
  projectId,
  app: existing,
  onConnected,
  onCancel,
  footerAction,
}: {
  orgId: string
  projectId: string
  app?: ProjectApp
  onConnected: (app: ProjectApp) => void
  onCancel?: () => void
  footerAction?: ReactNode
}) {
  const draft = useProjectAppDraft(orgId, projectId, 'github_pr', existing)
  const returned = useGitHubSetupReturn()
  const session = useProjectAppSetupState(existing, {
    credentialSecretId: returned.secretId !== '' ? returned.secretId : undefined,
    error: returned.error,
  })
  const guided = useGitHubGuidedSetup({
    orgId,
    projectId,
    ensureApp: draft.ensureApp,
    session,
    installationHint: returned.installationId,
    onConnected,
  })
  const [manual, setManual] = useState(
    returned.recoverManually || (Boolean(existing?.provider_tenant_id) && !returned.secretId),
  )

  if (manual)
    return (
      <div className="flex flex-col gap-4">
        {!draft.app?.provider_tenant_id && (
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
        <ProjectAppSetupForm
          orgId={orgId}
          projectId={projectId}
          app={existing}
          draft={draft}
          state={session}
          appType="github_pr"
          onSaved={onConnected}
          onCancel={onCancel}
          footerAction={footerAction}
        />
      </div>
    )

  return (
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
  )
}
