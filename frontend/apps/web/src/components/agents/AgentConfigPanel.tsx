import { useAgentConfig, useUpdateAgentConfig } from '@omnara/react'
import { type Agent, ApiError } from '@omnara/sdk'
import { type SyntheticEvent, useCallback, useEffect, useReducer, useState } from 'react'

import { deserializeBasicConfig } from '@/components/agents/agentConfigBasicDeserialization'
import { AgentConfigBasicForm } from '@/components/agents/AgentConfigBasicForm'
import { serializeBasicConfig } from '@/components/agents/agentConfigBasicSerialization'
import {
  type AgentConfigMode,
  agentConfigModeReducer,
  initialAgentConfigModeState,
} from '@/components/agents/agentConfigModeMachine'
import { AgentConfigYamlField } from '@/components/agents/AgentConfigYamlField'
import { ConfirmDiscardYamlDialog } from '@/components/agents/ConfirmDiscardYamlDialog'
import { PillTabs } from '@/components/agents/PillTabs'
import { Button } from '@/components/ui/button'
import { FieldGroup } from '@/components/ui/field'
import { Spinner } from '@/components/ui/spinner'

export const discardConfigEditsPrompt = 'You have unsaved configuration changes. Discard them?'

/** Builder/YAML editor for a live agent's config, mirroring the profile editor. */
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
  /** Lets the page confirm before unmounting the panel with unsaved edits. */
  onDirtyChange: (dirty: boolean) => void
  onClose: () => void
}) {
  const configQuery = useAgentConfig(orgId, projectId, agent.current_config_id)
  const [resetNonce, setResetNonce] = useState(0)
  const [preferredMode, setPreferredMode] = useState<AgentConfigMode>('builder')

  return (
    <div className="flex min-h-full flex-col gap-4">
      {configQuery.isPending ? (
        <>
          <h2 className="text-lg font-semibold tracking-tight">Agent configuration</h2>
          <Spinner className="mx-auto my-8" />
        </>
      ) : configQuery.isError ? (
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
      ) : (
        <AgentConfigPanelEditor
          key={`${configQuery.data.id}:${String(resetNonce)}`}
          orgId={orgId}
          projectId={projectId}
          agentId={agent.id}
          source={configQuery.data.source ?? ''}
          canManage={canManage}
          preferredMode={preferredMode}
          onModeChange={setPreferredMode}
          onDirtyChange={onDirtyChange}
          onDiscard={() => {
            setResetNonce((nonce) => nonce + 1)
          }}
          onClose={onClose}
        />
      )}
    </div>
  )
}

function AgentConfigPanelEditor({
  orgId,
  projectId,
  agentId,
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
  source: string
  canManage: boolean
  preferredMode: AgentConfigMode
  onModeChange: (mode: AgentConfigMode) => void
  onDirtyChange: (dirty: boolean) => void
  onDiscard: () => void
  onClose: () => void
}) {
  const updateConfig = useUpdateAgentConfig(orgId, projectId, agentId)
  const [initialBuilderConfig] = useState(() => (canManage ? deserializeBasicConfig(source) : null))
  const baselineYaml = initialBuilderConfig ? serializeBasicConfig(initialBuilderConfig) : source
  const [mode, dispatchMode] = useReducer(
    agentConfigModeReducer,
    initialAgentConfigModeState(initialBuilderConfig ? preferredMode : 'yaml', baselineYaml),
  )
  const [builderIncomplete, setBuilderIncomplete] = useState(false)
  const [error, setError] = useState('')

  // Stable identity: the builder's serialize effect depends on this callback.
  // An empty string means the builder's draft is incomplete; keep the last
  // complete YAML in the buffers so the YAML tab isn't blanked.
  const handleBuilderYamlChange = useCallback((value: string) => {
    setBuilderIncomplete(value === '')
    if (value !== '') dispatchMode({ type: 'builder-yaml-changed', yaml: value })
  }, [])

  const showBuilder = mode.mode === 'builder'
  const yaml = showBuilder ? mode.builderYaml : mode.editorYaml
  const dirty = yaml !== source && yaml !== baselineYaml
  const saveBlocked = yaml.trim() === '' || (showBuilder && builderIncomplete)

  useEffect(() => {
    onModeChange(mode.mode)
  }, [mode.mode, onModeChange])

  useEffect(() => {
    onDirtyChange(dirty)
  }, [dirty, onDirtyChange])

  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    setError('')
    try {
      await updateConfig.mutateAsync({ source: yaml, source_format: 'yaml' })
      onClose()
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Could not update the agent config')
    }
  }

  return (
    <form
      className="flex flex-1 flex-col"
      onSubmit={(event) => {
        void submit(event)
      }}
    >
      <FieldGroup>
        <div className="flex items-center justify-between gap-3">
          <h2 className="text-lg font-semibold tracking-tight">Agent configuration</h2>
          {initialBuilderConfig != null && (
            <PillTabs
              value={mode.mode}
              onValueChange={(nextMode) => {
                dispatchMode({ type: 'switch-mode', mode: nextMode })
              }}
              tabs={[
                { value: 'builder', label: 'Builder' },
                { value: 'yaml', label: 'YAML' },
              ]}
            />
          )}
        </div>
        {initialBuilderConfig != null && (
          <>
            <div className={showBuilder ? 'flex flex-col gap-8' : 'hidden'}>
              <AgentConfigBasicForm
                orgId={orgId}
                projectId={projectId}
                initialConfig={initialBuilderConfig}
                onYamlChange={handleBuilderYamlChange}
              />
            </div>
          </>
        )}
        {!showBuilder && (
          <AgentConfigYamlField
            id="agent-config-panel-yaml"
            value={mode.editorYaml}
            onChange={(value) => {
              dispatchMode({ type: 'editor-yaml-changed', yaml: value })
            }}
            readOnly={!canManage}
            className="h-[24rem]"
          />
        )}
        {canManage && initialBuilderConfig == null && source !== '' && (
          <p className="text-muted-foreground text-sm">
            This configuration can’t be shown in the builder, so it’s editable as YAML only.
          </p>
        )}
      </FieldGroup>
      <div className="bg-background sticky bottom-0 z-10 mt-auto flex items-center justify-between gap-4 border-t py-3">
        <Button
          type="button"
          variant="ghost"
          onClick={() => {
            if (dirty && !window.confirm(discardConfigEditsPrompt)) return
            onClose()
          }}
        >
          Cancel editing
        </Button>
        <div className="flex items-center gap-3">
          {error && <p className="text-destructive whitespace-pre-wrap text-sm">{error}</p>}
          {canManage && dirty && !updateConfig.isPending && (
            <Button type="button" variant="ghost" onClick={onDiscard}>
              Discard changes
            </Button>
          )}
          {canManage && (
            <Button type="submit" disabled={updateConfig.isPending || !dirty || saveBlocked}>
              {updateConfig.isPending && <Spinner />}
              Save config
            </Button>
          )}
        </div>
      </div>
      <ConfirmDiscardYamlDialog
        open={mode.confirmDiscard}
        onOpenChange={(nextOpen) => {
          dispatchMode({ type: 'set-confirm-discard', open: nextOpen })
        }}
        onConfirm={() => {
          dispatchMode({ type: 'discard-yaml-edits' })
        }}
      />
    </form>
  )
}
