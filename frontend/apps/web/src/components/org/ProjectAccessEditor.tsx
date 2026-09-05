import { useVisibleProjectsList } from '@omnara/react'
import type {
  ListProjectMembershipGrantsResponse,
  ProjectMembershipGrant,
  VisibleProject,
} from '@omnara/sdk'
import type { UseMutationResult } from '@tanstack/react-query'
import { useState } from 'react'

import { Button } from '@/components/ui/button'
import { createResourceCombobox } from '@/components/ui/resource-combobox'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Spinner } from '@/components/ui/spinner'
import { Table, TableBody, TableCell, TableRow } from '@/components/ui/table'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { errorMessage } from '@/lib/submit-status'

const PROJECT_ROLES = ['viewer', 'operator', 'developer', 'admin'] as const

type ProjectRole = (typeof PROJECT_ROLES)[number]

type SetAccessMutation = UseMutationResult<
  ProjectMembershipGrant,
  Error,
  { projectID: string; role: string }
>

type RemoveAccessMutation = UseMutationResult<void, Error, string>

const ProjectCombobox = createResourceCombobox<VisibleProject>({
  itemKey: (project) => project.id,
  itemLabel: (project) => project.name,
  placeholder: 'Add a project…',
  emptyMessage: 'No more projects to add.',
})

interface ProjectAccessQuery {
  isPending: boolean
  isError: boolean
  isFetching: boolean
  error: unknown
  data?: ListProjectMembershipGrantsResponse
  refetch: () => void
}

function RoleSelect({
  value,
  disabled,
  onChange,
}: {
  value: string
  disabled?: boolean
  onChange: (role: ProjectRole) => void
}) {
  return (
    <Select
      value={value}
      disabled={disabled}
      onValueChange={(next) => {
        const role = PROJECT_ROLES.find((candidate) => candidate === next)
        if (role !== undefined) onChange(role)
      }}
    >
      <SelectTrigger className="h-10 w-full capitalize sm:h-8">
        <SelectValue>{value}</SelectValue>
      </SelectTrigger>
      <SelectContent>
        {PROJECT_ROLES.map((role) => (
          <SelectItem key={role} value={role} className="capitalize">
            {role}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

function LoadError({
  message,
  retrying,
  onRetry,
}: {
  message: string
  retrying: boolean
  onRetry: () => void
}) {
  return (
    <div role="alert" className="border-destructive/30 bg-destructive/5 rounded-md border p-3">
      <p className="text-destructive text-sm">{message}</p>
      <Button
        type="button"
        size="sm"
        variant="outline"
        className="mt-3"
        disabled={retrying}
        loading={retrying}
        onClick={() => {
          onRetry()
        }}
      >
        Retry
      </Button>
    </div>
  )
}

function MutationErrorRow({ message }: { message: string }) {
  if (!message) return null
  return (
    <TableRow className="hover:bg-transparent">
      <TableCell colSpan={3} className="p-0 pt-1">
        <p role="alert" className="text-destructive text-xs">
          {message}
        </p>
      </TableCell>
    </TableRow>
  )
}

function GrantRow({
  grant,
  setAccess,
  removeAccess,
}: {
  grant: ProjectMembershipGrant
  setAccess: SetAccessMutation
  removeAccess: RemoveAccessMutation
}) {
  return (
    <TableRow className="h-11 hover:bg-transparent">
      <TableCell className="truncate p-0 pr-3 font-medium">{grant.project_name}</TableCell>
      <TableCell className="p-0 pr-2">
        <RoleSelect
          value={grant.role}
          onChange={(role) => {
            setAccess.mutate({ projectID: grant.project_id, role })
          }}
        />
      </TableCell>
      <TableCell className="p-0 text-right">
        <Button
          type="button"
          size="sm"
          variant="ghost"
          className="text-muted-foreground hover:text-destructive"
          onClick={() => {
            removeAccess.mutate(grant.project_id)
          }}
        >
          Remove
        </Button>
      </TableCell>
    </TableRow>
  )
}

function AddGrantRow({
  availableProjects,
  projectsQuery,
  setAccess,
}: {
  availableProjects: VisibleProject[]
  projectsQuery: ReturnType<typeof useVisibleProjectsList>
  setAccess: SetAccessMutation
}) {
  const [selectedProjectId, setSelectedProjectId] = useState('')
  const [selectedRole, setSelectedRole] = useState<ProjectRole>('viewer')
  const selectedProject =
    availableProjects.find((project) => project.id === selectedProjectId) ?? null
  const disabled = !selectedProject || setAccess.isPending

  function addAccess() {
    if (!selectedProject) return
    setAccess.mutate(
      { projectID: selectedProject.id, role: selectedRole },
      {
        onSuccess: () => {
          setSelectedProjectId('')
        },
      },
    )
  }

  return (
    <TableRow className="h-11 hover:bg-transparent">
      <TableCell className="p-0 pr-3">
        <ProjectCombobox
          items={availableProjects}
          value={selectedProject}
          onValueChange={(project) => {
            setSelectedProjectId(project?.id ?? '')
          }}
          query={projectsQuery}
          disabled={setAccess.isPending}
        />
      </TableCell>
      <TableCell className="p-0 pr-2">
        <RoleSelect value={selectedRole} disabled={disabled} onChange={setSelectedRole} />
      </TableCell>
      <TableCell className="p-0 text-right">
        <Button
          type="button"
          size="sm"
          variant="default"
          className="disabled:bg-muted disabled:text-muted-foreground disabled:opacity-60 disabled:shadow-none"
          disabled={disabled}
          loading={setAccess.isPending}
          onClick={addAccess}
        >
          Add
        </Button>
      </TableCell>
    </TableRow>
  )
}

export function ProjectAccessEditor({
  orgId,
  accessQuery,
  setAccess,
  removeAccess,
}: {
  orgId: string
  accessQuery: ProjectAccessQuery
  setAccess: SetAccessMutation
  removeAccess: RemoveAccessMutation
}) {
  const projectsQuery = useVisibleProjectsList(orgId)
  const projects = useInfiniteQueryItems(projectsQuery)

  if (accessQuery.isPending || projectsQuery.isPending) {
    return <Spinner />
  }

  if (accessQuery.isError) {
    return (
      <LoadError
        message={errorMessage(accessQuery.error, 'Could not load project access.')}
        retrying={accessQuery.isFetching}
        onRetry={() => {
          accessQuery.refetch()
        }}
      />
    )
  }

  if (projectsQuery.isError) {
    return (
      <LoadError
        message={errorMessage(projectsQuery.error, 'Could not load the available projects.')}
        retrying={projectsQuery.isFetching}
        onRetry={() => void projectsQuery.refetch()}
      />
    )
  }

  const grants = accessQuery.data?.data ?? []
  const grantedProjectIds = new Set(grants.map((grant) => grant.project_id))
  const availableProjects = projects.filter((project) => !grantedProjectIds.has(project.id))

  if (grants.length === 0 && availableProjects.length === 0 && !projectsQuery.hasNextPage) {
    return <p className="text-muted-foreground">No projects available to add yet.</p>
  }

  return (
    <Table className="min-w-[22rem] table-fixed sm:min-w-0">
      <colgroup>
        <col />
        <col className="w-36" />
        <col className="w-20" />
      </colgroup>
      <TableBody>
        {grants.map((grant) => (
          <GrantRow
            key={grant.project_id}
            grant={grant}
            setAccess={setAccess}
            removeAccess={removeAccess}
          />
        ))}
        <AddGrantRow
          availableProjects={availableProjects}
          projectsQuery={projectsQuery}
          setAccess={setAccess}
        />
        <MutationErrorRow
          message={
            setAccess.isError
              ? errorMessage(setAccess.error, 'Could not update project access.')
              : ''
          }
        />
        <MutationErrorRow
          message={
            removeAccess.isError
              ? errorMessage(removeAccess.error, 'Could not remove project access.')
              : ''
          }
        />
      </TableBody>
    </Table>
  )
}
