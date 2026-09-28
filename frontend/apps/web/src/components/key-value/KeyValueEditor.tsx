import type { Secret } from '@omnara/sdk'
import { useState } from 'react'

import { PillTabs } from '@/components/agents/PillTabs'
import { PlusIcon, Trash2Icon } from '@/components/icons'
import { NewSecretDialog } from '@/components/secrets/NewSecretDialog'
import { SecretSelect } from '@/components/secrets/SecretTypeaheadField'
import { Button } from '@/components/ui/button'
import { Field, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

import { newTextRow, type SecretRow, type TextRow } from './keyValueRows'

export function KeyValueEditor({
  orgId,
  projectId,
  enabled,
  label,
  itemLabel,
  keyPlaceholder,
  textRows,
  secretRows,
  onChange,
}: {
  orgId: string
  projectId?: string
  enabled: boolean
  label: string
  itemLabel: string
  keyPlaceholder: string
  textRows: TextRow[]
  secretRows: SecretRow[]
  onChange: (value: { textRows: TextRow[]; secretRows: SecretRow[] }) => void
}) {
  function updateTextRow(id: string, patch: Partial<TextRow>) {
    onChange({
      textRows: textRows.map((row) => (row.id === id ? { ...row, ...patch } : row)),
      secretRows,
    })
  }
  function updateSecretRow(id: string, patch: Partial<SecretRow>) {
    onChange({
      textRows,
      secretRows: secretRows.map((row) => (row.id === id ? { ...row, ...patch } : row)),
    })
  }
  const combinedRows = [
    ...textRows.map((row) => ({ kind: 'text' as const, ...row })),
    ...secretRows.map((row) => ({ kind: 'secret' as const, ...row })),
  ]
  const [creatingSecretFor, setCreatingSecretFor] = useState<string | null>(null)
  const [createdSecrets, setCreatedSecrets] = useState<ReadonlyMap<string, Secret>>(new Map())
  function setKind(row: (typeof combinedRows)[number], kind: string, unset: boolean) {
    if (kind === row.kind) return
    if (kind === 'secret') {
      onChange({
        textRows: textRows.filter((candidate) => candidate.id !== row.id),
        secretRows: [...secretRows, { id: row.id, key: row.key, secretId: unset ? null : '' }],
      })
      return
    }
    onChange({
      textRows: [...textRows, { id: row.id, key: row.key, value: unset ? null : '' }],
      secretRows: secretRows.filter((candidate) => candidate.id !== row.id),
    })
  }
  function removeRow(row: (typeof combinedRows)[number]) {
    if (row.kind === 'text') {
      onChange({ textRows: textRows.filter((candidate) => candidate.id !== row.id), secretRows })
      return
    }
    onChange({
      textRows,
      secretRows: secretRows.filter((candidate) => candidate.id !== row.id),
    })
  }
  return (
    <Field>
      <div className="flex items-center justify-between gap-3">
        <FieldLabel>{label}</FieldLabel>
        <Button
          type="button"
          size="sm"
          variant="ghost"
          className="text-muted-foreground -my-1"
          onClick={() => {
            onChange({ textRows: [...textRows, newTextRow()], secretRows })
          }}
        >
          <PlusIcon />
          Add {itemLabel.toLowerCase()}
        </Button>
      </div>
      <div className="overflow-hidden rounded-xl border">
        <div className="after:border-border/50 after:bg-muted/50 after:shadow-xs relative isolate after:pointer-events-none after:absolute after:-inset-x-px after:-top-px after:-z-10 after:hidden after:h-[calc(2.25rem+2px)] after:rounded-xl after:border sm:after:block">
          <Table className="block sm:table sm:table-fixed">
            <TableHeader className="hidden bg-transparent sm:table-header-group [&_tr]:border-0">
              <TableRow className="hover:bg-transparent">
                <TableHead className="h-[calc(2.25rem+3px)] w-40 px-4 pb-[3px]">Type</TableHead>
                <TableHead className="h-[calc(2.25rem+3px)] px-4 pb-[3px]">Key</TableHead>
                <TableHead className="h-[calc(2.25rem+3px)] px-4 pb-[3px]">Value</TableHead>
                <TableHead className="h-[calc(2.25rem+3px)] w-14 px-2 pb-[3px]">
                  <span className="sr-only">Remove</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody className="block sm:table-row-group">
              {combinedRows.length === 0 && (
                <TableRow className="block sm:table-row">
                  <TableCell
                    colSpan={4}
                    className="text-muted-foreground block whitespace-normal px-4 py-3 text-center sm:table-cell"
                  >
                    No {label.toLowerCase()}
                  </TableCell>
                </TableRow>
              )}
              {combinedRows.map((row) => {
                const unset = row.kind === 'text' ? row.value === null : row.secretId === null
                return (
                  <TableRow
                    key={row.id}
                    className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-2 p-3 hover:bg-transparent sm:table-row sm:p-0"
                  >
                    <TableCell className="block p-0 sm:table-cell sm:px-4 sm:py-2">
                      <PillTabs
                        value={row.kind}
                        onValueChange={(kind) => {
                          setKind(row, kind, unset)
                        }}
                        tabs={[
                          { value: 'text', label: 'Text' },
                          { value: 'secret', label: 'Secret' },
                        ]}
                      />
                    </TableCell>
                    <TableCell className="col-span-2 block p-0 sm:table-cell sm:px-4 sm:py-2">
                      <Input
                        value={row.key}
                        autoComplete="off"
                        placeholder={keyPlaceholder}
                        aria-label={`${itemLabel} name`}
                        className="font-mono"
                        onChange={(event) => {
                          if (row.kind === 'text') {
                            updateTextRow(row.id, { key: event.target.value })
                          } else {
                            updateSecretRow(row.id, { key: event.target.value })
                          }
                        }}
                      />
                    </TableCell>
                    <TableCell className="col-span-2 block p-0 sm:table-cell sm:px-4 sm:py-2">
                      {row.kind === 'text' ? (
                        <Input
                          value={row.value ?? ''}
                          autoComplete="off"
                          placeholder={unset ? 'unset — removes the inherited value' : 'value'}
                          aria-label={`${itemLabel} value`}
                          onChange={(event) => {
                            updateTextRow(row.id, { value: event.target.value })
                          }}
                        />
                      ) : row.secretId === null ? (
                        <Input
                          disabled
                          value=""
                          placeholder="unset — removes the inherited value"
                          aria-label={`${itemLabel} value`}
                        />
                      ) : (
                        <SecretSelect
                          orgId={orgId}
                          projectId={projectId}
                          enabled={enabled}
                          value={row.secretId}
                          knownSecret={createdSecrets.get(row.secretId)}
                          onChange={(secretId) => {
                            updateSecretRow(row.id, { secretId })
                          }}
                          onCreateSecret={() => {
                            setCreatingSecretFor(row.id)
                          }}
                        />
                      )}
                    </TableCell>
                    <TableCell className="col-start-2 row-start-1 block p-0 sm:table-cell sm:px-2 sm:py-2">
                      <Button
                        type="button"
                        size="icon"
                        variant="ghost"
                        className="text-muted-foreground size-10 sm:size-8"
                        aria-label={`Remove ${itemLabel.toLowerCase()}`}
                        onClick={() => {
                          removeRow(row)
                        }}
                      >
                        <Trash2Icon />
                      </Button>
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        </div>
      </div>
      {creatingSecretFor !== null && (
        <NewSecretDialog
          orgId={orgId}
          projectId={projectId}
          defaultName={secretRows.find((row) => row.id === creatingSecretFor)?.key ?? ''}
          onClose={() => {
            setCreatingSecretFor(null)
          }}
          onCreated={(secret) => {
            setCreatedSecrets((current) => new Map(current).set(secret.id, secret))
            updateSecretRow(creatingSecretFor, { secretId: secret.id })
            setCreatingSecretFor(null)
          }}
        />
      )}
    </Field>
  )
}
