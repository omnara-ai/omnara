import {
  useAgents,
  useCreateAssistantAgent,
  useMe,
  useUpdateAgentPanel,
  useVisibleProjectsList,
} from '@omnara/react'
import type { AgentPanel, CurrentUserOrg } from '@omnara/sdk'
import { useContext, useState } from 'react'

import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { ActiveOrgContext } from '@/lib/active-org-context'

import { AssistantCreateForm } from './AssistantCreateForm'
import { AgentPicker, OrgPicker, ProjectPicker } from './pickers'

type Mode = 'create' | 'connect'

const modes: { value: Mode; label: string }[] = [
  { value: 'create', label: 'Create new' },
  { value: 'connect', label: 'Connect existing' },
]

function initialMode(panel: AgentPanel, orgs: CurrentUserOrg[]): Mode {
  return panel.selection == null && orgs.some((org) => org.role === 'owner') ? 'create' : 'connect'
}

export function AgentPanelSetup({
  panel,
  onConnected,
  settings = false,
}: {
  panel: AgentPanel
  onConnected: () => void
  settings?: boolean
}) {
  const create = useCreateAssistantAgent()
  const update = useUpdateAgentPanel()
  const { data: me } = useMe()
  const activeOrg = useContext(ActiveOrgContext)?.activeOrg
  const [mode, setMode] = useState(() => initialMode(panel, me.orgs))
  const [selectedOrg, setSelectedOrg] = useState('')
  const orgs = mode === 'create' ? me.orgs.filter((org) => org.role === 'owner') : me.orgs
  const org =
    orgs.find((item) => item.id === selectedOrg) ??
    orgs.find((item) => item.id === activeOrg?.id) ??
    orgs[0]
  const pending = create.isPending || update.isPending
  function resetActions() {
    create.reset()
    update.reset()
  }
  function chooseMode(value: Mode) {
    setMode(value)
    resetActions()
  }

  return (
    <>
      <div className="space-y-4 overflow-y-auto p-4">
        <div className="flex gap-2" role="group" aria-label="Assistant setup">
          {modes.map(({ value, label }) => (
            <Button
              key={value}
              size="sm"
              variant={mode === value ? 'secondary' : 'outline'}
              aria-pressed={mode === value}
              disabled={pending}
              onClick={() => {
                chooseMode(value)
              }}
            >
              {label}
            </Button>
          ))}
        </div>
        {orgs.length === 0 ? (
          <p className="text-muted-foreground text-sm">
            {mode === 'create'
              ? 'Create or become an owner of an organization to create an assistant.'
              : 'Create or join an organization to connect an agent.'}
          </p>
        ) : (
          <>
            {orgs.length > 1 && (
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
            )}
            {org != null &&
              (mode === 'create' ? (
                <AssistantCreateForm
                  key={org.id}
                  idPrefix="panel"
                  org={org}
                  panel={panel}
                  create={create}
                  disabled={pending}
                  onChange={resetActions}
                  onCreated={onConnected}
                />
              ) : (
                <PanelConnectForm
                  key={org.id}
                  org={org}
                  update={update}
                  disabled={pending}
                  onChange={resetActions}
                  onConnected={onConnected}
                />
              ))}
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
      {settings && panel.selection != null && (
        <footer className="flex shrink-0 gap-2 border-t p-3">
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
        </footer>
      )}
    </>
  )
}

function PanelConnectForm({
  org,
  update,
  disabled,
  onChange,
  onConnected,
}: {
  org: CurrentUserOrg
  update: ReturnType<typeof useUpdateAgentPanel>
  disabled: boolean
  onChange: () => void
  onConnected: () => void
}) {
  const [projectID, setProjectID] = useState('')
  const [agentID, setAgentID] = useState('')
  const orgID = org.id
  const projectsQuery = useVisibleProjectsList(orgID, {})
  const projects = useInfiniteQueryItems(projectsQuery).filter((item) => item.org_id === orgID)
  const project = projects.find((item) => item.id === projectID) ?? null

  return (
    <>
      <div className="space-y-2">
        <Label htmlFor="panel-project">Project</Label>
        <ProjectPicker
          id="panel-project"
          items={projects}
          query={projectsQuery}
          value={project}
          disabled={disabled}
          onValueChange={(value) => {
            setProjectID(value?.id ?? '')
            setAgentID('')
            onChange()
          }}
        />
      </div>
      {project != null && (
        <div className="space-y-2">
          <Label htmlFor="panel-agent">Agent</Label>
          <PanelAgentPicker
            orgID={orgID}
            projectID={projectID}
            agentID={agentID}
            disabled={disabled}
            onChange={(id) => {
              setAgentID(id)
              onChange()
            }}
          />
        </div>
      )}
      <Button
        disabled={disabled || project == null || agentID === ''}
        loading={disabled}
        onClick={() => {
          update.mutate({ agent_id: agentID }, { onSuccess: onConnected })
        }}
      >
        Connect agent
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
