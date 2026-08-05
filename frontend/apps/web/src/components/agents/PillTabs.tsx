import { cn } from '@/lib/utils'

export function PillTabs<T extends string>({
  value,
  onValueChange,
  tabs,
}: {
  value: T
  onValueChange: (value: T) => void
  tabs: { value: T; label: string }[]
}) {
  return (
    <div className="bg-muted inline-flex rounded-md p-1">
      {tabs.map((tab) => (
        <button
          key={tab.value}
          type="button"
          className={cn(
            'rounded-sm px-3 py-1.5 text-sm font-medium transition-colors',
            value === tab.value
              ? 'bg-background text-foreground shadow-xs'
              : 'text-muted-foreground hover:text-foreground',
          )}
          onClick={() => {
            onValueChange(tab.value)
          }}
        >
          {tab.label}
        </button>
      ))}
    </div>
  )
}
