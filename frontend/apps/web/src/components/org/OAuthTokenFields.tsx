import { Plus, X } from '@/components/icons'
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
import { isOAuthKey, newOAuthEntry, type OAuthEntry, oauthKeys } from '@/lib/oauthEntries'

export function OAuthTokenFields({
  entries,
  onChange,
}: {
  entries: OAuthEntry[]
  onChange: (entries: OAuthEntry[]) => void
}) {
  const usedKeys = new Set(entries.map((entry) => entry.key))
  const firstUnusedKey = oauthKeys.find((option) => !usedKeys.has(option.value))?.value
  function patchEntry(id: string, patch: Partial<OAuthEntry>) {
    onChange(entries.map((entry) => (entry.id === id ? { ...entry, ...patch } : entry)))
  }

  return (
    <Field>
      <FieldLabel>Token fields</FieldLabel>
      <div className="flex flex-col gap-2">
        {entries.map((entry) => (
          <div key={entry.id} className="flex gap-2">
            <Select
              value={entry.key}
              onValueChange={(value) => {
                if (isOAuthKey(value)) patchEntry(entry.id, { key: value })
              }}
            >
              <SelectTrigger className="w-2/5">
                <SelectValue>
                  {oauthKeys.find((option) => option.value === entry.key)?.label ?? entry.key}
                </SelectValue>
              </SelectTrigger>
              <SelectContent>
                {oauthKeys.map((option) =>
                  option.value === entry.key || !usedKeys.has(option.value) ? (
                    <SelectItem key={option.value} value={option.value}>
                      {option.label}
                    </SelectItem>
                  ) : null,
                )}
              </SelectContent>
            </Select>
            <Input
              aria-label="Value"
              type={entry.key === 'access_token_expires_in_seconds' ? 'number' : 'password'}
              min={entry.key === 'access_token_expires_in_seconds' ? 1 : undefined}
              max={entry.key === 'access_token_expires_in_seconds' ? 2147483647 : undefined}
              value={entry.value}
              autoComplete="new-password"
              placeholder="value"
              className="flex-1"
              onChange={(event) => {
                patchEntry(entry.id, { value: event.target.value })
              }}
            />
            <Button
              type="button"
              variant="ghost"
              size="icon"
              disabled={entries.length === 1}
              aria-label="Remove field"
              onClick={() => {
                onChange(entries.filter((item) => item.id !== entry.id))
              }}
            >
              <X />
            </Button>
          </div>
        ))}
        {firstUnusedKey !== undefined && (
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="self-start"
            onClick={() => {
              onChange([...entries, newOAuthEntry(firstUnusedKey)])
            }}
          >
            <Plus />
            Add field
          </Button>
        )}
      </div>
      <FieldDescription>An access token is required.</FieldDescription>
    </Field>
  )
}
