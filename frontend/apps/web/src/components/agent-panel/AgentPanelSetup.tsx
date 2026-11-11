import {
  useAgents,
  useCreateAssistantAgent,
  useMe,
  useProjectModelGrants,
  useUpdateAgentPanel,
  useVisibleProjectsList,
} from '@omnara/react'
import type {
  Agent,
  AgentPanel,
  CurrentUserOrg,
  ProjectModelGrantListItem,
  VisibleProject,
} from '@omnara/sdk'
import { useContext, useRef, useState } from 'react'

import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { createResourceCombobox } from '@/components/ui/resource-combobox'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { ActiveOrgContext } from '@/lib/active-org-context'

const OrgPicker = createResourceCombobox<CurrentUserOrg>({
  itemKey: (org) => org.id,
  itemLabel: (org) => org.name,
  placeholder: 'Choose organization',
})
const ProjectPicker = createResourceCombobox<VisibleProject>({
  itemKey: (project) => project.id,
  itemLabel: (project) => project.name,
  placeholder: 'Choose project',
})
const AgentPicker = createResourceCombobox<Agent>({
  itemKey: (agent) => agent.id,
  itemLabel: (agent) => agent.name || agent.id,
  placeholder: 'Choose agent',
})
const ModelPicker = createResourceCombobox<ProjectModelGrantListItem>({
  itemKey: (item) => item.model.id,
  itemLabel: (item) => `${item.model.provider_config}/${item.model.name}`,
  placeholder: 'Automatic model selection',
})

interface Actions {
  create: ReturnType<typeof useCreateAssistantAgent>
  update: ReturnType<typeof useUpdateAgentPanel>
}

export function AgentPanelSetup({
  panel,
  onConnected,
  onClose,
}: {
  panel: AgentPanel
  onConnected: () => void
  onClose?: () => void
}) {
  const create = useCreateAssistantAgent()
  const update = useUpdateAgentPanel()
  const actions = { create, update }
  const { data: me } = useMe()
  const activeOrg = useContext(ActiveOrgContext)?.activeOrg
  const [mode, setMode] = useState<'connect' | 'create'>('connect')
  const [selectedOrg, setSelectedOrg] = useState('')
  const orgs = mode === 'create' ? me.orgs.filter((org) => org.role === 'owner') : me.orgs
  const org =
    orgs.find((item) => item.id === selectedOrg) ??
    orgs.find((item) => item.id === activeOrg?.id) ??
    orgs[0]
  const pending = actions.create.isPending || actions.update.isPending
  function resetActions() {
    actions.create.reset()
    actions.update.reset()
  }
  function chooseMode(value: 'connect' | 'create') {
    setMode(value)
    resetActions()
  }

  return (
    <>
      <div className="space-y-4 overflow-y-auto p-4">
        <p className="text-muted-foreground text-sm">
          Connect an agent to keep it with you across organizations and pages.
        </p>
        <div className="flex gap-2" role="group" aria-label="Assistant setup">
          <Button
            size="sm"
            variant={mode === 'connect' ? 'secondary' : 'outline'}
            aria-pressed={mode === 'connect'}
            disabled={pending}
            onClick={() => {
              chooseMode('connect')
            }}
          >
            Connect existing
          </Button>
          <Button
            size="sm"
            variant={mode === 'create' ? 'secondary' : 'outline'}
            aria-pressed={mode === 'create'}
            disabled={pending}
            onClick={() => {
              chooseMode('create')
            }}
          >
            Create new
          </Button>
        </div>
        {orgs.length === 0 ? (
          <p className="text-muted-foreground text-sm">
            {mode === 'create'
              ? 'Create or become an owner of an organization to create an assistant.'
              : 'Create or join an organization to connect an agent.'}
          </p>
        ) : (
          <>
            <div className="space-y-2">
              <Label htmlFor="panel-organization">Organization</Label>
              <OrgPicker
                id="panel-organization"
                items={orgs}
                value={org ?? null}
                clearable={false}
                disabled={pending}
                onValueChange={(value) => {
                  setSelectedOrg(value?.id ?? '')
                  resetActions()
                }}
              />
            </div>
            {org != null && (
              <PanelConnectionForm
                key={`${mode}:${org.id}`}
                org={org}
                mode={mode}
                panel={panel}
                actions={actions}
                onConnected={onConnected}
              />
            )}
          </>
        )}
        {create.error != null && (
          <p role="alert" className="text-destructive text-sm">
            {create.error.message}
          </p>
        )}
        {update.error != null && (
          <p role="alert" className="text-destructive text-sm">
            {update.error.message}
          </p>
        )}
      </div>
      {onClose != null && (
        <footer className="flex shrink-0 gap-2 border-t p-3">
          {panel.selection != null && (
            <Button
              size="sm"
              variant="outline"
              disabled={pending}
              onClick={() => {
                update.mutate({ agent_id: null })
              }}
            >
              Disconnect
            </Button>
          )}
          <Button
            size="sm"
            variant="ghost"
            disabled={pending}
            onClick={() => {
              void update.mutateAsync({ enabled: false }).then(onClose, () => undefined)
            }}
          >
            Turn off panel
          </Button>
        </footer>
      )}
    </>
  )
}

function PanelConnectionForm({
  org,
  mode,
  panel,
  actions,
  onConnected,
}: {
  org: CurrentUserOrg
  mode: 'connect' | 'create'
  panel: AgentPanel
  actions: Actions
  onConnected: () => void
}) {
  const [projectID, setProjectID] = useState('')
  const [agentID, setAgentID] = useState('')
  const [modelID, setModelID] = useState('')
  const requestID = useRef<string | null>(null)
  const orgID = org.id
  const projectsQuery = useVisibleProjectsList(orgID, {})
  const projects = useInfiniteQueryItems(projectsQuery).filter((item) => item.org_id === orgID)
  const project = projects.find((item) => item.id === projectID) ?? null
  const pending = actions.create.isPending || actions.update.isPending

  function resetRequest() {
    requestID.current = null
    actions.create.reset()
    actions.update.reset()
  }
  function submit() {
    if (mode === 'connect') {
      actions.update.mutate({ agent_id: agentID, enabled: true }, { onSuccess: onConnected })
    } else {
      // A fresh click may follow a rejected stale revision. Keep the request ID
      // so an ambiguous earlier success still replays without reselecting.
      requestID.current ??= crypto.randomUUID()
      actions.create.mutate(
        {
          orgID,
          body: {
            project_id: projectID,
            request_id: requestID.current,
            expected_revision: panel.revision,
            automatic: false,
            model_id: modelID || undefined,
          },
        },
        { onSuccess: onConnected },
      )
    }
  }

  return (
    <>
      <div className="space-y-2">
        <Label htmlFor="panel-project">Project</Label>
        <ProjectPicker
          id="panel-project"
          items={projects}
          query={projectsQuery}
          value={project}
          disabled={pending}
          onValueChange={(value) => {
            setProjectID(value?.id ?? '')
            setAgentID('')
            setModelID('')
            resetRequest()
          }}
        />
      </div>
      {project != null &&
        (mode === 'connect' ? (
          <div className="space-y-2">
            <Label htmlFor="panel-agent">Agent</Label>
            <PanelAgentPicker
              orgID={orgID}
              projectID={projectID}
              agentID={agentID}
              disabled={pending}
              onChange={(id) => {
                setAgentID(id)
                resetRequest()
              }}
            />
          </div>
        ) : (
          <>
            <div className="space-y-2">
              <Label htmlFor="panel-model">Model</Label>
              <PanelModelPicker
                orgID={orgID}
                projectID={projectID}
                modelID={modelID}
                disabled={pending}
                onChange={(id) => {
                  setModelID(id)
                  resetRequest()
                }}
              />
            </div>
            <p className="text-muted-foreground text-xs">
              Creates an ordinary agent with Omnara tools and your personal token. Project members
              with access can use it. Usage is billed to this organization. The model must support
              tools.
            </p>
          </>
        ))}
      <Button
        disabled={pending || project == null || (mode === 'connect' && agentID === '')}
        loading={pending}
        onClick={submit}
      >
        {mode === 'connect' ? 'Connect agent' : 'Create assistant'}
      </Button>
    </>
  )
}

function PanelAgentPicker({
  orgID,
  projectID,
  agentID,
  disabled,
  onChange,
}: {
  orgID: string
  projectID: string
  agentID: string
  disabled: boolean
  onChange: (id: string) => void
}) {
  const query = useAgents(orgID, projectID)
  const agents = useInfiniteQueryItems(query).filter((item) => item.project_id === projectID)
  return (
    <AgentPicker
      id="panel-agent"
      items={agents}
      query={query}
      value={agents.find((item) => item.id === agentID) ?? null}
      disabled={disabled}
      onValueChange={(value) => {
        onChange(value?.id ?? '')
      }}
    />
  )
}

function PanelModelPicker({
  orgID,
  projectID,
  modelID,
  disabled,
  onChange,
}: {
  orgID: string
  projectID: string
  modelID: string
  disabled: boolean
  onChange: (id: string) => void
}) {
  const query = useProjectModelGrants(orgID, projectID)
  const models = useInfiniteQueryItems(query).filter(
    (item) => item.grant.project_id === projectID && item.supports_tools,
  )
  return (
    <ModelPicker
      id="panel-model"
      items={models}
      query={query}
      value={models.find((item) => item.model.id === modelID) ?? null}
      disabled={disabled}
      onValueChange={(value) => {
        onChange(value?.model.id ?? '')
      }}
    />
  )
}
