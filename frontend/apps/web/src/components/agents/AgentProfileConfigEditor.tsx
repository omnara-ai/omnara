import { useCreateAgentConfig, useUpdateAgentProfile } from '@omnara/react'
import { type AgentProfile, ApiError } from '@omnara/sdk'
import { type SyntheticEvent, useCallback, useEffect, useReducer, useState } from 'react'

import { deserializeBasicConfig } from '@/components/agents/agentConfigBasicDeserialization'
import { AgentConfigBasicForm } from '@/components/agents/AgentConfigBasicForm'
import { serializeBasicConfig } from '@/components/agents/agentConfigBasicSerialization'
import {
  agentConfigModeReducer,
  initialAgentConfigModeState,
} from '@/components/agents/agentConfigModeMachine'
import { AgentConfigYamlField } from '@/components/agents/AgentConfigYamlField'
import { ConfirmDiscardYamlDialog } from '@/components/agents/ConfirmDiscardYamlDialog'
import { PillTabs } from '@/components/agents/PillTabs'
import { Button } from '@/components/ui/button'
import { FieldGroup } from '@/components/ui/field'
import { Spinner } from '@/components/ui/spinner'

export function AgentProfileConfigEditor({
  orgId,
  projectId,
  profile,
  canManage,
  onDirtyChange,
  onDiscard,
}: {
  orgId: string
  projectId: string
  profile: AgentProfile
  canManage: boolean
  onDirtyChange: (dirty: boolean) => void
  onDiscard: () => void
}) {
  const createConfig = useCreateAgentConfig(orgId, projectId)
  const updateProfile = useUpdateAgentProfile(orgId, projectId)
  const source = profile.current_config.source ?? ''
  const [initialBuilderConfig] = useState(() => (canManage ? deserializeBasicConfig(source) : null))
  // The builder emits normalized YAML, which may differ from an equivalent
  // hand-formatted source; treat that normal form as clean too.
  const baselineYaml = initialBuilderConfig ? serializeBasicConfig(initialBuilderConfig) : source
  const [mode, dispatchMode] = useReducer(
    agentConfigModeReducer,
    initialAgentConfigModeState(initialBuilderConfig ? 'builder' : 'yaml', baselineYaml),
  )
  const [builderIncomplete, setBuilderIncomplete] = useState(false)
  const [error, setError] = useState('')
  const pending = createConfig.isPending || updateProfile.isPending

  // Stable identity: the builder's serialize effect depends on this callback.
  // An empty string means the builder's draft is incomplete; keep the last
  // complete YAML in the buffers so the YAML tab isn't blanked.
  const handleBuilderYamlChange = useCallback((value: string) => {
    setBuilderIncomplete(value === '')
    if (value !== '') dispatchMode({ type: 'builder-yaml-changed', yaml: value })
  }, [])

  const showBuilder = mode.mode === 'builder'
  const yaml = showBuilder ? (builderIncomplete ? '' : mode.builderYaml) : mode.editorYaml
  const dirty = yaml !== source && yaml !== baselineYaml

  // Lets the page warn before launching an agent with unsaved edits.
  useEffect(() => {
    onDirtyChange(dirty)
  }, [dirty, onDirtyChange])

  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    setError('')
    try {
      const config = await createConfig.mutateAsync({ source: yaml, source_format: 'yaml' })
      await updateProfile.mutateAsync({
        agentProfileID: profile.id,
        config: config.id,
        expected_current_config_id: profile.current_config_id,
      })
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Could not update agent profile')
    }
  }

  return (
    <form
      onSubmit={(event) => {
        void submit(event)
      }}
    >
      <FieldGroup>
        {initialBuilderConfig != null && (
          <>
            <div className="flex justify-end">
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
            </div>
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
            id="agent-profile-config-yaml"
            value={mode.editorYaml}
            onChange={(value) => {
              dispatchMode({ type: 'editor-yaml-changed', yaml: value })
            }}
            readOnly={!canManage}
            className="h-[28rem]"
          />
        )}
        {canManage && initialBuilderConfig == null && source !== '' && (
          <p className="text-muted-foreground text-sm">
            This configuration can’t be shown in the builder, so it’s editable as YAML only.
          </p>
        )}
        {profile.current_config.source === undefined && yaml.trim() === '' && (
          <p className="text-muted-foreground text-sm">
            The current source is unavailable. Paste the replacement YAML configuration.
          </p>
        )}
        {error && <p className="text-destructive whitespace-pre-wrap text-sm">{error}</p>}
        {canManage && (
          <div className="flex items-center gap-2">
            <Button type="submit" disabled={pending || !dirty || yaml.trim() === ''}>
              {pending && <Spinner />}
              Save revision
            </Button>
            {dirty && !pending && (
              <Button type="button" variant="ghost" onClick={onDiscard}>
                Discard changes
              </Button>
            )}
          </div>
        )}
      </FieldGroup>
      <ConfirmDiscardYamlDialog
        open={mode.confirmDiscard}
        onOpenChange={(open) => {
          dispatchMode({ type: 'set-confirm-discard', open })
        }}
        onConfirm={() => {
          dispatchMode({ type: 'discard-yaml-edits' })
        }}
      />
    </form>
  )
}
