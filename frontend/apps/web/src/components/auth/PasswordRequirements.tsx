import { Check, Circle } from '@/components/icons'

export function PasswordRequirements({ id, password }: { id: string; password: string }) {
  const requirements = [
    { label: 'At least 12 characters', met: Array.from(password).length >= 12 },
    { label: 'Lowercase letter', met: /[a-z]/.test(password) },
    { label: 'Uppercase letter', met: /[A-Z]/.test(password) },
    { label: 'Number', met: /[0-9]/.test(password) },
    { label: 'Symbol', met: /[!-/:-@[-`{-~]/.test(password) },
  ]

  return (
    <ul
      id={id}
      aria-label="Password requirements"
      className="grid grid-cols-2 gap-x-4 gap-y-1 text-xs"
    >
      {requirements.map((requirement) => (
        <li
          key={requirement.label}
          className={
            requirement.met
              ? 'flex items-center gap-1.5 text-emerald-700 dark:text-emerald-400'
              : 'text-muted-foreground flex items-center gap-1.5'
          }
        >
          {requirement.met ? (
            <Check aria-hidden className="size-3.5 shrink-0" />
          ) : (
            <Circle aria-hidden className="size-3.5 shrink-0" />
          )}
          <span>
            <span className="sr-only">{requirement.met ? 'Met: ' : 'Not met: '}</span>
            {requirement.label}
          </span>
        </li>
      ))}
    </ul>
  )
}
