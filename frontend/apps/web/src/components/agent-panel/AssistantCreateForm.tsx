import type { useCreateAssistantAgent } from '@omnara/react'
import type { AgentPanel, CurrentUserOrg } from '@omnara/sdk'

import { ProjectModelCreateButton } from '@/components/projects/CreateModelButton'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'

import { ModelPicker, ProjectPicker } from './pickers'
import { useAssistantCreation } from './useAssistantCreation'

export function AssistantCreateForm({
  idPrefix,
  org,
  panel,
  create,
  disabled = false,
  onChange,
  onCreated,
}: {
  idPrefix: string
  org: CurrentUserOrg
  panel: AgentPanel
  create: ReturnType<typeof useCreateAssistantAgent>
  disabled?: boolean
  onChange?: () => void
  onCreated?: () => void
}) {
  const {
    projectsQuery,
    projects,
    project,
    modelsQuery,
    models,
    model,
    noModel,
    provisioning,
    creatingAutomatically,
    busy,
    chooseProject,
    chooseModel,
    submit,
  } = useAssistantCreation({ org, panel, create, disabled, onChange, onCreated })

  return (
    <>
      {projects.length > 1 && (
        <div className="space-y-2">
          <Label htmlFor={`${idPrefix}-project`}>Project</Label>
          <ProjectPicker
            id={`${idPrefix}-project`}
            items={projects}
            query={projectsQuery}
            value={project ?? null}
            clearable={false}
            disabled={busy}
            onValueChange={(value) => {
              chooseProject(value?.id ?? '')
            }}
          />
        </div>
      )}
      {models.length > 1 && (
        <div className="space-y-2">
          <Label htmlFor={`${idPrefix}-model`}>Model</Label>
          <ModelPicker
            id={`${idPrefix}-model`}
            items={models}
            query={modelsQuery}
            value={model ?? null}
            clearable={false}
            disabled={busy}
            onValueChange={(value) => {
              chooseModel(value?.model.id ?? '')
            }}
          />
        </div>
      )}
      {noModel && project != null ? (
        provisioning ? (
          <p role="status" className="text-muted-foreground text-sm">
            Setting up your model…
          </p>
        ) : project.access.can_manage_access ? (
          <ProjectModelCreateButton orgId={org.id} projectId={project.id}>
            Add a model
          </ProjectModelCreateButton>
        ) : (
          <p className="text-muted-foreground text-sm">
            Ask an admin to share a model with this project.
          </p>
        )
      ) : (
        <Button
          disabled={busy || project == null || model == null}
          loading={create.isPending || creatingAutomatically}
          onClick={submit}
        >
          Create assistant
        </Button>
      )}
      <p className="text-muted-foreground text-xs">
        It acts with your access and does not ask before making changes.
      </p>
    </>
  )
}
