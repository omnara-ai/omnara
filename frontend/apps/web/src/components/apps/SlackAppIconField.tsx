import { useRef } from 'react'

import { Upload, X } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'

import { type AppIcon, fileSizeLabel, noAppIcon, validateAppIcon } from './ConnectSlackFormState'

export function SlackAppIconField({
  value,
  onChange,
}: {
  value: AppIcon
  onChange: (value: AppIcon) => void
}) {
  const appIconInputRef = useRef<HTMLInputElement>(null)
  function resetIconInput() {
    if (appIconInputRef.current) appIconInputRef.current.value = ''
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
          const next = validateAppIcon(event.target.files?.[0] ?? null)
          onChange(next)
          if (next.kind !== 'file') resetIconInput()
        }}
      />
      <div className="border-input bg-muted/20 flex items-center justify-between gap-3 rounded-md border border-dashed px-3 py-3">
        <div className="min-w-0 text-sm">
          {value.kind === 'file' ? (
            <span className="flex min-w-0 items-center gap-2">
              <span className="text-foreground truncate">{value.file.name}</span>
              <span className="text-muted-foreground shrink-0">
                {fileSizeLabel(value.file.size)}
              </span>
            </span>
          ) : (
            <span className="text-muted-foreground">No icon selected</span>
          )}
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {value.kind === 'file' && (
            <Button
              type="button"
              size="sm"
              variant="ghost"
              onClick={() => {
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
      {value.kind === 'error' && (
        <FieldDescription className="text-destructive">{value.message}</FieldDescription>
      )}
    </Field>
  )
}
