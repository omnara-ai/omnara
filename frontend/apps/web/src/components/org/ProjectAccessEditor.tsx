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
  refetch: () => Promise<unknown>
}

export function ProjectAccessEditor({
  orgId,
  accessQuery,
  setAccess,
  removeAccess,
}: {
  orgId: string
  accessQuery: ProjectAccessQuery
  setAccess: UseMutationResult<ProjectMembershipGrant, Error, { projectID: string; role: string }>
  removeAccess: UseMutationResult<void, Error, string>
}) {
  const projectsQuery = useVisibleProjectsList(orgId)
  const projects = useInfiniteQueryItems(projectsQuery)
  const [selectedProjectId, setSelectedProjectId] = useState('')
  const [selectedRole, setSelectedRole] = useState<(typeof PROJECT_ROLES)[number]>('viewer')

  if (accessQuery.isPending || projectsQuery.isPending) {
    return <Spinner />
  }

  if (accessQuery.isError || projectsQuery.isError) {
    const message = accessQuery.isError
      ? errorMessage(accessQuery.error, 'Could not load project access.')
      : errorMessage(projectsQuery.error, 'Could not load the available projects.')
    return (
      <div role="alert" className="border-destructive/30 bg-destructive/5 rounded-md border p-3">
        <p className="text-destructive text-sm">{message}</p>
        <Button
          type="button"
          size="sm"
          variant="outline"
          className="mt-3"
          disabled={accessQuery.isFetching || projectsQuery.isFetching}
          loading={accessQuery.isFetching || projectsQuery.isFetching}
          onClick={() => {
            if (accessQuery.isError) void accessQuery.refetch()
            if (projectsQuery.isError) void projectsQuery.refetch()
          }}
        >
          Retry
        </Button>
      </div>
    )
  }

  const grants = accessQuery.data?.data ?? []
  const grantedProjectIds = new Set(grants.map((grant) => grant.project_id))
  const availableProjects = projects.filter((project) => !grantedProjectIds.has(project.id))
  const selectedProject =
    availableProjects.find((project) => project.id === selectedProjectId) ?? null
  const setAccessError = setAccess.isError
    ? errorMessage(setAccess.error, 'Could not update project access.')
    : ''
  const removeAccessError = removeAccess.isError
    ? errorMessage(removeAccess.error, 'Could not remove project access.')
    : ''

  if (grants.length === 0 && availableProjects.length === 0 && !projectsQuery.hasNextPage) {
    return <p className="text-muted-foreground">No projects available to add yet.</p>
  }

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
    <Table className="table-fixed">
      <colgroup>
        <col />
        <col className="w-36" />
        <col className="w-20" />
      </colgroup>
      <TableBody>
        {grants.map((grant) => (
          <TableRow key={grant.project_id} className="h-11 hover:bg-transparent">
            <TableCell className="truncate p-0 pr-3 font-medium">{grant.project_name}</TableCell>
            <TableCell className="p-0 pr-2">
              <Select
                value={grant.role}
                onValueChange={(role) => {
                  setAccess.mutate({ projectID: grant.project_id, role })
                }}
              >
                <SelectTrigger className="h-8 w-full capitalize">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {PROJECT_ROLES.map((role) => (
                    <SelectItem key={role} value={role} className="capitalize">
                      {role}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
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
        ))}
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
            <Select
              value={selectedRole}
              disabled={!selectedProject || setAccess.isPending}
              onValueChange={(value) => {
                setSelectedRole(value as (typeof PROJECT_ROLES)[number])
              }}
            >
              <SelectTrigger className="h-8 w-full capitalize">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {PROJECT_ROLES.map((role) => (
                  <SelectItem key={role} value={role} className="capitalize">
                    {role}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </TableCell>
          <TableCell className="p-0 text-right">
            <Button
              type="button"
              size="sm"
              variant="default"
              className="disabled:bg-muted disabled:text-muted-foreground disabled:opacity-60 disabled:shadow-none"
              disabled={!selectedProject || setAccess.isPending}
              loading={setAccess.isPending}
              onClick={addAccess}
            >
              Add
            </Button>
          </TableCell>
        </TableRow>
        {setAccessError && (
          <TableRow className="hover:bg-transparent">
            <TableCell colSpan={3} className="p-0 pt-1">
              <p role="alert" className="text-destructive text-xs">
                {setAccessError}
              </p>
            </TableCell>
          </TableRow>
        )}
        {removeAccessError && (
          <TableRow className="hover:bg-transparent">
            <TableCell colSpan={3} className="p-0 pt-1">
              <p role="alert" className="text-destructive text-xs">
                {removeAccessError}
              </p>
            </TableCell>
          </TableRow>
        )}
      </TableBody>
    </Table>
  )
}
