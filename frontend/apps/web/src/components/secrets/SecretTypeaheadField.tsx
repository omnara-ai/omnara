import {
  useProjectAvailableSecret,
  useProjectAvailableSecrets,
  useSecret,
  useSecrets,
} from '@omnara/react'
import type { Secret, SecretKind } from '@omnara/sdk'

import { PlusIcon } from '@/components/icons'
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
function referencableSecret(secret: Secret, kind: SecretKind) {
  return secret.management_kind === 'tenant' && secret.kind === kind
}

function listedSecrets(
  orgSecrets: Secret[],
  projectSecrets: Secret[],
  projectId: string | undefined,
  kind: SecretKind,
) {
  return (projectId === undefined ? orgSecrets : projectSecrets).filter((secret) =>
    referencableSecret(secret, kind),
  )
}

function includeKnownSecret(secrets: Secret[], knownSecret: Secret | undefined, kind: SecretKind) {
  if (!knownSecret || !referencableSecret(knownSecret, kind)) return secrets
  if (secrets.some((secret) => secret.id === knownSecret.id)) return secrets
  return [knownSecret, ...secrets]
}

function selectedSecret(
  secrets: Secret[],
  resolvedSecret: Secret | undefined,
  value: string,
  kind: SecretKind,
) {
  const listed = secrets.find((secret) => secret.id === value)
  if (listed) return listed
  if (resolvedSecret?.id === value && referencableSecret(resolvedSecret, kind)) {
    return resolvedSecret
  }
  return null
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
  emptyDescription,
  kind = 'generic',
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
  emptyDescription?: string
  kind?: SecretKind
}) {
  const search = useTypeaheadSearch()
  const orgQuery = useSecrets(
    orgId,
    { kind: 'org' },
    {
      filters: { ...search.filters, kind },
      sort: 'name',
      pageSize: 25,
      enabled: enabled && projectId === undefined,
    },
  )
  const projectQuery = useProjectAvailableSecrets(orgId, projectId ?? '', {
    filters: { ...search.filters, kind },
    sort: 'name',
    pageSize: 25,
    enabled: enabled && projectId !== undefined,
  })
  const query = projectId === undefined ? orgQuery : projectQuery
  const orgSecrets = useInfiniteQueryItems(orgQuery)
  const projectSecrets = useInfiniteQueryItems(projectQuery).map((access) => access.secret)
  const secrets = includeKnownSecret(
    listedSecrets(orgSecrets, projectSecrets, projectId, kind),
    knownSecret,
    kind,
  )
  const selectedOrgSecret = useSecret(orgId, value, {
    enabled: enabled && projectId === undefined,
  })
  const selectedProjectSecret = useProjectAvailableSecret(orgId, projectId ?? '', value, {
    enabled: enabled && projectId !== undefined,
  })
  const resolvedSecret = selectedProjectSecret.data?.secret ?? selectedOrgSecret.data
  const selected = selectedSecret(secrets, resolvedSecret, value, kind)
  const empty = !query.isPending && !query.isError && secrets.length === 0

  return (
    <>
      <SecretCombobox
        items={secrets}
        value={selected}
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
      {empty && emptyDescription && <FieldDescription>{emptyDescription}</FieldDescription>}
    </>
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
  kind = 'generic',
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
  kind?: SecretKind
}) {
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
        emptyDescription={emptyDescription}
        kind={kind}
      />
    </Field>
  )
}
