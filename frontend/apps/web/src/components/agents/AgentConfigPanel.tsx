import { useAgentConfig, useUpdateAgentConfig } from '@omnara/react'
import { type Agent, type AgentConfig, ApiError } from '@omnara/sdk'
import { type SyntheticEvent, useState } from 'react'

import { AgentConfigEditorFields } from '@/components/agents/AgentConfigEditor'
import { type AgentConfigMode } from '@/components/agents/agentConfigModeMachine'
import { useAgentConfigEditor } from '@/components/agents/useAgentConfigEditor'
import { Button } from '@/components/ui/button'
import { FieldGroup } from '@/components/ui/field'
import { Spinner } from '@/components/ui/spinner'

export const discardConfigEditsPrompt = 'You have unsaved configuration changes. Discard them?'

export function AgentConfigPanel({
  orgId,
  projectId,
  agent,
  canManage,
  onDirtyChange,
  onClose,
}: {
  orgId: string
  projectId: string
  agent: Agent
  canManage: boolean
  onDirtyChange: (dirty: boolean) => void
  onClose: () => void
}) {
  const configQuery = useAgentConfig(orgId, projectId, agent.current_config_id)
  const [resetNonce, setResetNonce] = useState(0)
  const [preferredMode, setPreferredMode] = useState<AgentConfigMode>('builder')
  const [snapshot, setSnapshot] = useState<AgentConfig | null>(null)
  if (snapshot === null && configQuery.data !== undefined) {
    setSnapshot(configQuery.data)
  }

  return (
    <div className="flex min-h-full flex-col gap-4">
      {snapshot !== null ? (
        <AgentConfigPanelEditor
          key={String(resetNonce)}
          orgId={orgId}
          projectId={projectId}
          agentId={agent.id}
          configId={snapshot.id}
          source={snapshot.source ?? ''}
          canManage={canManage}
          preferredMode={preferredMode}
          onModeChange={setPreferredMode}
          onDirtyChange={onDirtyChange}
          onDiscard={() => {
            setSnapshot(configQuery.data ?? null)
            setResetNonce((nonce) => nonce + 1)
            onDirtyChange(false)
          }}
          onClose={onClose}
        />
      ) : configQuery.isPending ? (
        <>
          <h2 className="text-lg font-semibold tracking-tight">Agent configuration</h2>
          <Spinner className="mx-auto my-8" />
        </>
      ) : (
        <>
          <h2 className="text-lg font-semibold tracking-tight">Agent configuration</h2>
          <div className="flex items-center gap-3">
            <p className="text-muted-foreground text-sm">Couldn&rsquo;t load the agent config.</p>
            <Button
              size="sm"
              variant="outline"
              onClick={() => {
                void configQuery.refetch()
              }}
            >
              Retry
            </Button>
          </div>
          <div className="mt-auto flex border-t py-3">
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel editing
            </Button>
          </div>
        </>
      )}
    </div>
  )
}

function AgentConfigPanelEditor({
  orgId,
  projectId,
  agentId,
  configId,
  source,
  canManage,
  preferredMode,
  onModeChange,
  onDirtyChange,
  onDiscard,
  onClose,
}: {
  orgId: string
  projectId: string
  agentId: string
  configId: string
  source: string
  canManage: boolean
  preferredMode: AgentConfigMode
  onModeChange: (mode: AgentConfigMode) => void
  onDirtyChange: (dirty: boolean) => void
  onDiscard: () => void
  onClose: () => void
}) {
  const updateConfig = useUpdateAgentConfig(orgId, projectId, agentId)
  const editor = useAgentConfigEditor({
    source,
    canManage,
    preferredMode,
    onModeChange,
    onDirtyChange,
  })
  const [error, setError] = useState('')

  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    setError('')
    try {
      await updateConfig.mutateAsync({
        source: editor.yaml,
        source_format: 'yaml',
        expected_current_config_id: configId,
      })
      onClose()
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) {
        setError(
          'The agent’s configuration changed while you were editing. Discard your changes to load the latest version, then reapply them.',
        )
        return
      }
      setError(err instanceof ApiError ? err.message : 'Could not update the agent config')
    }
  }

  return (
    <form
      noValidate
      className="mx-auto flex w-full max-w-3xl flex-1 flex-col"
      onSubmit={(event) => {
        void submit(event)
      }}
    >
      <FieldGroup>
        <AgentConfigEditorFields
          editor={editor}
          orgId={orgId}
          projectId={projectId}
          header={<h2 className="text-lg font-semibold tracking-tight">Agent configuration</h2>}
          yamlFieldId="agent-config-panel-yaml"
          yamlFieldClassName="h-[24rem]"
        />
      </FieldGroup>
      <div className="bg-background sticky bottom-0 z-10 mt-auto flex items-center justify-between gap-4 border-t py-3">
        <Button
          type="button"
          variant="ghost"
          onClick={() => {
            if (editor.dirty && !window.confirm(discardConfigEditsPrompt)) return
            onClose()
          }}
        >
          Cancel editing
        </Button>
        <div className="flex items-center gap-3">
          {error && <p className="text-destructive whitespace-pre-wrap text-sm">{error}</p>}
          {canManage && editor.dirty && !updateConfig.isPending && (
            <Button type="button" variant="ghost" onClick={onDiscard}>
              Discard changes
            </Button>
          )}
          {canManage && (
            <Button
              type="submit"
              disabled={updateConfig.isPending || !editor.dirty || editor.saveBlocked}
              loading={updateConfig.isPending}
            >
              Save config
            </Button>
          )}
        </div>
      </div>
    </form>
  )
}
