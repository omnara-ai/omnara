import { useCreateAssistantAgent } from '@omnara/react'
import type { AgentPanel, CurrentUserOrg } from '@omnara/sdk'
import { type ReactNode, useContext, useEffect } from 'react'

import { AssistantBootstrapError } from '@/components/agent-panel/AgentPanelBootstrap'
import { AssistantContext } from '@/components/agent-panel/assistant-context'
import { ModelPicker, ProjectPicker } from '@/components/agent-panel/pickers'
import { useAssistantCreation } from '@/components/agent-panel/useAssistantCreation'
import { BrandMark } from '@/components/brand/OmnaraMark'
import { ProjectModelCreateButton } from '@/components/projects/CreateModelButton'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Label } from '@/components/ui/label'
import { Spinner } from '@/components/ui/spinner'

type Creation = ReturnType<typeof useAssistantCreation>

/** A new organization's first screen. The page is the only way in, so the corner launcher steps aside. */
export function AssistantSetup({ org, panel }: { org: CurrentUserOrg; panel: AgentPanel }) {
  const create = useCreateAssistantAgent()
  const { hideLauncher } = useContext(AssistantContext)
  const setup = useAssistantCreation({ org, panel, create })
  useEffect(() => {
    hideLauncher(true)
    return () => {
      hideLauncher(false)
    }
  }, [hideLauncher])

  const needsModel =
    setup.model == null && setup.noModel && !setup.provisioning && !setup.creatingAutomatically
  return (
    <Card className="w-full max-w-md gap-0 overflow-hidden py-0">
      <div className="flex flex-col items-center gap-8 px-6 pt-10 pb-8 sm:px-10">
        <div className="flex flex-col items-center gap-5 text-center">
          <BrandMark className="size-11" />
          <div className="flex flex-col gap-2">
            <h1 className="type-title">Set up your assistant</h1>
            <p className="text-muted-foreground text-sm">
              <span className="block">It builds and manages agents for you.</span>
              {needsModel && <span className="block">Give it a model to get started.</span>}
            </p>
          </div>
        </div>
        <div className="flex w-full flex-col gap-4">
          <SetupAction org={org} setup={setup} creating={create.isPending} />
          <AssistantBootstrapError />
          {create.error != null && (
            <p role="alert" className="text-destructive text-center text-sm">
              {create.error.message}
            </p>
          )}
        </div>
      </div>
      <p className="text-muted-foreground bg-muted/40 border-t px-6 py-4 text-center text-xs text-balance">
        It acts with your access and does not ask before making changes.
      </p>
    </Card>
  )
}

function SetupAction({
  org,
  setup,
  creating,
}: {
  org: CurrentUserOrg
  setup: Creation
  creating: boolean
}) {
  const { project, model } = setup
  if (setup.creatingAutomatically) return <Waiting>Creating your assistant…</Waiting>
  if (model != null) {
    return (
      <>
        {(setup.projects.length > 1 || setup.models.length > 1) && <Choices setup={setup} />}
        <Button className="w-full" disabled={setup.busy} loading={creating} onClick={setup.submit}>
          Create assistant
        </Button>
      </>
    )
  }
  if (setup.provisioning) return <Waiting>Setting up your model…</Waiting>
  if (!setup.noModel || project == null) return <Waiting>Loading…</Waiting>
  if (!project.access.can_manage_access) {
    return (
      <p className="text-muted-foreground flex min-h-10 items-center justify-center text-center text-sm">
        Ask an admin to share a model with this project.
      </p>
    )
  }
  return (
    <ProjectModelCreateButton orgId={org.id} projectId={project.id} className="w-full">
      Add a model
    </ProjectModelCreateButton>
  )
}

function Choices({ setup }: { setup: Creation }) {
  return (
    <div className="flex flex-col gap-4 pb-2">
      {setup.projects.length > 1 && (
        <Choice id="assistant-setup-project" label="Project">
          <ProjectPicker
            id="assistant-setup-project"
            items={setup.projects}
            query={setup.projectsQuery}
            value={setup.project ?? null}
            clearable={false}
            disabled={setup.busy}
            onValueChange={(value) => {
              setup.chooseProject(value?.id ?? '')
            }}
          />
        </Choice>
      )}
      {setup.models.length > 1 && (
        <Choice id="assistant-setup-model" label="Model">
          <ModelPicker
            id="assistant-setup-model"
            items={setup.models}
            query={setup.modelsQuery}
            value={setup.model ?? null}
            clearable={false}
            disabled={setup.busy}
            onValueChange={(value) => {
              setup.chooseModel(value?.model.id ?? '')
            }}
          />
        </Choice>
      )}
    </div>
  )
}

function Choice({ id, label, children }: { id: string; label: string; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-2">
      <Label htmlFor={id}>{label}</Label>
      {children}
    </div>
  )
}

function Waiting({ children }: { children: string }) {
  return (
    <p
      role="status"
      className="text-muted-foreground flex min-h-10 items-center justify-center gap-2 text-sm"
    >
      <Spinner className="size-4" />
      {children}
    </p>
  )
}
