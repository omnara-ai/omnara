import type { Secret } from '@omnara/sdk'
import { useId, useState } from 'react'

import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { oauthKeys } from '@/lib/oauthEntries'

import { requiredSecretKeys } from './secretValueUpdates'

const awsFields = [
  { value: 'access_key_id', label: 'Access key ID' },
  { value: 'secret_access_key', label: 'Secret access key' },
  { value: 'session_token', label: 'Session token' },
  { value: 'role_arn', label: 'Role ARN' },
  { value: 'external_id', label: 'External ID' },
]

function StoredValueField({
  label,
  stored,
  required,
  multiline,
  value,
  onChange,
  onUndo,
}: {
  label: string
  stored: boolean
  required: boolean
  multiline: boolean
  value: string | undefined
  onChange: (value: string) => void
  onUndo: () => void
}) {
  const id = useId()
  const [revealed, setRevealed] = useState(false)
  const edited = value !== undefined
  return (
    <Field>
      <div className="flex items-center justify-between">
        <FieldLabel htmlFor={id}>{label}</FieldLabel>
        <div className="flex items-center gap-3">
          {stored && !required && value !== '' && (
            <Button
              type="button"
              variant="link"
              size="sm"
              className="type-control-small h-auto p-0"
              aria-label={`Clear ${label.toLowerCase()}`}
              onClick={() => {
                setRevealed(false)
                onChange('')
              }}
            >
              Clear
            </Button>
          )}
          {edited && (
            <Button
              type="button"
              variant="link"
              size="sm"
              className="type-control-small h-auto p-0"
              onClick={() => {
                setRevealed(false)
                onUndo()
              }}
              aria-label={`Undo ${label.toLowerCase()} change`}
            >
              Undo
            </Button>
          )}
        </div>
      </div>
      <div className="flex gap-2">
        {multiline ? (
          <Textarea
            id={id}
            autoComplete="off"
            spellCheck={false}
            value={value ?? ''}
            placeholder={stored && !edited ? '••••••••••••' : undefined}
            className={revealed ? undefined : '[-webkit-text-security:disc]'}
            onChange={(event) => {
              if (event.target.value === '') setRevealed(false)
              onChange(event.target.value)
            }}
          />
        ) : (
          <Input
            id={id}
            type={revealed ? 'text' : 'password'}
            autoComplete="new-password"
            value={value ?? ''}
            placeholder={stored && !edited ? '••••••••••••' : undefined}
            onChange={(event) => {
              if (event.target.value === '') setRevealed(false)
              onChange(event.target.value)
            }}
          />
        )}
        {edited && value !== '' && (
          <Button
            type="button"
            variant="ghost"
            size="sm"
            aria-label={`${revealed ? 'Hide' : 'Show'} ${label.toLowerCase()}`}
            onClick={() => {
              setRevealed(!revealed)
            }}
          >
            {revealed ? 'Hide' : 'Show'}
          </Button>
        )}
      </div>
      {stored && value === '' && !required && (
        <FieldDescription>Will be removed when you save.</FieldDescription>
      )}
    </Field>
  )
}

export function SecretValueEditor({
  secret,
  updates,
  onChange,
}: {
  secret: Secret
  updates: Record<string, string>
  onChange: (updates: Record<string, string>) => void
}) {
  const [added, setAdded] = useState<string[]>([])
  const fields =
    secret.kind === 'generic'
      ? [{ value: 'value', label: 'Value' }]
      : secret.kind === 'aws_credentials'
        ? awsFields
        : oauthKeys.filter((field) => field.value !== 'access_token_expires_in_seconds')
  const required = requiredSecretKeys(secret.kind)
  if (!required) return null
  const visible = fields.filter(
    (field) =>
      required.includes(field.value) ||
      secret.payload_keys.includes(field.value) ||
      added.includes(field.value),
  )
  const available = fields.filter((field) => !visible.some((item) => item.value === field.value))
  return (
    <>
      {visible.map((field) => (
        <StoredValueField
          key={field.value}
          label={field.label}
          multiline={secret.kind === 'generic'}
          required={required.includes(field.value)}
          stored={secret.payload_keys.includes(field.value)}
          value={updates[field.value]}
          onChange={(value) => {
            onChange({ ...updates, [field.value]: value })
          }}
          onUndo={() => {
            const next = Object.fromEntries(
              Object.entries(updates).filter(([key]) => key !== field.value),
            )
            onChange(next)
            setAdded(added.filter((key) => key !== field.value))
          }}
        />
      ))}
      {available.length > 0 && (
        <Select
          value=""
          onValueChange={(key) => {
            setAdded([...added, key])
          }}
        >
          <SelectTrigger aria-label="Add credential field">
            <SelectValue placeholder="Add field">Add field</SelectValue>
          </SelectTrigger>
          <SelectContent>
            {available.map((field) => (
              <SelectItem key={field.value} value={field.value}>
                {field.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      )}
    </>
  )
}
