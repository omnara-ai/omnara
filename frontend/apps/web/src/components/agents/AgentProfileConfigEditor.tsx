import { useCreateAgentConfig, useUpdateAgentProfile } from '@omnara/react'
import { type AgentProfile, ApiError } from '@omnara/sdk'
import { type SyntheticEvent, useState } from 'react'

import { AgentConfigEditorFields } from '@/components/agents/AgentConfigEditor'
import { type AgentConfigMode } from '@/components/agents/agentConfigModeMachine'
import { useAgentConfigEditor } from '@/components/agents/useAgentConfigEditor'
import { Button } from '@/components/ui/button'
import { FieldGroup } from '@/components/ui/field'
import { Spinner } from '@/components/ui/spinner'

export function AgentProfileConfigEditor({
  orgId,
  projectId,
  profile,
  canManage,
  preferredMode,
  onModeChange,
  onDirtyChange,
  onDiscard,
  onDelete,
}: {
  orgId: string
  projectId: string
  profile: AgentProfile
  canManage: boolean
  preferredMode: AgentConfigMode
  onModeChange: (mode: AgentConfigMode) => void
  onDirtyChange: (dirty: boolean) => void
  onDiscard: () => void
  onDelete: () => void
}) {
  const createConfig = useCreateAgentConfig(orgId, projectId)
  const updateProfile = useUpdateAgentProfile(orgId, projectId)
  const source = profile.current_config.source ?? ''
  const editor = useAgentConfigEditor({
    source,
    canManage,
    preferredMode,
    onModeChange,
    onDirtyChange,
  })
  const [error, setError] = useState('')
  const pending = createConfig.isPending || updateProfile.isPending

  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    setError('')
    try {
      const config = await createConfig.mutateAsync({ source: editor.yaml, source_format: 'yaml' })
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
        <AgentConfigEditorFields
          editor={editor}
          orgId={orgId}
          projectId={projectId}
          yamlFieldId="agent-profile-config-yaml"
          yamlFieldClassName="h-[28rem]"
        />
        {profile.current_config.source === undefined && editor.yaml.trim() === '' && (
          <p className="text-muted-foreground text-sm">
            The current source is unavailable. Paste the replacement YAML configuration.
          </p>
        )}
        {error && <p className="text-destructive whitespace-pre-wrap text-sm">{error}</p>}
        {canManage && (
          <div className="flex items-center justify-end gap-2">
            {editor.dirty && !pending && (
              <Button type="button" variant="ghost" onClick={onDiscard}>
                Discard changes
              </Button>
            )}
            <Button
              type="button"
              variant="ghost"
              className="text-destructive hover:text-destructive"
              onClick={onDelete}
            >
              Delete profile
            </Button>
            <Button type="submit" disabled={pending || !editor.dirty || editor.saveBlocked}>
              {pending && <Spinner />}
              Save revision
            </Button>
          </div>
        )}
      </FieldGroup>
    </form>
  )
}
