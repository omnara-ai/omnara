import { useProjectAvailableSecrets, useSecrets } from '@omnara/react'
import type { Secret } from '@omnara/sdk'
import { PlusIcon } from 'lucide-react'
import { useEffect, useState } from 'react'

import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { createResourceCombobox } from '@/components/ui/resource-combobox'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { useTypeaheadSearch } from '@/hooks/use-resource-list'

const SecretCombobox = createResourceCombobox<Secret>({
  itemKey: (secret) => secret.id,
  itemLabel: (secret) => secret.name,
  placeholder: 'Search secrets…',
  emptyMessage: 'No matching secrets found.',
})

/** Secrets machines and providers can reference at runtime. */
function referencableSecret(secret: Secret) {
  return secret.management_kind === 'tenant' && secret.kind === 'generic'
}

export function SecretSelect({
  orgId,
  projectId,
  enabled,
  value,
  onChange,
  placeholder = 'Search secrets…',
  onCreateSecret,
  knownSecret,
  onEmptyChange,
}: {
  orgId: string
  /** When set, offers project-available secrets instead of org-owned ones. */
  projectId?: string
  enabled: boolean
  value: string
  onChange: (value: string) => void
  placeholder?: string
  onCreateSecret?: () => void
  knownSecret?: Secret
  onEmptyChange?: (empty: boolean) => void
}) {
  const search = useTypeaheadSearch()
  const orgQuery = useSecrets(
    orgId,
    { kind: 'org' },
    {
      filters: search.filters,
      sort: 'name',
      pageSize: 25,
      enabled: enabled && projectId === undefined,
    },
  )
  const projectQuery = useProjectAvailableSecrets(orgId, projectId ?? '', {
    filters: search.filters,
    sort: 'name',
    pageSize: 25,
    enabled: enabled && projectId !== undefined,
  })
  const query = projectId === undefined ? orgQuery : projectQuery
  const orgSecrets = useInfiniteQueryItems(orgQuery)
  const projectSecrets = useInfiniteQueryItems(projectQuery).map((access) => access.secret)
  const listedSecrets = (projectId === undefined ? orgSecrets : projectSecrets).filter(
    referencableSecret,
  )
  const secrets =
    knownSecret && !listedSecrets.some((secret) => secret.id === knownSecret.id)
      ? [knownSecret, ...listedSecrets]
      : listedSecrets
  const empty = !query.isPending && !query.isError && secrets.length === 0
  useEffect(() => {
    onEmptyChange?.(empty)
  }, [empty, onEmptyChange])

  return (
    <SecretCombobox
      items={secrets}
      value={value || null}
      onValueChange={(secret) => {
        onChange(secret?.id ?? '')
      }}
      search={search}
      query={query}
      placeholder={placeholder}
      action={
        onCreateSecret && (
          <button
            type="button"
            className="hover:bg-accent hover:text-accent-foreground flex w-full cursor-default items-center gap-2 rounded-sm py-2 pl-2 pr-8 text-sm outline-none"
            onClick={onCreateSecret}
          >
            <PlusIcon className="size-4" />
            New secret
          </button>
        )
      }
    />
  )
}

export function SecretTypeaheadField({
  orgId,
  enabled,
  value,
  onChange,
  label,
  placeholder = 'Search secrets…',
  emptyDescription,
  onCreateSecret,
  knownSecret,
}: {
  orgId: string
  enabled: boolean
  value: string
  onChange: (value: string) => void
  label: string
  placeholder?: string
  emptyDescription: string
  onCreateSecret?: () => void
  knownSecret?: Secret
}) {
  const [empty, setEmpty] = useState(false)

  return (
    <Field>
      <FieldLabel>{label}</FieldLabel>
      <SecretSelect
        orgId={orgId}
        enabled={enabled}
        value={value}
        onChange={onChange}
        placeholder={placeholder}
        onCreateSecret={onCreateSecret}
        knownSecret={knownSecret}
        onEmptyChange={setEmpty}
      />
      {empty && <FieldDescription>{emptyDescription}</FieldDescription>}
    </Field>
  )
}
