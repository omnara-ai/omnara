import type { ProjectApp } from '@omnara/sdk'

import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'

export function ProjectAppNameField({
  name,
  onChange,
  saved,
}: {
  name: string
  onChange: (name: string) => void
  saved?: ProjectApp
}) {
  return (
    <Field>
      <FieldLabel htmlFor="app-name">App name</FieldLabel>
      <Input
        id="app-name"
        name="name"
        required
        maxLength={32}
        pattern="[A-Za-z][A-Za-z0-9-]{0,31}"
        value={name}
        readOnly={Boolean(saved)}
        onChange={(event) => {
          onChange(event.target.value)
        }}
      />
      <FieldDescription>
        {saved ? (
          <>
            App saved. Retrying uses this same app. You can also{' '}
            <a
              className="underline underline-offset-2"
              href={`/projects/${saved.project_id}/apps/${saved.id}`}
            >
              resume setup from its page
            </a>
            .
          </>
        ) : (
          'Start with a letter; use up to 32 letters, numbers or hyphens. This name cannot be changed.'
        )}
      </FieldDescription>
    </Field>
  )
}
