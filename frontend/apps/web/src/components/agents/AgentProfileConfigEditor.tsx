import { useCreateAgentConfig, useUpdateAgentProfile } from '@omnara/react'
import type { AgentProfile } from '@omnara/sdk'
import { type SyntheticEvent, useState } from 'react'

import { AgentConfigEditorFields } from '@/components/agents/AgentConfigEditor'
import { type AgentConfigMode } from '@/components/agents/agentConfigModeMachine'
import { useAgentConfigEditor } from '@/components/agents/useAgentConfigEditor'
import { Button } from '@/components/ui/button'
import { FieldGroup } from '@/components/ui/field'
import { type ConfigSubmitError, configSubmitError, noConfigError } from '@/lib/agent-config-issues'

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
  const [error, setError] = useState<ConfigSubmitError>(noConfigError)
  const pending = createConfig.isPending || updateProfile.isPending

  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    setError(noConfigError)
    try {
      const config = await createConfig.mutateAsync({ source: editor.yaml, source_format: 'yaml' })
      await updateProfile.mutateAsync({
        agentProfileID: profile.id,
        config: config.id,
        expected_current_config_id: profile.current_config_id,
      })
    } catch (err) {
      setError(configSubmitError(err, 'Could not update agent profile'))
    }
  }

  return (
    <form
      noValidate
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
          issues={error.issues}
        />
        {profile.current_config.source === undefined && editor.yaml.trim() === '' && (
          <p className="text-muted-foreground text-sm">
            The current source is unavailable. Paste the replacement YAML configuration.
          </p>
        )}
        {error.message && (
          <p className="text-destructive whitespace-pre-wrap text-sm">{error.message}</p>
        )}
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
            <Button
              type="submit"
              disabled={pending || !editor.dirty || editor.saveBlocked}
              loading={pending}
            >
              Save revision
            </Button>
          </div>
        )}
      </FieldGroup>
    </form>
  )
}
