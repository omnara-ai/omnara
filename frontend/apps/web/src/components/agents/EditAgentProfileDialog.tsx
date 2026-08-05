import { useCreateAgentConfig, useUpdateAgentProfile } from '@omnara/react'
import { type AgentProfile, ApiError } from '@omnara/sdk'
import { type SyntheticEvent, useState } from 'react'

import { AgentConfigYamlField } from '@/components/agents/AgentConfigYamlField'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { FieldGroup } from '@/components/ui/field'
import { Spinner } from '@/components/ui/spinner'

export function EditAgentProfileDialog({
  open,
  onOpenChange,
  orgId,
  projectId,
  profile,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
  projectId: string
  profile: AgentProfile
}) {
  const createConfig = useCreateAgentConfig(orgId, projectId)
  const updateProfile = useUpdateAgentProfile(orgId, projectId)
  const [yaml, setYaml] = useState(profile.current_config.source ?? '')
  const [error, setError] = useState('')
  const [editedProfileId, setEditedProfileId] = useState(profile.id)
  const pending = createConfig.isPending || updateProfile.isPending

  // Reset the editor when the dialog is reused for a different profile.
  if (editedProfileId !== profile.id) {
    setEditedProfileId(profile.id)
    setYaml(profile.current_config.source ?? '')
    setError('')
  }

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
      onOpenChange(false)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Could not update agent profile')
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-3xl">
        <DialogHeader>
          <DialogTitle>Edit {profile.name}</DialogTitle>
          <DialogDescription>
            Create the next configuration revision for this profile.
          </DialogDescription>
        </DialogHeader>
        <form
          onSubmit={(event) => {
            void submit(event)
          }}
        >
          <FieldGroup>
            <AgentConfigYamlField
              id="edit-agent-profile-yaml"
              value={yaml}
              className="h-[30rem]"
              onChange={setYaml}
            />
            {profile.current_config.source === undefined && yaml.trim() === '' && (
              <p className="text-muted-foreground text-sm">
                The current source is unavailable. Paste the replacement YAML configuration.
              </p>
            )}
            {error && <p className="text-destructive whitespace-pre-wrap text-sm">{error}</p>}
            <DialogFooter>
              <Button type="submit" disabled={pending || yaml.trim() === ''}>
                {pending && <Spinner />}
                Save revision
              </Button>
            </DialogFooter>
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}
