import {
  useCompleteGitHubConnection,
  useGitHubSetupInstallations,
  useGitHubSetupRepositories,
} from '@omnara/react'
import type { GitHubSetupInstallation, GitHubSetupRepository } from '@omnara/sdk'
import { useEffect, useRef, useState } from 'react'

import { DataTable } from '@/components/data-table/DataTable'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { usePagedQuery } from '@/hooks/use-paged-query'
import { errorMessage } from '@/lib/submit-status'

import { integrationAuthorizationUrl } from './integrationOAuth'

interface GitHubSetupScope {
  orgId: string
  projectId: string
  flowId: string
}

function GitHubInstallationStep({
  orgId,
  projectId,
  flowId,
  onSelect,
}: GitHubSetupScope & { onSelect: (installation: GitHubSetupInstallation) => void }) {
  const query = useGitHubSetupInstallations(orgId, projectId, flowId)
  const [refresh, setRefresh] = useState(0)
  const paged = usePagedQuery(query, `${flowId}:${refresh}`)
  const installUrl = integrationAuthorizationUrl(query.data?.pages[0]?.installation_url ?? '')
  return (
    <div className="grid gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-muted-foreground text-sm">
          Choose the GitHub account where the app is installed.
        </p>
        <Button
          variant="outline"
          size="sm"
          disabled={query.isFetching}
          onClick={() => {
            setRefresh((value) => value + 1)
            void query.refetch()
          }}
        >
          Refresh
        </Button>
      </div>
      <DataTable
        columns={[
          {
            id: 'account',
            header: 'GitHub account',
            cell: (installation) => installation.account_login,
          },
          {
            id: 'status',
            header: 'Status',
            cell: (installation) => (installation.suspended ? 'Suspended' : 'Available'),
          },
          {
            id: 'actions',
            header: '',
            isActions: true,
            cell: (installation) => (
              <Button
                size="sm"
                variant="outline"
                disabled={installation.suspended || query.isFetching}
                aria-label={`Select ${installation.account_login}`}
                onClick={() => {
                  onSelect(installation)
                }}
              >
                Select
              </Button>
            ),
          },
        ]}
        data={paged.rows}
        pagination={paged.pagination}
        getRowId={(installation) => installation.id}
        isPending={query.isPending}
        isError={query.isError}
        onRetry={() => {
          void query.refetch()
        }}
        emptyMessage="No GitHub installations available. Install the app, then refresh this list."
      />
      {installUrl && (
        <div className="text-sm">
          <a
            href={installUrl}
            target="_blank"
            rel="noopener noreferrer"
            className="text-foreground underline underline-offset-2"
          >
            Install app on GitHub
          </a>
          <p className="text-muted-foreground mt-1">
            Opens in a new tab. Return here and refresh after installation.
          </p>
        </div>
      )}
      {query.isError && (
        <p className="text-muted-foreground text-sm">
          If this setup has expired or was started by another user, close it and connect the app
          again.
        </p>
      )}
    </div>
  )
}

function GitHubRepositoryStep({
  orgId,
  projectId,
  flowId,
  installation,
  pending,
  onConnect,
  onBack,
}: GitHubSetupScope & {
  installation: GitHubSetupInstallation
  pending: boolean
  onConnect: (repository: GitHubSetupRepository) => void
  onBack: () => void
}) {
  const query = useGitHubSetupRepositories(orgId, projectId, flowId, installation.id)
  const paged = usePagedQuery(query, installation.id)
  return (
    <fieldset disabled={pending} className="grid gap-4">
      <div className="flex items-center justify-between gap-3">
        <p className="text-sm">Repositories in {installation.account_login}</p>
        <Button size="sm" variant="ghost" disabled={pending} onClick={onBack}>
          Change account
        </Button>
      </div>
      <DataTable
        columns={[
          { id: 'repository', header: 'Repository', cell: (repository) => repository.full_name },
          {
            id: 'visibility',
            header: 'Visibility',
            cell: (repository) => (repository.private ? 'Private' : 'Public'),
          },
          {
            id: 'actions',
            header: '',
            isActions: true,
            cell: (repository) =>
              repository.can_connect ? (
                <Button
                  size="sm"
                  disabled={pending || query.isFetching}
                  aria-label={`Connect ${repository.full_name}`}
                  onClick={() => {
                    onConnect(repository)
                  }}
                >
                  Connect
                </Button>
              ) : (
                <span className="text-muted-foreground text-xs">Admin access required</span>
              ),
          },
        ]}
        data={paged.rows}
        pagination={paged.pagination}
        getRowId={(repository) => repository.id}
        isPending={query.isPending}
        isError={query.isError}
        onRetry={() => {
          void query.refetch()
        }}
        emptyMessage="No repositories available to this app installation."
      />
      {query.isError && (
        <p className="text-muted-foreground text-sm">
          If this setup has expired, close it and connect the app again.
        </p>
      )}
    </fieldset>
  )
}

export function GitHubConnectionDialog({
  orgId,
  projectId,
  flowId,
  onClose,
  onConnected,
}: GitHubSetupScope & { onClose: () => void; onConnected: () => void }) {
  const mounted = useRef(true)
  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])
  const [installation, setInstallation] = useState<GitHubSetupInstallation | null>(null)
  const [error, setError] = useState('')
  const [connected, setConnected] = useState(false)
  const mutation = useCompleteGitHubConnection(orgId, projectId, flowId)
  const pending = mutation.isPending || connected
  async function connect(repository: GitHubSetupRepository) {
    if (!installation || installation.suspended || !repository.can_connect || pending) return
    setError('')
    try {
      await mutation.mutateAsync({
        installation_id: installation.id,
        repository_id: repository.id,
        repository_full_name: repository.full_name,
      })
      if (mounted.current) {
        setConnected(true)
        onConnected()
      }
    } catch (err) {
      setError(errorMessage(err, 'Could not connect repository'))
    }
  }
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !mutation.isPending) onClose()
      }}
    >
      <DialogContent className="sm:max-w-2xl" showCloseButton={!mutation.isPending}>
        <DialogHeader>
          <DialogTitle>Connect GitHub repository</DialogTitle>
          <DialogDescription>
            Select an installation and a repository you administer for this project.
          </DialogDescription>
        </DialogHeader>
        {installation ? (
          <GitHubRepositoryStep
            key={installation.id}
            orgId={orgId}
            projectId={projectId}
            flowId={flowId}
            installation={installation}
            pending={pending}
            onConnect={(repository) => {
              void connect(repository)
            }}
            onBack={() => {
              setInstallation(null)
              setError('')
            }}
          />
        ) : (
          <GitHubInstallationStep
            orgId={orgId}
            projectId={projectId}
            flowId={flowId}
            onSelect={setInstallation}
          />
        )}
        {connected && (
          <DialogFooter>
            <p role="status" className="text-sm">
              Repository connected.
            </p>
            <Button onClick={onClose}>Done</Button>
          </DialogFooter>
        )}
        {error && (
          <p role="alert" className="text-destructive text-sm">
            {error}
          </p>
        )}
      </DialogContent>
    </Dialog>
  )
}
