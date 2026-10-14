import { useRef } from 'react'

import { Upload, X } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldError, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'

import {
  type AppIcon,
  noAppIcon,
  slackIconRequirements,
  validateAppIcon,
} from './ConnectSlackFormState'

export function SlackAppIconField({
  value,
  onChange,
}: {
  value: AppIcon
  onChange: (value: AppIcon) => void
}) {
  const appIconInputRef = useRef<HTMLInputElement>(null)
  const selection = useRef(0)
  function resetIconInput() {
    if (appIconInputRef.current) appIconInputRef.current.value = ''
  }
  async function selectIcon(file: File | null) {
    const current = ++selection.current
    onChange({ kind: 'checking' })
    const next = await validateAppIcon(file)
    if (current !== selection.current || !appIconInputRef.current) return
    onChange(next)
    if (next.kind !== 'file') resetIconInput()
  }
  return (
    <Field className="gap-2.5">
      <FieldLabel htmlFor="slack-app-icon">Slack app icon (optional)</FieldLabel>
      <Input
        id="slack-app-icon"
        ref={appIconInputRef}
        type="file"
        accept="image/png,image/jpeg"
        className="hidden"
        onChange={(event) => {
          void selectIcon(event.target.files?.[0] ?? null)
        }}
      />
      <div className="border-input bg-muted/20 flex items-center justify-between gap-3 rounded-md border border-dashed px-3 py-3">
        <div className="min-w-0 text-sm">
          {value.kind === 'file' ? (
            <span className="text-foreground block truncate">{value.file.name}</span>
          ) : (
            <span className="text-muted-foreground">
              {value.kind === 'checking' ? 'Checking icon…' : 'No icon selected'}
            </span>
          )}
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {value.kind === 'file' && (
            <Button
              type="button"
              size="sm"
              variant="ghost"
              onClick={() => {
                selection.current++
                onChange(noAppIcon)
                resetIconInput()
              }}
            >
              <X />
              Remove
            </Button>
          )}
          <Button
            type="button"
            size="sm"
            variant="outline"
            onClick={() => appIconInputRef.current?.click()}
          >
            <Upload />
            Choose icon
          </Button>
        </div>
      </div>
      <FieldDescription>{slackIconRequirements()}</FieldDescription>
      {value.kind === 'error' && <FieldError>{value.message} No icon was added.</FieldError>}
    </Field>
  )
}
