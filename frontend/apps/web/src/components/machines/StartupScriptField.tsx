import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Textarea } from '@/components/ui/textarea'

export function StartupScriptField({
  id,
  label,
  provider,
  value,
  placeholder,
  onChange,
}: {
  id: string
  label: string
  provider: string
  value: string
  placeholder?: string
  onChange: (value: string) => void
}) {
  return (
    <Field>
      <FieldLabel htmlFor={id}>{label}</FieldLabel>
      <FieldDescription>
        Shell script that runs on each new machine before the daemon starts.
      </FieldDescription>
      {provider === 'unikraft' && (
        <FieldDescription>
          Unikraft only supports short startup scripts—about 1 KB in the default configuration, with
          less room when environment variables are added. Use a custom image for larger setup.
        </FieldDescription>
      )}
      <Textarea
        id={id}
        value={value}
        placeholder={placeholder}
        className="min-h-20 resize-y font-mono text-xs"
        onChange={(event) => {
          onChange(event.target.value)
        }}
      />
    </Field>
  )
}
