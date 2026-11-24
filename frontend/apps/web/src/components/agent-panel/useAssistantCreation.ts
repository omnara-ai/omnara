import {
  type useCreateAssistantAgent,
  useProjectModelGrants,
  useVisibleProjectsList,
} from '@omnara/react'
import type { AgentPanel, CurrentUserOrg, VisibleProject } from '@omnara/sdk'
import { useContext, useRef, useState } from 'react'

import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'

import { AssistantContext } from './assistant-context'

function defaultProject(projects: VisibleProject[], preferredID: string | undefined) {
  return (
    projects.find((item) => item.id === preferredID) ??
    projects
      .slice()
      .sort((left, right) => Date.parse(left.created_at) - Date.parse(right.created_at))[0]
  )
}

/**
 * What creating an assistant in an organization needs: the project and model to use,
 * prefilled, and whether creation is already queued to happen by itself.
 */
export function useAssistantCreation({
  org,
  panel,
  create,
  disabled = false,
  onChange,
  onCreated,
}: {
  org: CurrentUserOrg
  panel: AgentPanel
  create: ReturnType<typeof useCreateAssistantAgent>
  disabled?: boolean
  onChange?: () => void
  onCreated?: () => void
}) {
  const { bootstrap } = useContext(AssistantContext)
  const [projectID, setProjectID] = useState('')
  const [modelID, setModelID] = useState('')
  const requestID = useRef<string | null>(null)
  const projectsQuery = useVisibleProjectsList(org.id, {})
  const projects = useInfiniteQueryItems(projectsQuery).filter((item) => item.org_id === org.id)
  const project =
    projects.find((item) => item.id === projectID) ??
    defaultProject(projects, bootstrap?.scope.project_id)
  const modelsQuery = useProjectModelGrants(org.id, project?.id ?? '', {
    sort: 'created_at',
    pageSize: 100,
    enabled: project != null,
  })
  const models = useInfiniteQueryItems(modelsQuery).filter(
    (item) => item.grant.project_id === project?.id && item.supports_tools,
  )
  const model = models.find((item) => item.model.id === modelID) ?? models[0]
  const queued = bootstrap?.scope.project_id === project?.id ? bootstrap : null
  const creatingAutomatically =
    queued != null && queued.error == null && (queued.model === 'available' || queued.pending)

  function resetRequest() {
    requestID.current = null
    create.reset()
    onChange?.()
  }

  function chooseProject(id: string) {
    setProjectID(id)
    setModelID('')
    resetRequest()
  }
  function chooseModel(id: string) {
    setModelID(id)
    resetRequest()
  }
  function submit() {
    if (project == null || model == null) return
    // A fresh click may follow a rejected stale revision. Keep the request ID
    // so an ambiguous earlier success still replays without reselecting.
    requestID.current ??= crypto.randomUUID()
    // The form can unmount as soon as the created assistant is selected, which
    // would drop a per-call onSuccess; the promise still settles.
    void create
      .mutateAsync({
        orgID: org.id,
        body: {
          project_id: project.id,
          request_id: requestID.current,
          expected_revision: panel.revision,
          automatic: false,
          model_id: model.model.id,
        },
      })
      .then(onCreated, () => undefined)
  }

  return {
    projectsQuery,
    projects,
    project,
    modelsQuery,
    models,
    model,
    noModel: modelsQuery.isSuccess && !modelsQuery.hasNextPage && model == null,
    queued: queued != null,
    provisioning: queued?.model === 'provisioning',
    creatingAutomatically,
    busy: disabled || create.isPending || creatingAutomatically,
    chooseProject,
    chooseModel,
    submit,
  }
}
